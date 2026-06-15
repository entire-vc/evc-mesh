package service

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// mentionRegex matches @-slug references in comment bodies.
// Slug pattern mirrors agents.slug and users.username: starts/ends with alnum, middle may contain hyphens, 2-40 chars total.
// The leading boundary allows start-of-string, whitespace, or open bracket/paren/brace.
var mentionRegex = regexp.MustCompile(`(?:^|[\s(\[{])@([a-z0-9][a-z0-9-]{0,38}[a-z0-9])\b`)

// blockingMarkerRegex matches the "❓ **Blocking @<user>**" workflow marker
// (CLAUDE-workflow.md §0b) anchored to the start of a line. It tolerates the optional
// ❓ emoji and markdown bold (`**`), and is case-insensitive/multiline.
//   - The slug subpattern is identical to mentionRegex (and the username/slug DB
//     constraint): starts/ends with alnum, hyphens allowed in the middle, no underscore.
//   - "ℹ️ **FYI @user**" has no "Blocking" keyword → never matches (FYI is a no-op).
//   - A quoted line ("> ❓ **Blocking @x**") is NOT matched: after `^\s*`, the `>` is
//     neither whitespace nor one of ❓/`*`/`Blocking`, so the anchor fails.
var blockingMarkerRegex = regexp.MustCompile(`(?im)^\s*(?:❓\s*)?\*{0,2}\s*Blocking\s+@([a-z0-9][a-z0-9-]{0,38}[a-z0-9])\b`)

// systemActorID is the author_id used for system-generated comments. uuid.Nil is safe:
// comments.author_id is NOT NULL but carries no foreign key, and the author_name SELECT
// resolves system comments to NULL (rendered as "System" in the UI) regardless of value.
var systemActorID = uuid.Nil

// hasBlockingMarker reports whether body contains a "❓ **Blocking @user**" marker.
func hasBlockingMarker(body string) bool {
	return blockingMarkerRegex.MatchString(body)
}

// extractMentionSlugs returns unique lowercase slugs found in body, preserving order.
func extractMentionSlugs(body string) []string {
	matches := mentionRegex.FindAllStringSubmatch(body, -1)
	seen := make(map[string]bool, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		slug := strings.ToLower(m[1])
		if !seen[slug] {
			seen[slug] = true
			out = append(out, slug)
		}
	}
	return out
}

// truncateDesc truncates a string to maxLen characters.
func truncateDesc(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}

type commentService struct {
	commentRepo    repository.CommentRepository
	taskRepo       repository.TaskRepository
	activityRepo   repository.ActivityLogRepository
	agentNotifySvc AgentNotifyService
	agentSvc       AgentService
	notifySvc      NotificationService
	statusRepo     repository.TaskStatusRepository
	projectRepo    repository.ProjectRepository
	ctxCacheInv    ContextCacheInvalidator
	userRepo       repository.UserRepository
	mentionRepo    repository.CommentMentionRepository
	wsPublisher    WSPublisher
	taskSvc        TaskService
}

// CommentServiceOption configures optional dependencies for CommentService.
type CommentServiceOption func(*commentService)

// WithCommentAgentNotify sets the agent notification service on the comment service.
func WithCommentAgentNotify(ans AgentNotifyService) CommentServiceOption {
	return func(s *commentService) { s.agentNotifySvc = ans }
}

// WithCommentAgentService sets the agent service used to resolve @-mention slugs.
func WithCommentAgentService(as AgentService) CommentServiceOption {
	return func(s *commentService) { s.agentSvc = as }
}

// WithCommentUserRepo sets the user repository used to resolve @username mentions.
func WithCommentUserRepo(r repository.UserRepository) CommentServiceOption {
	return func(s *commentService) { s.userRepo = r }
}

// WithCommentMentionRepo sets the mention repository for persisting comment_mentions rows.
func WithCommentMentionRepo(r repository.CommentMentionRepository) CommentServiceOption {
	return func(s *commentService) { s.mentionRepo = r }
}

// WithCommentWSPublisher sets the WS publisher used to push badge-update events to users.
func WithCommentWSPublisher(p WSPublisher) CommentServiceOption {
	return func(s *commentService) { s.wsPublisher = p }
}

// WithCommentStatusRepo sets the status repo for building task snapshots.
func WithCommentStatusRepo(r repository.TaskStatusRepository) CommentServiceOption {
	return func(s *commentService) { s.statusRepo = r }
}

// WithCommentProjectRepo sets the project repo for resolving workspace_id.
func WithCommentProjectRepo(r repository.ProjectRepository) CommentServiceOption {
	return func(s *commentService) { s.projectRepo = r }
}

