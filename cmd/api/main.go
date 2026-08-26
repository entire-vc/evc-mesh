package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/pressly/goose/v3"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/entire-vc/evc-mesh/pkg/encryption"
	"github.com/entire-vc/evc-mesh/pkg/metrics"

	"github.com/redis/go-redis/v9"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/bootstrap"
	"github.com/entire-vc/evc-mesh/internal/config"
	"github.com/entire-vc/evc-mesh/internal/embedding"
	"github.com/entire-vc/evc-mesh/internal/eventbus"
	"github.com/entire-vc/evc-mesh/internal/handler"
	"github.com/entire-vc/evc-mesh/internal/integration/teamrelay"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/reconciler"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/internal/spark"
	"github.com/entire-vc/evc-mesh/internal/storage"
	wsHub "github.com/entire-vc/evc-mesh/internal/ws"
)

// Injected at build time via -ldflags. See docs/deploy.md.
var (
	BuildSHA     = "dev"
	BuildTime    = "unknown"
	BuildVersion = "dev"
	BuildEnv     = "dev"
)

func main() {
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

	defer func() { _ = db.Close() }()

	// Register task distribution gauge collector (emits mesh_tasks on each scrape).
	prometheus.MustRegister(metrics.NewTaskDistributionCollector(db.DB))

	// 4. Create all repository instances.
	workspaceRepo := postgres.NewWorkspaceRepo(db)
	projectRepo := postgres.NewProjectRepo(db)
	taskRepo := postgres.NewTaskRepo(db)
	taskStatusRepo := postgres.NewTaskStatusRepo(db)
	taskDependencyRepo := postgres.NewTaskDependencyRepo(db)
	commentRepo := postgres.NewCommentRepo(db)
	humanGateDecisionRepo := postgres.NewHumanGateDecisionRepo(db)
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
	memoryEdgesRepo := postgres.NewMemoryEdgesRepo(db)
	memoryChunkRepo := postgres.NewMemoryChunkRepo(db)
	commentMentionRepo := postgres.NewCommentMentionRepo(db)
	commentDeliveryRepo := postgres.NewCommentDeliveryOutcomeRepo(db)
	documentCommentMentionRepo := postgres.NewDocumentCommentMentionRepo(db)

	// 5. Create auth service.
	authService := auth.NewService(
		userRepo,
		refreshTokenRepo,
		workspaceRepo,
		workspaceMemberRepo,
		cfg.Auth.JWTSecret,
		auth.WithAllowRegistration(cfg.Auth.AllowRegistration),
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
	// rule is configured (#204c0311, live incident #815f703b).
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
	// #b052cdda) — without it a memory longer than the embedder's input window only ever
	// gets embedded from its first ~15% (#e8063a65).
	memoryService := service.NewMemoryService(memoryRepo, memoryEdgesRepo, embedder,
		service.MemoryWithProjectRepo(projectRepo),
		service.MemoryWithTaskRepo(taskRepo),
		service.MemoryWithDepRepo(taskDependencyRepo),
		service.MemoryWithEmbedConcurrency(cfg.Embedding.Concurrency),
		service.MemoryWithChunkRepo(memoryChunkRepo),
	)

	// Slack service sends notifications via Slack Incoming Webhooks when a workspace has
	// an active Slack integration configured. It is injected into webhookService below.
	slackService := service.NewSlackService(integrationRepo, cfg.Email.BaseURL)

	// Webhook service is created before taskService so it can be injected for agent wakeup dispatch.
	// SlackService is co-injected so that every Dispatch call also notifies Slack when configured.
	webhookService := service.NewWebhookService(webhookRepo, service.WithSlackService(slackService))

	projectIntegrationRepo := postgres.NewProjectIntegrationRepo(db)

	// One repo, two services, deliberately not one. secretService is what the
	// public CRUD handlers get and it cannot decrypt anything;
	// secretMaterializationService can, and reaches exactly one route.
	secretRepo := postgres.NewSecretRepo(db)
	secretService := service.NewSecretService(secretRepo)
	secretMaterializationService := service.NewSecretMaterializationService(secretRepo)
	secretMaterializeHandler := handler.NewSecretMaterializeHandler(secretMaterializationService)
	mw.CheckSpawnTokenConfigured()

	agentActLogRepo := postgres.NewAgentActivityLogRepo(db)
	// The cache wrapper is applied at construction so that EVERY consumer of
	// AgentService gets it — the three auth middlewares and the WebSocket
	// handshake all call Authenticate, and each of them was paying a full
	// cost-12 bcrypt comparison (~163 ms of CPU) on every single request from an
	// already-known key. Successful verifications only; failures still run
	// bcrypt, and rotation/deletion evict explicitly.
	agentService := service.NewCachedAgentAuth(
		service.NewAgentService(agentRepo, activityLogRepo, workspaceRepo),
		service.AgentAuthCacheTTL,
	)
	// Wire agent activity log repository for monitoring.
	if configurable, ok := agentService.(service.AgentServiceConfigurable); ok {
		configurable.SetAgentActivityLogRepo(agentActLogRepo)
	}

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

	// RulesService (assignment/workflow config) is created before taskService for auto-assign injection.
	rulesService := service.NewRulesServiceWithOptions(wsRuleRepo, projRuleRepo, ruleViolationLogRepo, agentRepo, workspaceMemberRepo, workspaceRepo, projectRepo,
		service.WithRulesRuleRepo(ruleRepo),
		service.WithRulesStatusRepo(taskStatusRepo),
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
	emailSvc := service.NewEmailService(cfg.Email)
	if emailSvc.Enabled() {
		log.Printf("Email ready: sending via %s:%d as %q", cfg.Email.Host, cfg.Email.Port, cfg.Email.From)
	} else {
		log.Printf("Email is not configured (SMTP_HOST is empty) — no invitation or notification emails will be sent. Invites still work: the invite link is shown in the UI for you to pass on. Set SMTP_HOST/SMTP_FROM to send email instead.")
	}

	// Telegram — bot API client + the manager that long-polls every active
	// workspace bot and handles /start. Created here, ahead of
	// notificationService, for the same reason as emailSvc: the dispatch
	// channel below needs it as a dependency.
	telegramClient := service.NewTelegramClient()
	telegramBotManager := service.NewTelegramBotManager(
		telegramClient, integrationRepo, notificationRepo, userRepo, projectRepo, workspaceRepo,
	)

	// Notification service for in-app push notifications to workspace users.
	// Created before taskService and commentService so it can be injected as a dependency.
	notificationService := service.NewNotificationService(notificationRepo,
		service.WithPushService(pushService),
		service.WithEmailService(emailSvc, userRepo, cfg.Email.BaseURL),
		service.WithTelegramService(telegramClient, integrationRepo, workspaceRepo, projectRepo),
	)

	// vcsIntegrationResolver replaces the process-start-only githubClient/
	// gitlabClient construction that used to live here (#33a4bb57, §C1 of
	// specsintegration-provider-contract): it resolves a GitHub/GitLab
	// connection fresh on EVERY call — the done-evidence gate's live
	// re-check, and the webhook receiver's signature validation — honoring
	// a workspace's own active integration row over these env values, per
	// §4's resolution order. The env values below are what governs when no
	// workspace has configured its own connection (the pre-#33a4bb57
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

	taskService := service.NewTaskService(taskRepo, taskStatusRepo, taskDependencyRepo, activityLogRepo,
		service.WithCustomFieldService(customFieldService),
		service.WithProjectRepo(projectRepo),
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
		// Enables stale-cursor rejection on list_tasks (ADR-0004). Without
		// this, List behaves exactly as it did before the option existed —
		// see WithTaskListRevisionRepo's doc comment.
		service.WithTaskListRevisionRepo(postgres.NewTaskListRevisionRepo(db)),
	)

	// Wire auto-transition service. It calls taskService.MoveTask, so taskService must already
	// exist. We inject it back via the configurable interface to trigger transitions on status
	// changes without introducing an import cycle.
	autoTransitionSvc := service.NewAutoTransitionService(taskRepo, taskStatusRepo, taskDependencyRepo, taskService, autoTransRuleRepo)
	if configurable, ok := taskService.(service.TaskServiceAutoTransitionConfigurable); ok {
		configurable.SetAutoTransitionService(autoTransitionSvc)
	}

	taskStatusService := service.NewTaskStatusService(taskStatusRepo, taskRepo, activityLogRepo)

	// Real service implementations (replacing stubs from earlier sprints).
	mentionService := service.NewMentionService(commentMentionRepo)
	documentMentionService := service.NewDocumentMentionService(documentCommentMentionRepo)

	commentService := service.NewCommentService(commentRepo, taskRepo, activityLogRepo,
		service.WithCommentAgentNotify(agentNotifySvc),
		service.WithCommentAgentService(agentService),
		service.WithCommentUserRepo(userRepo),
		service.WithCommentMentionRepo(commentMentionRepo),
		service.WithCommentDeliveryOutcomeRepo(commentDeliveryRepo),
		service.WithCommentWSPublisher(wsPublisher),
		service.WithCommentStatusRepo(taskStatusRepo),
		service.WithCommentProjectRepo(projectRepo),
		service.WithCommentContextCacheInvalidator(ctxCacheSvc),
		service.WithCommentNotificationService(notificationService),
		service.WithCommentTaskService(taskService),
		service.WithHumanGateDecisionRepo(humanGateDecisionRepo),
	)
	depService := service.NewTaskDependencyService(taskDependencyRepo, taskRepo, activityLogRepo)
	activityLogService := service.NewActivityLogService(activityLogRepo)

	// Member services.
	workspaceMemberService := service.NewWorkspaceMemberService(workspaceMemberRepo, userRepo, projectMemberRepo, activityLogRepo)
	projectMemberService := service.NewProjectMemberService(projectMemberRepo, workspaceMemberRepo, projectRepo,
		service.WithAgentRepo(agentRepo),
	)

	// Invite service (email-link flow).
	//
	// MESH_BASE_URL decides what every invite link points at. Left unset it
	// falls back to the Vite dev-server URL, and a deployed instance hands out
	// http://localhost:5173/accept-invite/<token> links that resolve to the
	// invitee's own laptop. Nothing downstream can detect that, and the operator
	// finds out only when the person they invited says the link is dead — so say
	// it here, once, at boot.
	if cfg.Email.BaseURLIsDefault() {
		log.Printf("[config] WARNING: MESH_BASE_URL is not set — workspace invite links will point at %s", cfg.Email.BaseURL)
		log.Printf("[config] WARNING: set MESH_BASE_URL to the public URL of your web UI (e.g. https://mesh.example.com) or invitees get an unusable link.")
	} else {
		log.Printf("[config] Invite links will be built from MESH_BASE_URL=%s", cfg.Email.BaseURL)
	}
	// SMTP_HOST set but SMTP_FROM not: previously this silently sent as
	// noreply@mesh.entire.host — our domain, on someone else's mail server.
	// Now it's an empty From header, which most SMTP servers reject outright
	// at send time. Either way the operator needs to know before their first
	// invite fails; say so once, at boot, the same as the base-URL check above.
	if cfg.Email.Host != "" && cfg.Email.From == "" {
		log.Printf("[config] WARNING: SMTP_HOST is set but SMTP_FROM is not — outbound email will use an empty From address and likely be rejected by your mail server. Set SMTP_FROM to an address your SMTP host is allowed to send as.")
	}
	inviteRepo := postgres.NewInviteRepo(db)
	// emailSvc was created earlier, ahead of notificationService — reused here
	// rather than constructed again so the two channels share one SMTP client
	// and one boot-time "Email ready"/"not configured" log line.
	inviteService := service.NewInviteService(inviteRepo, userRepo, workspaceMemberRepo, workspaceRepo, emailSvc, authService, cfg.Email.BaseURL)

	savedViewService := service.NewSavedViewService(savedViewRepo)
	vcsLinkService := service.NewVCSLinkService(vcsLinkRepo,
		service.WithVCSTaskRepo(taskRepo),
		service.WithVCSStatusRepo(taskStatusRepo),
		service.WithVCSTaskService(taskService),
		service.WithVCSCommentService(commentService),
	)
	integrationService := service.NewIntegrationService(integrationRepo)
	analyticsService := service.NewAnalyticsService(db)
	projectUpdateService := service.NewProjectUpdateService(projectUpdateRepo, projectRepo, taskRepo, taskStatusRepo)
	initiativeService := service.NewInitiativeService(initiativeRepo, projectRepo, db)
	triageService := service.NewTriageService(taskRepo)

	recurringService := service.NewRecurringService(recurringRepo, taskService,
		service.WithCommentRepoForRecurring(commentRepo),
		service.WithArtifactRepoForRecurring(artifactRepo),
	)

	taskTemplateService := service.NewTaskTemplateService(taskTemplateRepo, taskService)

	// rulesService, customFieldService, and notificationService were already created above (before taskService).

	// Initialize S3 storage client for artifacts and document bodies.
	var artifactService service.ArtifactService
	// documentStore stays nil until the client is proven usable — a typed-nil
	// *S3Client in an interface is not nil, and the service's "storage not
	// configured" branch tests exactly that.
	var documentStore service.DocumentStore
	var attachmentStore service.StorageClient
	s3Client, s3Err := storage.NewS3Client(
		cfg.S3.Endpoint,
		cfg.S3.AccessKeyID,
		cfg.S3.SecretAccessKey,
		cfg.S3.Bucket,
		cfg.S3.Region,
		cfg.S3.UseSSL,
	)
	if s3Err != nil {
		log.Printf("WARNING: S3 storage unavailable, artifact uploads will fail: %v", s3Err)
		// Use a nil-storage artifact service that will return errors on upload.
		// This is intentional: we no longer use a stub that silently discards uploads.
		artifactService = service.NewArtifactService(artifactRepo, nil, activityLogRepo)
	} else {
		if cfg.S3.PublicURL != "" {
			s3Client.SetPublicURL(cfg.S3.PublicURL)
			log.Printf("S3 presigned URLs will use public URL: %s", cfg.S3.PublicURL)
		}

		// Create the bucket if it is not there yet. Nothing else ever did — not
		// the compose file, not a migration, not the first upload — so a fresh
		// self-hosted install had an empty MinIO and every artifact and
		// workspace-icon upload failed against storage that was itself healthy.
		//
		// At boot rather than on first upload: the operator learns about a
		// broken storage config once, in the startup log they are already
		// watching, instead of hours later as a user-facing error. Upload still
		// re-creates the bucket on demand, which covers storage that comes up
		// after the API and a bucket deleted while running.
		//
		// Best-effort by design: storage being down must not stop the API from
		// serving the rest of the product.
		ensureCtx, cancelEnsure := context.WithTimeout(context.Background(), 15*time.Second)
		bucketErr := s3Client.EnsureBucket(ensureCtx)
		cancelEnsure()
		if bucketErr != nil {
			log.Printf("WARNING: object storage bucket %q on %s is not usable — artifact and workspace-icon uploads will fail until this is fixed: %v",
				s3Client.Bucket(), s3Client.Endpoint(), bucketErr)
		} else {
			log.Printf("Object storage ready: bucket %q on %s", s3Client.Bucket(), s3Client.Endpoint())
		}

		artifactService = service.NewArtifactService(artifactRepo, s3Client, activityLogRepo)
		documentStore = s3Client
		attachmentStore = s3Client
	}
	// Document subscriptions. Built before the document service because that
	// service takes it: every write path folds an edit into a pending notice, and
	// a document created without one would have no author subscribed to it.
	//
	// The quiet window is how long an editor must stop typing before their
	// session is announced. It is configurable because the right value is a
	// judgement about how people work, not a property of the system: it only has
	// to sit far above the editor's 2-second autosave debounce and far below the
	// length of a sitting.
	documentWatchService := service.NewDocumentWatchService(
		documentWatchRepo, documentRepo, notificationService, agentNotifySvc,
		documentWatchQuietWindow(),
	)

	// Constructed here, ahead of documentService, because documentService's
	// optional Team Relay refresher needs it to resolve a project's share/agent
	// key on every open of a mounted copy.
	projectIntegrationService := service.NewProjectIntegrationService(projectIntegrationRepo)
	teamRelayMountService := service.NewTeamRelayMountService(documentRepo, documentStore, projectIntegrationService, projectIntegrationRepo)

	// The comment repository is here because a body write moves the comments
	// anchored into that body: PATCH re-resolves every anchor against the markdown
	// it just stored, and nulls the ones whose text is gone. It is a required
	// argument, not an option — see the field's note in documentService.
	documentService := service.NewDocumentService(documentRepo, documentStore, projectRepo, documentCommentRepo,
		service.WithDocumentWatch(documentWatchService),
		service.WithTeamRelayRefresher(teamRelayMountService),
		// Same collaborator on both sides of the copy's lifecycle: it refreshes a
		// copy from its original on open, and pushes an edit back to that original
		// on save. Wiring one without the other is what produces a copy that can
		// drift — see the write-back ordering note in updateOnce.
		service.WithTeamRelayWriter(teamRelayMountService))

	// The attachment service takes the full StorageClient, not documentStore: an
	// attachment is fetched by the browser through a presigned URL (an <img> cannot
	// send an Authorization header), so it needs the GetPresignedURL that the
	// narrower document-body port deliberately omits. attachmentStore stays nil for
	// the same reason documentStore does — a typed-nil *S3Client in an interface is
	// not nil, and the service's "storage not configured" branch tests exactly that.
	documentAttachmentService := service.NewDocumentAttachmentService(documentAttachmentRepo, documentRepo, attachmentStore)

	// It takes the document repository so every entry point can resolve the
	// document inside the caller's workspace before touching a comment, and the
	// document SERVICE for the one path that needs the markdown itself: checking
	// that an anchor's byte offsets point at the anchor's own quote. The body
	// lives in object storage, so the repository alone cannot answer that — and a
	// guard reading an always-empty body would reject every anchored comment.
	//
	// The @-mention options are what make a mention in a document comment arrive
	// rather than merely be stored. All six are required for the feature to be
	// whole: agentService and userRepo resolve a slug (and, crucially, decide
	// whether it resolves at all — without them the service refuses to guess and
	// says so in the log), documentCommentMentionRepo records who was named,
	// agentNotifySvc is an agent's channel, and notificationService plus
	// wsPublisher are a person's.
	documentCommentService := service.NewDocumentCommentService(
		documentCommentRepo, documentRepo, documentService,
		service.WithDocumentCommentAgentService(agentService),
		service.WithDocumentCommentUserRepo(userRepo),
		service.WithDocumentCommentMentionRepo(documentCommentMentionRepo),
		service.WithDocumentCommentAgentNotifier(agentNotifySvc),
		service.WithDocumentCommentNotificationService(notificationService),
		service.WithDocumentCommentWSPublisher(wsPublisher),
		service.WithDocumentCommentWatch(documentWatchService),
	)

	// Wire Team Relay publisher into artifact service (best-effort; fires on upload).
	// projectIntegrationService itself is constructed earlier, alongside
	// teamRelayMountService — see the comment there.
	relayClient := teamrelay.NewClient(projectIntegrationRepo, taskRepo, projectRepo)
	if asc, ok := artifactService.(service.ArtifactServiceConfigurable); ok {
		asc.SetRelayPublisher(relayClient)
	}

	// 6a. Connect to NATS and Redis for the event bus (graceful: continue without if unavailable).
	var eb *eventbus.EventBus
	ebCfg := eventbus.EventBusConfig{
		NATSUrl:        cfg.NATS.URL,
		NATSMonitorURL: cfg.NATS.MonitorURL,
		RedisAddr:      cfg.Redis.Addr(),
		RedisPassword:  cfg.Redis.Password,
		RedisDB:        cfg.Redis.DB,
		NATSReplicas:   cfg.NATS.Replicas,
		StreamMaxAge:   cfg.NATS.StreamMaxAge,
		StreamMaxBytes: cfg.NATS.StreamMaxBytes,
		MaxMsgSize:     cfg.NATS.MaxMsgSize,
	}
	eb, err = eventbus.New(context.Background(), ebCfg, eventBusRepo)
	if err != nil {
		// Loud degradation, not silent — mirrors the MESH_TRUSTED_PROXIES
		// pattern below: a control nobody can see failing gets trusted and
		// stops being checked. Surfaced three ways: this WARN, the /health
		// field below, and the mesh_event_bus_enabled gauge on /metrics.
		log.Printf("WARNING: Event bus unavailable, running without NATS/Redis: %v — "+
			"event publishing (WS broadcast, cross-agent notifications, event history) "+
			"is disabled for this process", err)
		eb = nil
	} else {
		// Wire the event bus publisher into the event bus service.
		if configurable, ok := eventBusService.(service.EventBusServiceConfigurable); ok {
			configurable.SetEventBus(eb, workspaceRepo, projectRepo)
		}
		// Start background workers (PG writer + cleanup).
		eb.Start()
	}
	metrics.SetEventBusEnabled(eb != nil)
	// Wire memory service into eventBusService for memory extraction on Publish().
	// Done outside the NATS block so memory extraction works even without NATS.
	if configurable, ok := eventBusService.(service.EventBusServiceConfigurable); ok {
		configurable.SetMemoryService(memoryService)
	}

	// 7. Create all handler instances.
	authHandler := handler.NewAuthHandler(authService)
	workspaceHandler := handler.NewWorkspaceHandler(workspaceService)
	if s3Client != nil {
		workspaceHandler.WithStorage(s3Client)
	}
	projectHandler := handler.NewProjectHandler(projectService)
	sessionRepo := postgres.NewSessionRepo(db)
	taskHandler := handler.NewTaskHandlerWithSessions(taskService, sessionRepo).
		WithCommentService(commentService)
	statusHandler := handler.NewTaskStatusHandler(taskStatusService)
	commentHandler := handler.NewCommentHandler(commentService, taskService)
	humanGateDecisionHandler := handler.NewHumanGateDecisionHandler(commentService, taskService)
	artifactHandler := handler.NewArtifactHandler(artifactService, taskService)
	documentHandler := handler.NewDocumentHandler(documentService)
	documentAttachmentHandler := handler.NewDocumentAttachmentHandler(documentAttachmentService)
	documentCommentHandler := handler.NewDocumentCommentHandler(documentCommentService)
	documentWatchHandler := handler.NewDocumentWatchHandler(documentWatchService)
	depHandler := handler.NewDependencyHandler(depService, taskService)
	agentHandler := handler.NewAgentHandlerWithEvents(agentService, taskService, taskStatusService, agentNotifyRedis, agentEventsRepo, sessionRepo)
	eventHandler := handler.NewEventHandler(eventBusService)
	activityHandler := handler.NewActivityHandler(activityLogService)
	secretHandler := handler.NewSecretHandler(secretService, activityLogService)
	customFieldHandler := handler.NewCustomFieldHandler(customFieldService)
	taskContextHandler := handler.NewTaskContextHandlerWithCache(taskService, commentService, artifactService, depService, eventBusService, ctxCacheSvc, initiativeRepo)
	webhookHandler := handler.NewWebhookHandler(webhookService)
	savedViewHandler := handler.NewSavedViewHandler(savedViewService)
	vcsLinkHandler := handler.NewVCSLinkHandler(vcsLinkService,
		handler.WithVCSIntegrationResolver(vcsIntegrationResolver), // resolves webhook secrets fresh per request (see above); supersedes the static WithGitHubWebhookSecret/WithGitLabWebhookSecret options
		handler.WithWebhookDedupStore(handler.NewRedisWebhookDedupStore(agentNotifyRedis)),
	)
	integrationHandler := handler.NewIntegrationHandler(integrationService, telegramClient, telegramBotManager)
	analyticsHandler := handler.NewAnalyticsHandler(analyticsService)
	projectUpdateHandler := handler.NewProjectUpdateHandler(projectUpdateService)
	initiativeHandler := handler.NewInitiativeHandler(initiativeService)
	triageHandler := handler.NewTriageHandler(triageService)
	ruleHandler := handler.NewRuleHandler(ruleService)
	rulesHandler := handler.NewRulesHandler(rulesService)
	recurringHandler := handler.NewRecurringHandler(recurringService)
	taskTemplateHandler := handler.NewTaskTemplateHandler(taskTemplateService)
	workspaceMemberHandler := handler.NewWorkspaceMemberHandler(workspaceMemberService)
	inviteHandler := handler.NewInviteHandler(inviteService, authService)
	projectMemberHandler := handler.NewProjectMemberHandler(projectMemberService)
	notificationHandler := handler.NewNotificationHandler(notificationService, workspaceMemberRepo)
	pushSubscriptionHandler := handler.NewPushSubscriptionHandler(pushService)
	autoTransHandler := handler.NewAutoTransitionHandler(autoTransitionSvc)
	memoryHandler := handler.NewMemoryHandler(memoryService, workspaceMemberRepo)
	mentionHandler := handler.NewMentionHandler(mentionService)
	documentMentionHandler := handler.NewDocumentMentionHandler(documentMentionService)
	projectIntegrationHandler := handler.NewProjectIntegrationHandler(projectIntegrationService)
	trSearchHandler := handler.NewTrSearchHandler(projectIntegrationService)
	trDocumentHandler := handler.NewTrDocumentHandler(projectIntegrationService)
	trMountHandler := handler.NewTrMountHandler(teamRelayMountService)
	canonicalUpdatesHandler := handler.NewCanonicalUpdatesHandler(memoryService, sessionRepo, agentService)
	mentionablesService := service.NewMentionablesService(agentRepo, userRepo)
	mentionablesHandler := handler.NewMentionablesHandler(mentionablesService)

	// 8. Create Echo instance with global middleware.
	e := echo.New()
	e.HideBanner = true

	// MESH_TRUSTED_PROXIES gates whether c.RealIP() (used by
	// mw.RateLimitKeyByIP, and therefore by the /auth/login per-IP limiter
	// registered below) can be trusted. See RateLimitKeyByIP's doc comment
	// (internal/middleware/ratelimit.go) for the incident this exists to
	// prevent: a reverse-proxy hop that does not itself trust its own
	// upstream overwrites X-Forwarded-For with its own peer address, so
	// EVERY external client resolves to the SAME IP and a per-IP limiter
	// becomes one shared bucket for the whole internet — the opposite of
	// its purpose, and a built-in DoS (#5d759aad).
	//
	// Configuring this does NOT fix a misconfigured upstream proxy — our own
	// Caddy config on mesh-vm is deploy/caddy/mesh-vm.Caddyfile, tracked
	// separately (PR #677). It only tells mesh-api which additional network
	// ranges, beyond the loopback/link-local/private-net Echo trusts by
	// default, are allowed to relay a client IP via X-Forwarded-For.
	//
	// Left unset, mesh-api does not pretend to have per-client IP
	// granularity it cannot verify: e.IPExtractor stays at Echo's untouched
	// default, and the /auth/login per-IP limiter below is disabled outright
	// (see loginGroup) rather than silently operating as one shared bucket.
	var trustedProxyNets []*net.IPNet
	for _, cidr := range cfg.RateLimit.TrustedProxies {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			log.Printf("[config] MESH_TRUSTED_PROXIES: ignoring invalid CIDR %q: %v", cidr, err)
			continue
		}
		trustedProxyNets = append(trustedProxyNets, ipNet)
	}
	ipTrusted := len(trustedProxyNets) > 0
	if ipTrusted {
		trustOpts := make([]echo.TrustOption, 0, len(trustedProxyNets))
		for _, ipNet := range trustedProxyNets {
			trustOpts = append(trustOpts, echo.TrustIPRange(ipNet))
		}
		e.IPExtractor = echo.ExtractIPFromXFFHeader(trustOpts...)
		log.Printf("[config] MESH_TRUSTED_PROXIES configured (%d range(s)) — per-IP rate "+
			"limiting on /auth/login is ACTIVE", len(trustedProxyNets))
	} else {
		// Loud degradation, not silent — per this file's own deep-verify
		// discipline, a control nobody can see failing gets trusted and
		// stops being checked. Surfaced three ways: this WARN, the /health
		// field below, and the mesh_client_ip_trusted gauge on /metrics.
		log.Printf("[config] MESH_TRUSTED_PROXIES is unset — client IP cannot be verified " +
			"through the reverse-proxy chain, so /auth/login has NO per-IP rate-limit " +
			"granularity (it is disabled, not silently shared across every client). " +
			"Brute-force protection for /auth/login now relies solely on the per-account " +
			"failed-login lockout (MESH_RATE_LIMIT_AUTH_MAX_FAILURES, default 10/15min). " +
			"Set MESH_TRUSTED_PROXIES to the CIDR(s) of your trusted reverse-proxy hop(s) " +
			"to restore per-IP granularity.")
	}
	metrics.SetClientIPTrusted(ipTrusted)

	// Prometheus metrics — registered early so every request is counted.
	e.Use(mw.Metrics())

	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogURI:    true,
		LogStatus: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			log.Printf("%s %s -> %d", c.Request().Method, redactInviteToken(v.URI), v.Status)
			return nil
		},
	}))
	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())
	// AllowCredentials lets the browser store/send the httpOnly refresh
	// cookie on cross-origin requests (credentials: "include" — see
	// web/src/lib/api.ts). It is a no-op for the common same-origin
	// deployment (frontend and API behind one reverse proxy), where cookies
	// already flow regardless of any CORS header. It only matters once
	// MESH_CORS_ORIGINS names actual origins — the echo middleware refuses
	// to combine AllowCredentials with a literal "*" origin (that combination
	// is invalid per the Fetch spec and browsers reject it), so leaving
	// MESH_CORS_ORIGINS at its default wildcard silently blocks credentialed
	// cross-origin login, not same-origin login.
	if len(cfg.CORS.AllowOrigins) == 1 && cfg.CORS.AllowOrigins[0] == "*" {
		log.Printf("[config] MESH_CORS_ORIGINS is unset (default \"*\") — cross-origin login/refresh " +
			"will not work (browsers refuse credentialed requests against a wildcard origin). " +
			"Same-origin deployments (frontend served from the API's own host) are unaffected.")
	}
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins:     cfg.CORS.AllowOrigins,
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Authorization", "Content-Type", "X-Agent-Key", "X-Request-ID"},
		AllowCredentials: true,
	}))

	// Prometheus metrics scrape endpoint. Gated by MESH_METRICS_TOKEN when
	// set (mw.MetricsAuth is a no-op otherwise) — the self-host
	// docker-compose.prod.yml requires the var and configures Prometheus's
	// scrape config with the matching bearer_token_file, since that compose
	// stack publishes this port and has no front proxy of its own. An
	// unauthenticated deployment (e.g. internal prod, gated by Caddy
	// upstream) is unaffected by leaving the var unset.
	e.GET("/metrics", echo.WrapHandler(promhttp.Handler()), mw.MetricsAuth(cfg.Server.MetricsToken))

	// Health check.
	e.GET("/health", func(c echo.Context) error {
		return c.JSON(200, map[string]any{
			"status":            "ok",
			"service":           "evc-mesh-api",
			"client_ip_trusted": ipTrusted,
			"event_bus_enabled": eb != nil,
		})
	})

	// Build version — public, no auth. All three paths route through Caddy's /api/* block.
	// spark_enabled/spark_url ride along here rather than a dedicated endpoint: they're
	// the instance capabilities the frontend needs to know before the user picks a
	// workspace (the Spark Catalog nav link, gated by cfg.Spark.Enabled above — same
	// source of truth the route registration itself uses — and the "View on Spark"
	// links, which must point at whatever catalog this deployment configured rather
	// than a hardcoded vendor domain).
	versionHandler := func(c echo.Context) error {
		return c.JSON(200, map[string]any{
			"commit":        BuildSHA,
			"build_time":    BuildTime,
			"version":       BuildVersion,
			"environment":   BuildEnv,
			"service":       "evc-mesh-api",
			"spark_enabled": cfg.Spark.Enabled,
			"spark_url":     cfg.Spark.URL,
		})
	}
	e.GET("/api/version", versionHandler)
	e.GET("/api/v1/version", versionHandler)
	e.GET("/api/v1/healthz/version", versionHandler)

	// 8a. Shared Redis client used by the WebSocket hub and the rate limiter.
	// A single client is created here so all consumers share the same connection
	// pool rather than each opening independent connections.
	sharedRedis := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr(),
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})

	// Per-account failed-login lockout for /auth/login — the primary
	// brute-force defense (see mw.LoginLockout's doc comment). Wired
	// unconditionally: unlike the IP-based limiter below it does not depend
	// on trusting the reverse-proxy chain at all.
	authHandler.WithLoginLockout(mw.NewLoginLockout(
		sharedRedis, cfg.RateLimit.AuthMaxFailures, cfg.RateLimit.AuthLockoutWindow,
	))

	// WebSocket Hub for real-time event streaming.
	wsRedis := sharedRedis
	hub := wsHub.NewHub(wsRedis)
	hubCtx, hubCancel := context.WithCancel(context.Background())
	go hub.Run(hubCtx)
	log.Println("WebSocket hub started")

	// Activity tracker: batches last_heartbeat updates for API-calling agents.
	activityTracker := mw.NewActivityTracker(agentRepo)
	activityCtx, activityCancel := context.WithCancel(context.Background())
	go activityTracker.Run(activityCtx)
	log.Println("Agent activity tracker started")

	// Document-watch sweeper: turns pending change-notices into notifications
	// once their author has stopped typing for the quiet window.
	//
	// A 60-second tick against a 5-minute default window: the tick is the
	// granularity of the delay, not the delay itself, and a notice is never sent
	// early because the quiet test is a timestamp comparison inside the claim,
	// not a property of when the sweeper happened to run. Two replicas running
	// this at once is safe — the claim is atomic (FOR UPDATE SKIP LOCKED).
	watchSweepCtx, watchSweepCancel := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if n, err := documentWatchService.SweepPendingNotices(watchSweepCtx); err != nil {
					log.Printf("[doc-watch] ERROR sweeping pending change notices: %v", err)
				} else if n > 0 {
					log.Printf("[doc-watch] dispatched %d coalesced document change notice(s)", n)
				}
			case <-watchSweepCtx.Done():
				return
			}
		}
	}()
	defer watchSweepCancel()

	// Checkout reaper: periodically releases orphan task-locks whose TTL has expired.
	// Complements the lazy expiry in AtomicCheckout — handles tasks that no agent
	// retries after the original holder's session dies.
	// leaseReaper additionally moves expired in_progress tasks back to todo so that
	// the capacity slot (max_in_progress) is freed for other agents.
	leaseReaper := service.NewCheckoutLeaseReaper(taskRepo, taskStatusRepo, commentRepo, taskService, agentNotifySvc)
	reaperCtx, reaperCancel := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// Phase 1: move expired in_progress tasks back to todo (frees capacity).
				if n, err := leaseReaper.SweepExpiredLeases(reaperCtx); err != nil {
					log.Printf("[lease-reaper] ERROR sweeping expired leases: %v", err)
				} else if n > 0 {
					log.Printf("[lease-reaper] moved %d expired lease(s) back to todo", n)
				}
				// Phase 2: clear stale checkout fields on any remaining tasks.
				if n, err := taskRepo.ReleaseExpiredCheckouts(reaperCtx); err != nil {
					log.Printf("[checkout-reaper] ERROR releasing expired locks: %v", err)
				} else if n > 0 {
					log.Printf("[checkout-reaper] released %d expired checkout lock(s)", n)
				}
			case <-reaperCtx.Done():
				return
			}
		}
	}()

	// Telegram bot manager: long-polls getUpdates for every workspace with an
	// active bot, so a fresh /start lands within one poll cycle of being sent
	// rather than waiting for the next deploy/restart.
	telegramCtx, telegramCancel := context.WithCancel(context.Background())
	go telegramBotManager.Start(telegramCtx)
	log.Println("Telegram bot manager started")
	log.Println("Checkout reaper started (interval: 60s)")

	// WebSocket upgrade endpoint. It sits on the root instance, ahead of the
	// /api/v1 group, so none of the group middleware below (WorkspaceRLS,
	// RequireWorkspaceMemberScoped) can ever run on it: both authentication and
	// the cross-tenant check are the handler's own responsibility. The authorizer
	// is what lets it apply the same membership rule as the REST API instead of a
	// second, divergent copy.
	e.GET("/ws", wsHub.Handler(hub, authService, agentService, wsHub.NewDBAuthorizer(db)))

	// 9. Register all routes.
	v1 := e.Group("/api/v1")

	// --- Public routes (no auth required) ---

	// register: tight per-IP brute-force/spam protection (5 RPM default).
	// Unlike /login (below), there is no account to key a failure-lockout
	// counter on before one exists — per-IP is the only defense here, so
	// its Enabled flag is left as-is (cfg.RateLimit.Enabled only) rather
	// than additionally gated on trusted-proxy config. On an untrustworthy
	// topology this keeps register's existing (imperfect: shared-bucket)
	// protection rather than leaving it with none at all — a call this
	// card does not need to make, since register brute-forcing isn't the
	// credential-stuffing threat #5d759aad is about.
	registerGroup := v1.Group("/auth")
	registerGroup.Use(mw.RateLimit(mw.RateLimitConfig{
		Enabled:     cfg.RateLimit.Enabled,
		RPM:         cfg.RateLimit.AuthRPM,
		KeyFunc:     mw.RateLimitKeyByIP,
		RedisClient: sharedRedis,
		Name:        "auth-register",
	}))
	registerGroup.POST("/register", authHandler.Register)

	// login: per-IP limiting here is now a SECONDARY layer, only enabled
	// when the client IP can actually be trusted (ipTrusted, computed at
	// Echo-instance setup above from MESH_TRUSTED_PROXIES). The PRIMARY
	// defense is the per-account failed-login lockout wired into
	// authHandler above (mw.LoginLockout) — it works regardless of
	// reverse-proxy topology, which per-IP limiting fundamentally cannot.
	// Left enabled with an untrustworthy IP, this middleware reproduces the
	// exact defect #5d759aad reports: c.RealIP() resolves to ONE address
	// for all external traffic, so this "per-IP" limiter becomes a single
	// shared 5 RPM bucket for the entire internet — worse than no limiter,
	// since it also denies real users. Disabling it here is what "IP-ключ
	// НЕ включается" (the accepted decision, task comment) means in code.
	loginGroup := v1.Group("/auth")
	loginGroup.Use(mw.RateLimit(mw.RateLimitConfig{
		Enabled:     cfg.RateLimit.Enabled && ipTrusted,
		RPM:         cfg.RateLimit.AuthRPM,
		KeyFunc:     mw.RateLimitKeyByIP,
		RedisClient: sharedRedis,
		Name:        "auth-login",
	}))
	loginGroup.POST("/login", authHandler.Login)

	// GET /auth/config: unauthenticated, no side effects — the login/register
	// pages poll this once to decide whether to show the "Create an account"
	// link. No rate limit needed (read-only, no brute-force surface).
	v1.GET("/auth/config", authHandler.Config)

	// /auth/refresh: separate, fleet-safe per-IP limit (60 RPM default).
	// Refresh requires a valid refresh token — credential brute-force is not
	// a concern. A fleet of agents on a shared egress IP must all be able to
	// refresh their JWT tokens concurrently without hitting a shared 5-RPM cap.
	refreshGroup := v1.Group("/auth")
	refreshGroup.Use(mw.RateLimit(mw.RateLimitConfig{
		Enabled:     cfg.RateLimit.Enabled,
		RPM:         cfg.RateLimit.RefreshRPM,
		KeyFunc:     mw.RateLimitKeyByIP,
		RedisClient: sharedRedis,
		Name:        "auth-refresh",
	}))
	refreshGroup.POST("/refresh", authHandler.Refresh)

	// Public invite acceptance endpoints (rate-limited same as auth).
	invitePublicGroup := v1.Group("/invites")
	invitePublicGroup.Use(mw.RateLimit(mw.RateLimitConfig{
		Enabled:     cfg.RateLimit.Enabled,
		RPM:         cfg.RateLimit.AuthRPM,
		KeyFunc:     mw.RateLimitKeyByIP,
		RedisClient: sharedRedis,
		Name:        "invite-accept",
	}))
	invitePublicGroup.GET("/:token", inviteHandler.GetByToken)
	invitePublicGroup.POST("/:token/accept", inviteHandler.Accept)

	// Workspace icon (read). Unauthenticated on purpose, and it has to be: the
	// UI renders it as <img src="/api/v1/workspaces/{id}/icon"> and hands the
	// same URL to <link rel=icon>. Neither carries an Authorization header, so
	// behind the authenticated group this endpoint answered every browser with
	// 401 and the icon could never render — the upload was only the first of
	// the two walls.
	//
	// Mesh has no cookie auth — the server never sets one, the token lives only
	// in a header — so there is no way for a browser subresource to
	// authenticate at all. A signed token in the URL was rejected as the
	// alternative: it leaks through logs and Referer headers, for a logo.
	//
	// The exposure is a workspace logo to whoever already knows the workspace
	// UUID, which is only ever handed out in authenticated responses; the UUID
	// is the capability, exactly as it is for the presigned artifact URLs this
	// endpoint used to redirect to (those were unauthenticated too, and for far
	// more sensitive content). A missing workspace and a workspace with no icon
	// answer byte-identically, so this is not an existence oracle either.
	// Uploading (PUT) stays members-only and admin-gated.
	//
	// This exception is registered in tests/integration/cross_tenant_test.go
	// (wsPublicRoutes) and its behaviour is pinned there. Do not add to that
	// list without the same kind of justification.
	//
	// Registered per route with explicit middleware rather than on a
	// v1.Group("/workspaces"): a group would make "which paths are public"
	// depend on which registrations happen to sit under it, and this must be
	// exactly two routes and nothing else. Written with the full literal path
	// so it reads identically to the guarded routes below — the completeness
	// test greps for both forms and fails on any :ws_id route that appears in
	// neither table.
	iconRateLimit := mw.RateLimit(mw.RateLimitConfig{
		Enabled:     cfg.RateLimit.Enabled,
		RPM:         cfg.RateLimit.APIRPM,
		KeyFunc:     mw.RateLimitKeyByIP,
		RedisClient: sharedRedis,
		Name:        "workspace-icon",
	})
	v1.GET("/workspaces/:ws_id/icon", workspaceHandler.GetIcon, iconRateLimit)
	// HEAD as well: it is what `curl -I` and cache validators send, and Echo
	// does not derive it from the GET route. net/http drops the body itself.
	v1.HEAD("/workspaces/:ws_id/icon", workspaceHandler.GetIcon, iconRateLimit)

	// --- Protected routes (JWT or Agent Key) ---
	api := v1.Group("")
	api.Use(mw.DualAuth(authService, agentService))
	api.Use(activityTracker.Middleware())
	api.Use(mw.WorkspaceRLS(db, projectRepo))
	// Cross-tenant guard. Applies to every route in this group that carries a
	// :ws_id parameter, and is inert on the rest. Group-level on purpose: the
	// per-route form was attached to 2 of 46 :ws_id routes, and the 44 that were
	// missed leaked member emails, the team directory, analytics and the full
	// workspace config export to any logged-in stranger — and let one rename
	// another tenant's workspace. Placed here, a newly added :ws_id route is
	// guarded by construction rather than by the author remembering.
	// Must run after WorkspaceRLS, which resolves and stores the workspace role.
	api.Use(mw.RequireWorkspaceMemberScoped(db))
	// Rate-limit API endpoints by authenticated actor (user/agent ID).
	// Uses the Redis-backed sliding window limiter for multi-instance deployments.
	api.Use(mw.RateLimit(mw.RateLimitConfig{
		Enabled:     cfg.RateLimit.Enabled,
		RPM:         cfg.RateLimit.APIRPM,
		KeyFunc:     mw.RateLimitKeyByActor,
		RedisClient: sharedRedis,
		Name:        "api",
	}))

	// Auth - protected.
	api.GET("/auth/me", authHandler.Me)
	api.PATCH("/auth/me", authHandler.UpdateMe)
	api.GET("/auth/check-username", authHandler.CheckUsername)
	api.POST("/auth/logout", authHandler.Logout)

	// rbac is a shorthand helper to create per-route RBAC middleware.
	rbac := func(perm mw.Permission) echo.MiddlewareFunc {
		return mw.RequirePermission(perm, workspaceMemberRepo)
	}

	// Workspace routes.
	api.GET("/workspaces", workspaceHandler.List)
	api.POST("/workspaces", workspaceHandler.Create)
	api.GET("/workspaces/:ws_id", workspaceHandler.GetByID)
	// Update carries the workspace slug, and changing a slug rewrites every
	// /w/<slug>/... URL the team has bookmarked or pasted into a doc. Same bar as
	// DELETE and icon upload: owner/admin, not any member who happens to be in.
	api.PATCH("/workspaces/:ws_id", workspaceHandler.Update, rbac(mw.PermManageMembers))
	api.DELETE("/workspaces/:ws_id", workspaceHandler.Delete, rbac(mw.PermDeleteWorkspace))
	// GET /workspaces/:ws_id/icon is registered on the public group above —
	// browsers fetch it as a plain <img>/<link rel=icon> subresource.
	api.PUT("/workspaces/:ws_id/icon", workspaceHandler.UploadIcon, rbac(mw.PermManageMembers))

	// Workspace member routes.
	// NOTE: /members/me MUST be registered before /members/:user_id to avoid "me" being parsed as UUID.
	api.GET("/workspaces/:ws_id/members", workspaceMemberHandler.List)
	api.GET("/workspaces/:ws_id/members/me", workspaceMemberHandler.Me)
	api.POST("/workspaces/:ws_id/members", workspaceMemberHandler.Add, rbac(mw.PermManageMembers))
	api.PATCH("/workspaces/:ws_id/members/:user_id", workspaceMemberHandler.UpdateRole, rbac(mw.PermManageMembers))
	api.DELETE("/workspaces/:ws_id/members/:user_id", workspaceMemberHandler.Remove, rbac(mw.PermManageMembers))
	// SearchUsers cannot be narrowed to the workspace in the path — its entire
	// job is to surface people who are NOT in it yet, which is how one account
	// joins a second workspace instead of becoming a second account.
	//
	// It is bounded by the CALLER instead: an exact address match anywhere on the
	// instance (knowing the address is the credential for inviting somebody),
	// plus loose name matching restricted to people the caller already shares a
	// workspace with — data they can already read off a member list. See
	// UserRepo.SearchAddableUsers.
	//
	// Restricting the route to manage-members, which is all it had before, is not
	// by itself a tenant boundary: creating a workspace is open to every
	// authenticated user and makes them its owner, so anyone could hold that
	// permission and page the whole instance directory, addresses included, with
	// ?q=a. Invite-by-email (POST .../invites) remains the path that needs no
	// directory at all.
	api.GET("/workspaces/:ws_id/users/search", workspaceMemberHandler.SearchUsers, rbac(mw.PermManageMembers))

	// Workspace invite routes (email-link flow).
	api.POST("/workspaces/:ws_id/invites", inviteHandler.Create, rbac(mw.PermManageMembers))
	api.GET("/workspaces/:ws_id/invites", inviteHandler.List, rbac(mw.PermManageMembers))
	api.POST("/workspaces/:ws_id/invites/:invite_id/resend", inviteHandler.Resend, rbac(mw.PermManageMembers))
	api.DELETE("/workspaces/:ws_id/invites/:invite_id", inviteHandler.Revoke, rbac(mw.PermManageMembers))

	// Project routes.
	api.GET("/workspaces/:ws_id/projects", projectHandler.List)
	api.POST("/workspaces/:ws_id/projects", projectHandler.Create, rbac(mw.PermCreateProject))

	// Workspace membership guard — enforces the caller is a member of the requested workspace.
	wsAccess := mw.RequireWorkspaceMember(db)

	// Project-scoped routes — RequireProjectMember enforces membership for :proj_id routes.
	projAccess := mw.RequireProjectMember(db)

	// Body-scoped guard — for the routes whose tenant is named in the request body
	// rather than the path, where RequireWorkspaceMemberScoped has nothing to see.
	bodyWS := mw.RequireBodyWorkspace(db)
	api.GET("/projects/:proj_id", projectHandler.GetByID, projAccess)
	api.PATCH("/projects/:proj_id", projectHandler.Update, projAccess)
	api.DELETE("/projects/:proj_id", projectHandler.Delete, projAccess, rbac(mw.PermDeleteProject))

	// Project member routes.
	api.GET("/projects/:proj_id/members", projectMemberHandler.List, projAccess)
	api.POST("/projects/:proj_id/members", projectMemberHandler.Add, projAccess, rbac(mw.PermManageMembers))
	api.POST("/projects/:proj_id/members/agents", projectMemberHandler.AddAgent, projAccess, rbac(mw.PermManageMembers))
	api.PATCH("/projects/:proj_id/members/:user_id", projectMemberHandler.UpdateRole, projAccess, rbac(mw.PermManageMembers))
	api.DELETE("/projects/:proj_id/members/:user_id", projectMemberHandler.Remove, projAccess, rbac(mw.PermManageMembers))
	api.DELETE("/projects/:proj_id/members/agents/:member_agent_id", projectMemberHandler.RemoveAgent, projAccess, rbac(mw.PermManageMembers))

	// Task status routes.
	api.GET("/projects/:proj_id/statuses", statusHandler.List, projAccess)
	api.POST("/projects/:proj_id/statuses", statusHandler.Create, projAccess)
	api.PATCH("/projects/:proj_id/statuses/:status_id", statusHandler.Update, projAccess)
	api.PUT("/projects/:proj_id/statuses/reorder", statusHandler.Reorder, projAccess)

	// Custom field routes.
	api.GET("/projects/:proj_id/custom-fields", customFieldHandler.List, projAccess)
	api.POST("/projects/:proj_id/custom-fields", customFieldHandler.Create, projAccess, rbac(mw.PermManageCF))
	api.GET("/custom-fields/:field_id", customFieldHandler.GetByID)
	api.PATCH("/custom-fields/:field_id", customFieldHandler.Update, rbac(mw.PermManageCF))
	api.DELETE("/custom-fields/:field_id", customFieldHandler.Delete, rbac(mw.PermManageCF))
	api.PUT("/projects/:proj_id/custom-fields/reorder", customFieldHandler.Reorder, projAccess, rbac(mw.PermManageCF))

	// Task routes.
	api.GET("/projects/:proj_id/tasks", taskHandler.List, projAccess)
	api.POST("/projects/:proj_id/tasks", taskHandler.Create, projAccess, rbac(mw.PermCreateTask))
	api.POST("/projects/:proj_id/tasks/bulk-update", taskHandler.BulkUpdate, projAccess, rbac(mw.PermUpdateTask))
	api.GET("/tasks/by-short-id/:short", taskHandler.GetByShortID)
	api.GET("/tasks/:task_id", taskHandler.GetByID, wsAccess)
	api.PATCH("/tasks/:task_id", taskHandler.Update, wsAccess, rbac(mw.PermUpdateTask))
	api.DELETE("/tasks/:task_id", taskHandler.Delete, wsAccess, rbac(mw.PermDeleteTask))
	api.POST("/tasks/:task_id/move", taskHandler.MoveTask, wsAccess, rbac(mw.PermUpdateTask))
	api.POST("/tasks/:task_id/move-to-project", taskHandler.MoveToProject, wsAccess, rbac(mw.PermUpdateTask))
	// Statuses of the task's project, under the SAME gate as /tasks/:task_id/move.
	// Resolving a status slug is a precondition of the move; gating the lookup more
	// strictly than the move itself makes the move unreachable and reports the refusal
	// against the wrong resource. Project-scoped status routes above keep projAccess.
	api.GET("/tasks/:task_id/statuses", statusHandler.ListByTask, wsAccess)
	api.GET("/tasks/:task_id/subtasks", taskHandler.ListSubtasks, wsAccess)
	api.POST("/tasks/:task_id/subtasks", taskHandler.CreateSubtask, wsAccess, rbac(mw.PermCreateTask))
	api.POST("/tasks/:task_id/assign", taskHandler.AssignTask, wsAccess, rbac(mw.PermUpdateTask))
	api.POST("/tasks/:task_id/checkout", taskHandler.Checkout, wsAccess)
	api.DELETE("/tasks/:task_id/checkout", taskHandler.ReleaseCheckout, wsAccess)
	api.PATCH("/tasks/:task_id/checkout", taskHandler.ExtendCheckout, wsAccess)
	api.PATCH("/tasks/:task_id/ship", taskHandler.ShipTask, wsAccess, rbac(mw.PermUpdateTask))
	api.GET("/tasks/:task_id/context", taskContextHandler.GetTaskContext, wsAccess)
	api.GET("/tasks/:task_id/cost-summary", taskHandler.GetCostSummary, wsAccess)
	api.PATCH("/tasks/:task_id/dod-check", taskHandler.SetDodCheck, wsAccess, rbac(mw.PermUpdateTask))
	api.GET("/workspaces/:ws_id/tasks", taskHandler.SearchGlobal, wsAccess)

	// Dependency routes.
	api.GET("/tasks/:task_id/dependencies", depHandler.List, wsAccess)
	api.POST("/tasks/:task_id/dependencies", depHandler.Create, wsAccess, rbac(mw.PermUpdateTask))
	api.DELETE("/tasks/:task_id/dependencies/:dep_id", depHandler.Delete, wsAccess, rbac(mw.PermUpdateTask))
	api.GET("/projects/:proj_id/dependency-graph", depHandler.DependencyGraph, projAccess)

	// Comment routes.
	api.GET("/tasks/:task_id/comments", commentHandler.List, wsAccess)
	api.POST("/tasks/:task_id/comments", commentHandler.Create, wsAccess, rbac(mw.PermAddComment))
	api.PATCH("/comments/:comment_id", commentHandler.Update, rbac(mw.PermAddComment))
	api.DELETE("/comments/:comment_id", commentHandler.Delete, rbac(mw.PermAddComment))

	// Third human_gate exit — "decision recorded" (task #c56339b1, contract
	// docs/human-gate-decision-recorded.md). Revoke enforces user-only inside
	// the handler itself (mirrors task_handler.go's PATCH human_gate 403), not
	// via rbac, since an agent can legitimately hold PermAddComment.
	api.GET("/tasks/:task_id/human-gate-decisions", humanGateDecisionHandler.List, wsAccess)
	api.POST("/tasks/:task_id/human-gate-decisions", humanGateDecisionHandler.Create, wsAccess, rbac(mw.PermAddComment))
	api.POST("/human-gate-decisions/:decision_id/revoke", humanGateDecisionHandler.Revoke, rbac(mw.PermAddComment))

	// Artifact routes.
	api.GET("/tasks/:task_id/artifacts", artifactHandler.List, wsAccess)
	api.POST("/tasks/:task_id/artifacts", artifactHandler.Upload, wsAccess, rbac(mw.PermUploadArtifact))
	api.GET("/artifacts/:artifact_id", artifactHandler.GetByID, wsAccess)
	api.GET("/artifacts/:artifact_id/download", artifactHandler.Download, wsAccess)
	api.GET("/tasks/:task_id/artifacts/:artifact_id/download", artifactHandler.Download, wsAccess)
	api.DELETE("/artifacts/:artifact_id", artifactHandler.Delete, wsAccess, rbac(mw.PermUploadArtifact))

	// Document routes. The project-scoped pair takes projAccess; the object routes
	// take wsAccess, because RequireProjectMember answers 500 on a path with no
	// :proj_id in it.
	//
	// PermUploadArtifact is the closest existing write permission: a document is
	// project content whose body is written into object storage, which is exactly
	// what that permission already covers, and it is held by the same roles as
	// create_task (owner/admin/member and agents), so nothing gains or loses reach.
	api.GET("/projects/:proj_id/documents", documentHandler.List, projAccess)
	// Static segment before the parameterised sibling routes, and under
	// /projects/:proj_id so the tenant is named by the path rather than by a
	// query parameter.
	api.GET("/projects/:proj_id/documents/search", documentHandler.Search, projAccess)
	api.POST("/projects/:proj_id/documents", documentHandler.Create, projAccess, rbac(mw.PermUploadArtifact))
	api.GET("/documents/:doc_id", documentHandler.GetByID, wsAccess)
	api.PATCH("/documents/:doc_id", documentHandler.Update, wsAccess, rbac(mw.PermUploadArtifact))
	api.DELETE("/documents/:doc_id", documentHandler.Delete, wsAccess, rbac(mw.PermUploadArtifact))

	// Reading a document in pieces, for callers that should not have to fetch the
	// whole page to work with one part of it — an agent answering a question about
	// one section otherwise pays for every section.
	//
	// All reads, so wsAccess and no rbac: whoever may GET the document may ask for
	// its outline, for a section of it, or for where a sentence in it sits.
	//
	// resolve-anchor is a POST that changes nothing. It has to be a POST — its
	// input is a quotation of up to 2000 bytes, which does not belong in a query
	// string — and it is deliberately not behind a write permission, because it
	// writes nothing. It exists because an agent has no text selection to compute a
	// comment anchor from, and computing byte offsets from text is exactly where it
	// gets them wrong: a Cyrillic quote at byte 853 is at character 475, and an
	// anchor off by that much points at a different sentence with total confidence.
	api.GET("/documents/:doc_id/outline", documentHandler.Outline, wsAccess)
	api.GET("/documents/:doc_id/section", documentHandler.Section, wsAccess)
	api.POST("/documents/:doc_id/resolve-anchor", documentHandler.ResolveAnchor, wsAccess)

	// Subscribing to a document's changes. No RBAC beyond workspace access on
	// purpose: asking to be told about a page you can already read grants
	// nothing you did not already have, and requiring a write permission to
	// follow a document would lock read-only members out of the one feature that
	// exists for people who are not editing.
	api.GET("/documents/:doc_id/watch", documentWatchHandler.Get, wsAccess)
	api.PUT("/documents/:doc_id/watch", documentWatchHandler.Watch, wsAccess)
	api.DELETE("/documents/:doc_id/watch", documentWatchHandler.Unwatch, wsAccess)

	// Addressing a document by its slug path instead of its uuid, because agents
	// think in paths and making them resolve an id first adds a call to every
	// single access.
	//
	// projAccess, like the rest of the project-scoped pair: the only id on the
	// route is :proj_id, and the segments after by-path are slugs inside the
	// project the guard has already checked the caller against — they can name
	// nothing outside it.
	//
	// A trailing wildcard rather than a query parameter, so the URL reads the way
	// the path is written. It cannot collide with the collection route above: Echo
	// matches the literal `documents` segment first, and `by-path` is a literal
	// segment no document slug lookup starts from by accident.
	api.GET("/projects/:proj_id/documents/by-path/*", documentHandler.GetByPath, projAccess)

	// Document attachment routes. The upload/list pair hangs off :doc_id, which
	// already resolves a tenant; the object routes name the attachment directly and
	// are guarded by the :att_id resolver.
	//
	// :att_id appears under exactly one collection segment — document-attachments —
	// and must stay that way: resolvers are keyed on the parameter name alone, so
	// mounting the same id under /documents/:doc_id/attachments/:att_id as well
	// would make the name ambiguous (TestScopedParamNamesAreUnambiguous).
	//
	// PermUploadArtifact for the writes, same reasoning as the document routes: an
	// attachment is project content whose bytes go to object storage, which is
	// literally what that permission covers.
	api.GET("/documents/:doc_id/attachments", documentAttachmentHandler.List, wsAccess)
	api.POST("/documents/:doc_id/attachments", documentAttachmentHandler.Upload, wsAccess, rbac(mw.PermUploadArtifact))
	api.GET("/document-attachments/:att_id/download", documentAttachmentHandler.Download, wsAccess)
	api.DELETE("/document-attachments/:att_id", documentAttachmentHandler.Delete, wsAccess, rbac(mw.PermUploadArtifact))

	// Document comment routes — Confluence-style inline comments on document text.
	//
	// The list/create pair hangs off :doc_id, which already resolves a tenant; the
	// object routes name the comment directly and are guarded by the :dcom_id
	// resolver. wsAccess throughout, not projAccess: none of these paths carry a
	// :proj_id, and RequireProjectMember answers 500 on a path without one.
	//
	// :dcom_id appears under exactly one collection segment — document-comments —
	// and must stay that way: resolvers are keyed on the parameter name alone, so
	// mounting the same id under /documents/:doc_id/comments/:dcom_id as well
	// would make the name ambiguous (TestScopedParamNamesAreUnambiguous). It is
	// spelled :dcom_id rather than :comment_id for the same reason — that name is
	// taken by task comments, whose resolver reads the unrelated `comments` table.
	//
	// PermAddComment for the writes: commenting on a document is the same act the
	// permission already names, held by the same roles (owner/admin/member and
	// agents), so nothing gains or loses reach. Resolve/unresolve sit under it too
	// — putting a thread away is part of taking part in it, not an admin power.
	api.GET("/documents/:doc_id/comments", documentCommentHandler.List, wsAccess)
	api.POST("/documents/:doc_id/comments", documentCommentHandler.Create, wsAccess, rbac(mw.PermAddComment))
	api.PATCH("/document-comments/:dcom_id", documentCommentHandler.Update, wsAccess, rbac(mw.PermAddComment))
	api.POST("/document-comments/:dcom_id/resolve", documentCommentHandler.Resolve, wsAccess, rbac(mw.PermAddComment))
	api.POST("/document-comments/:dcom_id/unresolve", documentCommentHandler.Unresolve, wsAccess, rbac(mw.PermAddComment))
	api.DELETE("/document-comments/:dcom_id", documentCommentHandler.Delete, wsAccess, rbac(mw.PermAddComment))

	// Agent routes.
	// NOTE: /agents/me/* routes MUST be registered before /agents/:agent_id to avoid
	// "me" being parsed as a UUID parameter.
	api.GET("/workspaces/:ws_id/agents", agentHandler.List)
	api.POST("/workspaces/:ws_id/agents", agentHandler.Register, rbac(mw.PermRegisterAgent))
	api.GET("/agents/me", agentHandler.Me)
	api.PATCH("/agents/me", agentHandler.UpdateMe)
	api.GET("/agents/me/tasks", agentHandler.GetMyTasks)
	api.GET("/agents/me/events/stream", agentHandler.EventStream)
	api.GET("/agents/me/tasks/poll", agentHandler.PollTasks)
	api.POST("/agents/me/sessions/report", agentHandler.ReportSession)
	api.POST("/agents/heartbeat", agentHandler.Heartbeat)
	api.GET("/agents/:agent_id", agentHandler.GetByID)
	api.PATCH("/agents/:agent_id", agentHandler.Update, rbac(mw.PermDeleteAgent))
	api.DELETE("/agents/:agent_id", agentHandler.Delete, rbac(mw.PermDeleteAgent))
	api.POST("/agents/:agent_id/regenerate-key", agentHandler.RegenerateKey, rbac(mw.PermDeleteAgent))
	api.GET("/agents/:agent_id/sub-agents", agentHandler.ListSubAgents)
	api.GET("/agents/:agent_id/heartbeat", agentHandler.GetAgentHeartbeat)
	api.GET("/agents/:agent_id/activity", agentHandler.ListAgentActivity)
	api.POST("/agents/:agent_id/activity", agentHandler.CreateAgentActivity)
	api.GET("/workspaces/:ws_id/agents/status", agentHandler.GetAgentsStatus)

	// Event bus routes.
	api.GET("/projects/:proj_id/events", eventHandler.List, projAccess)
	api.POST("/projects/:proj_id/events", eventHandler.Create, projAccess, rbac(mw.PermPublishEvent))
	api.GET("/events/:event_id", eventHandler.GetByID)

	// Webhook routes.
	api.POST("/workspaces/:ws_id/webhooks", webhookHandler.Create, rbac(mw.PermManageWebhooks))
	api.GET("/workspaces/:ws_id/webhooks", webhookHandler.List, rbac(mw.PermManageWebhooks))
	api.GET("/webhooks/:webhook_id", webhookHandler.GetByID, rbac(mw.PermManageWebhooks))
	api.PATCH("/webhooks/:webhook_id", webhookHandler.Update, rbac(mw.PermManageWebhooks))
	api.DELETE("/webhooks/:webhook_id", webhookHandler.Delete, rbac(mw.PermManageWebhooks))
	api.GET("/webhooks/:webhook_id/deliveries", webhookHandler.ListDeliveries, rbac(mw.PermManageWebhooks))
	api.POST("/webhooks/:webhook_id/test", webhookHandler.Test, rbac(mw.PermManageWebhooks))

	// Saved view routes.
	api.GET("/projects/:proj_id/views", savedViewHandler.List, projAccess)
	api.POST("/projects/:proj_id/views", savedViewHandler.Create, projAccess)
	api.GET("/views/:view_id", savedViewHandler.GetByID)
	api.PATCH("/views/:view_id", savedViewHandler.Update)
	api.DELETE("/views/:view_id", savedViewHandler.Delete)

	// Activity log routes.
	api.GET("/workspaces/:ws_id/activity", activityHandler.ListByWorkspace, rbac(mw.PermExportAuditLog))
	api.GET("/workspaces/:ws_id/activity/export", activityHandler.Export, rbac(mw.PermExportAuditLog))
	api.GET("/tasks/:task_id/activity", activityHandler.ListByTask)

	// VCS link routes.
	api.GET("/tasks/:task_id/vcs-links", vcsLinkHandler.List)
	api.POST("/tasks/:task_id/vcs-links", vcsLinkHandler.Create, rbac(mw.PermUpdateTask))
	api.DELETE("/vcs-links/:link_id", vcsLinkHandler.Delete, rbac(mw.PermUpdateTask))

	// GitHub webhook receiver (public — no auth, HMAC validated when configured).
	// Two equivalent routes: the legacy /webhooks/github path that existing repos
	// are configured against, and the canonical /api/v1/integrations/github/webhook
	// alias documented in the integrations UI.
	e.POST("/webhooks/github", vcsLinkHandler.GitHubWebhook)
	e.POST("/api/v1/integrations/github/webhook", vcsLinkHandler.GitHubWebhook)

	// GitLab webhook receiver (public — no auth, X-Gitlab-Token validated
	// when configured). Mirrors the GitHub route above (#bc39d781) — no
	// legacy alias needed since this is a new route with no prior configured
	// callers to stay compatible with.
	e.POST("/webhooks/gitlab", vcsLinkHandler.GitLabWebhook)

	// Secret materialization — deliberately OUTSIDE the `api` group. DualAuth
	// there accepts a user JWT or ANY agent's API key; this route must accept
	// neither, since either would let something wielding an ordinary agent
	// identity decrypt secrets. Gated by mw.SpawnAuth() alone — see its doc
	// comment for the trust model.
	e.POST("/internal/secrets/materialize", secretMaterializeHandler.Materialize, mw.SpawnAuth())

	// Write-only secrets CRUD (task #64e84eb1, S3). Every route is gated by
	// PermManageSecrets, which no agent key holds — see its declaration in
	// rbac.go. That includes the masked LIST: it carries a fingerprint, and
	// the point of this store is that an agent identity learns nothing about
	// a value it did not supply.
	api.POST("/workspaces/:ws_id/secrets", secretHandler.Create, rbac(mw.PermManageSecrets))
	api.GET("/workspaces/:ws_id/secrets", secretHandler.List, rbac(mw.PermManageSecrets))
	api.POST("/secrets/:secret_id/rotate", secretHandler.Rotate, rbac(mw.PermManageSecrets))
	api.DELETE("/secrets/:secret_id", secretHandler.Delete, rbac(mw.PermManageSecrets))

	// Integration config routes.
	api.GET("/workspaces/:ws_id/integrations", integrationHandler.List)
	api.POST("/workspaces/:ws_id/integrations", integrationHandler.Configure, rbac(mw.PermManageWebhooks))
	api.PATCH("/integrations/:int_id", integrationHandler.Update, rbac(mw.PermManageWebhooks))
	api.DELETE("/integrations/:int_id", integrationHandler.Delete, rbac(mw.PermManageWebhooks))

	// Project integrations (Team Relay).
	// NOTE: /integrations/team-relay MUST be registered before /integrations to avoid routing ambiguity.
	api.GET("/projects/:proj_id/integrations/team-relay", projectIntegrationHandler.GetTeamRelay, projAccess)
	api.PATCH("/projects/:proj_id/integrations/team-relay", projectIntegrationHandler.UpsertTeamRelay, projAccess, rbac(mw.PermManageWebhooks))
	api.DELETE("/projects/:proj_id/integrations/team-relay", projectIntegrationHandler.DeleteTeamRelay, projAccess, rbac(mw.PermManageWebhooks))
	api.GET("/projects/:proj_id/integrations", projectIntegrationHandler.List, projAccess)
	// TR file search — share_id (slug) passed as query param, not project path param.
	api.GET("/tr/search", projectIntegrationHandler.TrSearch)

	// TR document search and authenticated preview-url resolution (Team Relay share contents).
	api.GET("/projects/:proj_id/tr/search", trSearchHandler.Search, projAccess)
	// The Team Relay document is read server-side and rendered by our own editor
	// (D10). The route it replaces resolved an iframe src for an embedded
	// TeamRelay page; the iframe, its 6s timeout and its dead end are gone with it.
	api.GET("/projects/:proj_id/tr/document", trDocumentHandler.Get, projAccess)
	// R3: materialize the configured share into this project's Docs tree.
	// Explicit action, not implicit on a tree read — see TrMountHandler's doc
	// comment.
	api.POST("/projects/:proj_id/tr/mount", trMountHandler.Sync, projAccess, rbac(mw.PermManageWebhooks))

	// Analytics routes.
	api.GET("/workspaces/:ws_id/analytics", analyticsHandler.GetMetrics)
	api.GET("/workspaces/:ws_id/analytics/export", analyticsHandler.ExportMetrics)

	// Project update routes.
	api.POST("/projects/:proj_id/updates", projectUpdateHandler.Create, projAccess)
	api.GET("/projects/:proj_id/updates", projectUpdateHandler.List, projAccess)
	api.GET("/projects/:proj_id/updates/latest", projectUpdateHandler.GetLatest, projAccess)

	// Initiative routes.
	api.POST("/workspaces/:ws_id/initiatives", initiativeHandler.Create, rbac(mw.PermCreateProject))
	api.GET("/workspaces/:ws_id/initiatives", initiativeHandler.List)
	api.GET("/initiatives/:init_id", initiativeHandler.GetByID)
	api.PATCH("/initiatives/:init_id", initiativeHandler.Update, rbac(mw.PermCreateProject))
	api.DELETE("/initiatives/:init_id", initiativeHandler.Delete, rbac(mw.PermDeleteProject))
	api.POST("/initiatives/:init_id/projects", initiativeHandler.LinkProject, rbac(mw.PermCreateProject))
	api.DELETE("/initiatives/:init_id/projects/:proj_id", initiativeHandler.UnlinkProject, rbac(mw.PermCreateProject))

	// Triage inbox route.
	api.GET("/workspaces/:ws_id/triage", triageHandler.List)

	// Recurring task schedule routes.
	//
	// The parameter is spelled :recurring_id rather than :id so WorkspaceRLS can
	// resolve the schedule's workspace from it and the membership guard applies.
	// The URLs are unchanged. :id is also what /memories/:id is spelled with, and
	// one central resolver cannot serve two different tables.
	api.POST("/projects/:proj_id/recurring", recurringHandler.Create, projAccess, rbac(mw.PermCreateTask))
	api.GET("/projects/:proj_id/recurring", recurringHandler.List, projAccess)
	api.GET("/recurring/:recurring_id", recurringHandler.GetByID)
	api.PATCH("/recurring/:recurring_id", recurringHandler.Update, rbac(mw.PermUpdateTask))
	api.DELETE("/recurring/:recurring_id", recurringHandler.Delete, rbac(mw.PermDeleteTask))
	api.POST("/recurring/:recurring_id/trigger", recurringHandler.Trigger, rbac(mw.PermCreateTask))
	api.GET("/recurring/:recurring_id/history", recurringHandler.History)

	// Task template routes.
	api.POST("/projects/:proj_id/templates", taskTemplateHandler.Create, projAccess, rbac(mw.PermCreateTask))
	api.GET("/projects/:proj_id/templates", taskTemplateHandler.List, projAccess)
	api.GET("/templates/:tmpl_id", taskTemplateHandler.GetByID)
	api.PATCH("/templates/:tmpl_id", taskTemplateHandler.Update, rbac(mw.PermUpdateTask))
	api.DELETE("/templates/:tmpl_id", taskTemplateHandler.Delete, rbac(mw.PermDeleteTask))
	api.POST("/templates/:tmpl_id/create-task", taskTemplateHandler.CreateTask, rbac(mw.PermCreateTask))

	// Team Directory routes (Sprint 20).
	api.GET("/workspaces/:ws_id/team", rulesHandler.GetTeamDirectory)
	// Self-service (an agent updating its own profile via X-Agent-Key) is always
	// allowed; rewriting another agent's profile requires PermDeleteAgent, same
	// bar as PATCH /agents/:agent_id above.
	api.PUT("/agents/:agent_id/profile", rulesHandler.UpdateAgentProfile, mw.RequireSelfOrPermission("agent_id", mw.PermDeleteAgent, workspaceMemberRepo))

	// Assignment Rules routes (Sprint 20).
	api.GET("/workspaces/:ws_id/rules/assignment", rulesHandler.GetWorkspaceAssignmentRules)
	api.PUT("/workspaces/:ws_id/rules/assignment", rulesHandler.SetWorkspaceAssignmentRules, rbac(mw.PermManageMembers))
	api.GET("/projects/:proj_id/rules/assignment", rulesHandler.GetEffectiveAssignmentRules, projAccess)
	api.PUT("/projects/:proj_id/rules/assignment", rulesHandler.SetProjectAssignmentRules, projAccess, rbac(mw.PermManageMembers))

	// Workflow Rules routes (Sprint 20).
	api.GET("/projects/:proj_id/rules/workflow", rulesHandler.GetProjectWorkflowRules, projAccess)
	api.PUT("/projects/:proj_id/rules/workflow", rulesHandler.SetProjectWorkflowRules, projAccess, rbac(mw.PermManageMembers))

	// Violation Log routes (Sprint 20).
	api.GET("/workspaces/:ws_id/violations", rulesHandler.ListViolations)

	// Config Import/Export routes (Sprint 21).
	api.POST("/workspaces/:ws_id/config/import", rulesHandler.ImportConfig, rbac(mw.PermManageMembers))
	api.GET("/workspaces/:ws_id/config/export", rulesHandler.ExportConfig)
	api.POST("/workspaces/:ws_id/team/import", rulesHandler.ImportTeam, rbac(mw.PermManageMembers))

	// Workflow Templates routes (Sprint 21).
	api.GET("/workspaces/:ws_id/rules/workflow-templates", rulesHandler.GetWorkflowTemplates)
	api.PUT("/workspaces/:ws_id/rules/workflow-templates", rulesHandler.SetWorkflowTemplates, rbac(mw.PermManageMembers))

	// Governance rule routes.
	api.POST("/workspaces/:ws_id/rules", ruleHandler.CreateWorkspaceRule, rbac(mw.PermManageRules))
	api.GET("/workspaces/:ws_id/rules", ruleHandler.ListWorkspaceRules)
	api.GET("/workspaces/:ws_id/rules/effective", ruleHandler.GetWorkspaceEffectiveRules)
	api.POST("/projects/:proj_id/rules", ruleHandler.CreateProjectRule, projAccess, rbac(mw.PermManageRules))
	api.GET("/projects/:proj_id/rules", ruleHandler.ListProjectRules, projAccess)
	api.GET("/projects/:proj_id/rules/effective", ruleHandler.GetProjectEffectiveRules, projAccess)
	api.GET("/rules/:rule_id", ruleHandler.GetRule)
	api.PATCH("/rules/:rule_id", ruleHandler.UpdateRule, rbac(mw.PermManageRules))
	api.DELETE("/rules/:rule_id", ruleHandler.DeleteRule, rbac(mw.PermManageRules))
	// The tenant of this one is in the body, not the path: bodyWS is what checks it.
	// Without it any authenticated caller could paste another workspace's id in and
	// read that tenant's rule names and violation messages back.
	api.POST("/rules/evaluate", ruleHandler.EvaluateRules, bodyWS)

	// Auto-transition rule routes.
	api.GET("/projects/:proj_id/auto-transition-rules", autoTransHandler.List, projAccess)
	api.POST("/projects/:proj_id/auto-transition-rules", autoTransHandler.Create, projAccess, rbac(mw.PermManageRules))
	api.PUT("/projects/:proj_id/auto-transition-rules/:atr_id", autoTransHandler.Update, projAccess, rbac(mw.PermManageRules))
	api.DELETE("/projects/:proj_id/auto-transition-rules/:atr_id", autoTransHandler.Delete, projAccess, rbac(mw.PermManageRules))

	// Notification routes.
	api.GET("/notifications", notificationHandler.List)
	api.POST("/notifications/mark-read", notificationHandler.MarkRead)
	api.GET("/notifications/preferences", notificationHandler.GetPreferences)
	api.GET("/notifications/email-availability", notificationHandler.GetEmailAvailability)
	// requireWorkspaceMember (called inside the handler) is the tenancy guard
	// here — see declaredQueryTenantParams in internal/middleware for why a
	// query-string workspace_id needs one of its own.
	api.GET("/notifications/telegram-bot-info", notificationHandler.GetTelegramBotInfo)
	// bodyWS: the workspace being subscribed to is named in the request body, so
	// RequireWorkspaceMemberScoped sees a path with nothing in it to resolve and
	// waves it through. Without this guard any authenticated caller could
	// subscribe to any workspace and be delivered its comment bodies.
	api.PUT("/notifications/preferences", notificationHandler.UpdatePreferences, bodyWS)
	api.DELETE("/notifications/preferences/:pref_id", notificationHandler.DeletePreference)

	// Web Push subscription routes.
	// NOTE: /me/push-subscriptions/vapid-key MUST be before /me/push-subscriptions to avoid routing conflict.
	api.GET("/me/push-subscriptions/vapid-key", pushSubscriptionHandler.GetVAPIDKey)
	api.GET("/me/push-subscriptions", pushSubscriptionHandler.List)
	api.POST("/me/push-subscriptions", pushSubscriptionHandler.Subscribe)
	api.DELETE("/me/push-subscriptions", pushSubscriptionHandler.Unsubscribe)

	// @mention inbox (REST).
	api.GET("/me/mentions", mentionHandler.List)
	api.GET("/me/mentions/unseen_count", mentionHandler.UnseenCount)
	api.POST("/me/mentions/:comment_id/seen", mentionHandler.MarkSeen)
	// The same inbox for @-mentions inside document comments. Separate routes
	// rather than a widened /me/mentions: the rows name a document and no task,
	// and folding them in would put a nullable task_id on a response shape three
	// existing screens read as non-null.
	//
	// :dcom_id is resolved to a workspace by workspaceParamResolvers, so
	// RequireWorkspaceMemberScoped (group-level, above) refuses another tenant's
	// comment id here exactly as it does on /document-comments/:dcom_id.
	api.GET("/me/document-mentions", documentMentionHandler.List)
	api.GET("/me/document-mentions/unseen_count", documentMentionHandler.UnseenCount)
	api.POST("/me/document-mentions/:dcom_id/seen", documentMentionHandler.MarkSeen)
	api.GET("/workspaces/:ws_id/mentionables", mentionablesHandler.Search)

	// Current user's active tasks (excludes done/cancelled).
	api.GET("/me/tasks", taskHandler.GetCurrentUserTasks)

	// Activity feed — my comments + workspace-wide recent comments.
	api.GET("/me/comments", commentHandler.GetMyComments)
	api.GET("/workspaces/:ws_id/comments/recent", commentHandler.GetRecentByWorkspace, wsAccess)

	// Memory routes.
	// NOTE: fixed-path routes (/memories/search, /memories/export, /memories/import,
	// /memories/reindex, /memories/recall_graph) MUST be registered before /memories/:id
	// to avoid the literal path segments being parsed as UUID parameters.
	api.POST("/memories", memoryHandler.Remember)
	api.GET("/memories", memoryHandler.List)
	api.GET("/memories/search", memoryHandler.Search)
	api.GET("/memories/recall_graph", memoryHandler.RecallGraph)
	api.GET("/memories/export", memoryHandler.ExportMemories)
	api.POST("/memories/import", memoryHandler.ImportMemories)
	api.POST("/memories/reindex", memoryHandler.Reindex)
	api.POST("/memories/backfill-chunks", memoryHandler.BackfillChunks)
	api.POST("/memories/rechunk-stale", memoryHandler.RechunkStale)
	api.GET("/memories/:id", memoryHandler.GetByID)
	api.GET("/memories/:id/related", memoryHandler.FindRelated)
	api.GET("/memories/:id/revisions", memoryHandler.Revisions)
	api.DELETE("/memories/:id", memoryHandler.Delete)
	api.GET("/projects/:proj_id/knowledge", memoryHandler.GetProjectKnowledge, projAccess)
	api.POST("/projects/:proj_id/knowledge", memoryHandler.SetProjectKnowledge, projAccess)

	// C1 canonical updates feed — returns privacy:public canonical-decision memories since a cursor.
	api.GET("/canonical_updates", canonicalUpdatesHandler.GetCanonicalUpdates)

	// Spark catalog routes (optional; only registered when MESH_SPARK_ENABLED=true).
	if cfg.Spark.Enabled {
		sparkClient := spark.NewClient(cfg.Spark.URL)
		sparkHandler := handler.NewSparkHandler(sparkClient, agentService, workspaceMemberRepo)
		api.GET("/spark/agents", sparkHandler.Search)
		api.GET("/spark/agents/popular", sparkHandler.Popular)
		api.GET("/spark/agents/:agent_id", sparkHandler.GetByID)
		api.POST("/spark/agents/:agent_id/install", sparkHandler.Install)
		log.Printf("Spark catalog integration enabled (base URL: %s)", cfg.Spark.URL)
	}

	// 10. Start recurring task scheduler.
	schedulerShutdownCh := make(chan struct{})
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				count, err := recurringService.RunDue(ctx)
				cancel()
				if err != nil {
					log.Printf("[scheduler] ERROR running due schedules: %v", err)
				} else if count > 0 {
					log.Printf("[scheduler] Created %d recurring task instances", count)
				}
			case <-schedulerShutdownCh:
				log.Println("[scheduler] Shutting down")
				return
			}
		}
	}()
	log.Println("Recurring task scheduler started (60s interval)")

	// 10a. Memory decay + cleanup scheduler (every 6 hours).
	// DecayRelevance, CleanExpired, DecayWeights, and PruneDeadEdges are all idempotent.
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				if n, decayErr := memoryRepo.DecayRelevance(ctx); decayErr != nil {
					log.Printf("[memory-decay] ERROR: %v", decayErr)
				} else if n > 0 {
					log.Printf("[memory-decay] Decayed %d stale memories", n)
				}
				if n, cleanErr := memoryRepo.CleanExpired(ctx); cleanErr != nil {
					log.Printf("[memory-cleanup] ERROR: %v", cleanErr)
				} else if n > 0 {
					log.Printf("[memory-cleanup] Cleaned %d expired memories", n)
				}
				if n, archErr := memoryRepo.ArchiveStaleWorkspaceCheckpoints(ctx, 30*24*time.Hour, 0.40); archErr != nil {
					log.Printf("[memory-archive] ERROR: %v", archErr)
				} else if n > 0 {
					log.Printf("[memory-archive] Archived %d stale workspace checkpoints", n)
				}
				if n, edgeDecayErr := memoryEdgesRepo.DecayWeights(ctx); edgeDecayErr != nil {
					log.Printf("[edge-decay] ERROR: %v", edgeDecayErr)
				} else if n > 0 {
					log.Printf("[edge-decay] Decayed %d stale edges", n)
				}
				if n, pruneErr := memoryEdgesRepo.PruneDeadEdges(ctx); pruneErr != nil {
					log.Printf("[edge-prune] ERROR: %v", pruneErr)
				} else if n > 0 {
					log.Printf("[edge-prune] Pruned %d dead edges", n)
				}
				cancel()

				// Memory reconciler (P1-C): monitor (expire/stale) + linker (supersede).
				// Given a longer timeout since the linker may issue embedding API calls.
				recCtx, recCancel := context.WithTimeout(context.Background(), 5*time.Minute)
				if err := memReconciler.Run(recCtx); err != nil {
					log.Printf("[memory-reconciler] ERROR: %v", err)
				}
				recCancel()
			case <-schedulerShutdownCh:
				return
			}
		}
	}()
	log.Println("Memory decay scheduler started (6h interval)")

	// 10b. Agent events sweeper — delete expired rows from agent_events every 5 minutes.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				if n, sweepErr := agentEventsRepo.DeleteExpired(ctx); sweepErr != nil {
					log.Printf("[agent-events-sweeper] ERROR: %v", sweepErr)
				} else if n > 0 {
					log.Printf("[agent-events-sweeper] Deleted %d expired events", n)
				}
				cancel()
			case <-schedulerShutdownCh:
				return
			}
		}
	}()
	log.Println("Agent events sweeper started (5m interval)")

	// 10b-bis. Monitor promotion sweeper — auto-unparks backlog+kind:monitor tasks
	// whose due_date has passed, moving them back to todo (CLAUDE-workflow-reference.md
	// §0m passive-wait pattern; complements the event-driven dependency-unblock
	// auto-transition, which only fires on task completion, with a time-based trigger).
	monitorPromotionSvc := service.NewMonitorPromotionService(taskRepo, taskStatusRepo, commentRepo, taskService)
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				n, err := monitorPromotionSvc.SweepDueMonitorTasks(ctx)
				cancel()
				if err != nil {
					log.Printf("[monitor-promotion] ERROR sweeping due monitor tasks: %v", err)
				} else if n > 0 {
					log.Printf("[monitor-promotion] promoted %d backlog+kind:monitor task(s) to todo", n)
				}
			case <-schedulerShutdownCh:
				return
			}
		}
	}()
	log.Println("Monitor promotion sweeper started (60s interval)")

	// 10b-ter. human_gate soft-timeout sweeper (task #b1d5c742, contract
	// docs/human-gate-decision-recorded.md §5). Releases soft-classified gates armed
	// past DefaultHumanGateSoftTimeout; a hard-classified gate is structurally
	// unreachable from this sweep (see FindSoftTimedOutGates in task_repo.go) — no
	// interval here can accidentally release one, so a short poll interval is safe.
	humanGateSoftWindow := humanGateSoftTimeoutWindow()
	humanGateSoftTimeoutSvc := service.NewHumanGateSoftTimeoutService(taskRepo, commentRepo, humanGateSoftWindow)
	if humanGateSoftWindow > 0 {
		log.Printf("[human-gate-soft-timeout] window overridden via HUMAN_GATE_SOFT_TIMEOUT=%s", humanGateSoftWindow)
	} else {
		log.Printf("[human-gate-soft-timeout] window = default %s", service.DefaultHumanGateSoftTimeout)
	}
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				n, err := humanGateSoftTimeoutSvc.SweepExpiredSoftGates(ctx)
				cancel()
				if err != nil {
					log.Printf("[human-gate-soft-timeout] ERROR sweeping expired soft gates: %v", err)
				} else if n > 0 {
					log.Printf("[human-gate-soft-timeout] released %d soft-classified gate(s)", n)
				}
			case <-schedulerShutdownCh:
				return
			}
		}
	}()
	log.Println("human_gate soft-timeout sweeper started (15m interval)")

	// 10c. Stale session sweeper — end agent sessions left active longer than 6h every hour.
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				if n, sweepErr := sessionRepo.EndStale(ctx, 6*time.Hour); sweepErr != nil {
					log.Printf("[session-sweeper] ERROR: %v", sweepErr)
				} else if n > 0 {
					log.Printf("[session-sweeper] Ended %d stale sessions", n)
				}
				cancel()
			case <-schedulerShutdownCh:
				return
			}
		}
	}()
	log.Println("Stale session sweeper started (1h interval)")

	// 11. Start server with graceful shutdown.
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("Starting evc-mesh API server on %s", addr)

	// Start server in a goroutine.
	go func() {
		if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	// Wait for interrupt signal.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	// Stop the scheduler.
	close(schedulerShutdownCh)

	// Graceful shutdown with timeout.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	if err := e.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}

	// Stop checkout reaper.
	reaperCancel()

	// Stop every Telegram bot poller.
	telegramCancel()

	// Flush activity tracker and cancel its background loop.
	activityCancel()
	activityTracker.Flush(shutdownCtx)

	// Close WebSocket hub and shared Redis (also used by the rate limiter).
	hubCancel()
	if err := sharedRedis.Close(); err != nil {
		log.Printf("Error closing shared Redis: %v", err)
	}
	if err := agentNotifyRedis.Close(); err != nil {
		log.Printf("Error closing agent-notify Redis: %v", err)
	}
	if err := ctxCacheRedis.Close(); err != nil {
		log.Printf("Error closing context-cache Redis: %v", err)
	}
	if err := wsBadgeRedis.Close(); err != nil {
		log.Printf("Error closing ws-badge Redis: %v", err)
	}

	// Close event bus.
	if eb != nil {
		if err := eb.Close(); err != nil {
			log.Printf("Error closing event bus: %v", err)
		}
	}

	log.Println("Server stopped")
}

