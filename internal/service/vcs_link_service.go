package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	gitlabapi "github.com/entire-vc/evc-mesh/internal/integration/gitlab"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/metrics"
)

// vcsLinkService implements VCSLinkService.
type vcsLinkService struct {
	repo        repository.VCSLinkRepository
	taskRepo    repository.TaskRepository
	statusRepo  repository.TaskStatusRepository
	taskSvc     TaskService
	commentSvc  CommentService
	projectRepo repository.ProjectRepository
	// pipelineSource resolves a per-workspace GitLab client to read an MR's
	// head pipeline when the MR merges; nil disables that sync.
	pipelineSource func(ctx context.Context, workspaceID uuid.UUID) (MRHeadPipelineSource, bool)
	// mrStateSource resolves a per-workspace GitLab client to read an MR's live
	// state when a link is added without a status; nil disables that check.
	mrStateSource func(ctx context.Context, workspaceID uuid.UUID) (gitlabapi.MergeRequestChecker, bool)
}

// vcsLinkCreateLiveCheckTimeout bounds the GitLab round trip Create makes to
// learn an MR's real state. Link creation never fails because of it.
const vcsLinkCreateLiveCheckTimeout = 3 * time.Second

// WithVCSMRStateSource lets Create ask GitLab for the state of an MR linked
// without an explicit status, so a link to an MR that merged before any webhook
// could see it is stored as merged/closed instead of open forever. It mirrors
// the checker the done gate resolves (taskService.resolveGitLabChecker).
func WithVCSMRStateSource(f func(ctx context.Context, workspaceID uuid.UUID) (gitlabapi.MergeRequestChecker, bool)) VCSLinkServiceOption {
	return func(s *vcsLinkService) { s.mrStateSource = f }
}

// MRHeadPipelineSource reads the head pipeline of a GitLab merge request.
// ok=false with a nil error means the MR has no pipeline.
type MRHeadPipelineSource interface {
	GetMergeRequestHeadPipeline(ctx context.Context, projectPath string, iid int) (gitlabapi.HeadPipeline, bool, error)
}

// WithVCSMRPipelineSource lets the merge webhook pull the MR's head pipeline,
// so pipeline_status does not depend on a Pipeline Hook that may never have
// reached a link (the link is created by the merge event itself).
func WithVCSMRPipelineSource(f func(ctx context.Context, workspaceID uuid.UUID) (MRHeadPipelineSource, bool)) VCSLinkServiceOption {
	return func(s *vcsLinkService) { s.pipelineSource = f }
}

// VCSLinkServiceOption configures optional dependencies on vcsLinkService.
// They are optional in the sense that the basic CRUD path works without
// them; HandleGitHubPullRequestEvent requires all four to be wired.
type VCSLinkServiceOption func(*vcsLinkService)

// WithVCSTaskRepo injects the task repository used to read project_id and
// the current status of a task when applying transitions.
func WithVCSTaskRepo(r repository.TaskRepository) VCSLinkServiceOption {
	return func(s *vcsLinkService) { s.taskRepo = r }
}

// WithVCSStatusRepo injects the task status repository used to resolve
// status slugs to IDs within a project.
func WithVCSStatusRepo(r repository.TaskStatusRepository) VCSLinkServiceOption {
	return func(s *vcsLinkService) { s.statusRepo = r }
}

// WithVCSTaskService injects the task service used to apply MoveTask.
func WithVCSTaskService(svc TaskService) VCSLinkServiceOption {
	return func(s *vcsLinkService) { s.taskSvc = svc }
}

// WithVCSCommentService injects the comment service used to post system
// auto-transition comments authored as Gary.
func WithVCSCommentService(svc CommentService) VCSLinkServiceOption {
	return func(s *vcsLinkService) { s.commentSvc = svc }
}

// WithVCSProjectRepo injects the project repository used to look up a
// resolved task's workspace, so webhook task-ref resolution can be scoped to
// the workspace whose secret validated the request. Optional in
// the same sense the others are documented to be: without it, workspace
// scoping fails closed (taskInWorkspace refuses rather than guesses) for any
// caller that actually passes a non-nil workspaceID; a caller that always
// passes uuid.Nil (unscoped) never touches this dependency at all.
func WithVCSProjectRepo(r repository.ProjectRepository) VCSLinkServiceOption {
	return func(s *vcsLinkService) { s.projectRepo = r }
}