// WithCommentContextCacheInvalidator sets an optional cache invalidator that is
// called after every comment mutation so the parent task's context cache is evicted.
func WithCommentContextCacheInvalidator(inv ContextCacheInvalidator) CommentServiceOption {
	return func(s *commentService) { s.ctxCacheInv = inv }
}

// WithCommentNotificationService sets the notification service for dispatching
// in-app notifications to workspace users when a new comment is created.
func WithCommentNotificationService(ns NotificationService) CommentServiceOption {
	return func(s *commentService) { s.notifySvc = ns }
}

// WithCommentTaskService injects the task service used to auto-move a task to triage
// when a "❓ Blocking @user" marker targeting a human is detected in a comment
// (server-side enforcement of CLAUDE-workflow.md §0b). Optional — if unset, the
// enforcement step is skipped entirely.
func WithCommentTaskService(ts TaskService) CommentServiceOption {
	return func(s *commentService) { s.taskSvc = ts }
}

// NewCommentService returns a new CommentService backed by the given repositories.
func NewCommentService(
	commentRepo repository.CommentRepository,
	taskRepo repository.TaskRepository,
	activityRepo repository.ActivityLogRepository,
	opts ...CommentServiceOption,
) CommentService {
	s := &commentService{
		commentRepo:  commentRepo,
		taskRepo:     taskRepo,
		activityRepo: activityRepo,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Create validates and persists a new comment.
// It checks that the task exists, the body is not empty, and if a parent comment
// is specified, the parent exists and belongs to the same task.
func (s *commentService) Create(ctx context.Context, comment *domain.Comment) error {
	if strings.TrimSpace(comment.Body) == "" {
		return apierror.ValidationError(map[string]string{
			"body": "body is required",
		})
	}

	// Validate the task exists.
	task, err := s.taskRepo.GetByID(ctx, comment.TaskID)
	if err != nil {
		return err
	}
	if task == nil {
		return apierror.NotFound("Task")
	}

	// Validate parent comment if provided.
	if comment.ParentCommentID != nil {
		parent, err := s.commentRepo.GetByID(ctx, *comment.ParentCommentID)
		if err != nil {
			return err
		}
		if parent == nil {
			return apierror.NotFound("ParentComment")
		}
		if parent.TaskID != comment.TaskID {
			return apierror.BadRequest("parent comment does not belong to the same task")
		}
	}

	if comment.ID == uuid.Nil {
		comment.ID = uuid.New()
	}

	now := timeNow()
	comment.CreatedAt = now
	comment.UpdatedAt = now

	if err := s.commentRepo.Create(ctx, comment); err != nil {
		return err
	}
	// Re-fetch so the returned comment includes the computed author_name.
	if enriched, err2 := s.commentRepo.GetByID(ctx, comment.ID); err2 == nil && enriched != nil {
		*comment = *enriched
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, comment.TaskID)
	}

	// Resolve workspace ID once for all agent notifications.
	var wsID uuid.UUID
	if s.projectRepo != nil {
		if proj, err := s.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
			wsID = proj.WorkspaceID
		}
	}

	// Notify assigned agent about the new comment.
	if s.agentNotifySvc != nil && task.AssigneeType == domain.AssigneeTypeAgent && task.AssigneeID != nil {
		taskSnap := s.buildTaskSnap(ctx, task)

		commentBody := comment.Body
		if len(commentBody) > 500 {
			commentBody = commentBody[:500]
		}

		// Extract actor info from request context.
		actorID, actorType := actorctx.FromContext(ctx)
		actorName := actorctx.NameFromContext(ctx)

		s.agentNotifySvc.NotifyAgent(ctx, *task.AssigneeID, AgentNotification{
			EventType:   "task.commented",
			Timestamp:   now,
			WorkspaceID: wsID,
			Task:        taskSnap,
			AgentID:     *task.AssigneeID,
			ActorID:     actorID,
			ActorType:   string(actorType),
			ActorName:   actorName,
			Comment: map[string]any{
				"id":        comment.ID,
				"body":      commentBody,
				"author_id": comment.AuthorID,
			},
			TaskID:    task.ID,
			ProjectID: task.ProjectID,
		})
	}

	// Notify @-mentioned agents.
	if wsID != uuid.Nil {
		s.notifyMentions(ctx, comment, task, "", wsID)
		// Server-side enforcement: "❓ Blocking @user" → auto-move task to triage.
		s.enforceBlockingTriage(ctx, comment, task, wsID)
	}

	// Dispatch in-app notification to subscribed workspace users for comment.created.
	if s.notifySvc != nil && s.projectRepo != nil {
		if proj, err := s.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
			taskIDCopy := task.ID
			projIDCopy := task.ProjectID
			commentBody := comment.Body
			if len(commentBody) > 200 {
				commentBody = commentBody[:200]
			}
			s.notifySvc.Notify(ctx, domain.NotificationEvent{
				WorkspaceID: proj.WorkspaceID,
				TaskID:      &taskIDCopy,
				ProjectID:   &projIDCopy,
				EventType:   "comment.created",
				Title:       "New comment on: " + task.Title,
				Body:        commentBody,
				Metadata: map[string]any{
					"task_id":    task.ID,
					"task_title": task.Title,
					"project_id": task.ProjectID,
					"comment_id": comment.ID,
				},
			})
		}
	}

	return nil
}

