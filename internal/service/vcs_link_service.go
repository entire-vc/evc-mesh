package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// garfieldAgentID is the agent UUID under which the webhook orchestrator
// authors auto-transition system comments. Per subtask 93cb9141 spec we use
// Garfield rather than introducing a separate "system" agent record.
var garfieldAgentID = uuid.MustParse("99a835df-fe8c-424b-84c5-949595fb9eb2")

// vcsLinkService implements VCSLinkService.
type vcsLinkService struct {
	repo       repository.VCSLinkRepository
	taskRepo   repository.TaskRepository
	statusRepo repository.TaskStatusRepository
	taskSvc    TaskService
	commentSvc CommentService
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
// auto-transition comments authored as Garfield.
func WithVCSCommentService(svc CommentService) VCSLinkServiceOption {
	return func(s *vcsLinkService) { s.commentSvc = svc }
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
	// #2697392d). A caller that omits it leaves the column "" — which is not
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
	if !explicitStatus && linkType == domain.VCSLinkTypePR {
		status = domain.VCSLinkStatusOpen
	}

	if explicitStatus {
		// A hand-declared status (add_vcs_link ... status=merged) is the
		// documented way to correct a stale/missing record — but the same
		// call is indistinguishable in shape from declaring something merged
		// that isn't. A gate removable by self-declaration only protects
		// until the first agent it inconveniences (#5f7f8c6e), so record WHO
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

	if explicitStatus {
		// A caller who explicitly states the status knows something the
		// stored row might not — most commonly, linking a PR that was
		// already merged before the link existed (#df734dd9: no webhook
		// will ever arrive for that after the fact) or correcting a link
		// created before status tracking existed. Upsert so this succeeds
		// whether or not the link already exists, instead of failing on
		// (task_id, provider, link_type, external_id) uniqueness — the
		// exact wall that made a3bdf4ad's rows permanently uncorrectable.
		// A caller that does NOT pass status keeps the plain-Create path
		// below unchanged: an accidental duplicate add still fails loudly
		// instead of silently resetting an existing 'merged' row to 'open'.
		//
		// Upsert mutates link's ID/CreatedAt in place to the actual persisted
		// row when this call updated an existing one (see its doc) — so link
		// is safe to return as-is either way.
		created, err := s.repo.Upsert(ctx, link)
		if err != nil {
			return nil, false, fmt.Errorf("create vcs link: %w", err)
		}
		return link, created, nil
	}

	if err := s.repo.Create(ctx, link); err != nil {
		return nil, false, fmt.Errorf("create vcs link: %w", err)
	}
	return link, true, nil
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
	taskID, matched, outcome := s.resolveTaskRef(ctx, sources...)
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
			for _, l := range stale {
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
		// stored first. That is not hypothetical — PR #433 attached itself to
		// the task its body quoted as an example, and because each subsequent
		// delivery took this fallback, the refusal added one commit earlier
		// could never undo it. A link that can only be corrected by hand is a
		// link that stays wrong.
		log.Printf("[vcs-webhook] pr=%s#%d ambiguous_task_ref: refusing to reuse any stored link for this PR",
			ev.Repository, ev.PRNumber)
		return PRHandleResult{Reason: "ambiguous_task_ref"}, nil
	}
	if taskID == uuid.Nil {
		links, err := s.repo.ListByExternalID(ctx, domain.VCSProviderGitHub, domain.VCSLinkTypePR, strconv.Itoa(ev.PRNumber))
		if err != nil {
			return PRHandleResult{}, fmt.Errorf("list vcs links by external_id: %w", err)
		}
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

	// 5a. closed without merge — post comment, no status change.
	if !ev.PRMerged {
		body := fmt.Sprintf("🤖 Auto: PR #%d closed without merge — no status change.", ev.PRNumber)
		if cerr := s.postSystemComment(ctx, taskID, body); cerr != nil {
			log.Printf("[vcs-webhook] post comment failed task=%s: %v", taskID, cerr)
		}
		return PRHandleResult{TaskID: taskID, Reason: "closed_without_merge"}, nil
	}

	// 5b. closed + merged. Check that all PRs linked to this task are terminal
	//     (merged or closed). If not, this is a partial merge in a multi-PR
	//     task — comment and wait.
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
		body := fmt.Sprintf("🤖 Auto: PR #%d merged. Awaiting %d more PR(s) before status transition.", ev.PRNumber, pendingCount)
		if cerr := s.postSystemComment(ctx, taskID, body); cerr != nil {
			log.Printf("[vcs-webhook] post comment failed task=%s: %v", taskID, cerr)
		}
		return PRHandleResult{TaskID: taskID, Reason: "awaiting_other_prs"}, nil
	}

	// 5c. All PRs terminal — apply the transition policy.
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
		body := fmt.Sprintf("🤖 Auto: PR #%d merged. Task in `%s`; no auto-transition.", ev.PRNumber, currentStatus.Slug)
		if cerr := s.postSystemComment(ctx, taskID, body); cerr != nil {
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

	body := fmt.Sprintf("🤖 Auto: PR #%d merged (commit `%s`) → moved to %s.", ev.PRNumber, shortSHA(ev.MergeSHA), targetSlug)
	if cerr := s.postSystemComment(ctx, taskID, body); cerr != nil {
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

// postSystemComment writes a non-internal comment authored as Garfield.
func (s *vcsLinkService) postSystemComment(ctx context.Context, taskID uuid.UUID, body string) error {
	c := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     taskID,
		AuthorID:   garfieldAgentID,
		AuthorType: domain.ActorTypeAgent,
		Body:       body,
		Metadata:   json.RawMessage(`{"source":"github-webhook"}`),
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
func (s *vcsLinkService) ResolveTaskRef(ctx context.Context, sources ...TaskRefSource) (uuid.UUID, TaskRef) {
	id, ref, _ := s.resolveTaskRef(ctx, sources...)
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

func (s *vcsLinkService) resolveTaskRef(ctx context.Context, sources ...TaskRefSource) (uuid.UUID, TaskRef, refOutcome) {
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

		id, ok := s.lookupRef(ctx, ref)
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		hits = append(hits, hit{id: id, ref: ref})
	}

	switch len(hits) {
	case 0:
		return uuid.Nil, TaskRef{}, refNone
	case 1:
		return hits[0].id, hits[0].ref, refResolved
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
		return fromTitle[0].id, fromTitle[0].ref, refResolved
	}

	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.id.String()+"("+string(h.ref.Kind)+"/"+h.ref.Source+")")
	}
	log.Printf("[vcs-webhook] ambiguous payload names %d distinct tasks, linking none: %s",
		len(hits), strings.Join(ids, " "))
	return uuid.Nil, TaskRef{}, refAmbiguous
}

// lookupRef turns one candidate into a task id, reporting whether it named a
// task that exists.
func (s *vcsLinkService) lookupRef(ctx context.Context, ref TaskRef) (uuid.UUID, bool) {
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
		return t.ID, true
	}
	return uuid.Nil, false
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