// NewVCSLinkService creates a new VCSLinkService.
func NewVCSLinkService(repo repository.VCSLinkRepository, opts ...VCSLinkServiceOption) VCSLinkService {
	s := &vcsLinkService{repo: repo}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Create creates a new VCS link, or updates an existing one when the caller
// supplies an explicit status (see the explicitStatus branch below). The
// returned bool is true when a new row was inserted, false when an existing
// one was updated in place — the handler uses it to pick 201 vs 200.
func (s *vcsLinkService) Create(ctx context.Context, input domain.CreateVCSLinkInput) (*domain.VCSLink, bool, error) {
	if input.TaskID == uuid.Nil {
		return nil, false, apierror.BadRequest("task_id is required")
	}
	if input.URL == "" {
		return nil, false, apierror.BadRequest("url is required")
	}
	if input.ExternalID == "" {
		return nil, false, apierror.BadRequest("external_id is required")
	}
	if input.LinkType == "" {
		return nil, false, apierror.BadRequest("link_type is required")
	}

	provider := input.Provider
	if provider == "" {
		provider = domain.VCSProviderGitHub
	}

	// Canonicalise here as well as at the HTTP edge: the uniqueness index and
	// every downstream reader assume one spelling per concept, so an alias
	// must never reach the repository regardless of which caller supplied it.
	linkType := input.LinkType
	if canonical, ok := domain.ParseVCSLinkType(string(linkType)); ok {
		linkType = canonical
	}

	metadata := input.Metadata
	if metadata == nil {
		metadata = []byte("{}")
	}

	// PR links carry a status the done-evidence gate reads (service.MoveTask,
	// an internal ticket). A caller that omits it leaves the column "" — which is not
	// "open", it's "unknown", but the gate's inequality check
	// (Status != merged && Status != closed) treats "" exactly like "open"
	// and blocks anyway. Make that implicit behavior explicit so the stored
	// value never lies about what we actually know: an empty status IS the
	// safe default (fail closed), not a bug to route around with silence.
	//
	// explicitStatus (before defaulting) also decides Create vs Upsert below
	// — track it here, not by re-checking input.Status later.
	explicitStatus := input.Status != ""
	status := input.Status
	liveVerified := false
	if !explicitStatus && linkType == domain.VCSLinkTypePR {
		status = domain.VCSLinkStatusOpen
		// No webhook can report an MR that merged before this link existed, so
		// ask GitLab once instead of storing "open" for ever. Best effort.
		if provider == domain.VCSProviderGitLab {
			if live, ok := s.liveGitLabLinkStatus(ctx, input.TaskID, input.URL); ok {
				status = live
				liveVerified = true
			}
		}
	}

	if explicitStatus {
		// A hand-declared status (add_vcs_link ... status=merged) is the
		// documented way to correct a stale/missing record — but the same
		// call is indistinguishable in shape from declaring something merged
		// that isn't. A gate removable by self-declaration only protects
		// until the first agent it inconveniences, so record WHO
		// declared it and WHEN, distinguishable from a status the done-
		// evidence gate measured itself (webhook delivery or a live GitHub
		// check) — those never go through this branch. Merges into whatever
		// metadata the caller already supplied rather than discarding it.
		actorID, actorType := actorctx.FromContext(ctx)
		metadata = stampManualStatusDeclaration(metadata, status, actorID, actorType, time.Now())
	}

	link := &domain.VCSLink{
		ID:         uuid.New(),
		TaskID:     input.TaskID,
		Provider:   provider,
		LinkType:   linkType,
		ExternalID: input.ExternalID,
		URL:        input.URL,
		Title:      input.Title,
		Status:     status,
		Metadata:   metadata,
		CreatedAt:  time.Now(),
	}

	if liveVerified {
		// GitLab itself reported a terminal state. Upsert (not Create) because
		// only Upsert stamps status_synced_at, the "confirmed at" mark the
		// done gate and staleness checks read; this status is measured, not
		// self-declared, so no status_declared_manually trail is written.
		created, err := s.repo.Upsert(ctx, link)
		if err != nil {
			return nil, false, wrapVCSLinkRepoErr(err)
		}
		return link, created, nil
	}

	if explicitStatus {
		// A caller who explicitly states the status knows something the
		// stored row might not — most commonly, linking a PR that was
		// already merged before the link existed (an internal ticket: no webhook
		// will ever arrive for that after the fact) or correcting a link
		// created before status tracking existed. Upsert so this succeeds
		// whether or not the link already exists, instead of failing on
		// (task_id, provider, link_type, external_id) uniqueness — the
		// exact wall that made an internal ticket's rows permanently uncorrectable.
		// A caller that does NOT pass status keeps the plain-Create path
		// below unchanged: an accidental duplicate add still fails loudly
		// instead of silently resetting an existing 'merged' row to 'open'.
		//
		// Upsert mutates link's ID/CreatedAt in place to the actual persisted
		// row when this call updated an existing one (see its doc) — so link
		// is safe to return as-is either way.
		created, err := s.repo.Upsert(ctx, link)
		if err != nil {
			return nil, false, wrapVCSLinkRepoErr(err)
		}
		return link, created, nil
	}

	if err := s.repo.Create(ctx, link); err != nil {
		return nil, false, wrapVCSLinkRepoErr(err)
	}
	return link, true, nil
}

// wrapVCSLinkRepoErr wraps a repository error for logging context, EXCEPT
// when it is already an *apierror.Error — the repo returns
// apierror.Conflict directly for a caller-actionable identity collision, and
// the HTTP layer's error mapping type-asserts on *apierror.Error rather than
// unwrapping (handleError in task_handler.go), so wrapping it here would
// silently downgrade a clear 409 into a generic 500.
func wrapVCSLinkRepoErr(err error) error {
	var apiErr *apierror.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return fmt.Errorf("create vcs link: %w", err)
}

// liveGitLabLinkStatus asks GitLab for the current state of the MR a new link
// names. ok=true only when GitLab answered merged or closed; every other
// outcome (no source wired, task or workspace unresolvable, URL not an MR URL,
// no token, timeout, API error, MR still open) returns ok=false and the caller
// keeps its "open" default. It never returns an error: a GitLab problem must
// not fail link creation.
func (s *vcsLinkService) liveGitLabLinkStatus(ctx context.Context, taskID uuid.UUID, mrURL string) (domain.VCSLinkStatus, bool) {
	if s.mrStateSource == nil || s.taskRepo == nil || s.projectRepo == nil {
		return "", false
	}
	projectPath, iid, ok := gitlabapi.ParseMergeRequestURL(mrURL)
	if !ok {
		return "", false
	}
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil || task == nil {
		return "", false
	}
	proj, err := s.projectRepo.GetByID(ctx, task.ProjectID)
	if err != nil || proj == nil {
		return "", false
	}
	checker, ok := s.mrStateSource(ctx, proj.WorkspaceID)
	if !ok || checker == nil {
		return "", false
	}
	checkCtx, cancel := context.WithTimeout(ctx, vcsLinkCreateLiveCheckTimeout)
	defer cancel()
	state, err := checker.GetMergeRequestState(checkCtx, projectPath, iid)
	if err != nil {
		log.Printf("[vcs-link] live gitlab state for new link %s!%d (task=%s) unavailable, storing open: %v", projectPath, iid, taskID, err)
		return "", false
	}
	switch {
	case state.Merged || state.State == "merged":
		return domain.VCSLinkStatusMerged, true
	case state.State == "closed":
		return domain.VCSLinkStatusClosed, true
	}
	return "", false
}

// GetByID retrieves a VCS link by ID.
func (s *vcsLinkService) GetByID(ctx context.Context, id uuid.UUID) (*domain.VCSLink, error) {
	link, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get vcs link: %w", err)
	}
	if link == nil {
		return nil, apierror.NotFound("VCSLink")
	}
	return link, nil
}

// Delete removes a VCS link by ID.
func (s *vcsLinkService) Delete(ctx context.Context, id uuid.UUID) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete vcs link: %w", err)
	}
	return nil
}

// PatchMetadata validates the agent patch against the whitelist and merges it.
func (s *vcsLinkService) PatchMetadata(ctx context.Context, taskID, linkID uuid.UUID, patch map[string]any) (*domain.VCSLink, error) {
	if len(patch) == 0 {
		return nil, apierror.BadRequest("metadata patch is empty")
	}
	// Keys come from the whitelist and values are validated lowercase hex, so
	// concatenating them cannot produce invalid JSON.
	var raw strings.Builder
	raw.WriteByte('{')
	first := true
	for k, v := range patch {
		if !domain.AgentWritableVCSMetadataKeys[k] {
			return nil, apierror.BadRequest("metadata key " + strconv.Quote(k) + " is not writable; allowed: ship_sha")
		}
		str, ok := v.(string)
		if !ok || !domain.IsFullCommitSHA(str) {
			return nil, apierror.BadRequest(k + " must be a 40-character lowercase hex string")
		}
		if !first {
			raw.WriteByte(',')
		}
		first = false
		raw.WriteString(strconv.Quote(k) + ":" + strconv.Quote(str))
	}
	raw.WriteByte('}')
	link, err := s.repo.MergeMetadata(ctx, taskID, linkID, json.RawMessage(raw.String()))
	if err != nil {
		return nil, fmt.Errorf("patch vcs link metadata: %w", err)
	}
	if link == nil {
		return nil, apierror.NotFound("VCSLink")
	}
	return link, nil
}

