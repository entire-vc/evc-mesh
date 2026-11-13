package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/bootstrap"
	"github.com/entire-vc/evc-mesh/internal/embedding"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	githubext "github.com/entire-vc/evc-mesh/internal/extensions/github"
	gitlabext "github.com/entire-vc/evc-mesh/internal/extensions/gitlab"
	runnerext "github.com/entire-vc/evc-mesh/internal/extensions/runner"
	sparkext "github.com/entire-vc/evc-mesh/internal/extensions/spark"
	teamrelayext "github.com/entire-vc/evc-mesh/internal/extensions/team-relay"
	telegramext "github.com/entire-vc/evc-mesh/internal/extensions/telegram"
	"github.com/entire-vc/evc-mesh/internal/handler"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/reconciler"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/internal/themes"
)

// wireCoreServices creates authentication, the first-admin bootstrap and the
// core services up to and including the task service.
func wireCoreServices(deps *apiDeps) {
	cfg := deps.cfg
	db := deps.db
	workspaceRepo := deps.workspaceRepo
	projectRepo := deps.projectRepo
	taskRepo := deps.taskRepo
	taskStatusRepo := deps.taskStatusRepo
	taskDependencyRepo := deps.taskDependencyRepo
	commentRepo := deps.commentRepo
	agentRepo := deps.agentRepo
	eventBusRepo := deps.eventBusRepo
	activityLogRepo := deps.activityLogRepo
	userRepo := deps.userRepo
	refreshTokenRepo := deps.refreshTokenRepo
	workspaceMemberRepo := deps.workspaceMemberRepo
	projectMemberRepo := deps.projectMemberRepo
	webhookRepo := deps.webhookRepo
	vcsLinkRepo := deps.vcsLinkRepo
	integrationRepo := deps.integrationRepo
	extensionRegistry := deps.extensionRegistry
	extensionPolicyRepo := deps.extensionPolicyRepo
	ruleRepo := deps.ruleRepo
	wsRuleRepo := deps.wsRuleRepo
	projRuleRepo := deps.projRuleRepo
	ruleViolationLogRepo := deps.ruleViolationLogRepo
	notificationRepo := deps.notificationRepo
	autoTransRuleRepo := deps.autoTransRuleRepo
	memoryRepo := deps.memoryRepo
	memoryEdgesRepo := deps.memoryEdgesRepo
	memoryChunkRepo := deps.memoryChunkRepo
	documentChunkRepo := deps.documentChunkRepo

	// 5. Create auth service.
	authService := auth.NewService(
		userRepo,
		refreshTokenRepo,
		workspaceRepo,
		workspaceMemberRepo,
		cfg.Auth.JWTSecret,
		auth.WithAllowRegistration(cfg.Auth.AllowRegistration),
		auth.WithAgentRepo(agentRepo),
		auth.WithRefreshReuseGraceWindow(cfg.Auth.RefreshReuseGraceWindow),
	)

	// Create the first admin on a fresh install — and always say what happened.
	// Logic and tests live in internal/bootstrap.
	bootstrap.Admin(context.Background(), bootstrap.Deps{
		CountUsers: func(ctx context.Context) (int, error) {
			var n int
			countErr := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&n)
			return n, countErr
		},
		Register: func(ctx context.Context, email, password, name string) error {
			_, _, regErr := authService.Register(ctx, email, password, name)
			return regErr
		},
		Getenv: os.Getenv,
		Logf:   log.Printf,
	})

	// 6. Create all service instances.
	workspaceService := service.NewWorkspaceService(workspaceRepo, activityLogRepo, workspaceMemberRepo)
	projectService := service.NewProjectService(projectRepo, taskStatusRepo, activityLogRepo,
		service.WithProjectMemberRepo(projectMemberRepo),
		service.WithAutoTransRuleRepo(autoTransRuleRepo),
	)
	customFieldDefRepo := postgres.NewCustomFieldDefinitionRepo(db)
	customFieldService := service.NewCustomFieldService(customFieldDefRepo, activityLogRepo)

	// Rule service is created before taskService so it can be injected as an option.
	// WithRuleTaskStatusRepo is required for transition_gate.require_subtasks_done's
	// allow_cancelled option: without it, evalRequireSubtasksDone silently skips the
	// cancelled-subtask check (deps.taskStatusRepo == nil), so a parent with even one
	// legitimately-cancelled subtask can never reach done/review no matter how the
	// rule is configured (an internal ticket, live incident an internal ticket).
	ruleService := service.NewRuleService(ruleRepo, activityLogRepo,
		service.WithRuleCommentRepo(commentRepo),
		service.WithRuleTaskRepo(taskRepo),
		service.WithRuleTaskStatusRepo(taskStatusRepo),
	)

	// Event bus service is created early so it can be injected into taskService.
	// Task mutations (create/update/move/delete) will auto-publish events.
	eventBusService := service.NewEventBusService(eventBusRepo, activityLogRepo)

	// Embedding provider for vector search (optional; degrades to keyword-only when "none").
	embedder := embedding.NewEmbedder(cfg.Embedding)
	log.Printf("Embedding provider: %s", cfg.Embedding.Provider)

	// Memory reconciler (P1-C): drives the freshness lifecycle (expire/stale/supersede).
	// RECONCILER_EPOCH gates the cold-start stale avalanche — only memories created after
	// the epoch are eligible for stale marking. Defaults to the binary build time.
	reconcilerEpoch := BuildTime // use binary build time as default
	var reconcilerEpochTime time.Time
	if envEpoch := os.Getenv("RECONCILER_EPOCH"); envEpoch != "" {
		if t, parseErr := time.Parse(time.RFC3339, envEpoch); parseErr == nil {
			reconcilerEpochTime = t
		} else {
			log.Printf("RECONCILER_EPOCH parse error (ignored, using build time): %v", parseErr)
		}
	} else if reconcilerEpoch != "unknown" {
		if t, parseErr := time.Parse(time.RFC3339, reconcilerEpoch); parseErr == nil {
			reconcilerEpochTime = t
		}
	}
	memReconciler := reconciler.New(memoryRepo, memoryEdgesRepo, embedder, reconciler.Config{
		Epoch: reconcilerEpochTime,
	})

	// Memory service is wired into eventBusService so Publish() can extract memories.
	// MemoryWithProjectRepo enables automatic project:<slug> tag → project_id resolution on write.
	// MemoryWithChunkRepo switches the embed write path to per-chunk storage (ADR-0002,
	// an internal ticket) — without it a memory longer than the embedder's input window only ever
	// gets embedded from its first ~15%.
	memoryService := service.NewMemoryService(memoryRepo, memoryEdgesRepo, embedder,
		service.MemoryWithProjectRepo(projectRepo),
		service.MemoryWithWorkspaceRepo(workspaceRepo),
		service.MemoryWithTaskRepo(taskRepo),
		service.MemoryWithDepRepo(taskDependencyRepo),
		service.MemoryWithEmbedConcurrency(cfg.Embedding.Concurrency),
		service.MemoryWithChunkRepo(memoryChunkRepo),
		// Recall over Mesh Docs: both flags default OFF. WRITE builds the
		// index; RECALL exposes it to recall. Separate so an index can be built and
		// checked before any recall sees it.
		service.MemoryWithDocIndex(documentChunkRepo, os.Getenv("DOC_INDEX_RECALL") == "true"),
	)

	// Webhook service is created before taskService so it can be injected for agent wakeup dispatch.
	// Extension subscriptions with delivery=webhook ride the same service; the
	// signing secret is resolved from the workspace secret named by secret_ref.
	// Workspace secrets live in Keep only: secretRepo holds the
	// Keep provider of the process store, nil when Keep is not configured.
	secretRepo := postgres.NewSecretRefRepo(db, deps.secretRouter.Keep)
	// Project scope of extension connections: one repo answers it for the API
	// guard, event delivery, ui-context, the project list and the export.
	extensionProjectScopeRepo := postgres.NewExtensionProjectScopeRepo(db)
	// What the installed manifest of an extension declares right now: the
	// ceiling of every right a consent grants (token capabilities and events).
	extensionDeclared := func(id string) ([]string, bool) {
		p, ok := extensionRegistry.Get(extensionRegistry.Canonical(id))
		if !ok {
			return nil, false
		}
		return extensions.DeclaredCapabilities(p.Manifest().Capabilities), true
	}
	webhookService := service.NewWebhookService(webhookRepo,
		service.WithExtensionDelivery(
			postgres.NewExtensionEventSubscriptionRepo(db),
			postgres.NewIntegrationRepo(db),
			secretRepo,
		),
		service.WithExtensionSignatureVersion(func(id string) int {
			if p, ok := extensionRegistry.Get(id); ok {
				if ext := p.Manifest().External; ext != nil {
					return ext.SignatureVersion
				}
			}
			return 1
		}),
		service.WithExtensionConsentGate(postgres.NewIntegrationConsentRepo(db), extensionDeclared),
		service.WithExtensionProjectScope(extensionProjectScopeRepo))

	projectIntegrationRepo := postgres.NewProjectIntegrationRepo(db)

	// One repo, two services, deliberately not one. secretService is what the
	// public CRUD handlers get and it cannot decrypt anything;
	// secretMaterializationService can, and reaches exactly one route.
	secretService := service.NewSecretService(secretRepo)
	secretMaterializationService := service.NewSecretMaterializationService(secretRepo)
	mw.LogSpawnerAllowlist()

	agentActLogRepo := postgres.NewAgentActivityLogRepo(db)
	// agent_workspace_grants (migration 20260909001, task U1) — the login
	// path Authenticate now resolves through first (task U2); agents.* is
	// only the fallback for a connection-less agent.
	agentWorkspaceGrantRepo := postgres.NewAgentWorkspaceGrantRepo(db)
	// The cache wrapper is applied at construction so that EVERY consumer of
	// AgentService gets it — the three auth middlewares and the WebSocket
	// handshake all call Authenticate, and each of them was paying a full
	// cost-12 bcrypt comparison (~163 ms of CPU) on every single request from an
	// already-known key. Successful verifications only; failures still run
	// bcrypt, and rotation/deletion evict explicitly.
	agentService := service.NewCachedAgentAuth(
		service.NewAgentService(agentRepo, activityLogRepo, workspaceRepo, userRepo),
		service.AgentAuthCacheTTL,
	)
	// Wire agent activity log repository for monitoring, and the connection
	// repository Authenticate reads through.
	if configurable, ok := agentService.(service.AgentServiceConfigurable); ok {
		configurable.SetAgentActivityLogRepo(agentActLogRepo)
		configurable.SetAgentWorkspaceGrantRepo(agentWorkspaceGrantRepo)
	}
	// Additional agent keys (POST /agents/:id/keys): Authenticate consults them,
	// the cache re-checks revocation on every hit.
	agentAPIKeyRepo := postgres.NewAgentAPIKeyRepo(db)
	if configurable, ok := agentService.(service.AgentAPIKeyConfigurable); ok {
		configurable.SetAgentAPIKeyRepo(agentAPIKeyRepo)
	}
	agentKeyService := service.NewAgentKeyService(agentRepo, workspaceRepo, agentAPIKeyRepo, activityLogRepo, webhookService, agentService.(service.AgentCacheInvalidator))
	agentKeyHandler := handler.NewAgentKeyHandler(agentKeyService)
	// Hierarchy export + agents.sponsor_user_id, plus the project and member
	// events extensions subscribe to.
	workspaceExportRepo := postgres.NewWorkspaceExportRepo(db)
	workspaceExportHandler := handler.NewWorkspaceExportHandler(workspaceExportRepo).WithProjectScope(extensionProjectScopeRepo)
	if r, ok := agentService.(service.AgentSponsorConfigurable); ok {
		r.SetAgentSponsorRecorder(workspaceExportRepo)
	}
	if r, ok := agentKeyService.(service.AgentSponsorConfigurable); ok {
		r.SetAgentSponsorRecorder(workspaceExportRepo)
	}
	if r, ok := projectService.(interface {
		SetEventDispatcher(service.PublicEventDispatcher)
	}); ok {
		r.SetEventDispatcher(webhookService)
	}

	// Service principal of extension connections (ext_ tokens).
	extensionActorService := service.NewExtensionActorService(postgres.NewExtensionActorRepo(db), integrationRepo, workspaceRepo)
	extensionActorHandler := handler.NewExtensionActorHandler(extensionActorService)
	// Invite/revoke/list on top of the same table (task U3) — a separate
	// service from AgentService/agentService above: that one only ever READS
	// agent_workspace_grants (the auth path), this one owns the writes.
	agentWorkspaceGrantService := service.NewAgentWorkspaceGrantService(agentWorkspaceGrantRepo, agentRepo, workspaceRepo, activityLogRepo)

	// OAuth 2.0 Authorization Server (task MCP-OAuth 1/5, migration
	// 20260924004): lets an MCP client obtain a mot_ access token through
	// user consent instead of a shared agk_ agent key. Depends on
	// agentService (already wrapped in the auth cache above) to create/read
	// the per-consent connector agent — same instance every other consumer
	// of AgentService gets.
	oauthRepo := postgres.NewOAuthRepo(db)
	oauthService := service.NewOAuthService(oauthRepo, agentService, userRepo, workspaceRepo, workspaceMemberRepo, agentWorkspaceGrantRepo, projectMemberRepo)
	chatSessionService := service.NewChatSessionService(postgres.NewChatSessionTokenRepo(db), agentService, workspaceRepo, workspaceMemberRepo, projectRepo, projectMemberRepo, agentWorkspaceGrantRepo)

	// Agent notification service for push mechanisms (callback_url, SSE, long-poll).
	// Reuses the same Redis connection as the WebSocket hub (created below in step 8a).
	// We create a dedicated client here so the notify service can be injected into taskService
	// before wsRedis is declared later in main.
	agentNotifyRedis := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr(),
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	agentEventsRepo := postgres.NewAgentEventsRepo(db)
	agentNotifySvc := service.NewAgentNotifyService(agentService, agentNotifyRedis, agentEventsRepo)

	// Context cache for GET /tasks/:task_id/context (60-second TTL).
	// A dedicated Redis client is used so it can be closed independently.
	ctxCacheRedis := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr(),
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	ctxCacheSvc := service.NewContextCacheService(ctxCacheRedis)

	// WS badge publisher — dedicated Redis client so it can be injected into commentService
	// before the shared ws hub Redis client is created in step 8a.
	wsBadgeRedis := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr(),
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	wsPublisher := service.NewRedisWSPublisher(wsBadgeRedis)
	chatEvents := service.NewChatEventPublisher(wsPublisher, service.NewRedisChatSeq(wsBadgeRedis))

	// RulesService (assignment/workflow config) is created before taskService for auto-assign injection.
	rulesService := service.NewRulesServiceWithOptions(wsRuleRepo, projRuleRepo, ruleViolationLogRepo, agentRepo, workspaceMemberRepo, workspaceRepo, projectRepo,
		service.WithRulesRuleRepo(ruleRepo),
		service.WithRulesStatusRepo(taskStatusRepo),
		// Enables GET /workspaces/:ws_id/team to list guest agents (task
		// U3/an internal ticket) alongside home ones, each tagged is_home. Without
		// this, the endpoint reverts to home-only — see GetTeamDirectory.
		service.WithRulesAgentGrantRepo(agentWorkspaceGrantRepo),
	)

	// Web Push service — graceful: no-op when VAPID keys are absent.
	pushSubRepo := postgres.NewPushSubscriptionRepo(db)
	pushService := service.NewPushService(pushSubRepo, notificationRepo, cfg.VAPID.PublicKey, cfg.VAPID.PrivateKey, cfg.VAPID.Subject)
	if cfg.VAPID.PublicKey != "" {
		log.Printf("Web Push VAPID enabled (public key length: %d)", len(cfg.VAPID.PublicKey))
	}

	// Email service — shared by the invite flow (below) and the email
	// notification channel. Created here, ahead of both, so notificationService
	// can be wired with it; the invite-specific base-URL/SMTP_FROM warnings stay
	// down by inviteService since they are about invite links specifically.
	// Brand source for outgoing mail: the instance/workspace theme. Only the
	// choice and the installed row are read, so no object store is needed.
	mailThemes := &themes.Service{
		Installed: postgres.NewInstalledExtensionRepo(db),
		Themes:    postgres.NewThemeRepo(db),
		Allow:     extensionPolicyRepo,
	}
	emailSvc := service.NewEmailService(cfg.Email, service.WithBrandSource(mailThemes))
	if emailSvc.Enabled() {
		log.Printf("Email ready: sending via %s:%d as %q", cfg.Email.Host, cfg.Email.Port, cfg.Email.From)
	} else {
		log.Printf("Email is not configured (SMTP_HOST is empty) — no invitation or notification emails will be sent. Invites still work: the invite link is shown in the UI for you to pass on. Set SMTP_HOST/SMTP_FROM to send email instead.")
	}

	// Telegram — bot API client + the manager that long-polls every active
	// workspace bot and handles /start. Created here, ahead of
	// notificationService, for the same reason as emailSvc: the dispatch
	// channel below needs it as a dependency.
	//
	// MESH_TELEGRAM_ENABLED=false leaves BOTH nil, which is a state every
	// telegram code path downstream already handles and already explains:
	// dispatch logs "the Telegram channel is not wired up on this instance",
	// TelegramReachable answers "not enabled on this instance", TelegramBotInfo
	// reports no bot, and the Integrations handler refuses a bot_token with
	// "Telegram is not available on this instance". None of that is new code —
	// the nil case was written for tests, and it says the true thing here too.
	telegramProvider, telegramRegistered := extensionRegistry.Get(telegramext.ID)
	telegramProv, telegramIsProvider := telegramProvider.(*telegramext.Provider)
	if !telegramRegistered || !telegramIsProvider {
		log.Fatalf("[extensions] telegram provider is not registered") //nolint:gocritic // startup: the Telegram channel cannot be gated without it
	}
	// The instance switch is read at the moment of use (poll iteration,
	// dispatch, Integrations request), so flipping it needs no restart.
	telegramGate := service.TelegramGate(telegramProv.InstanceEnabled)
	telegramClient := service.NewTelegramClient()
	telegramBotManager := service.NewTelegramBotManager(
		telegramClient, integrationRepo, notificationRepo, userRepo, projectRepo, workspaceRepo,
	).WithGate(telegramGate).WithBotResolver(telegramProv.ResolveBot)
	if !telegramProv.InstanceEnabled(context.Background()) {
		log.Println("Telegram channel switched off on this instance (instance setting / MESH_TELEGRAM_ENABLED=false): no bot polling, no Telegram notifications, and the Integrations page refuses to connect a bot. Workspace integration_configs rows are left untouched; switch the extension on to resume.")
	}

	// Notification service for in-app push notifications to workspace users.
	// Created before taskService and commentService so it can be injected as a dependency.
	notificationOpts := []service.NotificationServiceOption{
		service.WithPushService(pushService),
		service.WithEmailService(emailSvc, userRepo, cfg.Email.BaseURL),
		service.WithEmailBrand(mailThemes),
		service.WithLocaleLookups(userRepo, workspaceRepo),
	}
	notificationOpts = append(notificationOpts,
		service.WithTelegramService(telegramClient, integrationRepo, workspaceRepo, projectRepo),
		service.WithTelegramGate(telegramGate))
	notificationService := service.NewNotificationService(notificationRepo, notificationOpts...)

	// vcsIntegrationResolver replaces the process-start-only githubClient/
	// gitlabClient construction that used to live here (an internal ticket, §C1 of
	// specsintegration-provider-contract): it resolves a GitHub/GitLab
	// connection fresh on EVERY call — the done-evidence gate's live
	// re-check, and the webhook receiver's signature validation — honoring
	// a workspace's own active integration row over these env values, per
	// §4's resolution order. The env values below are what governs when no
	// workspace has configured its own connection (the pre
	// single-instance behavior, unchanged) — logged explicitly here because
	// that is the one thing genuinely fixed at process start; a workspace
	// row can flip the effective state on the next request without a
	// restart.
	vcsIntegrationResolver := service.NewVCSIntegrationResolver(integrationRepo, service.VCSEnvFallback{
		GitHubToken:         cfg.Webhook.GitHubToken,
		GitHubWebhookSecret: cfg.Webhook.GitHubSecret,
		GitLabBaseURL:       cfg.Webhook.GitLabURL,
		GitLabToken:         cfg.Webhook.GitLabToken,
		GitLabWebhookSecret: cfg.Webhook.GitLabSecret,
	})
	// GitHub resolves through its extension: workspace row, then the instance
	// level (MESH_GITHUB_* is bootstrapped into it), then off.
	githubProv, ghOK := extensionRegistry.Get("github")
	if !ghOK {
		log.Fatal("[extensions] github extension is not registered")
	}
	githubExt := githubProv.(*githubext.Provider)
	vcsIntegrationResolver.WithGitHubExtension(githubExt)
	// GitLab resolves the same way: workspace row, then the instance level
	// (MESH_GITLAB_* is bootstrapped into it), then off.
	gitlabProv, glOK := extensionRegistry.Get("gitlab")
	if !glOK {
		log.Fatal("[extensions] gitlab extension is not registered")
	}
	gitlabExt := gitlabProv.(*gitlabext.Provider)
	vcsIntegrationResolver.WithGitLabExtension(gitlabExt)
	if cfg.Webhook.GitHubToken != "" {
		log.Printf("[config] GitHub env fallback: live PR-status check available when no workspace has its own GitHub integration configured")
	} else {
		log.Printf("[config] MESH_GITHUB_TOKEN not set — GitHub env fallback has no live PR-status check; a workspace can still enable one via its own integration")
	}
	if cfg.Webhook.GitLabURL != "" && cfg.Webhook.GitLabToken != "" {
		log.Printf("[config] GitLab env fallback: live MR-status check available when no workspace has its own GitLab integration configured (%s)", cfg.Webhook.GitLabURL)
	} else {
		log.Printf("[config] MESH_GITLAB_URL/MESH_GITLAB_TOKEN not set — GitLab env fallback has no live MR-status check; a workspace can still enable one via its own integration")
	}

	// The runtime asks the team-relay extension (workspace row, then instance
	// setting) for the relay URL; the status screen reads the same provider.
	teamRelayProvider, teamRelayRegistered := extensionRegistry.Get(teamrelayext.ID)
	teamRelayExt, teamRelayIsProvider := teamRelayProvider.(*teamrelayext.Provider)
	if !teamRelayRegistered || !teamRelayIsProvider {
		log.Fatalf("[extensions] team-relay provider is not registered") //nolint:gocritic // startup: Team Relay cannot resolve its relay URL without it
	}
	if cfg.TeamRelay.RelayURL != "" {
		log.Printf("[config] Team Relay seed: relay URL available to initialize the instance setting (%s)", cfg.TeamRelay.RelayURL)
	} else {
		log.Printf("[config] Team Relay seed has no relay URL; a workspace can still enable one via its own integration")
	}

	// sparkIntegrationResolver replaces the process-start-only cfg.Spark.Enabled
	// gate that used to decide, once and for all, whether /spark/* routes existed
	// at all — the exact C1/C2 violation an internal ticket exists to close: is_active on a
	// workspace's own spark row could never gate anything, because by the time a
	// request arrived the client (or its absence) had already been baked in.
	// Routes are now always registered (below); this resolver is consulted fresh
	// on every request instead.
	runnerProvider, runnerRegistered := extensionRegistry.Get(runnerext.ID)
	runnerProv, runnerIsProvider := runnerProvider.(*runnerext.Provider)
	if !runnerRegistered || !runnerIsProvider {
		log.Fatalf("[extensions] runner provider is not registered") //nolint:gocritic // startup: the runtime write guard needs it
	}
	sparkProvider, sparkRegistered := extensionRegistry.Get(sparkext.ID)
	sparkIntegrationResolver, sparkIsProvider := sparkProvider.(*sparkext.Provider)
	if !sparkRegistered || !sparkIsProvider {
		log.Fatalf("[extensions] spark provider is not registered") //nolint:gocritic // startup: Spark routes cannot work without it
	}
	if sparkIntegrationResolver.InstanceAvailable(context.Background()) {
		log.Printf("[config] Spark instance level: catalog URL available when no workspace has its own spark integration configured")
	} else {
		log.Printf("[config] Spark instance level not configured (no instance setting, no MESH_SPARK_URL/MESH_SPARK_ENABLED) — a workspace can still enable one via its own integration")
	}

	// Append-only record of gate-arming predicate evaluations. The
	// guard enforces with or without it; this only makes the two-week refusals-vs-arms
	// ratio the card asks for answerable.
	gatePredicateLogRepo := postgres.NewGatePredicateLogRepo(db)

	taskService := service.NewTaskService(taskRepo, taskStatusRepo, taskDependencyRepo, activityLogRepo,
		service.WithGatePredicateLog(gatePredicateLogRepo),
		service.WithCustomFieldService(customFieldService),
		service.WithProjectRepo(projectRepo),
		service.WithTaskBatchMover(taskRepo),
		service.WithRuleService(ruleService),
		service.WithEventBusService(eventBusService),
		service.WithWebhookService(webhookService),
		service.WithAgentNotifyService(agentNotifySvc),
		service.WithRulesConfigService(rulesService),
		service.WithContextCacheInvalidator(ctxCacheSvc),
		service.WithNotificationService(notificationService),
		service.WithProjectMemberRepoTask(projectMemberRepo),
		service.WithTaskAgentRepo(agentRepo),
		service.WithUserRepoTask(userRepo),
		service.WithCommentRepoTask(commentRepo),                   // enables review-evidence gate
		service.WithVCSLinkRepoTask(vcsLinkRepo),                   // enables done-evidence gate
		service.WithVCSIntegrationResolver(vcsIntegrationResolver), // per-workspace GitHub/GitLab live-check client, resolved on every call (see above)
		// Human half of the assignee tenancy guard. Without it the user path of
		// assertAssigneeInProjectWorkspace cannot be decided and refuses every
		// user assignment, so this wiring is load-bearing, not optional —
		// TestTaskServiceWiresTheAssigneeTenancyGuard reads it back out of this file.
		service.WithWorkspaceMembershipReader(postgres.NewWorkspaceMembershipReader(db)),
		// Agent half of the same guard for the multi-workspace case (task
		// U3/an internal ticket): without it, a guest agent (home workspace is
		// elsewhere, reached here via an active agent_workspace_grants
		// connection) can never pass assertAssigneeInProjectWorkspace's agent
		// branch, and every guest-workspace assignment refuses.
		service.WithTaskAgentGrantRepo(agentWorkspaceGrantRepo),
		// Enables stale-cursor rejection on list_tasks (ADR-0004). Without
		// this, List behaves exactly as it did before the option existed —
		// see WithTaskListRevisionRepo's doc comment.
		service.WithTaskListRevisionRepo(postgres.NewTaskListRevisionRepo(db)),
	)

	// Wire auto-transition service. It calls taskService.MoveTask, so taskService must already
	// exist. We inject it back via the configurable interface to trigger transitions on status
	// changes without introducing an import cycle.
	autoTransitionSvc := service.NewAutoTransitionService(taskRepo, taskStatusRepo, taskDependencyRepo, taskService, autoTransRuleRepo, commentRepo)
	if configurable, ok := taskService.(service.TaskServiceAutoTransitionConfigurable); ok {
		configurable.SetAutoTransitionService(autoTransitionSvc)
	}

	// Wire taskService into agentService's Heartbeat path so a live heartbeat can
	// extend the agent's checkout leases (audit §1.2, mid_pipeline.heartbeat_extends_checkout).
	// Same back-injection reason as SetAutoTransitionService above: agentService is
	// constructed long before taskService exists (nearly everything else, including
	// taskService's own middleware chain, depends on agentService.Authenticate first).
	//
	// Two type assertions, not one: taskService here is typed as the wide
	// TaskService interface (NewTaskService's return type), which deliberately
	// does NOT declare ExtendCheckoutsOnHeartbeat — see CheckoutHeartbeatExtender's
	// doc comment for why it stays off that interface. The concrete *taskService
	// value underneath satisfies it structurally; this asserts that back out.
	if configurable, ok := agentService.(service.AgentServiceConfigurable); ok {
		if extender, ok := taskService.(service.CheckoutHeartbeatExtender); ok {
			configurable.SetCheckoutHeartbeatExtender(extender)
		}
	}

	taskStatusService := service.NewTaskStatusService(taskStatusRepo, taskRepo, activityLogRepo)

	deps.authService = authService
	deps.workspaceService = workspaceService
	deps.projectService = projectService
	deps.customFieldService = customFieldService
	deps.ruleService = ruleService
	deps.eventBusService = eventBusService
	deps.embedder = embedder
	deps.memReconciler = memReconciler
	deps.memoryService = memoryService
	deps.secretRepo = secretRepo
	deps.extensionProjectScopeRepo = extensionProjectScopeRepo
	deps.extensionDeclared = extensionDeclared
	deps.webhookService = webhookService
	deps.projectIntegrationRepo = projectIntegrationRepo
	deps.secretService = secretService
	deps.secretMaterializationService = secretMaterializationService
	deps.agentWorkspaceGrantRepo = agentWorkspaceGrantRepo
	deps.agentService = agentService
	deps.agentKeyHandler = agentKeyHandler
	deps.workspaceExportHandler = workspaceExportHandler
	deps.extensionActorService = extensionActorService
	deps.extensionActorHandler = extensionActorHandler
	deps.agentWorkspaceGrantService = agentWorkspaceGrantService
	deps.oauthRepo = oauthRepo
	deps.oauthService = oauthService
	deps.chatSessionService = chatSessionService
	deps.agentNotifyRedis = agentNotifyRedis
	deps.agentEventsRepo = agentEventsRepo
	deps.agentNotifySvc = agentNotifySvc
	deps.ctxCacheRedis = ctxCacheRedis
	deps.ctxCacheSvc = ctxCacheSvc
	deps.wsBadgeRedis = wsBadgeRedis
	deps.wsPublisher = wsPublisher
	deps.chatEvents = chatEvents
	deps.rulesService = rulesService
	deps.pushService = pushService
	deps.emailSvc = emailSvc
	deps.telegramGate = telegramGate
	deps.telegramClient = telegramClient
	deps.telegramBotManager = telegramBotManager
	deps.notificationService = notificationService
	deps.vcsIntegrationResolver = vcsIntegrationResolver
	deps.githubExt = githubExt
	deps.gitlabExt = gitlabExt
	deps.teamRelayExt = teamRelayExt
	deps.runnerProv = runnerProv
	deps.sparkIntegrationResolver = sparkIntegrationResolver
	deps.taskService = taskService
	deps.autoTransitionSvc = autoTransitionSvc
	deps.taskStatusService = taskStatusService
}
