package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	runnerext "github.com/entire-vc/evc-mesh/internal/extensions/runner"
	"github.com/entire-vc/evc-mesh/internal/handler"
	githubapi "github.com/entire-vc/evc-mesh/internal/integration/github"
	gitlabapi "github.com/entire-vc/evc-mesh/internal/integration/gitlab"
	"github.com/entire-vc/evc-mesh/internal/integration/teamrelay"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/internal/storage"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// wireDomainServices creates the member, invite, storage, artifact and document
// services.
func wireDomainServices(deps *apiDeps) {
	cfg := deps.cfg
	db := deps.db
	workspaceRepo := deps.workspaceRepo
	projectRepo := deps.projectRepo
	taskRepo := deps.taskRepo
	taskStatusRepo := deps.taskStatusRepo
	taskDependencyRepo := deps.taskDependencyRepo
	commentRepo := deps.commentRepo
	closedFollowUpRootRepo := deps.closedFollowUpRootRepo
	closedFollowUpPendingRepo := deps.closedFollowUpPendingRepo
	humanGateDecisionRepo := deps.humanGateDecisionRepo
	taskExternalRefRepo := deps.taskExternalRefRepo
	artifactRepo := deps.artifactRepo
	documentRepo := deps.documentRepo
	documentAttachmentRepo := deps.documentAttachmentRepo
	documentCommentRepo := deps.documentCommentRepo
	documentWatchRepo := deps.documentWatchRepo
	agentRepo := deps.agentRepo
	activityLogRepo := deps.activityLogRepo
	userRepo := deps.userRepo
	refreshTokenRepo := deps.refreshTokenRepo
	workspaceMemberRepo := deps.workspaceMemberRepo
	projectMemberRepo := deps.projectMemberRepo
	savedViewRepo := deps.savedViewRepo
	vcsLinkRepo := deps.vcsLinkRepo
	integrationRepo := deps.integrationRepo
	projectUpdateRepo := deps.projectUpdateRepo
	initiativeRepo := deps.initiativeRepo
	recurringRepo := deps.recurringRepo
	taskTemplateRepo := deps.taskTemplateRepo
	documentChunkRepo := deps.documentChunkRepo
	commentMentionRepo := deps.commentMentionRepo
	commentDeliveryRepo := deps.commentDeliveryRepo
	documentCommentMentionRepo := deps.documentCommentMentionRepo
	authService := deps.authService
	embedder := deps.embedder
	webhookService := deps.webhookService
	projectIntegrationRepo := deps.projectIntegrationRepo
	secretMaterializationService := deps.secretMaterializationService
	agentWorkspaceGrantRepo := deps.agentWorkspaceGrantRepo
	agentService := deps.agentService
	agentNotifySvc := deps.agentNotifySvc
	ctxCacheSvc := deps.ctxCacheSvc
	wsPublisher := deps.wsPublisher
	rulesService := deps.rulesService
	emailSvc := deps.emailSvc
	notificationService := deps.notificationService
	vcsIntegrationResolver := deps.vcsIntegrationResolver
	teamRelayExt := deps.teamRelayExt
	runnerProv := deps.runnerProv
	taskService := deps.taskService
	autoTransitionSvc := deps.autoTransitionSvc

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
		service.WithCommentWebhookService(webhookService),
		service.WithCommentContextCacheInvalidator(ctxCacheSvc),
		service.WithCommentNotificationService(notificationService),
		service.WithCommentTaskService(taskService),
		service.WithCommentRulesService(rulesService),
		service.WithHumanGateDecisionRepo(humanGateDecisionRepo),
		service.WithCommentExternalRefRepo(taskExternalRefRepo),
		service.WithCommentDependencyRepo(taskDependencyRepo),
		service.WithClosedFollowUpRootRepo(closedFollowUpRootRepo),
		service.WithClosedFollowUpPendingRepo(closedFollowUpPendingRepo),
		service.WithCommentTextGateParsing(cfg.HumanGate.CommentTextParsing),
	)
	deps.depService = service.WithDependencyResolver(service.NewTaskDependencyService(taskDependencyRepo, taskRepo, activityLogRepo, projectRepo), autoTransitionSvc)
	deps.depService = service.WithDependencyVCSLinks(deps.depService, vcsLinkRepo.ListByTask)
	activityLogService := service.NewActivityLogService(activityLogRepo)
	// Built here, not beside secretMaterializationService above: every
	// issuance is journaled, so it needs the activity service.
	secretMaterializeHandler := handler.NewSecretMaterializeHandler(secretMaterializationService, activityLogService)

	// Member services.
	workspaceMemberService := service.NewWorkspaceMemberService(workspaceMemberRepo, userRepo, projectMemberRepo, activityLogRepo, agentRepo)
	if r, ok := workspaceMemberService.(interface {
		SetEventDispatcher(service.PublicEventDispatcher)
	}); ok {
		r.SetEventDispatcher(webhookService)
	}
	if r, ok := workspaceMemberService.(interface {
		SetAccountEditDeps(repository.WorkspaceRepository, service.RefreshTokenRevoker)
	}); ok {
		r.SetAccountEditDeps(workspaceRepo, refreshTokenRepo)
	} else {
		log.Fatal("workspace member service does not accept account-edit deps")
	}
	projectMemberService := service.NewProjectMemberService(projectMemberRepo, workspaceMemberRepo, projectRepo,
		service.WithAgentRepo(agentRepo),
		service.WithAgentWorkspaceGrantRepo(agentWorkspaceGrantRepo),
	)

	// Invite service (email-link flow).
	//
	// MESH_BASE_URL decides what every invite link points at. Left unset it
	// falls back to the Vite dev-server URL, and a deployed instance hands out
	// <dev-server-url>/accept-invite/<token> links that resolve to the
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
	// noreply@mesh.example.com — our domain, on someone else's mail server.
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
		service.WithVCSMRPipelineSource(func(ctx context.Context, ws uuid.UUID) (service.MRHeadPipelineSource, bool) {
			cfg, _, ok := vcsIntegrationResolver.ResolveGitLab(ctx, ws)
			if !ok || cfg.BaseURL == "" || cfg.Token == "" {
				return nil, false
			}
			return gitlabapi.NewClient(cfg.BaseURL, cfg.Token), true
		}), // stores the head pipeline when an MR merges, for the delivery-proof gate
		service.WithVCSGitHubEvidenceSource(func(ctx context.Context, ws uuid.UUID) (githubapi.PullRequestEvidenceSource, bool) {
			cfg, _, ok := vcsIntegrationResolver.ResolveGitHub(ctx, ws)
			if !ok || cfg.Token == "" {
				return nil, false
			}
			return githubapi.NewClient(cfg.Token), true
		}), // stores the merged PR's head commit and check result for the delivery-proof gate
		service.WithVCSMRStateSource(func(ctx context.Context, ws uuid.UUID) (gitlabapi.MergeRequestChecker, bool) {
			cfg, _, ok := vcsIntegrationResolver.ResolveGitLab(ctx, ws)
			if !ok || cfg.BaseURL == "" || cfg.Token == "" {
				return nil, false
			}
			return gitlabapi.NewClient(cfg.BaseURL, cfg.Token), true
		}), // lets add_vcs_link without status learn that an MR already merged/closed (same resolution as the done gate)
		service.WithVCSMREvidenceSource(func(ctx context.Context, ws uuid.UUID) (gitlabapi.MergeRequestEvidenceSource, bool) {
			cfg, _, ok := vcsIntegrationResolver.ResolveGitLab(ctx, ws)
			if !ok || cfg.BaseURL == "" || cfg.Token == "" {
				return nil, false
			}
			return gitlabapi.NewClient(cfg.BaseURL, cfg.Token), true
		}), // fills HEAD and head pipeline of an MR linked after its merge (no webhook will reach it)
		service.WithVCSDependencyResolver(autoTransitionSvc), // a recorded deploy releases dependents waiting on a "deployed" edge
		service.WithVCSProjectRepo(projectRepo),              // scopes webhook task-ref resolution to the workspace whose secret validated the request
	)
	// The runtime catalog is changed only through the versioned runtime API;
	// the Runner extension's Validate decides what a generic write may do, under
	// both the extension id and the legacy provider name.
	runnerGuard := service.ProviderGuard{
		Write: func(config json.RawMessage) error {
			var settings map[string]any
			if len(config) == 0 || json.Unmarshal(config, &settings) != nil {
				return apierror.BadRequest(runnerext.ErrVersionedAPI.Error())
			}
			return apierror.BadRequest(runnerProv.Validate(extensions.LevelWorkspace, settings).Error())
		},
		Delete: func() error { return apierror.BadRequest("disable runtime through the versioned runtime API") },
	}
	integrationService := service.NewIntegrationService(integrationRepo,
		service.WithProviderGuard(runnerGuard, domain.IntegrationProviderRunner, domain.IntegrationProviderAgentRuntime))
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
	// documentStore stays nil until the client is proven usable — a typed-nil
	// *S3Client in an interface is not nil, and the service's "storage not
	// configured" branch tests exactly that.
	var documentStore service.DocumentStore
	var attachmentStore service.StorageClient
	// Same typed-nil care as documentStore: the chat handler tests it against nil.
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
		deps.artifactService = service.NewArtifactService(artifactRepo, nil, activityLogRepo,
			service.WithArtifactLimits(cfg.ArtifactMaxBytes, cfg.ArtifactTaskMaxBytes))
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

		deps.artifactService = service.NewArtifactService(artifactRepo, s3Client, activityLogRepo,
			service.WithArtifactLimits(cfg.ArtifactMaxBytes, cfg.ArtifactTaskMaxBytes))
		documentStore = s3Client
		attachmentStore = s3Client
		deps.chatAttachmentStore = s3Client
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
	// key on every open of a mounted copy. The attachment repo wires the embed
	// importer (image embeds of mounted copies become document attachments);
	// without it the mount works, images in bodies just don't resolve.
	projectIntegrationService := service.NewProjectIntegrationService(projectIntegrationRepo, service.WithProjectIntegrationRelayResolver(teamRelayExt, projectRepo))
	teamRelayMountService := service.NewTeamRelayMountService(
		documentRepo, documentStore, projectIntegrationService, projectIntegrationRepo,
		service.WithAttachmentRepository(documentAttachmentRepo),
		service.WithRelayURLResolver(teamRelayExt, projectRepo),
	)

	// The comment repository is here because a body write moves the comments
	// anchored into that body: PATCH re-resolves every anchor against the markdown
	// it just stored, and nulls the ones whose text is gone. It is a required
	// argument, not an option — see the field's note in documentService.
	documentIndexer := service.NewDocumentIndexer(documentChunkRepo, embedder, documentStore, os.Getenv("DOC_INDEX_WRITE") == "true")
	documentService := service.NewDocumentService(documentRepo, documentStore, projectRepo, documentCommentRepo,
		service.WithDocumentIndexer(documentIndexer),
		service.WithDocumentBodyLimits(cfg.DocBodySoftLimit, cfg.DocBodyHardLimit),
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

	// Reuses documentAttachmentRepo and documentStore rather than depending on
	// documentAttachmentService: an export reads attachment ROWS across an
	// entire subtree (already proven in-scope by WalkExportTree), not one
	// attachment behind its own wsAccess-checked id, so the narrower
	// repository call is the right shape here, not the presigned-URL service.
	documentExportService := service.NewDocumentExportService(documentService, documentAttachmentRepo, documentStore)

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
	relayClient := teamrelay.NewClient(projectIntegrationRepo, taskRepo, projectRepo, teamRelayExt)
	if asc, ok := deps.artifactService.(service.ArtifactServiceConfigurable); ok {
		asc.SetRelayPublisher(relayClient)
	}

	deps.mentionService = mentionService
	deps.documentMentionService = documentMentionService
	deps.commentService = commentService
	deps.activityLogService = activityLogService
	deps.secretMaterializeHandler = secretMaterializeHandler
	deps.workspaceMemberService = workspaceMemberService
	deps.projectMemberService = projectMemberService
	deps.inviteService = inviteService
	deps.savedViewService = savedViewService
	deps.vcsLinkService = vcsLinkService
	deps.integrationService = integrationService
	deps.analyticsService = analyticsService
	deps.projectUpdateService = projectUpdateService
	deps.initiativeService = initiativeService
	deps.triageService = triageService
	deps.recurringService = recurringService
	deps.taskTemplateService = taskTemplateService
	deps.s3Client = s3Client
	deps.documentWatchService = documentWatchService
	deps.projectIntegrationService = projectIntegrationService
	deps.teamRelayMountService = teamRelayMountService
	deps.documentIndexer = documentIndexer
	deps.documentService = documentService
	deps.documentAttachmentService = documentAttachmentService
	deps.documentExportService = documentExportService
	deps.documentCommentService = documentCommentService
}
