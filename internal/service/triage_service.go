package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

type triageService struct {
	taskRepo repository.TaskRepository
}

// NewTriageService creates a new TriageService.
func NewTriageService(taskRepo repository.TaskRepository) TriageService {
	return &triageService{taskRepo: taskRepo}
}

// ListTriageTasks returns paginated tasks in "triage" category across all workspace projects.
func (s *triageService) ListTriageTasks(ctx context.Context, workspaceID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	pg.Normalize()
	return s.taskRepo.ListByStatusCategory(ctx, workspaceID, domain.StatusCategoryTriage, pg)
}
