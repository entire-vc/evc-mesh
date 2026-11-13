package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/entire-vc/evc-mesh/internal/config"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/builtin"
	"github.com/entire-vc/evc-mesh/internal/extensions/secretstore"
	"github.com/entire-vc/evc-mesh/internal/handler"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/encryption"
	"github.com/entire-vc/evc-mesh/pkg/metrics"
)

// wireStorage loads the configuration, opens PostgreSQL, runs the migrations,
// creates the repositories and the extension registry.
func wireStorage(deps *apiDeps) {
	// 1. Load configuration from environment.
	cfg := config.Load()

	// Say out loud, once, whether integration credentials are actually being
	// encrypted at rest. Previously the only signal was a log line emitted
	// lazily on the first Encrypt/Decrypt call — on a quiet instance that can
	// be days after boot, buried in request logs, and it read identically
	// whether the key was missing or merely mistyped. Publishing it as a gauge
	// makes "the control is off" alertable instead of something discovered by
	// reading rows. Checked before anything is opened: it needs no
	// dependencies, and a deployment that demanded encryption should not get
	// as far as a half-initialised process.
	if err := encryption.Validate(); err != nil {
		log.Fatalf("Refusing to start: %v", err)
	}
	encState, encRequired := encryption.Status()
	metrics.SetIntegrationEncryptionState(encState.String(), encRequired)

	// 2. Connect to PostgreSQL.
	db, err := postgres.NewDB(cfg.Database.DSN())
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	log.Println("Connected to PostgreSQL")

	// 3. Run database migrations.
	// WithAllowMissing lets the binary survive out-of-order migrations (e.g. a
	// hotfix branch with an older timestamp that lands after a newer one was
	// already applied on prod).  The CI gate in migration-check.yml still
	// enforces monotonic numbering at merge time — this is the prod-side safety net.
	if migErr := goose.Up(db.DB, "migrations", goose.WithAllowMissing()); migErr != nil {
		_ = db.Close()
		log.Fatalf("Failed to run migrations: %v", migErr)
	}
	log.Println("Database migrations applied")

	deps.atExit(func() { _ = db.Close() })

	// Register task distribution gauge collector (emits mesh_tasks on each scrape).
	prometheus.MustRegister(metrics.NewTaskDistributionCollector(db.DB))

	// 4. Create all repository instances.
	workspaceRepo := postgres.NewWorkspaceRepo(db)
	projectRepo := postgres.NewProjectRepo(db)
	taskRepo := postgres.NewTaskRepo(db)
	// HUMAN_GATE_DEFAULT_TIMEOUT_H (an internal ticket, Alex decision 2026-09-06: 24h
	// default) — wired here rather than a NewTaskRepo parameter to avoid touching
	// its 60+ existing call sites for one optional knob.
	taskRepo.SetDefaultGateTimeoutHours(cfg.HumanGate.DefaultTimeoutHours)
	taskStatusRepo := postgres.NewTaskStatusRepo(db)
	taskDependencyRepo := postgres.NewTaskDependencyRepo(db)
	commentRepo := postgres.NewCommentRepo(db)
	closedFollowUpRootRepo := postgres.NewClosedFollowUpRootRepo(db)
	closedFollowUpPendingRepo := postgres.NewClosedFollowUpPendingRepo(db)
	humanGateDecisionRepo := postgres.NewHumanGateDecisionRepo(db)
	taskExternalRefRepo := postgres.NewTaskExternalRefRepo(db)
	artifactRepo := postgres.NewArtifactRepo(db)
	documentRepo := postgres.NewDocumentRepo(db)
	documentAttachmentRepo := postgres.NewDocumentAttachmentRepo(db)
	documentCommentRepo := postgres.NewDocumentCommentRepo(db)
	documentWatchRepo := postgres.NewDocumentWatchRepo(db)
	agentRepo := postgres.NewAgentRepo(db)
	eventBusRepo := postgres.NewEventBusMessageRepo(db)
	activityLogRepo := postgres.NewActivityLogRepo(db)
	userRepo := postgres.NewUserRepo(db)
	refreshTokenRepo := postgres.NewRefreshTokenRepo(db)
	workspaceMemberRepo := postgres.NewWorkspaceMemberRepo(db)
	projectMemberRepo := postgres.NewProjectMemberRepo(db)
	webhookRepo := postgres.NewWebhookRepo(db)
	savedViewRepo := postgres.NewSavedViewRepo(db)
	vcsLinkRepo := postgres.NewVCSLinkRepo(db)
	integrationRepo := postgres.NewIntegrationRepo(db)

	// Extension registry. Builtin providers (internal/extensions/builtin)
	// register here; providers not yet moved keep the old per-provider path.
	extensionRegistry := extensions.NewRegistry()
	// Instance-level extension settings: MESH_* env is copied into the table
	// once (source=env, idempotent); for one release env stays a fallback
	// (DBInstanceStore warns when it is used).
	instanceSettingRepo := postgres.NewInstanceExtensionSettingRepo(db)

	// Secret store. Keep is reachable only when MESH_KEEP_URL/ORG_ID/WORKSPACE_ID/EVENTS_SECRET
	// are set; without it a keep: reference is answered as degraded, never as
	// an empty value. New values go to the provider named by the keep
	// extension's secret_provider instance setting (default builtin); keep
	// selected without a client fails the save instead of falling back.
	var keepErr error
	deps.secretRouter, keepErr = secretstore.NewKeepRouterFromEnv(os.Getenv)
	if keepErr != nil {
		log.Fatalf("[secrets] %v", keepErr) //nolint:gocritic // startup: a half-set Keep config must not start silently on builtin
	}
	if deps.secretRouter != nil {
		log.Printf("[secrets] Keep secret store enabled for keep: references")
	} else {
		deps.secretRouter = &secretstore.Router{Builtin: secretstore.Builtin{}}
	}
	deps.secretRouter.Selected = (&secretstore.ProviderSetting{Read: instanceSettingRepo.SecretProvider, TTL: 30 * time.Second}).Selected
	secretstore.SetDefault(deps.secretRouter)
	extensionEnvSources := extensions.BuiltinEnvSources()
	if bootErr := extensions.BootstrapInstanceSettings(context.Background(), instanceSettingRepo, cfg, extensionEnvSources); bootErr != nil {
		log.Printf("[extensions] instance settings bootstrap failed (env fallback stays in force): %v", bootErr)
	}
	// Builtin providers resolve instance-level settings through this store
	// (table first, env fallback with a warning).
	extensionInstanceStore := &extensions.DBInstanceStore{Repo: instanceSettingRepo, Cfg: cfg, Sources: extensionEnvSources}
	// The instance allow-list governs every resolution, not only connecting.
	extensionPolicyRepo := postgres.NewInstanceExtensionPolicyRepo(db)
	deps.extensionDeps = extensions.Deps{
		Conns: extensions.IntegrationConnections{Repo: integrationRepo},
		Inst:  extensionInstanceStore,
		Allow: extensionPolicyRepo,
	}
	extensionProviders := builtin.Providers(deps.extensionDeps)
	for _, p := range extensionProviders {
		if regErr := extensionRegistry.Register(p); regErr != nil {
			log.Fatalf("[extensions] register builtin %q: %v", p.Manifest().ID, regErr) //nolint:gocritic // startup: fail fast on a duplicate builtin provider
		}
	}
	// Sync only after registration: SyncBuiltin walks the registry, so run before
	// Register it wrote nothing and GET /extensions answered an empty list.
	if syncErr := extensionRegistry.SyncBuiltin(context.Background(), postgres.NewInstalledExtensionRepo(db)); syncErr != nil {
		log.Printf("[extensions] sync builtin extensions failed (registry stays usable): %v", syncErr)
	}
	if cfg.ExtensionRetention.PurgeEnabled && (cfg.ExtensionRetention.Interval <= 0 || cfg.ExtensionRetention.Retention <= 0) {
		log.Printf("[extensions] retention purge NOT started: interval (%s) and retention (%s) must be positive", cfg.ExtensionRetention.Interval, cfg.ExtensionRetention.Retention)
	} else if cfg.ExtensionRetention.PurgeEnabled {
		purgeCtx, purgeCancel := context.WithCancel(context.Background())
		deps.atExit(purgeCancel)
		go func() {
			ticker := time.NewTicker(cfg.ExtensionRetention.Interval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					res, purgeErr := postgres.PurgeExpiredExtensionData(purgeCtx, db, time.Now(), cfg.ExtensionRetention.Retention)
					if purgeErr != nil {
						log.Printf("[extensions] retention purge: %v", purgeErr)
					}
					for _, r := range res {
						log.Printf("[extensions] purged data of removed extension %q: %d table(s)", r.ExtensionID, len(r.Tables))
					}
				case <-purgeCtx.Done():
					return
				}
			}
		}()
	}
	extensionInstanceSettingsHandler := handler.NewExtensionInstanceSettingsHandler(instanceSettingRepo, instanceSettingRepo, extensionRegistry, extensionEnvSources).WithJournal(extensionPolicyRepo)
	extensionInstanceAdminHandler := handler.NewExtensionInstanceAdminHandler(extensionPolicyRepo, instanceSettingRepo, postgres.NewInstalledExtensionRepo(db), instanceSettingRepo).WithUsage(postgres.NewInstalledExtensionRepo(db))
	runtimeRepo := postgres.NewRuntimeRepo(db).WithAllowList(extensionPolicyRepo).WithStrictWorkspaceIsolation(cfg.Runtime.StrictWorkspaceIsolation)
	projectUpdateRepo := postgres.NewProjectUpdateRepo(db)
	initiativeRepo := postgres.NewInitiativeRepo(db)
	ruleRepo := postgres.NewRuleRepo(db)
	wsRuleRepo := postgres.NewWorkspaceRuleRepo(db)
	projRuleRepo := postgres.NewProjectRuleRepo(db)
	ruleViolationLogRepo := postgres.NewRuleViolationLogRepo(db)
	recurringRepo := postgres.NewRecurringRepo(db)
	taskTemplateRepo := postgres.NewTaskTemplateRepo(db)
	notificationRepo := postgres.NewNotificationRepo(db)
	autoTransRuleRepo := postgres.NewAutoTransitionRuleRepo(db)
	memoryRepo := postgres.NewMemoryRepo(db)
	schedulerStateRepo := postgres.NewSchedulerStateRepo(db)
	memoryEdgesRepo := postgres.NewMemoryEdgesRepo(db)
	memoryChunkRepo := postgres.NewMemoryChunkRepo(db)
	documentChunkRepo := postgres.NewDocumentChunkRepo(db)
	commentMentionRepo := postgres.NewCommentMentionRepo(db)
	commentDeliveryRepo := postgres.NewCommentDeliveryOutcomeRepo(db)
	documentCommentMentionRepo := postgres.NewDocumentCommentMentionRepo(db)

	deps.cfg = cfg
	deps.db = db
	deps.workspaceRepo = workspaceRepo
	deps.projectRepo = projectRepo
	deps.taskRepo = taskRepo
	deps.taskStatusRepo = taskStatusRepo
	deps.taskDependencyRepo = taskDependencyRepo
	deps.commentRepo = commentRepo
	deps.closedFollowUpRootRepo = closedFollowUpRootRepo
	deps.closedFollowUpPendingRepo = closedFollowUpPendingRepo
	deps.humanGateDecisionRepo = humanGateDecisionRepo
	deps.taskExternalRefRepo = taskExternalRefRepo
	deps.artifactRepo = artifactRepo
	deps.documentRepo = documentRepo
	deps.documentAttachmentRepo = documentAttachmentRepo
	deps.documentCommentRepo = documentCommentRepo
	deps.documentWatchRepo = documentWatchRepo
	deps.agentRepo = agentRepo
	deps.eventBusRepo = eventBusRepo
	deps.activityLogRepo = activityLogRepo
	deps.userRepo = userRepo
	deps.refreshTokenRepo = refreshTokenRepo
	deps.workspaceMemberRepo = workspaceMemberRepo
	deps.projectMemberRepo = projectMemberRepo
	deps.webhookRepo = webhookRepo
	deps.savedViewRepo = savedViewRepo
	deps.vcsLinkRepo = vcsLinkRepo
	deps.integrationRepo = integrationRepo
	deps.extensionRegistry = extensionRegistry
	deps.instanceSettingRepo = instanceSettingRepo
	deps.extensionInstanceStore = extensionInstanceStore
	deps.extensionPolicyRepo = extensionPolicyRepo
	deps.extensionInstanceSettingsHandler = extensionInstanceSettingsHandler
	deps.extensionInstanceAdminHandler = extensionInstanceAdminHandler
	deps.runtimeRepo = runtimeRepo
	deps.projectUpdateRepo = projectUpdateRepo
	deps.initiativeRepo = initiativeRepo
	deps.ruleRepo = ruleRepo
	deps.wsRuleRepo = wsRuleRepo
	deps.projRuleRepo = projRuleRepo
	deps.ruleViolationLogRepo = ruleViolationLogRepo
	deps.recurringRepo = recurringRepo
	deps.taskTemplateRepo = taskTemplateRepo
	deps.notificationRepo = notificationRepo
	deps.autoTransRuleRepo = autoTransRuleRepo
	deps.memoryRepo = memoryRepo
	deps.schedulerStateRepo = schedulerStateRepo
	deps.memoryEdgesRepo = memoryEdgesRepo
	deps.memoryChunkRepo = memoryChunkRepo
	deps.documentChunkRepo = documentChunkRepo
	deps.commentMentionRepo = commentMentionRepo
	deps.commentDeliveryRepo = commentDeliveryRepo
	deps.documentCommentMentionRepo = documentCommentMentionRepo
}