// PatchShipSHAByURL resolves the pr links naming mrURL (number from the last
// path segment, url compared after normalisation, so another project's same
// number never matches) inside workspaceID and merges ship_sha into each.
func (s *vcsLinkService) PatchShipSHAByURL(ctx context.Context, workspaceID uuid.UUID, mrURL, shipSHA string) ([]domain.VCSLink, error) {
	if !domain.IsFullCommitSHA(shipSHA) {
		return nil, apierror.BadRequest("ship_sha must be a 40-character lowercase hex string")
	}
	u, err := url.Parse(strings.TrimSpace(mrURL))
	if err != nil || u.Host == "" {
		return nil, apierror.BadRequest("url must be an absolute MR/PR url")
	}
	iid := path.Base(strings.TrimRight(u.Path, "/"))
	if n, convErr := strconv.Atoi(iid); convErr != nil || n <= 0 {
		return nil, apierror.BadRequest("url must end with the MR/PR number")
	}
	var links []domain.VCSLink
	for _, provider := range []domain.VCSProvider{domain.VCSProviderGitLab, domain.VCSProviderGitHub} {
		found, listErr := s.repo.ListByExternalID(ctx, provider, domain.VCSLinkTypePR, iid)
		if listErr != nil {
			return nil, fmt.Errorf("list vcs links: %w", listErr)
		}
		links = append(links, filterLinksByURL(found, mrURL)...)
	}
	links = s.filterLinksInWorkspace(ctx, workspaceID, links)
	if len(links) == 0 {
		return nil, apierror.NotFound("VCSLink")
	}
	out := make([]domain.VCSLink, 0, len(links))
	for _, l := range links {
		if l.ShipSHA() == shipSHA {
			// Retried publish (glab-merge after code review): nothing to write.
			out = append(out, l)
			continue
		}
		patched, patchErr := s.PatchMetadata(ctx, l.TaskID, l.ID, map[string]any{domain.VCSLinkMetaShipSHA: shipSHA})
		if patchErr != nil {
			return nil, patchErr
		}
		out = append(out, *patched)
	}
	return out, nil
}

// ListByTask returns all VCS links for a given task.
func (s *vcsLinkService) ListByTask(ctx context.Context, taskID uuid.UUID) ([]domain.VCSLink, error) {
	links, err := s.repo.ListByTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("list vcs links: %w", err)
	}
	return links, nil
}

// HandleGitHubPullRequestEvent applies the webhook → task transition policy.
func (s *vcsLinkService) HandleGitHubPullRequestEvent(ctx context.Context, ev GitHubWebhookEvent) (PRHandleResult, error) {
	if s.taskRepo == nil || s.statusRepo == nil || s.taskSvc == nil || s.commentSvc == nil {
		return PRHandleResult{}, errors.New("vcsLinkService: missing dependency for HandleGitHubPullRequestEvent (taskRepo/statusRepo/taskSvc/commentSvc must be wired)")
	}

	// 1. Resolve task_id from any recognised reference spelling in the title,
	//    body or head branch, then fall back to a previously-linked
	//    (provider, link_type, external_id).
	sources := []TaskRefSource{
		{Name: "title", Text: ev.PRTitle},
		{Name: "body", Text: ev.PRBody},
		{Name: "branch", Text: ev.PRBranch},
	}
	taskID, matched, outcome, candidates := s.resolveTaskRef(ctx, ev.WorkspaceID, sources...)
	if taskID != uuid.Nil {
		log.Printf("[vcs-webhook] pr=%s#%d resolved task=%s via=%s src=%s raw=%q",
			ev.Repository, ev.PRNumber, taskID, matched.Kind, matched.Source, truncate(matched.Raw, 60))
		// A stored link naming a DIFFERENT task is the fingerprint of an
		// earlier mislink: the payload is legible now, so whatever is on file
		// was written when it was not. Upsert keys on task_id, so the old row
		// survives untouched beside the new one and nothing else would ever
		// mention it. Say so — the previous version of this bug was invisible
		// precisely because the wrong row was silent.
		if stale, err := s.repo.ListByExternalID(ctx, domain.VCSProviderGitHub, domain.VCSLinkTypePR, strconv.Itoa(ev.PRNumber)); err == nil {
			for _, l := range filterLinksByURL(stale, ev.PRHTMLURL) {
				if l.TaskID != taskID {
					log.Printf("[vcs-webhook] pr=%s#%d stored link points at task=%s but payload names task=%s; leaving the stale row for review (link_id=%s)",
						ev.Repository, ev.PRNumber, l.TaskID, taskID, l.ID)
				}
			}
		}
	}
	if taskID == uuid.Nil && outcome == refAmbiguous {
		// Do NOT fall back to the stored link here. The fallback below exists
		// for payloads that name nothing — a PR linked by hand once, whose
		// later deliveries should keep updating that link. An AMBIGUOUS payload
		// is the opposite situation: the fresh read disagreed with itself, and
		// reusing history would make every redelivery re-confirm whatever was
		// stored first. That is not hypothetical — a PR attached itself to
		// the task its body quoted as an example, and because each subsequent
		// delivery took this fallback, the refusal added one commit earlier
		// could never undo it. A link that can only be corrected by hand is a
		// link that stays wrong.
		//
		// What an ambiguous delivery MAY do is refresh the status of a link that
		// already names exactly this PR on one of the tasks the payload mentioned:
		// the PR's own state is not in doubt, only which task it belongs to. It
		// never picks a task, creates a link, or transitions a task, so the #433
		// protection is untouched; without this a merged PR left its link open
		// for ever whenever the body quoted a second task id.
		synced, err := s.syncExistingLinksOnAmbiguousRef(ctx, ev.WorkspaceID, domain.VCSProviderGitHub,
			strconv.Itoa(ev.PRNumber), ev.PRHTMLURL, candidates, githubLinkStatus(ev), []byte("{}"))
		recordAmbiguousTaskRef(domain.VCSProviderGitHub, fmt.Sprintf("%s#%d", ev.Repository, ev.PRNumber), ev.PRHTMLURL, len(candidates), synced)
		if err != nil {
			return PRHandleResult{Reason: "ambiguous_task_ref"}, err
		}
		return PRHandleResult{Reason: "ambiguous_task_ref", LinksSynced: synced}, nil
	}
	if taskID == uuid.Nil {
		links, err := s.repo.ListByExternalID(ctx, domain.VCSProviderGitHub, domain.VCSLinkTypePR, strconv.Itoa(ev.PRNumber))
		if err != nil {
			return PRHandleResult{}, fmt.Errorf("list vcs links by external_id: %w", err)
		}
		// A PR number is a small, guessable integer with no workspace
		// component — without this filter, a workspace B secret could
		// silently reuse a stored link belonging to a task in workspace A
		// just by naming the right number (an internal ticket, same class the
		// title/body/branch path is scoped against in lookupRef).
		links = s.filterLinksInWorkspace(ctx, ev.WorkspaceID, filterLinksByURL(links, ev.PRHTMLURL))
		switch {
		case len(links) == 0:
			// Say what was looked at, not just that nothing was found: for six
			// weeks this branch was taken 444 times and left no trace at all,
			// so "200 and silence" was indistinguishable from a healthy webhook.
			log.Printf("[vcs-webhook] pr=%s#%d no_task_ref: candidates=%d title=%q body=%q branch=%q",
				ev.Repository, ev.PRNumber, len(ExtractTaskRefs(sources...)),
				truncate(ev.PRTitle, 80), truncate(ev.PRBody, 160), truncate(ev.PRBranch, 60))
			return PRHandleResult{Reason: "no_task_ref"}, nil
		case len(links) == 1:
			taskID = links[0].TaskID
		default:
			// Multiple historical links; ListByExternalID orders by created_at
			// DESC so the first row is the newest association.
			log.Printf("[vcs-webhook] PR #%d has %d historical task links; using newest task_id=%s",
				ev.PRNumber, len(links), links[0].TaskID)
			taskID = links[0].TaskID
		}
	}

	// 2. Compute target link status.
	linkStatus := githubLinkStatus(ev)

	// 3. Upsert the link so subsequent webhooks update the same row.
	link := &domain.VCSLink{
		ID:         uuid.New(),
		TaskID:     taskID,
		Provider:   domain.VCSProviderGitHub,
		LinkType:   domain.VCSLinkTypePR,
		ExternalID: strconv.Itoa(ev.PRNumber),
		URL:        ev.PRHTMLURL,
		Title:      ev.PRTitle,
		Status:     linkStatus,
		Metadata:   []byte("{}"),
		CreatedAt:  time.Now(),
	}
	if _, err := s.repo.Upsert(ctx, link); err != nil {
		return PRHandleResult{TaskID: taskID}, fmt.Errorf("upsert vcs link: %w", err)
	}

	// 4. Transitions only happen on action=closed. Open / synchronize /
	//    reopened just sync the link row (already done above) and exit.
	if ev.Action != "closed" {
		return PRHandleResult{TaskID: taskID, Reason: "not_closed"}, nil
	}

	res, err := s.applyPRTransitionPolicy(ctx, taskID, ev.PRMerged,
		fmt.Sprintf("PR #%d", ev.PRNumber), shortSHA(ev.MergeSHA), "github-webhook")
	s.closeReviewSubjects(ctx, ev.WorkspaceID, domain.VCSProviderGitHub, strconv.Itoa(ev.PRNumber), ev.PRHTMLURL, taskID,
		ev.PRMerged, fmt.Sprintf("PR #%d", ev.PRNumber), shortSHA(ev.MergeSHA), "github-webhook")
	return res, err
}

