package service

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

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
	notifySvc      NotificationService
	statusRepo     repository.TaskStatusRepository
	projectRepo    repository.ProjectRepository
	ctxCacheInv    ContextCacheInvalidator
}

// CommentServiceOption configures optional dependencies for CommentService.
type CommentServiceOption func(*commentService)

// WithCommentAgentNotify sets the agent notification service on the comment service.
func WithCommentAgentNotify(ans AgentNotifyService) CommentServiceOption {
	return func(s *commentService) { s.agentNotifySvc = ans }
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
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, comment.TaskID)
	}

	// Notify assigned agent about the new comment.
	if s.agentNotifySvc != nil && task.AssigneeType == domain.AssigneeTypeAgent && task.AssigneeID != nil {
		var wsID uuid.UUID
		if s.projectRepo != nil {
			if proj, err := s.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
				wsID = proj.WorkspaceID
			}
		}

		taskSnap := map[string]any{
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
				taskSnap["status"] = map[string]any{
					"id": status.ID, "name": status.Name, "category": string(status.Category),
				}
			}
		}

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
	existing.Body = comment.Body
	existing.UpdatedAt = timeNow()

	if err := s.commentRepo.Update(ctx, existing); err != nil {
		return err
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, existing.TaskID)
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
