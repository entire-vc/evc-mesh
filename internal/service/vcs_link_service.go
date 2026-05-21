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

// Create creates a new VCS link.
func (s *vcsLinkService) Create(ctx context.Context, input domain.CreateVCSLinkInput) (*domain.VCSLink, error) {
	if input.TaskID == uuid.Nil {
		return nil, apierror.BadRequest("task_id is required")
	}
	if input.URL == "" {
		return nil, apierror.BadRequest("url is required")
	}
	if input.ExternalID == "" {
		return nil, apierror.BadRequest("external_id is required")
	}
	if input.LinkType == "" {
		return nil, apierror.BadRequest("link_type is required")
	}

	provider := input.Provider
	if provider == "" {
		provider = domain.VCSProviderGitHub
	}

	metadata := input.Metadata
	if metadata == nil {
		metadata = []byte("{}")
	}

	link := &domain.VCSLink{
		ID:         uuid.New(),
		TaskID:     input.TaskID,
		Provider:   provider,
		LinkType:   input.LinkType,
		ExternalID: input.ExternalID,
		URL:        input.URL,
		Title:      input.Title,
		Status:     input.Status,
		Metadata:   metadata,
		CreatedAt:  time.Now(),
	}

	if err := s.repo.Create(ctx, link); err != nil {
		return nil, fmt.Errorf("create vcs link: %w", err)
	}
	return link, nil
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

	// 1. Resolve task_id from MESH-<uuid> in title, then body, then fallback
	//    by previously-linked (provider, link_type, external_id).
	taskID := extractMeshTaskIDFromTexts(ev.PRTitle, ev.PRBody)
	if taskID == uuid.Nil {
		links, err := s.repo.ListByExternalID(ctx, domain.VCSProviderGitHub, domain.VCSLinkTypePR, strconv.Itoa(ev.PRNumber))
		if err != nil {
			return PRHandleResult{}, fmt.Errorf("list vcs links by external_id: %w", err)
		}
		switch {
		case len(links) == 0:
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
	if err := s.repo.Upsert(ctx, link); err != nil {
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

// extractMeshTaskIDFromTexts scans title then body for the MESH-<uuid> pattern.
func extractMeshTaskIDFromTexts(parts ...string) uuid.UUID {
	for _, p := range parts {
		if id := extractMeshTaskIDFromText(p); id != uuid.Nil {
			return id
		}
	}
	return uuid.Nil
}

// extractMeshTaskIDFromText parses a full MESH-<uuid> token from text. Only a
// complete (36-char) UUID is recognised; short prefixes seen in the UI must
// be resolved by callers via the external_id fallback path.
func extractMeshTaskIDFromText(text string) uuid.UUID {
	const prefix = "MESH-"
	idx := strings.Index(text, prefix)
	if idx == -1 {
		return uuid.Nil
	}
	rest := text[idx+len(prefix):]
	end := len(rest)
	if end > 36 {
		end = 36
	}
	candidate := rest[:end]
	for i, ch := range candidate {
		if !isHexOrDashByte(ch) {
			candidate = candidate[:i]
			break
		}
	}
	id, err := uuid.Parse(candidate)
	if err != nil {
		return uuid.Nil
	}
	return id
}

func isHexOrDashByte(ch rune) bool {
	return (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F') || ch == '-'
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}