// HandleGitLabMergeRequestEvent applies the same webhook → task transition
// policy as HandleGitHubPullRequestEvent — task-ref resolution,
// link upsert, and the in_progress→review→done transition are identical
// once GitHub's "PR" and GitLab's "MR" are reduced to (task ref, terminal
// state); only the wire-level field names and vocabulary differ.
func (s *vcsLinkService) HandleGitLabMergeRequestEvent(ctx context.Context, ev GitLabWebhookEvent) (PRHandleResult, error) {
	if s.taskRepo == nil || s.statusRepo == nil || s.taskSvc == nil || s.commentSvc == nil {
		return PRHandleResult{}, errors.New("vcsLinkService: missing dependency for HandleGitLabMergeRequestEvent (taskRepo/statusRepo/taskSvc/commentSvc must be wired)")
	}

	// 1. Resolve task_id from any recognised reference spelling in the title,
	//    body or source branch, then fall back to a previously-linked
	//    (provider, link_type, external_id).
	sources := []TaskRefSource{
		{Name: "title", Text: ev.MRTitle},
		{Name: "body", Text: ev.MRBody},
		{Name: "branch", Text: ev.MRBranch},
	}
	taskID, matched, outcome, candidates := s.resolveTaskRef(ctx, ev.WorkspaceID, sources...)
	if taskID != uuid.Nil {
		log.Printf("[vcs-webhook] mr=%s!%d resolved task=%s via=%s src=%s raw=%q",
			ev.ProjectPath, ev.MRIID, taskID, matched.Kind, matched.Source, truncate(matched.Raw, 60))
		if stale, err := s.repo.ListByExternalID(ctx, domain.VCSProviderGitLab, domain.VCSLinkTypePR, strconv.Itoa(ev.MRIID)); err == nil {
			for _, l := range filterLinksByURL(stale, ev.MRURL) {
				if l.TaskID != taskID {
					log.Printf("[vcs-webhook] mr=%s!%d stored link points at task=%s but payload names task=%s; leaving the stale row for review (link_id=%s)",
						ev.ProjectPath, ev.MRIID, l.TaskID, taskID, l.ID)
				}
			}
		}
	}
	if taskID == uuid.Nil && outcome == refAmbiguous {
		// See the identical guard in HandleGitHubPullRequestEvent: an
		// ambiguous payload must not fall back to whatever was stored for a
		// prior delivery of the same MR — that reuse is what let a PR
		// stick to the wrong task irreversibly. It may only refresh the status
		// of links that already name this exact MR on a mentioned task.
		synced, err := s.syncExistingLinksOnAmbiguousRef(ctx, ev.WorkspaceID, domain.VCSProviderGitLab,
			strconv.Itoa(ev.MRIID), ev.MRURL, candidates, gitlabLinkStatus(ev.MRState), gitLabLinkMetadata(ev.HeadSHA))
		recordAmbiguousTaskRef(domain.VCSProviderGitLab, fmt.Sprintf("%s!%d", ev.ProjectPath, ev.MRIID), ev.MRURL, len(candidates), synced)
		if err != nil {
			return PRHandleResult{Reason: "ambiguous_task_ref"}, err
		}
		if synced > 0 && ev.MRState == "merged" {
			s.syncMergedPipeline(ctx, ev)
		}
		return PRHandleResult{Reason: "ambiguous_task_ref", LinksSynced: synced}, nil
	}
	if taskID == uuid.Nil {
		links, err := s.repo.ListByExternalID(ctx, domain.VCSProviderGitLab, domain.VCSLinkTypePR, strconv.Itoa(ev.MRIID))
		if err != nil {
			return PRHandleResult{}, fmt.Errorf("list vcs links by external_id: %w", err)
		}
		// See the identical comment in HandleGitHubPullRequestEvent: an MR
		// iid is just as guessable as a PR number, so the same workspace
		// filter applies here.
		links = s.filterLinksInWorkspace(ctx, ev.WorkspaceID, filterLinksByURL(links, ev.MRURL))
		switch {
		case len(links) == 0:
			log.Printf("[vcs-webhook] mr=%s!%d no_task_ref: candidates=%d title=%q body=%q branch=%q",
				ev.ProjectPath, ev.MRIID, len(ExtractTaskRefs(sources...)),
				truncate(ev.MRTitle, 80), truncate(ev.MRBody, 160), truncate(ev.MRBranch, 60))
			return PRHandleResult{Reason: "no_task_ref"}, nil
		case len(links) == 1:
			taskID = links[0].TaskID
		default:
			// Multiple historical links; ListByExternalID orders by created_at
			// DESC so the first row is the newest association.
			log.Printf("[vcs-webhook] MR !%d has %d historical task links; using newest task_id=%s",
				ev.MRIID, len(links), links[0].TaskID)
			taskID = links[0].TaskID
		}
	}

	// 2. Compute target link status. GitLab's `state` already distinguishes
	//    merged from closed (unlike GitHub's open/closed-only `state`, which
	//    needs the separate `merged` boolean), so this is a direct mapping.
	linkStatus := gitlabLinkStatus(ev.MRState)

	// 3. Upsert the link so subsequent webhooks update the same row.
	link := &domain.VCSLink{
		ID:         uuid.New(),
		TaskID:     taskID,
		Provider:   domain.VCSProviderGitLab,
		LinkType:   domain.VCSLinkTypePR,
		ExternalID: strconv.Itoa(ev.MRIID),
		URL:        ev.MRURL,
		Title:      ev.MRTitle,
		Status:     linkStatus,
		Metadata:   gitLabLinkMetadata(ev.HeadSHA),
		CreatedAt:  time.Now(),
	}
	if _, err := s.repo.Upsert(ctx, link); err != nil {
		return PRHandleResult{TaskID: taskID}, fmt.Errorf("upsert vcs link: %w", err)
	}

	// 4. Transitions only happen once the MR reaches a terminal state.
	//    GitLab has no single boolean equivalent to GitHub's
	//    action=="closed" gate — `state` already carries this distinction.
	if linkStatus != domain.VCSLinkStatusMerged && linkStatus != domain.VCSLinkStatusClosed {
		return PRHandleResult{TaskID: taskID, Reason: "not_closed"}, nil
	}

	merged := linkStatus == domain.VCSLinkStatusMerged
	if merged {
		s.syncMergedPipeline(ctx, ev)
	}
	res, err := s.applyPRTransitionPolicy(ctx, taskID, merged,
		fmt.Sprintf("MR !%d", ev.MRIID), shortSHA(ev.MergeSHA), "gitlab-webhook")
	s.closeReviewSubjects(ctx, ev.WorkspaceID, domain.VCSProviderGitLab, strconv.Itoa(ev.MRIID), ev.MRURL, taskID,
		merged, fmt.Sprintf("MR !%d", ev.MRIID), shortSHA(ev.MergeSHA), "gitlab-webhook")
	return res, err
}

