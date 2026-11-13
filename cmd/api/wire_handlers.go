package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/extensions/external"
	"github.com/entire-vc/evc-mesh/internal/extensions/uictx"
	"github.com/entire-vc/evc-mesh/internal/handler"
	"github.com/entire-vc/evc-mesh/internal/hostlocalqa"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// wireHandlers creates the HTTP handlers shared by the route modules.
func wireHandlers(deps *apiDeps) {
	cfg := deps.cfg
	db := deps.db
	projectRepo := deps.projectRepo
	taskRepo := deps.taskRepo
	taskDependencyRepo := deps.taskDependencyRepo
	agentRepo := deps.agentRepo
	userRepo := deps.userRepo
	workspaceMemberRepo := deps.workspaceMemberRepo
	projectMemberRepo := deps.projectMemberRepo
	vcsLinkRepo := deps.vcsLinkRepo
	integrationRepo := deps.integrationRepo
	extensionRegistry := deps.extensionRegistry
	extensionPolicyRepo := deps.extensionPolicyRepo
	runtimeRepo := deps.runtimeRepo
	initiativeRepo := deps.initiativeRepo
	authService := deps.authService
	workspaceService := deps.workspaceService
	projectService := deps.projectService
	customFieldService := deps.customFieldService
	ruleService := deps.ruleService
	eventBusService := deps.eventBusService
	memoryService := deps.memoryService
	secretRepo := deps.secretRepo
	extensionProjectScopeRepo := deps.extensionProjectScopeRepo
	extensionDeclared := deps.extensionDeclared
	webhookService := deps.webhookService
	projectIntegrationRepo := deps.projectIntegrationRepo
	secretService := deps.secretService
	agentService := deps.agentService
	extensionActorService := deps.extensionActorService
	extensionActorHandler := deps.extensionActorHandler
	agentWorkspaceGrantService := deps.agentWorkspaceGrantService
	oauthService := deps.oauthService
	chatSessionService := deps.chatSessionService
	agentNotifyRedis := deps.agentNotifyRedis
	agentEventsRepo := deps.agentEventsRepo
	ctxCacheSvc := deps.ctxCacheSvc
	wsBadgeRedis := deps.wsBadgeRedis
	chatEvents := deps.chatEvents
	rulesService := deps.rulesService
	pushService := deps.pushService
	telegramGate := deps.telegramGate
	telegramClient := deps.telegramClient
	telegramBotManager := deps.telegramBotManager
	notificationService := deps.notificationService
	vcsIntegrationResolver := deps.vcsIntegrationResolver
	teamRelayExt := deps.teamRelayExt
	sparkIntegrationResolver := deps.sparkIntegrationResolver
	taskService := deps.taskService
	autoTransitionSvc := deps.autoTransitionSvc
	taskStatusService := deps.taskStatusService
	mentionService := deps.mentionService
	documentMentionService := deps.documentMentionService
	commentService := deps.commentService
	activityLogService := deps.activityLogService
	workspaceMemberService := deps.workspaceMemberService
	projectMemberService := deps.projectMemberService
	inviteService := deps.inviteService
	savedViewService := deps.savedViewService
	vcsLinkService := deps.vcsLinkService
	integrationService := deps.integrationService
	analyticsService := deps.analyticsService
	projectUpdateService := deps.projectUpdateService
	initiativeService := deps.initiativeService
	triageService := deps.triageService
	recurringService := deps.recurringService
	taskTemplateService := deps.taskTemplateService
	s3Client := deps.s3Client
	documentWatchService := deps.documentWatchService
	projectIntegrationService := deps.projectIntegrationService
	teamRelayMountService := deps.teamRelayMountService
	documentIndexer := deps.documentIndexer
	documentService := deps.documentService
	documentAttachmentService := deps.documentAttachmentService
	documentExportService := deps.documentExportService
	documentCommentService := deps.documentCommentService

	// 7. Create all handler instances.
	authHandler := handler.NewAuthHandler(authService)
	workspaceHandler := handler.NewWorkspaceHandler(workspaceService)
	if s3Client != nil {
		workspaceHandler.WithStorage(s3Client)
	}
	projectHandler := handler.NewProjectHandler(projectService)
	sessionRepo := postgres.NewSessionRepo(db)
	taskHandler := handler.NewTaskHandlerWithSessions(taskService, sessionRepo).
		WithCommentService(commentService).
		WithDependencyReader(taskDependencyRepo)
	// native review admission: no clock protocol is proven yet, so the repo gets
	// none and reserve is denied by default; no trusted issuer signer is wired
	// either, so issue is denied too (release gates OPEN).
	// Issuer signer: absent = nil = receipts refused. A configured but invalid
	// key file is fatal (fail closed). The clock stays nil: reserve remains denied.
	var nativeSigner postgres.NativeSigner
	if keyFile := os.Getenv("NATIVE_REVIEW_SIGNER_KEY_FILE"); keyFile != "" {
		fs, serr := postgres.LoadFileSigner(keyFile)
		if serr != nil {
			log.Fatalf("NATIVE_REVIEW_SIGNER_KEY_FILE: %v", serr)
		}
		nativeSigner = fs
	}
	nativeReviewHandler := handler.NewNativeReviewHandler(postgres.NewNativeReviewRepo(db, nil, nativeSigner))
	transitionReceiptHandler := handler.NewTransitionReceiptHandler(
		service.NewTransitionReceiptService(postgres.NewTransitionReceiptRepo(db), taskRepo), taskService)
	statusHandler := handler.NewTaskStatusHandler(taskStatusService)
	commentHandler := handler.NewCommentHandler(commentService, taskService)
	humanGateDecisionHandler := handler.NewHumanGateDecisionHandler(commentService, taskService)
	artifactHandler := handler.NewArtifactHandler(deps.artifactService, taskService).WithMaxFileBytes(cfg.ArtifactMaxBytes)
	documentHandler := handler.NewDocumentHandler(documentService)
	documentExportHandler := handler.NewDocumentExportHandler(documentExportService)
	documentAttachmentHandler := handler.NewDocumentAttachmentHandler(documentAttachmentService)
	documentCommentHandler := handler.NewDocumentCommentHandler(documentCommentService)
	documentWatchHandler := handler.NewDocumentWatchHandler(documentWatchService)
	depHandler := handler.NewDependencyHandler(deps.depService, taskService).WithGraphReader(taskDependencyRepo).WithVCSLinks(vcsLinkRepo.ListByTask).WithBindingProposals(postgres.NewBindingProposalRepo(db)).WithConditionResolver(autoTransitionSvc)
	agentHandler := handler.NewAgentHandlerWithEvents(agentService, taskService, taskStatusService, agentNotifyRedis, agentEventsRepo, sessionRepo)
	agentControllerRepo := postgres.NewAgentControllerRepo(db)
	agentHandler.SetControllerRepo(agentControllerRepo)
	agentControllerHandler := handler.NewAgentControllerHandler(agentControllerRepo).WithChatSnapshots(agentControllerRepo)
	// Closed first thing on SIGTERM so SSE streams and long-polls end before
	// e.Shutdown, which would otherwise wait on them until its deadline.
	httpShutdownCh := make(chan struct{})
	agentHandler.SetShutdownSignal(httpShutdownCh)
	agentHandler.SetWorkspaceService(workspaceService)
	agentRepositoryAccessRepo := postgres.NewAgentRepositoryAccessRepo(db)
	agentHandler.SetRepositoryAccessReader(agentRepositoryAccessRepo)
	agentRepositoryAccessHandler := handler.NewAgentRepositoryAccessHandler(agentRepositoryAccessRepo)
	eventHandler := handler.NewEventHandler(eventBusService)
	activityHandler := handler.NewActivityHandler(activityLogService)
	secretHandler := handler.NewSecretHandler(secretService, activityLogService)
	customFieldHandler := handler.NewCustomFieldHandler(customFieldService)
	taskContextHandler := handler.NewTaskContextHandlerWithCache(taskService, commentService, deps.artifactService, deps.depService, eventBusService, ctxCacheSvc, initiativeRepo)
	webhookHandler := handler.NewWebhookHandler(webhookService)
	extensionEventHandler := handler.NewExtensionEventHandler(service.WithSubscriptionConsents(service.NewExtensionEventSubscriptionService(
		postgres.NewExtensionEventSubscriptionRepo(db), integrationRepo, postgres.NewInstalledExtensionRepo(db)),
		postgres.NewIntegrationConsentRepo(db)))
	savedViewHandler := handler.NewSavedViewHandler(savedViewService)
	vcsLinkHandler := handler.NewVCSLinkHandler(vcsLinkService,
		handler.WithVCSIntegrationResolver(vcsIntegrationResolver), // resolves webhook secrets fresh per request (see above); supersedes the static WithGitHubWebhookSecret/WithGitLabWebhookSecret options
		handler.WithWebhookDedupStore(handler.NewRedisWebhookDedupStore(agentNotifyRedis)),
	)
	integrationHandler := handler.NewIntegrationHandler(integrationService, telegramClient, telegramBotManager).
		WithTelegramGate(telegramGate).
		WithStatusResolver(service.NewIntegrationStatusResolver(integrationRepo, vcsIntegrationResolver, sparkIntegrationResolver, teamRelayExt, false).WithTelegramGate(telegramGate))
	extensionConsentRepo := postgres.NewIntegrationConsentRepo(db)
	service.WithConsents(extensionActorService, extensionConsentRepo)
	service.WithDeclaredCapabilities(extensionActorService, extensionDeclared)
	extensionRegistry.WithIntegrationConsents(extensionConsentRepo)
	// Extensions installed from a repository (runtime=external): registered in
	// the registry from installed_extensions at start and after install, update
	// and rollback; the connect hook runs the install handshake with the
	// service and subscribes it to its manifest events.
	externalHTTPClient := service.NewExtensionHTTPClient()
	externalHandshaker := &external.Handshaker{
		Client: externalHTTPClient, Conns: integrationRepo, Secrets: secretRepo,
		Reader: secretRepo, Subs: postgres.NewExtensionEventSubscriptionRepo(db),
		Tokens: extensionActorService, Consents: extensionConsentRepo,
	}
	extensionActorHandler.WithTokenDelivery(&external.TokenRotator{Reg: extensionRegistry, Conns: integrationRepo, HS: externalHandshaker})
	externalRuntime := &external.Runtime{
		Reg: extensionRegistry, Installed: postgres.NewInstalledExtensionRepo(db), Deps: deps.extensionDeps,
		Client: externalHTTPClient, HS: externalHandshaker,
	}
	if syncErr := externalRuntime.Sync(context.Background()); syncErr != nil {
		log.Printf("[extensions] external runtime sync failed (builtin extensions are unaffected): %v", syncErr)
	}
	go externalRuntime.Run(context.Background(), time.Minute)
	extensionWorkspaceHandler := handler.NewExtensionWorkspaceHandler(
		service.NewExtensionWorkspaceService(extensionRegistry, integrationRepo, extensionConsentRepo).
			WithAllowList(extensionPolicyRepo).
			WithInstalledReleases(postgres.NewInstalledExtensionRepo(db)).
			WithVCSActivity(vcsLinkRepo).
			WithExternalHook(&external.Hook{Reg: extensionRegistry, Conns: integrationRepo, HS: externalHandshaker}).
			WithSaveHook("telegram", service.NewTelegramSaveHook(telegramClient, telegramBotManager, telegramGate))).
		WithProjectScope(extensionRegistry, extensionProjectScopeRepo)
	// Mesh as the identity provider of external-extension iframe screens: instance
	// signing key (rotates with an overlap), JWKS and the context token.
	uiContextService := uictx.New(postgres.NewSigningKeyRepo(db), cfg.Email.BaseURL)
	extensionUIHandler := handler.NewExtensionUIHandler(extensionRegistry, uiContextService, workspaceMemberRepo, userRepo, taskRepo, projectRepo, projectMemberRepo).
		WithSelfOrigins(append([]string{cfg.Email.BaseURL}, cfg.CORS.AllowOrigins...)...).
		WithProjectBindings(projectIntegrationService).
		WithProjectScope(extensionProjectScopeRepo)
	runtimeHandler := handler.NewRuntimeHandler(runtimeRepo)
	userRuntimeHandler := handler.NewUserRuntimeHandler(postgres.NewUserRuntimeRepo(db))
	chatAgentPickerHandler := handler.NewChatAgentPickerHandler(postgres.NewChatAgentPickerRepo(db))
	chatAttachmentHandler := handler.NewChatAttachmentHandler(postgres.NewChatAttachmentRepo(db), deps.chatAttachmentStore)
	// A nil *S3Client must not become a non-nil interface: the purge checks for nil.
	var chatObjects postgres.ChatObjectStore
	if s3Client != nil {
		chatObjects = s3Client
	}
	chatThreadDeleteRepo := postgres.NewChatThreadDeleteRepo(db, chatObjects)
	chatThreadDeleteHandler := handler.NewChatThreadDeleteHandler(chatThreadDeleteRepo).WithEvents(chatEvents)
	agentChatHandler := handler.NewAgentChatHandler(postgres.NewAgentChatRepo(db), integrationRepo, projectIntegrationRepo).WithEvents(chatEvents).
		WithAttachments(postgres.NewChatAttachmentRepo(db), deps.chatAttachmentStore).WithRunnerHints(agentNotifyRedis).
		WithRunnerSnapshots(agentControllerRepo)
	chatAuditHandler := handler.NewChatAuditHandler(postgres.NewChatAuditRepo(db))
	chatRunnerRepo := postgres.NewChatRunnerRepo(db)
	chatRunnerHandler := handler.NewChatRunnerHandler(chatRunnerRepo)
	existingSessionHandler := handler.NewExistingSessionHandler(postgres.NewExistingSessionRepo(db), chatRunnerRepo)
	hostLocalQAPolicy, qaPolicyErr := hostlocalqa.LoadPolicy()
	if qaPolicyErr != nil {
		log.Fatalf("host-local QA policy: %v", qaPolicyErr)
	}
	// OFF unless explicitly "true": the routes then refuse every authenticated caller.
	hostLocalQAHandler := handler.NewHostLocalQAHandler(postgres.NewHostLocalQARepo(db, hostLocalQAPolicy), chatRunnerRepo,
		hostLocalQAPolicy, os.Getenv("HOST_LOCAL_QA_ASSIGNMENT_ENABLED") == "true")
	chatClaimHandler := handler.NewChatClaimHandler(postgres.NewChatRunnerRepo(db), agentNotifyRedis, deps.chatAttachmentStore)
	chatClaimHandler.SetShutdownSignal(httpShutdownCh)
	chatTurnEventsService := service.NewChatTurnEventsService(
		postgres.NewChatTurnEventsRepo(db),
		chatEvents).
		WithRateLimiter(service.NewRedisChatRateLimiter(wsBadgeRedis))
	chatRunnerEventsHandler := handler.NewChatRunnerEventsHandler(chatTurnEventsService)
	chatTurnStartHandler := handler.NewChatTurnStartHandler(postgres.NewChatRunnerRepo(db), chatSessionService)
	analyticsHandler := handler.NewAnalyticsHandler(analyticsService)
	projectUpdateHandler := handler.NewProjectUpdateHandler(projectUpdateService)
	initiativeHandler := handler.NewInitiativeHandler(initiativeService)
	triageHandler := handler.NewTriageHandler(triageService)
	ruleHandler := handler.NewRuleHandler(ruleService)
	rulesHandler := handler.NewRulesHandler(rulesService)
	recurringHandler := handler.NewRecurringHandler(recurringService, projectService, workspaceService)
	taskTemplateHandler := handler.NewTaskTemplateHandler(taskTemplateService)
	workspaceMemberHandler := handler.NewWorkspaceMemberHandler(workspaceMemberService)
	agentWorkspaceGrantHandler := handler.NewAgentWorkspaceGrantHandler(agentWorkspaceGrantService)
	oauthHandler := handler.NewOAuthHandler(oauthService, cfg.Email.BaseURL)
	inviteHandler := handler.NewInviteHandler(inviteService, authService)
	projectMemberHandler := handler.NewProjectMemberHandler(projectMemberService)
	notificationHandler := handler.NewNotificationHandler(notificationService, workspaceMemberRepo)
	pushSubscriptionHandler := handler.NewPushSubscriptionHandler(pushService)
	autoTransHandler := handler.NewAutoTransitionHandler(autoTransitionSvc)
	memoryHandler := handler.NewMemoryHandler(memoryService, workspaceMemberRepo)
	memoryHandler.SetDocumentIndexer(documentIndexer)
	memoryHandler.SetAgentRoleLookup(func(ctx context.Context, agentID uuid.UUID) (string, error) {
		a, err := agentRepo.GetByID(ctx, agentID)
		if err != nil || a == nil {
			return "", err
		}
		return a.Role, nil
	})
	memoryHandler.SetProjectRoleLookup(handler.ProjectMemberRoleFromDB(db))
	mentionHandler := handler.NewMentionHandler(mentionService)
	documentMentionHandler := handler.NewDocumentMentionHandler(documentMentionService)
	userUIStateHandler := handler.NewUserUIStateHandler(db)
	projectIntegrationHandler := handler.NewProjectIntegrationHandler(projectIntegrationService)
	trSearchHandler := handler.NewTrSearchHandler(projectIntegrationService, projectService, teamRelayExt)
	trDocumentHandler := handler.NewTrDocumentHandler(projectIntegrationService, projectService, teamRelayExt)
	trMountHandler := handler.NewTrMountHandler(teamRelayMountService)
	canonicalUpdatesHandler := handler.NewCanonicalUpdatesHandler(memoryService, sessionRepo, agentService)
	mentionablesService := service.NewMentionablesService(agentRepo, userRepo)
	mentionablesHandler := handler.NewMentionablesHandler(mentionablesService)

	deps.authHandler = authHandler
	deps.workspaceHandler = workspaceHandler
	deps.projectHandler = projectHandler
	deps.sessionRepo = sessionRepo
	deps.taskHandler = taskHandler
	deps.nativeReviewHandler = nativeReviewHandler
	deps.transitionReceiptHandler = transitionReceiptHandler
	deps.statusHandler = statusHandler
	deps.commentHandler = commentHandler
	deps.humanGateDecisionHandler = humanGateDecisionHandler
	deps.artifactHandler = artifactHandler
	deps.documentHandler = documentHandler
	deps.documentExportHandler = documentExportHandler
	deps.documentAttachmentHandler = documentAttachmentHandler
	deps.documentCommentHandler = documentCommentHandler
	deps.documentWatchHandler = documentWatchHandler
	deps.depHandler = depHandler
	deps.agentHandler = agentHandler
	deps.agentControllerHandler = agentControllerHandler
	deps.httpShutdownCh = httpShutdownCh
	deps.agentRepositoryAccessHandler = agentRepositoryAccessHandler
	deps.eventHandler = eventHandler
	deps.activityHandler = activityHandler
	deps.secretHandler = secretHandler
	deps.customFieldHandler = customFieldHandler
	deps.taskContextHandler = taskContextHandler
	deps.webhookHandler = webhookHandler
	deps.extensionEventHandler = extensionEventHandler
	deps.savedViewHandler = savedViewHandler
	deps.vcsLinkHandler = vcsLinkHandler
	deps.integrationHandler = integrationHandler
	deps.externalRuntime = externalRuntime
	deps.extensionWorkspaceHandler = extensionWorkspaceHandler
	deps.extensionUIHandler = extensionUIHandler
	deps.runtimeHandler = runtimeHandler
	deps.userRuntimeHandler = userRuntimeHandler
	deps.chatAgentPickerHandler = chatAgentPickerHandler
	deps.chatAttachmentHandler = chatAttachmentHandler
	deps.chatThreadDeleteRepo = chatThreadDeleteRepo
	deps.chatThreadDeleteHandler = chatThreadDeleteHandler
	deps.agentChatHandler = agentChatHandler
	deps.chatAuditHandler = chatAuditHandler
	deps.chatRunnerHandler = chatRunnerHandler
	deps.existingSessionHandler = existingSessionHandler
	deps.hostLocalQAHandler = hostLocalQAHandler
	deps.chatClaimHandler = chatClaimHandler
	deps.chatTurnEventsService = chatTurnEventsService
	deps.chatRunnerEventsHandler = chatRunnerEventsHandler
	deps.chatTurnStartHandler = chatTurnStartHandler
	deps.analyticsHandler = analyticsHandler
	deps.projectUpdateHandler = projectUpdateHandler
	deps.initiativeHandler = initiativeHandler
	deps.triageHandler = triageHandler
	deps.ruleHandler = ruleHandler
	deps.rulesHandler = rulesHandler
	deps.recurringHandler = recurringHandler
	deps.taskTemplateHandler = taskTemplateHandler
	deps.workspaceMemberHandler = workspaceMemberHandler
	deps.agentWorkspaceGrantHandler = agentWorkspaceGrantHandler
	deps.oauthHandler = oauthHandler
	deps.inviteHandler = inviteHandler
	deps.projectMemberHandler = projectMemberHandler
	deps.notificationHandler = notificationHandler
	deps.pushSubscriptionHandler = pushSubscriptionHandler
	deps.autoTransHandler = autoTransHandler
	deps.memoryHandler = memoryHandler
	deps.mentionHandler = mentionHandler
	deps.documentMentionHandler = documentMentionHandler
	deps.userUIStateHandler = userUIStateHandler
	deps.projectIntegrationHandler = projectIntegrationHandler
	deps.trSearchHandler = trSearchHandler
	deps.trDocumentHandler = trDocumentHandler
	deps.trMountHandler = trMountHandler
	deps.canonicalUpdatesHandler = canonicalUpdatesHandler
	deps.mentionablesHandler = mentionablesHandler
}
