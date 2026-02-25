package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

type activityLogService struct {
	activityRepo repository.ActivityLogRepository
}

// NewActivityLogService returns a new ActivityLogService backed by the given repository.
func NewActivityLogService(
	activityRepo repository.ActivityLogRepository,
) ActivityLogService {
	return &activityLogService{
		activityRepo: activityRepo,
	}
}

// Log creates a new activity log entry.
func (s *activityLogService) Log(ctx context.Context, entry *domain.ActivityLog) error {
	if entry.ID == uuid.Nil {
		entry.ID = uuid.New()
	}
	entry.CreatedAt = timeNow()

	return s.activityRepo.Create(ctx, entry)
}

// List returns a paginated list of activity log entries for the given workspace.
func (s *activityLogService) List(ctx context.Context, workspaceID uuid.UUID, filter repository.ActivityLogFilter, pg pagination.Params) (*pagination.Page[domain.ActivityLog], error) {
	pg.Normalize()
	return s.activityRepo.List(ctx, workspaceID, filter, pg)
}

// ListByTask returns a paginated list of activity log entries for the given task.
func (s *activityLogService) ListByTask(ctx context.Context, taskID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.ActivityLog], error) {
	pg.Normalize()
	return s.activityRepo.ListByTask(ctx, taskID, pg)
}

// Export returns up to limit activity log entries matching the filter, for CSV/JSON export.
func (s *activityLogService) Export(ctx context.Context, workspaceID uuid.UUID, filter repository.ActivityLogFilter, limit int) ([]domain.ActivityLog, error) {
	const maxLimit = 10000
	if limit <= 0 || limit > maxLimit {
		limit = maxLimit
	}
	return s.activityRepo.Export(ctx, workspaceID, filter, limit)
}
