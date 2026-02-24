package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type taskDependencyService struct {
	depRepo      repository.TaskDependencyRepository
	taskRepo     repository.TaskRepository
	activityRepo repository.ActivityLogRepository
}

// NewTaskDependencyService returns a new TaskDependencyService backed by the given repositories.
func NewTaskDependencyService(
	depRepo repository.TaskDependencyRepository,
	taskRepo repository.TaskRepository,
	activityRepo repository.ActivityLogRepository,
) TaskDependencyService {
	return &taskDependencyService{
		depRepo:      depRepo,
		taskRepo:     taskRepo,
		activityRepo: activityRepo,
	}
}

// Create validates and persists a new task dependency.
// It checks that both tasks exist, the dependency is not self-referencing,
// no duplicate exists, and adding it would not create a cycle.
func (s *taskDependencyService) Create(ctx context.Context, dep *domain.TaskDependency) error {
	// Validate not self-referencing.
	if dep.TaskID == dep.DependsOnTaskID {
		return apierror.BadRequest("a task cannot depend on itself")
	}

	// Validate both tasks exist.
	task, err := s.taskRepo.GetByID(ctx, dep.TaskID)
	if err != nil {
		return err
	}
	if task == nil {
		return apierror.NotFound("Task")
	}

	depTask, err := s.taskRepo.GetByID(ctx, dep.DependsOnTaskID)
	if err != nil {
		return err
	}
	if depTask == nil {
		return apierror.NotFound("Task")
	}

	// Check for duplicate.
	exists, err := s.depRepo.Exists(ctx, dep.TaskID, dep.DependsOnTaskID)
	if err != nil {
		return err
	}
	if exists {
		return apierror.Conflict("dependency already exists")
	}

	// Check for cycle: if we add taskID -> dependsOnTaskID, then from
	// dependsOnTaskID we should not be able to reach taskID.
	hasCycle, err := s.CheckCycle(ctx, dep.TaskID, dep.DependsOnTaskID)
	if err != nil {
		return err
	}
	if hasCycle {
		return apierror.BadRequest("adding this dependency would create a cycle")
	}

	if dep.ID == uuid.Nil {
		dep.ID = uuid.New()
	}
	dep.CreatedAt = timeNow()

	return s.depRepo.Create(ctx, dep)
}

// Delete removes a task dependency.
func (s *taskDependencyService) Delete(ctx context.Context, id uuid.UUID) error {
	return s.depRepo.Delete(ctx, id)
}

// ListByTask returns all dependencies for the given task.
func (s *taskDependencyService) ListByTask(ctx context.Context, taskID uuid.UUID) ([]domain.TaskDependency, error) {
	return s.depRepo.ListByTask(ctx, taskID)
}

// CheckCycle implements DFS cycle detection.
// It checks whether adding an edge taskID -> dependsOnTaskID would create a cycle.
// A cycle exists if, starting from dependsOnTaskID and following existing "depends on"
// edges, we can reach taskID.
func (s *taskDependencyService) CheckCycle(ctx context.Context, taskID, dependsOnTaskID uuid.UUID) (bool, error) {
	visited := make(map[uuid.UUID]bool)
	return s.dfs(ctx, dependsOnTaskID, taskID, visited)
}

// dfs traverses dependencies from current looking for target.
// Returns true if target is reachable from current.
func (s *taskDependencyService) dfs(ctx context.Context, current, target uuid.UUID, visited map[uuid.UUID]bool) (bool, error) {
	if current == target {
		return true, nil
	}
	if visited[current] {
		return false, nil
	}
	visited[current] = true

	deps, err := s.depRepo.ListByTask(ctx, current)
	if err != nil {
		return false, err
	}

	for _, dep := range deps {
		found, err := s.dfs(ctx, dep.DependsOnTaskID, target, visited)
		if err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
	}

	return false, nil
}