// Update validates that the comment exists and persists body changes.
func (s *commentService) Update(ctx context.Context, comment *domain.Comment) error {
	existing, err := s.commentRepo.GetByID(ctx, comment.ID)
	if err != nil {
		return err
	}
	if existing == nil {
		return apierror.NotFound("Comment")
	}

	actorID, actorType := actorctx.FromContext(ctx)
	if existing.AuthorID != actorID || existing.AuthorType != actorType {
		return apierror.Forbidden("you can only edit your own comments")
	}

	// Only allow body updates; preserve other fields from the existing record.
	oldBody := existing.Body
	existing.Body = comment.Body
	existing.UpdatedAt = timeNow()

	if err := s.commentRepo.Update(ctx, existing); err != nil {
		return err
	}
	// Re-fetch so the caller gets the fully enriched record (author_name etc.).
	if enriched, err2 := s.commentRepo.GetByID(ctx, existing.ID); err2 == nil && enriched != nil {
		*comment = *enriched
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, existing.TaskID)
	}

	// Notify newly @-mentioned agents/users (diff against previous body).
	if s.projectRepo != nil {
		if task, err := s.taskRepo.GetByID(ctx, existing.TaskID); err == nil && task != nil {
			if proj, err := s.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
				s.notifyMentions(ctx, existing, task, oldBody, proj.WorkspaceID)
				// Re-evaluate enforcement only when the marker was newly added by this
				// edit (absent in oldBody). The status idempotency check below is a
				// second safeguard against double-triage.
				if !hasBlockingMarker(oldBody) {
					s.enforceBlockingTriage(ctx, existing, task, proj.WorkspaceID)
				}
			}
		}
	}

	return nil
}

// Delete removes a comment after verifying it exists.
func (s *commentService) Delete(ctx context.Context, id uuid.UUID) error {
	existing, err := s.commentRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return apierror.NotFound("Comment")
	}

	actorID, actorType := actorctx.FromContext(ctx)
	if existing.AuthorID != actorID || existing.AuthorType != actorType {
		return apierror.Forbidden("you can only delete your own comments")
	}

	if err := s.commentRepo.Delete(ctx, id); err != nil {
		return err
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, existing.TaskID)
	}
	return nil
}

// ListByTask returns a paginated list of comments for the given task.
func (s *commentService) ListByTask(ctx context.Context, taskID uuid.UUID, filter repository.CommentFilter, pg pagination.Params) (*pagination.Page[domain.Comment], error) {
	pg.Normalize()
	return s.commentRepo.ListByTask(ctx, taskID, filter, pg)
}

// ListByAuthor returns the caller's own comments, newest first (activity feed).
func (s *commentService) ListByAuthor(ctx context.Context, authorID uuid.UUID, filter repository.CommentViewFilter) (*domain.CommentViewPage, error) {
	items, nextCursor, err := s.commentRepo.ListByAuthor(ctx, authorID, filter)
	if err != nil {
		return nil, err
	}
	return &domain.CommentViewPage{Items: items, NextCursor: nextCursor}, nil
}

// ListRecentByWorkspace returns workspace-wide recent comments, newest first (activity feed).
func (s *commentService) ListRecentByWorkspace(ctx context.Context, wsID uuid.UUID, filter repository.CommentViewFilter) (*domain.CommentViewPage, error) {
	items, nextCursor, err := s.commentRepo.ListRecentByWorkspace(ctx, wsID, filter)
	if err != nil {
		return nil, err
	}
	return &domain.CommentViewPage{Items: items, NextCursor: nextCursor}, nil
}

