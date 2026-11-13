package main

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/catalog"
	"github.com/entire-vc/evc-mesh/internal/extensions/repoinstall"
	"github.com/entire-vc/evc-mesh/internal/handler"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/internal/themes"
)

// registerExtensionRoutes mounts integration and extension routes, the
// extension catalog and repository installs.
func registerExtensionRoutes(deps *apiDeps) {
	cfg := deps.cfg
	db := deps.db
	extensionRegistry := deps.extensionRegistry
	instanceSettingRepo := deps.instanceSettingRepo
	extensionInstanceStore := deps.extensionInstanceStore
	extensionPolicyRepo := deps.extensionPolicyRepo
	notificationRepo := deps.notificationRepo
	extensionProjectScopeRepo := deps.extensionProjectScopeRepo
	webhookService := deps.webhookService
	extensionActorService := deps.extensionActorService
	extensionActorHandler := deps.extensionActorHandler
	sparkIntegrationResolver := deps.sparkIntegrationResolver
	extensionEventHandler := deps.extensionEventHandler
	integrationHandler := deps.integrationHandler
	externalRuntime := deps.externalRuntime
	extensionWorkspaceHandler := deps.extensionWorkspaceHandler
	extensionUIHandler := deps.extensionUIHandler
	sharedRedis := deps.sharedRedis
	themeService := deps.themeService
	themeHandler := deps.themeHandler
	api := deps.api
	rbac := deps.rbac
	bodyWS := deps.bodyWS
	idempotent := deps.idempotent

	// Integration config routes.
	api.GET("/extensions/events", extensionEventHandler.ListEvents)
	// Instance extension catalog. Install/update/rollback are 501 stubs, human-only
	// until instance-admin gating arrives.
	installedExtRepo := postgres.NewInstalledExtensionRepo(db)
	// Signed Spark catalog: hourly refresh, ed25519-verified, last good copy kept.
	// The pinned key can be replaced by the Spark instance setting catalog_public_key.
	extensionCatalog := catalog.NewClient(
		func() string { return sparkIntegrationResolver.CatalogConfig(context.Background()).URL },
		func() string { return sparkIntegrationResolver.CatalogConfig(context.Background()).PublicKey },
	)
	extensionCatalog.OnRefresh = (&catalog.Enforcer{
		Store: installedExtRepo, Journal: extensionPolicyRepo,
		Notify: func(ctx context.Context, id, reason string, blocked bool) {
			log.Printf("[extensions] catalog blocklist: %s blocked=%t reason=%q", id, blocked, reason)
			if !blocked {
				return
			}
			wsID, admins, aerr := instanceSettingRepo.InstanceAdminTargets(ctx)
			if aerr != nil {
				log.Printf("[extensions] blocklist notice: resolve admins: %v", aerr)
				return
			}
			code := "notif.extension.blocked"
			params, _ := json.Marshal(map[string]any{"extension": id, "reason": reason})
			meta, _ := json.Marshal(map[string]any{"extension_id": id})
			for i := range admins {
				uid := admins[i]
				n := &domain.Notification{
					WorkspaceID: wsID, UserID: &uid, EventType: "extension.blocked",
					Title: "Extension switched off: " + id, Body: "The Spark blocklist switched \u00ab" + id + "\u00bb off on this instance. Reason: " + reason,
					Metadata: meta, MessageCode: &code, MessageParams: params,
				}
				if nerr := notificationRepo.CreateNotification(ctx, n); nerr != nil {
					log.Printf("[extensions] blocklist notice for %s: %v", uid, nerr)
				}
			}
		},
		OnChange: func(ctx context.Context) {
			if syncErr := externalRuntime.Sync(ctx); syncErr != nil {
				log.Printf("[extensions] external runtime sync after blocklist: %v", syncErr)
			}
		},
		// Scoped (pinned) installs: their exact receipt is re-read every pass.
		Exact: func(ctx context.Context, id, version string) (*catalog.ExactReceipt, error) {
			return extensionCatalog.FetchExact(ctx, extensions.ExtensionsAPIVersion, id, version)
		},
	}).Apply
	go extensionCatalog.Run(context.Background(), extensions.ExtensionsAPIVersion)
	// One id per extension in every source: the code shipped with Mesh owns
	// its ids, the signed catalog owns the rest it lists.
	shipsWithMesh := func(id string) bool {
		_, ok := extensionRegistry.Get(id)
		return ok || themes.BuiltinKnown(id)
	}
	extensionHandler := handler.NewExtensionHandler(installedExtRepo, extensionInstanceStore).WithAllowList(extensionPolicyRepo).WithCatalog(extensionCatalog).WithBuiltins(shipsWithMesh)
	api.GET("/extensions", extensionHandler.List)
	api.GET("/extensions/catalog", extensionHandler.Catalog)
	api.GET("/extensions/:ext_id/catalog-card", extensionHandler.CatalogCard)
	api.GET("/extensions/manifest-schema", extensionHandler.ManifestSchema)
	// Install from a repository (BRAT mode), Updates tab, rollback: instance admin only.
	repoInstallSvc := &repoinstall.Service{
		Client: repoinstall.NewClient(), Repo: postgres.NewRepoExtensionRepo(db),
		Installed: postgres.NewInstalledExtensionRepo(db), Policy: extensionPolicyRepo,
		OnChange: func(ctx context.Context) {
			if syncErr := externalRuntime.Sync(ctx); syncErr != nil {
				log.Printf("[extensions] external runtime sync after change: %v", syncErr)
			}
		},
	}
	repoInstallSvc.Themes = themeService
	repoInstallSvc.Owner = func(_ context.Context, id string) string {
		if shipsWithMesh(id) {
			return string(domain.ExtensionSourceBuiltin)
		}
		if cat, cerr := extensionCatalog.Current(); cerr == nil {
			if _, listed := cat.Entry(id); listed {
				return string(domain.ExtensionSourceCatalog)
			}
		}
		return ""
	}
	themeHandler.WithRepoInstall(repoInstallSvc)
	catalogInstaller := &catalog.Installer{
		Catalog:   extensionCatalog,
		SparkURL:  func() string { return sparkIntegrationResolver.CatalogConfig(context.Background()).URL },
		CounterOn: func() bool { return sparkIntegrationResolver.CatalogConfig(context.Background()).CounterOn },
		Store:     installedExtRepo, Journal: extensionPolicyRepo,
		OnChange: func(ctx context.Context) {
			if syncErr := externalRuntime.Sync(ctx); syncErr != nil {
				log.Printf("[extensions] external runtime sync after catalog install: %v", syncErr)
			}
		},
	}
	repoInstallSvc.CatalogUpdate = func(ctx context.Context) ([]repoinstall.UpdateResult, error) {
		return catalogInstaller.UpdateAll(ctx, nil)
	}
	go repoInstallSvc.RunAutoUpdate(context.Background(), 30*time.Minute)
	extensionRepoInstallHandler := handler.NewExtensionRepoInstallHandler(repoInstallSvc, instanceSettingRepo).WithCatalogInstall(catalogInstaller).WithCatalogRefresh(func(ctx context.Context) (catalog.Receipt, error) {
		return extensionCatalog.RefreshNow(ctx, extensions.ExtensionsAPIVersion)
	})
	api.POST("/extensions/install", extensionRepoInstallHandler.Install, mw.RequireUserAuth())
	api.POST("/extensions/repo/refs", extensionRepoInstallHandler.Refs, mw.RequireUserAuth())
	api.POST("/extensions/repo/preview", extensionRepoInstallHandler.Preview, mw.RequireUserAuth())
	api.GET("/extensions/updates", extensionRepoInstallHandler.Updates, mw.RequireUserAuth())
	api.POST("/extensions/catalog/refresh", extensionRepoInstallHandler.RefreshCatalog, mw.RequireUserAuth())
	api.POST("/extensions/update-all", extensionRepoInstallHandler.UpdateAll, mw.RequireUserAuth())
	api.POST("/extensions/:ext_id/update", extensionRepoInstallHandler.Update, mw.RequireUserAuth())
	api.POST("/extensions/:ext_id/rollback", extensionRepoInstallHandler.Rollback, mw.RequireUserAuth())
	// Repository -> catalog move and repository re-pin: plan writes nothing,
	// apply carries the planned state and is safe to retry with Idempotency-Key.
	extensionRepoInstallHandler.WithCatalogMove(catalogInstaller)
	api.POST("/extensions/:ext_id/move-to-catalog/plan", extensionRepoInstallHandler.PlanMoveToCatalog, mw.RequireUserAuth())
	api.POST("/extensions/:ext_id/move-to-catalog", extensionRepoInstallHandler.MoveToCatalog, mw.RequireUserAuth(), idempotent)
	api.POST("/extensions/:ext_id/repin/plan", extensionRepoInstallHandler.PlanRepin, mw.RequireUserAuth())
	api.POST("/extensions/:ext_id/repin", extensionRepoInstallHandler.Repin, mw.RequireUserAuth(), idempotent)
	api.GET("/integrations/:int_id/event-subscriptions", extensionEventHandler.List, rbac(mw.PermManageWebhooks))
	api.POST("/integrations/:int_id/event-subscriptions", extensionEventHandler.Subscribe, rbac(mw.PermManageWebhooks))
	api.DELETE("/event-subscriptions/:sub_id", extensionEventHandler.Unsubscribe, rbac(mw.PermManageWebhooks))
	api.GET("/workspaces/:ws_id/integrations", integrationHandler.List, handler.DeprecatedAlias("/api/v1/workspaces/{ws}/extensions"))
	api.POST("/workspaces/:ws_id/integrations", integrationHandler.Configure, handler.DeprecatedAlias("/api/v1/workspaces/{ws}/extensions/{id}/connect"), rbac(mw.PermManageWebhooks))
	api.GET("/workspaces/:ws_id/extensions", extensionWorkspaceHandler.List)
	// The workspace is named in the body, not the path: bodyWS checks the caller against it.
	api.POST("/ext/:ext_id/ui-context", extensionUIHandler.UIContext, mw.RequireUserAuth(), bodyWS)
	api.POST("/workspaces/:ws_id/extensions/:ext_id/connect", extensionWorkspaceHandler.Connect, rbac(mw.PermManageWebhooks))
	api.POST("/workspaces/:ws_id/extensions/:ext_id/consent", extensionWorkspaceHandler.Reconsent, rbac(mw.PermManageWebhooks))
	api.PATCH("/workspaces/:ws_id/extensions/:ext_id/settings", extensionWorkspaceHandler.PatchSettings, rbac(mw.PermManageWebhooks))
	api.POST("/workspaces/:ws_id/extensions/:ext_id/enable", extensionWorkspaceHandler.Enable, rbac(mw.PermManageWebhooks))
	api.POST("/workspaces/:ws_id/extensions/:ext_id/disable", extensionWorkspaceHandler.Disable, rbac(mw.PermManageWebhooks))
	api.POST("/workspaces/:ws_id/extensions/:ext_id/test", extensionWorkspaceHandler.Test, rbac(mw.PermManageWebhooks))
	api.PUT("/workspaces/:ws_id/extensions/:ext_id/project-scope", extensionWorkspaceHandler.SetProjectScope, rbac(mw.PermManageWebhooks))
	api.DELETE("/workspaces/:ws_id/extensions/:ext_id", extensionWorkspaceHandler.Disconnect, rbac(mw.PermManageWebhooks))
	api.POST("/integrations/:int_id/rotate-token", extensionActorHandler.RotateToken, rbac(mw.PermManageWebhooks))
	api.GET("/ext/me", extensionActorHandler.Me)
	api.POST("/ext/me/token/rotate", extensionActorHandler.SelfRotate)
	api.DELETE("/ext/me/token/previous", extensionActorHandler.SelfRevokePrevious)
	// Own limit per extension actor: an external service may call this once per
	// user request, and it must not become a cheap way to probe the workspace.
	api.POST("/ext/me/authorize", handler.NewExtensionAuthorizeHandler(db, extensionProjectScopeRepo, extensionActorService).Authorize, mw.RateLimit(mw.RateLimitConfig{
		Enabled:     cfg.RateLimit.Enabled,
		RPM:         cfg.RateLimit.ExtAuthorizeRPM,
		KeyFunc:     mw.RateLimitKeyByActor,
		RedisClient: sharedRedis,
		Name:        "ext-authorize",
	}))
	extensionEventReplayer, _ := webhookService.(service.ExtensionEventReplayer)
	extensionEventReplayHandler := handler.NewExtensionEventReplayHandler(extensionEventReplayer)
	api.GET("/ext/me/events", extensionEventReplayHandler.List)
	api.POST("/ext/me/events/:delivery_id/redeliver", extensionEventReplayHandler.Redeliver)
	api.PATCH("/integrations/:int_id", integrationHandler.Update, handler.DeprecatedAlias("/api/v1/workspaces/{ws}/extensions/{id}/settings"), rbac(mw.PermManageWebhooks))
	api.DELETE("/integrations/:int_id", integrationHandler.Delete, handler.DeprecatedAlias("/api/v1/workspaces/{ws}/extensions/{id}"), rbac(mw.PermManageWebhooks))
}