// githubLinkStatus maps a pull_request delivery to the status its link stores.
func githubLinkStatus(ev GitHubWebhookEvent) domain.VCSLinkStatus {
	linkStatus := domain.VCSLinkStatus(strings.ToLower(ev.PRState))
	if ev.PRMerged {
		linkStatus = domain.VCSLinkStatusMerged
	}
	if ev.Action == "closed" && !ev.PRMerged {
		linkStatus = domain.VCSLinkStatusClosed
	}
	if linkStatus == "" {
		linkStatus = domain.VCSLinkStatusOpen
	}
	return linkStatus
}

// gitlabLinkStatus maps a GitLab MR `state` to the status its link stores.
func gitlabLinkStatus(mrState string) domain.VCSLinkStatus {
	switch mrState {
	case "merged":
		return domain.VCSLinkStatusMerged
	case "closed":
		return domain.VCSLinkStatusClosed
	default: // "opened", "locked" (locked is transient while GitLab processes a merge)
		return domain.VCSLinkStatusOpen
	}
}

// recordAmbiguousTaskRef makes a refused delivery visible: a warn-level
// structured line and a counter. Before this, an ambiguous merge event left one
// log line without the MR url and no metric, so ten links went stale unseen.
func recordAmbiguousTaskRef(provider domain.VCSProvider, ref, prURL string, candidates, synced int) {
	outcome := "none"
	if synced > 0 {
		outcome = "status_synced"
	}
	slog.Warn("vcs webhook ambiguous_task_ref",
		"provider", string(provider),
		"ref", ref,
		"url", prURL,
		"candidates", candidates,
		"links_synced", synced,
	)
	metrics.RecordVCSWebhookAmbiguousTaskRef(string(provider), outcome)
}

// syncExistingLinksOnAmbiguousRef refreshes the status (and head metadata) of
// links that already name exactly this PR/MR on one of candidates — the tasks
// an ambiguous payload mentioned — and returns how many it wrote.
//
// It is deliberately narrower than the "payload names nothing" fallback: the
// link must match by external id AND url (an iid alone is reused across
// projects), live in the delivering workspace, and belong to a mentioned task.
// An empty url matches nothing here, because without it a same-numbered MR of
// another project could not be told apart. It writes through Upsert on the
// stored row itself, so it can neither create a link nor choose a task, and it
// never reaches applyPRTransitionPolicy or closeReviewSubjects: no task moves.
func (s *vcsLinkService) syncExistingLinksOnAmbiguousRef(ctx context.Context, workspaceID uuid.UUID, provider domain.VCSProvider, externalID, rawURL string, candidates []uuid.UUID, status domain.VCSLinkStatus, metadata []byte) (int, error) {
	if rawURL == "" || len(candidates) == 0 {
		return 0, nil
	}
	links, err := s.repo.ListByExternalID(ctx, provider, domain.VCSLinkTypePR, externalID)
	if err != nil {
		return 0, fmt.Errorf("list vcs links by external_id: %w", err)
	}
	links = s.filterLinksInWorkspace(ctx, workspaceID, filterLinksByURL(links, rawURL))
	mentioned := make(map[uuid.UUID]bool, len(candidates))
	for _, id := range candidates {
		mentioned[id] = true
	}
	synced := 0
	for _, l := range links {
		if !mentioned[l.TaskID] {
			continue
		}
		l := l
		l.Status = status
		l.Metadata = metadata
		if _, err := s.repo.Upsert(ctx, &l); err != nil {
			return synced, fmt.Errorf("upsert vcs link: %w", err)
		}
		synced++
	}
	return synced, nil
}

// syncMergedPipeline stores the MR's head pipeline on its links at merge time.
// Best effort: a GitLab failure is logged and leaves pipeline_status as it was,
// so the delivery-proof gate stays closed rather than guessing.
func (s *vcsLinkService) syncMergedPipeline(ctx context.Context, ev GitLabWebhookEvent) {
	if s.pipelineSource == nil || ev.ProjectPath == "" {
		return
	}
	src, ok := s.pipelineSource(ctx, ev.WorkspaceID)
	if !ok {
		return
	}
	hp, found, err := src.GetMergeRequestHeadPipeline(ctx, ev.ProjectPath, ev.MRIID)
	if err != nil {
		log.Printf("[vcs-webhook] mr=%s!%d head pipeline sync failed: %v", ev.ProjectPath, ev.MRIID, err)
		return
	}
	if !found {
		return
	}
	if _, err := s.HandleGitLabPipelineEvent(ctx, GitLabPipelineEvent{
		PipelineID: hp.ID, Status: hp.Status, SHA: hp.SHA, MRIID: ev.MRIID,
		MRURL: ev.MRURL, ProjectPath: ev.ProjectPath, WorkspaceID: ev.WorkspaceID,
	}); err != nil {
		log.Printf("[vcs-webhook] mr=%s!%d store head pipeline: %v", ev.ProjectPath, ev.MRIID, err)
	}
}

// HandleGitLabPipelineEvent records the latest pipeline state on every link
// that names this MR (same url, same workspace). It only updates existing
// links: a pipeline for an MR no task refers to is not a reason to create one.
func (s *vcsLinkService) HandleGitLabPipelineEvent(ctx context.Context, ev GitLabPipelineEvent) (PipelineHandleResult, error) {
	if ev.MRIID == 0 || ev.MRURL == "" {
		return PipelineHandleResult{Reason: "no_merge_request"}, nil
	}
	status, ok := domain.NormalizePipelineStatus(ev.Status)
	if !ok {
		log.Printf("[vcs-webhook] mr=%s!%d pipeline=%d unknown pipeline status %q", ev.ProjectPath, ev.MRIID, ev.PipelineID, ev.Status)
		return PipelineHandleResult{Reason: "unknown_status"}, nil
	}
	links, err := s.repo.ListByExternalID(ctx, domain.VCSProviderGitLab, domain.VCSLinkTypePR, strconv.Itoa(ev.MRIID))
	if err != nil {
		return PipelineHandleResult{}, fmt.Errorf("list vcs links by external_id: %w", err)
	}
	// The iid is project-scoped, so the url is what tells two projects' !12 apart.
	want := domain.NormalizeVCSURL(ev.MRURL)
	updated := 0
	for _, l := range s.filterLinksInWorkspace(ctx, ev.WorkspaceID, links) {
		if domain.NormalizeVCSURL(l.URL) != want {
			continue
		}
		written, werr := s.repo.SetPipelineStatus(ctx, l.ID, ev.PipelineID, status, ev.SHA)
		if werr != nil {
			return PipelineHandleResult{Updated: updated}, fmt.Errorf("set pipeline status: %w", werr)
		}
		if written {
			updated++
		}
	}
	if updated == 0 {
		return PipelineHandleResult{Reason: "no_link"}, nil
	}
	return PipelineHandleResult{Updated: updated}, nil
}