// buildTaskSnap constructs the task snapshot map used in agent notifications.
func (s *commentService) buildTaskSnap(ctx context.Context, task *domain.Task) map[string]any {
	snap := map[string]any{
		"id":            task.ID,
		"project_id":    task.ProjectID,
		"title":         task.Title,
		"priority":      string(task.Priority),
		"description":   truncateDesc(task.Description, 500),
		"assignee_id":   task.AssigneeID,
		"assignee_type": string(task.AssigneeType),
		"labels":        task.Labels,
	}
	if s.statusRepo != nil {
		if status, err := s.statusRepo.GetByID(ctx, task.StatusID); err == nil && status != nil {
			snap["status"] = map[string]any{
				"id": status.ID, "name": status.Name, "category": string(status.Category),
			}
		}
	}
	return snap
}

// notifyMentions resolves @-mentioned slugs to agents/users, persists comment_mentions rows,
// sends task.mentioned SSE to agents, and pushes WS badge-update events to users.
// When oldBody is non-empty, only slugs newly added (not in oldBody) are processed.
func (s *commentService) notifyMentions(
	ctx context.Context,
	comment *domain.Comment,
	task *domain.Task,
	oldBody string,
	workspaceID uuid.UUID,
) {
	newSlugs := extractMentionSlugs(comment.Body)
	if len(newSlugs) == 0 {
		return
	}
	if oldBody != "" {
		oldSet := make(map[string]bool)
		for _, sl := range extractMentionSlugs(oldBody) {
			oldSet[sl] = true
		}
		diff := newSlugs[:0]
		for _, sl := range newSlugs {
			if !oldSet[sl] {
				diff = append(diff, sl)
			}
		}
		newSlugs = diff
		if len(newSlugs) == 0 {
			return
		}
	}

	actorID, actorType := actorctx.FromContext(ctx)
	actorName := actorctx.NameFromContext(ctx)

	commentBody := comment.Body
	if len(commentBody) > 500 {
		commentBody = commentBody[:500]
	}

	var taskSnap map[string]any
	if s.agentNotifySvc != nil {
		taskSnap = s.buildTaskSnap(ctx, task)
	}
	now := timeNow()

	seenID := make(map[uuid.UUID]bool)
	var dbRows []domain.CommentMention

	for _, slug := range newSlugs {
		// Try agent lookup first.
		if s.agentSvc != nil {
			agent, err := s.agentSvc.GetBySlug(ctx, workspaceID, slug)
			if err == nil && agent != nil && !seenID[agent.ID] {
				seenID[agent.ID] = true
				dbRows = append(dbRows, domain.CommentMention{
					CommentID:     comment.ID,
					MentionedID:   agent.ID,
					MentionedKind: "agent",
					MentionedSlug: slug,
					ExtractedAt:   now,
				})
				if s.agentNotifySvc != nil && (actorType != domain.ActorTypeAgent || agent.ID != actorID) {
					s.agentNotifySvc.NotifyAgent(ctx, agent.ID, AgentNotification{
						EventType:   "task.mentioned",
						Timestamp:   now,
						WorkspaceID: workspaceID,
						Task:        taskSnap,
						AgentID:     agent.ID,
						ActorID:     actorID,
						ActorType:   string(actorType),
						ActorName:   actorName,
						Comment: map[string]any{
							"id":        comment.ID,
							"body":      commentBody,
							"author_id": comment.AuthorID,
						},
						TaskID:    task.ID,
						ProjectID: task.ProjectID,
						Payload:   map[string]any{"mentioned_slug": slug},
					})
				}
				continue
			}
		}

		// Fall back to user lookup.
		if s.userRepo != nil {
			user, err := s.userRepo.GetByUsername(ctx, workspaceID, slug)
			if err == nil && user != nil && !seenID[user.ID] &&
				(actorType != domain.ActorTypeUser || user.ID != actorID) {
				seenID[user.ID] = true
				dbRows = append(dbRows, domain.CommentMention{
					CommentID:     comment.ID,
					MentionedID:   user.ID,
					MentionedKind: "user",
					MentionedSlug: slug,
					ExtractedAt:   now,
				})
				if s.wsPublisher != nil {
					channel := "ws:user:" + user.ID.String()
					_ = s.wsPublisher.Publish(ctx, channel, map[string]any{
						"event":        "mention.badge",
						"workspace_id": workspaceID,
						"task_id":      task.ID,
						"comment_id":   comment.ID,
					})
				}
			}
		}
	}

	if s.mentionRepo != nil && len(dbRows) > 0 {
		_ = s.mentionRepo.InsertBatch(ctx, dbRows)
	}
}

