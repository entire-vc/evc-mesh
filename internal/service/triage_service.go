package service

import (
	"context"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

type triageService struct {
	tasks repository.TaskQueryRepository
}

// NewTriageService creates a new TriageService.
func NewTriageService(tasks repository.TaskQueryRepository) TriageService {
	return &triageService{tasks: tasks}
}

// ListTriageTasks returns the paginated human-attention queue across the workspace
// projects the viewer may read: tasks in a triage-category status plus tasks with a
// live human gate (auto-delegation cards are never moved to triage, so the gate is
// the only signal for them). q narrows it by project or to hard gates; the gate
// predicate and the inbox order are always applied.
func (s *triageService) ListTriageTasks(ctx context.Context, q repository.TaskQuery, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	pg.Normalize()
	q.Gated = true
	q.Sort = repository.TaskQuerySortTriage
	return s.tasks.QueryTasks(ctx, q, pg)
}

// QueryTasks runs a dashboard TaskQuery as given.
func (s *triageService) QueryTasks(ctx context.Context, q repository.TaskQuery, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	pg.Normalize()
	return s.tasks.QueryTasks(ctx, q, pg)
}
