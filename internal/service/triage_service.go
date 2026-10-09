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

// ListTriageTasks returns the paginated human-attention queue across all workspace
// projects: tasks in a triage-category status plus tasks with a live human gate
// (auto-delegation cards are never moved to triage, so the gate is the only signal).
func (s *triageService) ListTriageTasks(ctx context.Context, workspaceID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	pg.Normalize()
	return s.taskRepo.ListTriageQueue(ctx, workspaceID, pg)
}