// enforceBlockingTriage is the server-side defense-in-depth for CLAUDE-workflow.md §0b:
// when a comment contains a "❓ **Blocking @user**" marker pointing at a human user, the
// parent task is auto-moved to the project's triage column with an explanatory system
// comment. Every step is best-effort — failures are logged but never block the comment
// mutation that triggered them.
//
// Guards (in order):
//   - required deps (taskSvc/statusRepo/userRepo) present, else no-op;
//   - body actually carries a Blocking marker, else no-op;
//   - human-gate: at least one @-mentioned slug resolves to a user, not just agents
//     (agents are notified via SSE and do not need a triage move);
//   - idempotency: the task is not already in triage/done/cancelled;
//   - the project actually has a triage status column.
func (s *commentService) enforceBlockingTriage(ctx context.Context, comment *domain.Comment, task *domain.Task, wsID uuid.UUID) {
	if s.taskSvc == nil || s.statusRepo == nil || s.userRepo == nil {
		return
	}
	if !hasBlockingMarker(comment.Body) {
		return
	}

	// Human-gate: only act when a real user (not just an agent) is @-mentioned.
	// Resolve this BEFORE the auto-mode early return so we can set the sticky flag.
	userSlug := s.firstMentionedUserSlug(ctx, wsID, comment.Body)
	if userSlug == "" {
		return
	}

	// Arm the sticky human_gate flag regardless of delegation level so the MoveTask
	// gate protects the task even after a delegation_level change (audit P0 #3).
	if setErr := s.taskSvc.SetHumanGate(ctx, task.ID, true); setErr != nil {
		log.Printf("[comment-triage] WARNING: SetHumanGate on task %s failed: %v", task.ID, setErr)
	}

	// auto-mode tasks self-manage; triage escalation is suppressed (flag already set above).
	if task.DelegationLevel == domain.DelegationLevelAuto {
		return
	}

	// Idempotency: never re-triage a task already in triage or a terminal category.
	curStatus, err := s.statusRepo.GetByID(ctx, task.StatusID)
	if err != nil || curStatus == nil {
		return
	}
	switch curStatus.Category {
	case domain.StatusCategoryTriage, domain.StatusCategoryDone, domain.StatusCategoryCancelled:
		return
	}

	// Resolve the project's triage column; graceful no-op if it has none.
	triageID, err := findStatusIDByCategory(ctx, s.statusRepo, task.ProjectID, domain.StatusCategoryTriage)
	if err != nil || triageID == uuid.Nil {
		return
	}

	// Move via TaskService so the move gets activity-log + SSE + auto-transition cascade.
	if err := s.taskSvc.MoveTask(ctx, task.ID, MoveTaskInput{StatusID: &triageID}); err != nil {
		log.Printf("[comment-triage] WARNING: move task %s to triage failed: %v", task.ID, err)
		return
	}

	// Append an explanatory system comment. Written directly through the repo (not
	// s.Create) to avoid re-running the marker parser; the body does not start with a
	// Blocking marker, so it cannot re-trigger enforcement regardless.
	now := timeNow()
	sysComment := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     task.ID,
		AuthorID:   systemActorID,
		AuthorType: domain.ActorTypeSystem,
		Body: fmt.Sprintf(
			"🤖 Auto: задача переведена в triage из-за «❓ Blocking @%s» в комментарии выше (per CLAUDE-workflow.md §0b)",
			userSlug,
		),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.commentRepo.Create(ctx, sysComment); err != nil {
		log.Printf("[comment-triage] WARNING: create system comment on task %s failed: %v", task.ID, err)
		return
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, task.ID)
	}

	// Emit blocking_triage notification so the mentioned user is actually alerted.
	if s.notifySvc != nil {
		taskIDCopy := task.ID
		projIDCopy := task.ProjectID
		s.notifySvc.Notify(ctx, domain.NotificationEvent{
			WorkspaceID: wsID,
			TaskID:      &taskIDCopy,
			ProjectID:   &projIDCopy,
			EventType:   "task.blocking_triage",
			Title:       "Task moved to triage: " + task.Title,
			Body:        fmt.Sprintf("@%s marked this task as blocking — auto-moved to triage.", userSlug),
			Metadata: map[string]any{
				"task_id":    task.ID,
				"task_title": task.Title,
				"project_id": task.ProjectID,
				"user_slug":  userSlug,
			},
		})
	}
}

// firstMentionedUserSlug returns the first @-mentioned slug in body that resolves to a
// human user in the workspace, or "" if no mentioned slug maps to a user. Agent-only
// slugs return (nil, nil) from GetByUsername and are skipped.
func (s *commentService) firstMentionedUserSlug(ctx context.Context, wsID uuid.UUID, body string) string {
	for _, slug := range extractMentionSlugs(body) {
		if user, err := s.userRepo.GetByUsername(ctx, wsID, slug); err == nil && user != nil {
			return slug
		}
	}
	return ""
}
