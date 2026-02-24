package service

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

type commentService struct {
	commentRepo  repository.CommentRepository
	taskRepo     repository.TaskRepository
	activityRepo repository.ActivityLogRepository
}

// NewCommentService returns a new CommentService backed by the given repositories.
func NewCommentService(
	commentRepo repository.CommentRepository,
	taskRepo repository.TaskRepository,
	activityRepo repository.ActivityLogRepository,
) CommentService {
	return &commentService{
		commentRepo:  commentRepo,
		taskRepo:     taskRepo,
		activityRepo: activityRepo,
	}
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

	return s.commentRepo.Create(ctx, comment)
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

	// Only allow body updates; preserve other fields from the existing record.
	existing.Body = comment.Body
	existing.UpdatedAt = timeNow()

	return s.commentRepo.Update(ctx, existing)
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
	return s.commentRepo.Delete(ctx, id)
}

// ListByTask returns a paginated list of comments for the given task.
func (s *commentService) ListByTask(ctx context.Context, taskID uuid.UUID, filter repository.CommentFilter, pg pagination.Params) (*pagination.Page[domain.Comment], error) {
	pg.Normalize()
	return s.commentRepo.ListByTask(ctx, taskID, filter, pg)
}