// redactInviteToken strips the invite token out of a request URI before the
// access log sees it.
//
// GET /api/v1/invites/:token and its /accept sibling are public routes whose
// path segment IS the credential: anyone holding it can join the workspace. The
// access logger printed the raw URI, so every time an invitee opened their link
// the token was written to the log in full — and logs get shipped to collectors
// that far more people can read than the workspace has members.
//
// Only the public top-level route is redacted. The admin route
// /api/v1/workspaces/:ws_id/invites/:invite_id carries a database id, not a
// secret, and stays readable.
func redactInviteToken(uri string) string {
	const prefix = "/api/v1/invites/"
	if !strings.HasPrefix(uri, prefix) {
		return uri
	}
	rest := uri[len(prefix):]
	if rest == "" {
		return uri
	}
	// Preserve any trailing path ("/accept") and query string so the log line
	// still says which operation it was.
	if end := strings.IndexAny(rest, "/?"); end != -1 {
		return prefix + "<redacted>" + rest[end:]
	}
	return prefix + "<redacted>"
}

// documentWatchQuietWindow reads DOCUMENT_WATCH_QUIET_WINDOW, the idle period an
// editor must leave before their edits are announced to a document's watchers.
//
// It exists as a knob because the right value is a judgement about how people
// work rather than a property of the system. The bounds are not: a window at or
// below the editor's 2-second autosave debounce coalesces nothing and restores
// the notification storm the feature was built to prevent, so a value that small
// is refused rather than honoured. An unparseable or out-of-range value falls
// back to the default and says so — a typo here would otherwise be discovered as
// "why is everyone getting a hundred emails".
func documentWatchQuietWindow() time.Duration {
	const (
		minWindow = 30 * time.Second
		maxWindow = 24 * time.Hour
	)
	raw := strings.TrimSpace(os.Getenv("DOCUMENT_WATCH_QUIET_WINDOW"))
	if raw == "" {
		return 0 // service applies its own default
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("[doc-watch] DOCUMENT_WATCH_QUIET_WINDOW=%q is not a duration (e.g. \"5m\") — using the default", raw)
		return 0
	}
	if d < minWindow || d > maxWindow {
		log.Printf("[doc-watch] DOCUMENT_WATCH_QUIET_WINDOW=%s is outside [%s, %s] — using the default", d, minWindow, maxWindow)
		return 0
	}
	return d
}