// closeReviewSubjects settles the review cards bound to this PR/MR (links
// carrying role=review, see taskService.bindReviewSubject) once it reaches a
// terminal state. The task the payload names is the work; a review of it is a
// different card that no title, body or branch mentions, so without this the
// webhook never reaches it (an internal ticket).
//
// Matching is by the normalised URL, not by external_id: an MR iid is only
// unique within its project, so !19 of one repository must not settle a
// review of !19 in another. Merged → the review card goes to done from any
// open status (its subject no longer exists to be reviewed); closed without
// merge → comment only. Failures are logged and never fail the delivery: the
// primary transition has already been applied and a redelivery would redo it.
func (s *vcsLinkService) closeReviewSubjects(ctx context.Context, workspaceID uuid.UUID, provider domain.VCSProvider, externalID, rawURL string, primary uuid.UUID, merged bool, refLabel, mergeRefLabel, source string) {
	if s.taskRepo == nil || s.statusRepo == nil || s.taskSvc == nil || s.commentSvc == nil || rawURL == "" {
		return
	}
	links, err := s.repo.ListByExternalID(ctx, provider, domain.VCSLinkTypePR, externalID)
	if err != nil {
		log.Printf("[vcs-webhook] review-subjects list %s: %v", refLabel, err)
		return
	}
	want := domain.NormalizeVCSURL(rawURL)
	for _, l := range s.filterLinksInWorkspace(ctx, workspaceID, links) {
		if l.TaskID == primary || !l.IsReviewSubject() || domain.NormalizeVCSURL(l.URL) != want {
			continue
		}
		l := l
		if merged {
			l.Status = domain.VCSLinkStatusMerged
		} else {
			l.Status = domain.VCSLinkStatusClosed
		}
		if _, err := s.repo.Upsert(ctx, &l); err != nil {
			log.Printf("[vcs-webhook] review-subject upsert task=%s: %v", l.TaskID, err)
			continue
		}
		if !merged {
			body := fmt.Sprintf("🤖 Auto: %s closed without merge — the review card stays open.", refLabel)
			if cerr := s.postSystemComment(ctx, l.TaskID, body, source); cerr != nil {
				log.Printf("[vcs-webhook] post comment failed task=%s: %v", l.TaskID, cerr)
			}
			continue
		}
		task, err := s.taskRepo.GetByID(ctx, l.TaskID)
		if err != nil || task == nil {
			log.Printf("[vcs-webhook] review-subject get task=%s: %v", l.TaskID, err)
			continue
		}
		cur, err := s.statusRepo.GetByID(ctx, task.StatusID)
		if err != nil || cur == nil || cur.Category == domain.StatusCategoryDone || cur.Category == domain.StatusCategoryCancelled {
			continue
		}
		done, err := s.resolveStatusBySlug(ctx, task.ProjectID, "done")
		if err != nil || done == nil {
			log.Printf("[vcs-webhook] review-subject no done status task=%s: %v", l.TaskID, err)
			continue
		}
		if err := s.taskSvc.MoveTask(ctx, l.TaskID, MoveTaskInput{StatusID: &done.ID}); err != nil {
			log.Printf("[vcs-webhook] review-subject move task=%s: %v", l.TaskID, err)
			continue
		}
		body := fmt.Sprintf("🤖 Auto: %s merged (commit %#q) — the reviewed change is in, review card closed.", refLabel, mergeRefLabel)
		if cerr := s.postSystemComment(ctx, l.TaskID, body, source); cerr != nil {
			log.Printf("[vcs-webhook] post comment failed task=%s: %v", l.TaskID, cerr)
		}
	}
}

// applyPRTransitionPolicy runs the merge/close transition policy shared by
// HandleGitHubPullRequestEvent and HandleGitLabMergeRequestEvent, once each
// has resolved a task and upserted its link: post a comment and stop for a
// close-without-merge; wait for sibling PR/MR links to also go terminal;
// otherwise move the task in_progress→review or review→done. refLabel
// (e.g. "a PR" / "a review") and mergeRefLabel (a short commit label) are
// already formatted for the auto-comment text — this function does not
// know or care which provider it's being called for. source tags the
// posted comment's metadata ("github-webhook" / "gitlab-webhook") so a
// reader can tell which webhook actually triggered a given transition.
func (s *vcsLinkService) applyPRTransitionPolicy(ctx context.Context, taskID uuid.UUID, merged bool, refLabel, mergeRefLabel, source string) (PRHandleResult, error) {
	// 5a. closed without merge — post comment, no status change.
	if !merged {
		body := fmt.Sprintf("🤖 Auto: %s closed without merge — no status change.", refLabel)
		if cerr := s.postSystemComment(ctx, taskID, body, source); cerr != nil {
			log.Printf("[vcs-webhook] post comment failed task=%s: %v", taskID, cerr)
		}
		return PRHandleResult{TaskID: taskID, Reason: "closed_without_merge"}, nil
	}

	// 5b. closed + merged. Check that all PR/MR links tied to this task are
	//     terminal (merged or closed). If not, this is a partial merge in a
	//     multi-PR task — comment and wait.
	taskLinks, err := s.repo.ListByTask(ctx, taskID)
	if err != nil {
		return PRHandleResult{TaskID: taskID}, fmt.Errorf("list vcs links by task: %w", err)
	}
	var pendingCount int
	for _, l := range taskLinks {
		if l.LinkType != domain.VCSLinkTypePR {
			continue
		}
		if l.Status != domain.VCSLinkStatusMerged && l.Status != domain.VCSLinkStatusClosed {
			pendingCount++
		}
	}
	if pendingCount > 0 {
		body := fmt.Sprintf("🤖 Auto: %s merged. Awaiting %d more PR(s) before status transition.", refLabel, pendingCount)
		if cerr := s.postSystemComment(ctx, taskID, body, source); cerr != nil {
			log.Printf("[vcs-webhook] post comment failed task=%s: %v", taskID, cerr)
		}
		return PRHandleResult{TaskID: taskID, Reason: "awaiting_other_prs"}, nil
	}

	// 5c. All PRs/MRs terminal — apply the transition policy.
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return PRHandleResult{TaskID: taskID}, fmt.Errorf("get task %s: %w", taskID, err)
	}
	if task == nil {
		return PRHandleResult{TaskID: taskID, Reason: "task_not_found"}, nil
	}

	currentStatus, err := s.statusRepo.GetByID(ctx, task.StatusID)
	if err != nil {
		return PRHandleResult{TaskID: taskID}, fmt.Errorf("get status %s: %w", task.StatusID, err)
	}
	if currentStatus == nil {
		return PRHandleResult{TaskID: taskID, Reason: "current_status_missing"}, nil
	}

	var targetSlug string
	switch currentStatus.Category {
	case domain.StatusCategoryInProgress:
		targetSlug = "review"
	case domain.StatusCategoryReview:
		targetSlug = "done"
	default:
		body := fmt.Sprintf("🤖 Auto: %s merged. Task in %#q; no auto-transition.", refLabel, currentStatus.Slug)
		if cerr := s.postSystemComment(ctx, taskID, body, source); cerr != nil {
			log.Printf("[vcs-webhook] post comment failed task=%s: %v", taskID, cerr)
		}
		return PRHandleResult{TaskID: taskID, OldStatus: currentStatus.Slug, Reason: "source_status_not_eligible"}, nil
	}

	targetStatus, err := s.resolveStatusBySlug(ctx, task.ProjectID, targetSlug)
	if err != nil {
		return PRHandleResult{TaskID: taskID, OldStatus: currentStatus.Slug}, fmt.Errorf("resolve target status slug=%s project=%s: %w", targetSlug, task.ProjectID, err)
	}
	if targetStatus == nil {
		return PRHandleResult{TaskID: taskID, OldStatus: currentStatus.Slug, Reason: "target_status_missing"}, nil
	}

	if err := s.taskSvc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &targetStatus.ID}); err != nil {
		return PRHandleResult{TaskID: taskID, OldStatus: currentStatus.Slug}, fmt.Errorf("move task %s to %s: %w", taskID, targetSlug, err)
	}

	body := fmt.Sprintf("🤖 Auto: %s merged (commit %#q) → moved to %s.", refLabel, mergeRefLabel, targetSlug)
	if cerr := s.postSystemComment(ctx, taskID, body, source); cerr != nil {
		log.Printf("[vcs-webhook] post comment failed task=%s: %v", taskID, cerr)
	}

	return PRHandleResult{
		TaskID:       taskID,
		OldStatus:    currentStatus.Slug,
		NewStatus:    targetStatus.Slug,
		Transitioned: true,
		Reason:       "transitioned",
	}, nil
}

