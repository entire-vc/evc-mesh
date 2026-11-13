package main

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/config"
	"github.com/entire-vc/evc-mesh/internal/embedding"
	"github.com/entire-vc/evc-mesh/internal/eventbus"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/external"
	githubext "github.com/entire-vc/evc-mesh/internal/extensions/github"
	gitlabext "github.com/entire-vc/evc-mesh/internal/extensions/gitlab"
	runnerext "github.com/entire-vc/evc-mesh/internal/extensions/runner"
	"github.com/entire-vc/evc-mesh/internal/extensions/secretstore"
	sparkext "github.com/entire-vc/evc-mesh/internal/extensions/spark"
	teamrelayext "github.com/entire-vc/evc-mesh/internal/extensions/team-relay"
	"github.com/entire-vc/evc-mesh/internal/handler"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/reconciler"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/internal/storage"
	"github.com/entire-vc/evc-mesh/internal/themes"
	wsHub "github.com/entire-vc/evc-mesh/internal/ws"
)

// apiDeps carries what one API module hands to the modules registered after
// it (see apiModules): repositories, services, handlers, the Echo instance and
// its route groups, and the shutdown hooks serve() needs. A module reads what
// it uses at its top (taskService := deps.taskService) and publishes what later
// modules need at its end (deps.fooHandler = fooHandler).
//
// A field is added here only when a later module or serve() reads it; a value
// used inside one module stays a local of that module.
type apiDeps struct {
	cfg                              *config.Config
	db                               *sqlx.DB
	workspaceRepo                    *postgres.WorkspaceRepo
	projectRepo                      *postgres.ProjectRepo
	taskRepo                         *postgres.TaskRepo
	taskStatusRepo                   *postgres.TaskStatusRepo
	taskDependencyRepo               *postgres.TaskDependencyRepo
	commentRepo                      *postgres.CommentRepo
	closedFollowUpRootRepo           *postgres.ClosedFollowUpRootRepo
	closedFollowUpPendingRepo        *postgres.ClosedFollowUpPendingRepo
	humanGateDecisionRepo            *postgres.HumanGateDecisionRepo
	taskExternalRefRepo              *postgres.TaskExternalRefRepo
	artifactRepo                     *postgres.ArtifactRepo
	documentRepo                     *postgres.DocumentRepo
	documentAttachmentRepo           *postgres.DocumentAttachmentRepo
	documentCommentRepo              *postgres.DocumentCommentRepo
	documentWatchRepo                *postgres.DocumentWatchRepo
	agentRepo                        *postgres.AgentRepo
	eventBusRepo                     *postgres.EventBusMessageRepo
	activityLogRepo                  *postgres.ActivityLogRepo
	userRepo                         *postgres.UserRepo
	refreshTokenRepo                 *postgres.RefreshTokenRepo
	workspaceMemberRepo              *postgres.WorkspaceMemberRepo
	projectMemberRepo                *postgres.ProjectMemberRepo
	webhookRepo                      *postgres.WebhookRepo
	savedViewRepo                    *postgres.SavedViewRepo
	vcsLinkRepo                      *postgres.VCSLinkRepo
	integrationRepo                  *postgres.IntegrationRepo
	extensionRegistry                *extensions.Registry
	instanceSettingRepo              *postgres.InstanceExtensionSettingRepo
	secretRouter                     *secretstore.Router
	extensionInstanceStore           *extensions.DBInstanceStore
	extensionPolicyRepo              *postgres.InstanceExtensionPolicyRepo
	extensionDeps                    extensions.Deps
	extensionInstanceSettingsHandler *handler.ExtensionInstanceSettingsHandler
	extensionInstanceAdminHandler    *handler.ExtensionInstanceAdminHandler
	runtimeRepo                      *postgres.RuntimeRepo
	projectUpdateRepo                *postgres.ProjectUpdateRepo
	initiativeRepo                   *postgres.InitiativeRepo
	ruleRepo                         *postgres.RuleRepo
	wsRuleRepo                       *postgres.WorkspaceRuleRepo
	projRuleRepo                     *postgres.ProjectRuleRepo
	ruleViolationLogRepo             *postgres.RuleViolationLogRepo
	recurringRepo                    *postgres.RecurringRepo
	taskTemplateRepo                 *postgres.TaskTemplateRepo
	notificationRepo                 *postgres.NotificationRepo
	autoTransRuleRepo                *postgres.AutoTransitionRuleRepo
	memoryRepo                       *postgres.MemoryRepo
	schedulerStateRepo               *postgres.SchedulerStateRepo
	memoryEdgesRepo                  *postgres.MemoryEdgesRepo
	memoryChunkRepo                  *postgres.MemoryChunkRepo
	documentChunkRepo                *postgres.DocumentChunkRepo
	commentMentionRepo               *postgres.CommentMentionRepo
	commentDeliveryRepo              *postgres.CommentDeliveryOutcomeRepo
	documentCommentMentionRepo       *postgres.DocumentCommentMentionRepo
	authService                      *auth.Service
	workspaceService                 service.WorkspaceService
	projectService                   service.ProjectService
	customFieldService               service.CustomFieldService
	ruleService                      service.RuleService
	eventBusService                  service.EventBusService
	embedder                         embedding.Embedder
	memReconciler                    *reconciler.MemoryReconciler
	memoryService                    service.MemoryService
	secretRepo                       *postgres.SecretRefRepo
	extensionProjectScopeRepo        *postgres.ExtensionProjectScopeRepo
	extensionDeclared                func(string) ([]string, bool)
	webhookService                   service.WebhookService
	projectIntegrationRepo           *postgres.ProjectIntegrationRepo
	secretService                    service.SecretService
	secretMaterializationService     service.SecretMaterializationService
	agentWorkspaceGrantRepo          *postgres.AgentWorkspaceGrantRepo
	agentService                     service.AgentService
	agentKeyHandler                  *handler.AgentKeyHandler
	workspaceExportHandler           *handler.WorkspaceExportHandler
	extensionActorService            service.ExtensionActorService
	extensionActorHandler            *handler.ExtensionActorHandler
	agentWorkspaceGrantService       service.AgentWorkspaceGrantService
	oauthRepo                        *postgres.OAuthRepo
	oauthService                     service.OAuthService
	chatSessionService               service.ChatSessionService
	agentNotifyRedis                 *redis.Client
	agentEventsRepo                  *postgres.AgentEventsRepo
	agentNotifySvc                   service.AgentNotifyService
	ctxCacheRedis                    *redis.Client
	ctxCacheSvc                      *service.ContextCacheService
	wsBadgeRedis                     *redis.Client
	wsPublisher                      service.WSPublisher
	chatEvents                       *service.ChatEventPublisher
	rulesService                     service.RulesService
	pushService                      service.PushService
	emailSvc                         service.EmailService
	telegramGate                     service.TelegramGate
	telegramClient                   service.TelegramClient
	telegramBotManager               *service.TelegramBotManager
	notificationService              service.NotificationService
	vcsIntegrationResolver           *service.VCSIntegrationResolver
	githubExt                        *githubext.Provider
	gitlabExt                        *gitlabext.Provider
	teamRelayExt                     *teamrelayext.Provider
	runnerProv                       *runnerext.Provider
	sparkIntegrationResolver         *sparkext.Provider
	taskService                      service.TaskService
	autoTransitionSvc                service.AutoTransitionService
	taskStatusService                service.TaskStatusService
	mentionService                   service.MentionService
	documentMentionService           service.DocumentMentionService
	commentService                   service.CommentService
	depService                       service.TaskDependencyService
	activityLogService               service.ActivityLogService
	secretMaterializeHandler         *handler.SecretMaterializeHandler
	workspaceMemberService           service.WorkspaceMemberService
	projectMemberService             service.ProjectMemberService
	inviteService                    service.WorkspaceInviteService
	savedViewService                 service.SavedViewService
	vcsLinkService                   service.VCSLinkService
	integrationService               service.IntegrationService
	analyticsService                 service.AnalyticsService
	projectUpdateService             service.ProjectUpdateService
	initiativeService                service.InitiativeService
	triageService                    service.TriageService
	recurringService                 service.RecurringService
	taskTemplateService              service.TaskTemplateService
	artifactService                  service.ArtifactService
	chatAttachmentStore              handler.ChatAttachmentStore
	s3Client                         *storage.S3Client
	documentWatchService             service.DocumentWatchService
	projectIntegrationService        service.ProjectIntegrationService
	teamRelayMountService            service.TeamRelayMountService
	documentIndexer                  *service.DocumentIndexer
	documentService                  service.DocumentService
	documentAttachmentService        service.DocumentAttachmentService
	documentExportService            service.DocumentExportService
	documentCommentService           service.DocumentCommentService
	eb                               *eventbus.EventBus
	authHandler                      *handler.AuthHandler
	workspaceHandler                 *handler.WorkspaceHandler
	projectHandler                   *handler.ProjectHandler
	sessionRepo                      *postgres.SessionRepo
	taskHandler                      *handler.TaskHandler
	nativeReviewHandler              *handler.NativeReviewHandler
	transitionReceiptHandler         *handler.TransitionReceiptHandler
	statusHandler                    *handler.TaskStatusHandler
	commentHandler                   *handler.CommentHandler
	humanGateDecisionHandler         *handler.HumanGateDecisionHandler
	artifactHandler                  *handler.ArtifactHandler
	documentHandler                  *handler.DocumentHandler
	documentExportHandler            *handler.DocumentExportHandler
	documentAttachmentHandler        *handler.DocumentAttachmentHandler
	documentCommentHandler           *handler.DocumentCommentHandler
	documentWatchHandler             *handler.DocumentWatchHandler
	depHandler                       *handler.DependencyHandler
	agentHandler                     *handler.AgentHandler
	agentControllerHandler           *handler.AgentControllerHandler
	httpShutdownCh                   chan struct{}
	agentRepositoryAccessHandler     *handler.AgentRepositoryAccessHandler
	eventHandler                     *handler.EventHandler
	activityHandler                  *handler.ActivityHandler
	secretHandler                    *handler.SecretHandler
	customFieldHandler               *handler.CustomFieldHandler
	taskContextHandler               *handler.TaskContextHandler
	webhookHandler                   *handler.WebhookHandler
	extensionEventHandler            *handler.ExtensionEventHandler
	savedViewHandler                 *handler.SavedViewHandler
	vcsLinkHandler                   *handler.VCSLinkHandler
	integrationHandler               *handler.IntegrationHandler
	externalRuntime                  *external.Runtime
	extensionWorkspaceHandler        *handler.ExtensionWorkspaceHandler
	extensionUIHandler               *handler.ExtensionUIHandler
	runtimeHandler                   *handler.RuntimeHandler
	userRuntimeHandler               *handler.UserRuntimeHandler
	chatAgentPickerHandler           *handler.ChatAgentPickerHandler
	chatAttachmentHandler            *handler.ChatAttachmentHandler
	chatThreadDeleteRepo             *postgres.ChatThreadDeleteRepo
	chatThreadDeleteHandler          *handler.ChatThreadDeleteHandler
	agentChatHandler                 *handler.AgentChatHandler
	chatAuditHandler                 *handler.ChatAuditHandler
	chatRunnerHandler                *handler.ChatRunnerHandler
	existingSessionHandler           *handler.ExistingSessionHandler
	hostLocalQAHandler               *handler.HostLocalQAHandler
	chatClaimHandler                 *handler.ChatClaimHandler
	chatTurnEventsService            *service.ChatTurnEventsService
	chatRunnerEventsHandler          *handler.ChatRunnerEventsHandler
	chatTurnStartHandler             *handler.ChatTurnStartHandler
	analyticsHandler                 *handler.AnalyticsHandler
	projectUpdateHandler             *handler.ProjectUpdateHandler
	initiativeHandler                *handler.InitiativeHandler
	triageHandler                    *handler.TriageHandler
	ruleHandler                      *handler.RuleHandler
	rulesHandler                     *handler.RulesHandler
	recurringHandler                 *handler.RecurringHandler
	taskTemplateHandler              *handler.TaskTemplateHandler
	workspaceMemberHandler           *handler.WorkspaceMemberHandler
	agentWorkspaceGrantHandler       *handler.AgentWorkspaceGrantHandler
	oauthHandler                     *handler.OAuthHandler
	inviteHandler                    *handler.InviteHandler
	projectMemberHandler             *handler.ProjectMemberHandler
	notificationHandler              *handler.NotificationHandler
	pushSubscriptionHandler          *handler.PushSubscriptionHandler
	autoTransHandler                 *handler.AutoTransitionHandler
	memoryHandler                    *handler.MemoryHandler
	mentionHandler                   *handler.MentionHandler
	documentMentionHandler           *handler.DocumentMentionHandler
	userUIStateHandler               *handler.UserUIStateHandler
	projectIntegrationHandler        *handler.ProjectIntegrationHandler
	trSearchHandler                  *handler.TrSearchHandler
	trDocumentHandler                *handler.TrDocumentHandler
	trMountHandler                   *handler.TrMountHandler
	canonicalUpdatesHandler          *handler.CanonicalUpdatesHandler
	mentionablesHandler              *handler.MentionablesHandler
	e                                *echo.Echo
	ipTrusted                        bool
	sharedRedis                      *redis.Client
	hub                              *wsHub.Hub
	hubCancel                        context.CancelFunc
	hubDone                          chan struct{}
	activityTracker                  *mw.ActivityTracker
	activityCancel                   context.CancelFunc
	toolBreakdownTracker             *mw.ToolBreakdownTracker
	toolBreakdownCancel              context.CancelFunc
	reaperCancel                     context.CancelFunc
	telegramCancel                   context.CancelFunc
	v1                               *echo.Group
	themeService                     *themes.Service
	themeHandler                     *handler.ThemeHandler
	api                              *echo.Group
	rbac                             func(perm mw.Permission) echo.MiddlewareFunc
	connectorRBAC                    func(perm mw.Permission) echo.MiddlewareFunc
	connectorSelfOrRBAC              func(agentIDParam string, perm mw.Permission) echo.MiddlewareFunc
	runtimeCatalogRBAC               echo.MiddlewareFunc
	wsAccess                         echo.MiddlewareFunc
	idempotencyRepo                  *postgres.IdempotencyRepo
	idempotent                       echo.MiddlewareFunc
	projAccess                       echo.MiddlewareFunc
	bodyWS                           echo.MiddlewareFunc
	schedulerShutdownCh              chan struct{}
	cancelOutbox                     context.CancelFunc
	outboxDone                       chan struct{}

	// exitFns run when main returns, last registered first, in place of
	// the defers main() used to hold before it was split into modules.
	exitFns []func()
}

// atExit registers fn to run when main returns. It replaces a defer that
// lived directly in main() before the split, so the order is the same: last
// registered, first run.
func (d *apiDeps) atExit(fn func()) {
	d.exitFns = append(d.exitFns, fn)
}

// runAtExit runs the functions registered with atExit, last registered first.
func (d *apiDeps) runAtExit() {
	for i := len(d.exitFns) - 1; i >= 0; i-- {
		d.exitFns[i]()
	}
}