// humanGateSoftTimeoutWindow reads HUMAN_GATE_SOFT_TIMEOUT, the arm-age past which a
// soft-classified human_gate is eligible for the 15-minute sweeper's release (contract
// docs/human-gate-decision-recorded.md §5, task #4dc9467b).
//
// It exists as a knob for the same reason DefaultHumanGateSoftTimeout (24h) was a
// hardcoded constant until now: proving the sweeper actually releases a gate at prod
// scale costs one full window of real wait time, which makes the mechanism cheaper to
// believe than to verify. A lower bound still applies — a window at or below the
// sweeper's own 15-minute tick would race the first tick after arming and make a
// release look flaky when it isn't, so a value that small is refused rather than
// honoured. An unparseable or out-of-range value falls back to the default and says so.
func humanGateSoftTimeoutWindow() time.Duration {
	const (
		minWindow = 15 * time.Minute
		maxWindow = 30 * 24 * time.Hour
	)
	raw := strings.TrimSpace(os.Getenv("HUMAN_GATE_SOFT_TIMEOUT"))
	if raw == "" {
		return 0 // service applies its own default (DefaultHumanGateSoftTimeout)
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("[human-gate-soft-timeout] HUMAN_GATE_SOFT_TIMEOUT=%q is not a duration (e.g. \"2h\") — using the default", raw)
		return 0
	}
	if d < minWindow || d > maxWindow {
		log.Printf("[human-gate-soft-timeout] HUMAN_GATE_SOFT_TIMEOUT=%s is outside [%s, %s] — using the default", d, minWindow, maxWindow)
		return 0
	}
	return d
}