// postSystemComment writes a non-internal comment authored as Gary.
// source tags which webhook triggered it ("github-webhook" /
// "gitlab-webhook") in the comment's metadata for provenance.
func (s *vcsLinkService) postSystemComment(ctx context.Context, taskID uuid.UUID, body, source string) error {
	metadata, err := json.Marshal(map[string]string{"source": source})
	if err != nil {
		// Only possible if source somehow isn't valid UTF-8; fall back to a
		// literal rather than fail the whole webhook over a metadata detail.
		metadata = []byte(`{"source":"webhook"}`)
	}
	c := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     taskID,
		AuthorID:   uuid.Nil,
		AuthorType: domain.ActorTypeSystem,
		SystemCode: SysCodePtr(SysCodeVCSWebhook),
		Body:       body,
		Metadata:   metadata,
		IsInternal: false,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	return s.commentSvc.Create(ctx, c)
}

// resolveStatusBySlug looks up a task status by slug within a project.
func (s *vcsLinkService) resolveStatusBySlug(ctx context.Context, projectID uuid.UUID, slug string) (*domain.TaskStatus, error) {
	statuses, err := s.statusRepo.ListByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for i := range statuses {
		if statuses[i].Slug == slug {
			return &statuses[i], nil
		}
	}
	return nil, nil
}

// maxRefCandidates bounds how many candidate references one payload may cost
// in database lookups. A PR body naming more than a dozen ids is prose about
// tasks, not a reference to one.
const maxRefCandidates = 12

// ResolveTaskRef scans the sources for every recognised task-reference spelling
// and returns the task they name — but only when they agree on ONE task.
//
// Existence is checked, not assumed: vcs_links.task_id carries an FK to
// tasks(id), so an unverified id turns into a constraint violation at insert
// time rather than a clean "no reference here". A candidate that resolves to
// nothing is dropped quietly; a PR body may quote a deleted task or one from
// another workspace beside the real reference.
//
// When candidates resolve to SEVERAL different tasks the payload is ambiguous
// and nothing is returned, unless exactly one of them was found in the title.
// This is the same refusal as an ambiguous short prefix, one level up: picking
// the first by position is a guess, and a wrong link silently misattributes a
// pull request. Verified the hard way — the first PR this code saw in
// production was one whose body documented the reference syntax, and it
// attached itself to the unrelated task used as the example.
//
// Returns uuid.Nil when nothing resolves or the payload is ambiguous.
func (s *vcsLinkService) ResolveTaskRef(ctx context.Context, workspaceID uuid.UUID, sources ...TaskRefSource) (uuid.UUID, TaskRef) {
	id, ref, _, _ := s.resolveTaskRef(ctx, workspaceID, sources...)
	return id, ref
}

// refOutcome says WHY ResolveTaskRef came back empty. Callers that fall back to
// stored state need the distinction: "nothing here named a task" invites a
// fallback, whereas "something did and we refused to choose between them" is an
// active disagreement, and treating the two alike lets a wrong link outlive the
// refusal that was supposed to prevent it.
type refOutcome int

const (
	// refNone — no candidate in the payload named an existing task.
	refNone refOutcome = iota
	// refResolved — exactly one task was named (or the title broke the tie).
	refResolved
	// refAmbiguous — several distinct tasks were named and none was decisive.
	refAmbiguous
)

// The fourth result lists the distinct existing tasks the payload named; it is
// only consulted for refAmbiguous, where it bounds which tasks' links an
// ambiguous delivery may refresh (see syncExistingLinksOnAmbiguousRef).
func (s *vcsLinkService) resolveTaskRef(ctx context.Context, workspaceID uuid.UUID, sources ...TaskRefSource) (uuid.UUID, TaskRef, refOutcome, []uuid.UUID) { //nolint:gocritic // unnamedResult: the 4 results are documented above; naming them would shadow the locals below
	type hit struct {
		id  uuid.UUID
		ref TaskRef
	}
	var (
		hits    []hit
		seen    = map[uuid.UUID]bool{}
		checked int
	)

	for _, ref := range ExtractTaskRefs(sources...) {
		if checked >= maxRefCandidates {
			log.Printf("[vcs-webhook] stopped after %d candidate refs; payload names too many ids", maxRefCandidates)
			break
		}
		checked++

		id, ok := s.lookupRef(ctx, workspaceID, ref)
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		hits = append(hits, hit{id: id, ref: ref})
	}

	switch len(hits) {
	case 0:
		return uuid.Nil, TaskRef{}, refNone, nil
	case 1:
		return hits[0].id, hits[0].ref, refResolved, []uuid.UUID{hits[0].id}
	}

	candidates := make([]uuid.UUID, 0, len(hits))
	for _, h := range hits {
		candidates = append(candidates, h.id)
	}

	// Ambiguous. A title is short and deliberate, so a single reference there
	// outranks a crowded body; anything else is a guess we decline to make.
	var fromTitle []hit
	for _, h := range hits {
		if h.ref.Source == "title" {
			fromTitle = append(fromTitle, h)
		}
	}
	if len(fromTitle) == 1 {
		return fromTitle[0].id, fromTitle[0].ref, refResolved, candidates
	}

	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.id.String()+"("+string(h.ref.Kind)+"/"+h.ref.Source+")")
	}
	log.Printf("[vcs-webhook] ambiguous payload names %d distinct tasks, linking none: %s",
		len(hits), strings.Join(ids, " "))
	return uuid.Nil, TaskRef{}, refAmbiguous, candidates
}

// lookupRef turns one candidate into a task id, reporting whether it named a
// task that exists AND belongs to workspaceID (uuid.Nil skips the workspace
// check entirely — see WebhookSecret's doc comment for when that's the
// deliberate unscoped case vs. §an internal ticket's bug). A task that exists but
// belongs to a different workspace is reported exactly like a task that
// doesn't exist at all — the caller (resolveTaskRef) must not be able to
// tell "wrong workspace" apart from "no such task", the same way it already
// cannot tell apart a deleted task from a typo'd id: either distinction
// would let a probing request fingerprint another workspace's task ids.
func (s *vcsLinkService) lookupRef(ctx context.Context, workspaceID uuid.UUID, ref TaskRef) (uuid.UUID, bool) {
	switch {
	case ref.Full != uuid.Nil:
		if s.taskRepo == nil {
			// CRUD-only construction: no way to verify, and the spelling was
			// explicit enough to be taken at face value.
			return ref.Full, true
		}
		t, err := s.taskRepo.GetByID(ctx, ref.Full)
		if err != nil {
			log.Printf("[vcs-webhook] lookup task=%s (%s) failed: %v", ref.Full, ref.Kind, err)
			return uuid.Nil, false
		}
		if t == nil {
			return uuid.Nil, false
		}
		if !s.projectInWorkspace(ctx, t.ProjectID, workspaceID) {
			return uuid.Nil, false
		}
		return t.ID, true

	case ref.Short != "":
		if s.taskRepo == nil {
			return uuid.Nil, false
		}
		// GetByShortID refuses an ambiguous prefix (apierror.BadRequest) rather
		// than picking one — two tenants behind one 8-hex prefix must not
		// silently resolve to whichever row sorted first.
		t, err := s.taskRepo.GetByShortID(ctx, ref.Short)
		if err != nil {
			log.Printf("[vcs-webhook] short id %q (%s) unresolved: %v", ref.Short, ref.Kind, err)
			return uuid.Nil, false
		}
		if t == nil {
			return uuid.Nil, false
		}
		if !s.projectInWorkspace(ctx, t.ProjectID, workspaceID) {
			return uuid.Nil, false
		}
		return t.ID, true
	}
	return uuid.Nil, false
}

// projectInWorkspace reports whether projectID belongs to workspaceID.
// workspaceID==uuid.Nil is the deliberate unscoped case (the request was
// validated by the instance-wide env fallback secret, which isn't owned by
// any one workspace — see WebhookSecret's doc comment) and always returns
// true without touching projectRepo at all.
//
// For any other workspaceID this fails CLOSED: no projectRepo wired, a
// lookup error, or a missing project all return false, never true.
// "Couldn't verify ownership" must never be silently treated as "verified"
// — that is exactly how an internal ticket stayed open, and the same fail-open vs.
// fail-closed distinction the team workflow guide §1q Правило 3 draws for the
// merge train applies here.
func (s *vcsLinkService) projectInWorkspace(ctx context.Context, projectID, workspaceID uuid.UUID) bool {
	if workspaceID == uuid.Nil {
		return true
	}
	if s.projectRepo == nil {
		log.Printf("[vcs-webhook] workspace scope check refused: no projectRepo wired (project=%s workspace=%s)", projectID, workspaceID)
		return false
	}
	proj, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil {
		log.Printf("[vcs-webhook] workspace scope check failed for project=%s: %v — refusing", projectID, err)
		return false
	}
	if proj == nil {
		return false
	}
	return proj.WorkspaceID == workspaceID
}

// taskInWorkspace is projectInWorkspace's task-id-first counterpart, used by
// the "no ref in this payload, fall back to a stored link" path in
// HandleGitHubPullRequestEvent/HandleGitLabMergeRequestEvent — that path
// finds a task via link_type+external_id (a PR/MR number: a small,
// guessable integer with no workspace component at all), so it needs the
// same scoping lookupRef applies to a title/body/branch-named task, just
// starting from a task id instead of an already-fetched *domain.Task.
func (s *vcsLinkService) taskInWorkspace(ctx context.Context, taskID, workspaceID uuid.UUID) bool {
	if workspaceID == uuid.Nil {
		return true
	}
	if s.taskRepo == nil {
		return false
	}
	t, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil || t == nil {
		return false
	}
	return s.projectInWorkspace(ctx, t.ProjectID, workspaceID)
}

// filterLinksByURL keeps the links naming the same PR/MR as rawURL. The
// stored external_id is only the number, which is unique per project, so the
// url is what tells two projects' !88 apart. An empty rawURL filters nothing.
func filterLinksByURL(links []domain.VCSLink, rawURL string) []domain.VCSLink {
	if rawURL == "" {
		return links
	}
	want := domain.NormalizeVCSURL(rawURL)
	out := make([]domain.VCSLink, 0, len(links))
	for _, l := range links {
		if domain.NormalizeVCSURL(l.URL) == want {
			out = append(out, l)
		}
	}
	return out
}

// filterLinksInWorkspace drops every link whose task does not belong to
// workspaceID (a no-op returning links unchanged when workspaceID is
// uuid.Nil — the unscoped env-fallback case). Used by the "no ref named in
// this payload, fall back to whatever's already linked to this PR/MR
// number" branch of HandleGitHubPullRequestEvent/HandleGitLabMergeRequestEvent.
func (s *vcsLinkService) filterLinksInWorkspace(ctx context.Context, workspaceID uuid.UUID, links []domain.VCSLink) []domain.VCSLink {
	if workspaceID == uuid.Nil {
		return links
	}
	out := make([]domain.VCSLink, 0, len(links))
	for _, l := range links {
		if s.taskInWorkspace(ctx, l.TaskID, workspaceID) {
			out = append(out, l)
		}
	}
	return out
}

// stampManualStatusDeclaration records who declared a VCS link's status by
// hand (an explicit status passed to add_vcs_link) and when, as a
// "status_declared_manually" object nested in the link's metadata. This is
// the trail that lets a reader tell a self-declared status apart from one
// the done-evidence gate actually measured (a GitHub webhook delivery, or a
// live API check — see taskService.healVCSLinkStatus, which never calls
// this function). Merges into whatever metadata the caller already
// supplied; falls back to the untouched input on a marshal failure rather
// than losing caller-supplied metadata.
func stampManualStatusDeclaration(raw json.RawMessage, status domain.VCSLinkStatus, actorID uuid.UUID, actorType domain.ActorType, at time.Time) json.RawMessage {
	m := map[string]interface{}{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			m = map[string]interface{}{}
		}
	}
	declaration := map[string]interface{}{
		"status": string(status),
		"at":     at.UTC().Format(time.RFC3339),
	}
	if actorType != "" {
		declaration["by_type"] = string(actorType)
	}
	if actorID != uuid.Nil {
		declaration["by_id"] = actorID.String()
	}
	m["status_declared_manually"] = declaration

	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// gitLabLinkMetadata is the metadata patch a MR webhook writes: always a
// head_sha key. A payload without a head commit writes "" so a previously
// stored head can never be mistaken for the current one (the gate treats an
// empty head as unproven). Upsert merges the patch into the stored metadata,
// so ship_sha and other keys survive every sync.
func gitLabLinkMetadata(headSHA string) []byte {
	headSHA = strings.TrimSpace(headSHA)
	b, _ := json.Marshal(map[string]string{domain.VCSLinkMetaHeadSHA: headSHA}) // a string map cannot fail to marshal
	return b
}
