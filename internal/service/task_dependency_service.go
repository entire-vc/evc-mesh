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

	// Both ends of the edge have to be in the same project.
	//
	// depends_on_task_id arrives in the request body, where no route parameter
	// names it and the workspace guard therefore cannot see it. Until this check
	// existed, "both tasks exist" was the whole of the validation: a member of any
	// workspace could point one of their own tasks at a stranger's task id and get
	// 201, which both wrote an edge across the tenant boundary and — by answering
	// 404 for an id that does not exist and 201 for one that does — turned the
	// endpoint into an oracle for enumerating other tenants' task ids.
	if depTask.ProjectID != task.ProjectID {
		return apierror.BadRequest("depends_on_task_id must be a task in the same project")
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

	// is_child_of is the only dependency type that also drives the actual
	// parent/subtask hierarchy (parent_task_id), which is what feeds the
	// Subtasks tab and subtask_count. Without this, selecting "Child of" in
	// the Dependencies tab recorded an edge nothing else read.
	if dep.DependencyType == domain.DependencyTypeIsChildOf {
		if task.ParentTaskID != nil && *task.ParentTaskID != dep.DependsOnTaskID {
			return apierror.Conflict("task already has a parent; remove the existing parent relationship first")
		}
		hasParentCycle, err := s.hasParentCycle(ctx, dep.TaskID, dep.DependsOnTaskID)
		if err != nil {
			return err
		}
		if hasParentCycle {
			return apierror.BadRequest("adding this dependency would create a cycle in the task hierarchy")
		}
	}

	if dep.ID == uuid.Nil {
		dep.ID = uuid.New()
	}
	dep.CreatedAt = timeNow()

	if err := s.depRepo.Create(ctx, dep); err != nil {
		return err
	}

	if dep.DependencyType == domain.DependencyTypeIsChildOf {
		parentID := dep.DependsOnTaskID
		task.ParentTaskID = &parentID
		if err := s.taskRepo.Update(ctx, task); err != nil {
			return err
		}
	}

	return nil
}

// hasParentCycle reports whether making newParentID the parent of taskID
// would create a cycle in the parent_task_id tree, i.e. whether taskID is
// already an ancestor of newParentID.
func (s *taskDependencyService) hasParentCycle(ctx context.Context, taskID, newParentID uuid.UUID) (bool, error) {
	current := newParentID
	visited := make(map[uuid.UUID]bool)
	for {
		if current == taskID {
			return true, nil
		}
		if visited[current] {
			return false, nil
		}
		visited[current] = true

		t, err := s.taskRepo.GetByID(ctx, current)
		if err != nil {
			return false, err
		}
		if t == nil || t.ParentTaskID == nil {
			return false, nil
		}
		current = *t.ParentTaskID
	}
}

// Delete removes a task dependency.
//
// Removing an is_child_of edge also clears the parent_task_id it set. Without
// this, Create's side effect is one-way and the pair traps the user: the edge
// is the only UI that can set a parent, and Create refuses a second one
// ("remove the existing parent relationship first") — so once the edge is gone
// the task is parented permanently. PATCH cannot help either; parent_task_id is
// accepted on create but not on updateTaskRequest.
func (s *taskDependencyService) Delete(ctx context.Context, id uuid.UUID) error {
	// Read the edge before deleting it — afterwards its type and endpoints are gone.
	// A read failure must not block the delete: the edge removal is what the caller
	// asked for, and leaving it in place because we could not check its type would
	// be the worse failure. Treat unknown as "nothing to undo".
	dep, getErr := s.depRepo.GetByID(ctx, id)
	if getErr != nil {
		dep = nil
	}

	if err := s.depRepo.Delete(ctx, id); err != nil {
		return err
	}

	if dep == nil || dep.DependencyType != domain.DependencyTypeIsChildOf {
		return nil
	}

	task, err := s.taskRepo.GetByID(ctx, dep.TaskID)
	if err != nil {
		return err
	}
	// Only clear a parent this edge is actually responsible for. If the task has
	// since been re-parented elsewhere, deleting this stale edge must not detach it.
	if task == nil || task.ParentTaskID == nil || *task.ParentTaskID != dep.DependsOnTaskID {
		return nil
	}
	task.ParentTaskID = nil
	return s.taskRepo.Update(ctx, task)
}

// ListByTask returns all dependencies for the given task.
func (s *taskDependencyService) ListByTask(ctx context.Context, taskID uuid.UUID) ([]domain.TaskDependency, error) {
	return s.depRepo.ListByTask(ctx, taskID)
}

// ListByTaskBothDirections implements TaskDependencyService.
func (s *taskDependencyService) ListByTaskBothDirections(ctx context.Context, taskID uuid.UUID) (outgoing, incoming []domain.EnrichedTaskDependency, err error) {
	outDeps, err := s.depRepo.ListByTask(ctx, taskID)
	if err != nil {
		return nil, nil, err
	}
	inDeps, err := s.depRepo.ListDependents(ctx, taskID)
	if err != nil {
		return nil, nil, err
	}

	// One cache shared across both directions: a small dependency set commonly
	// repeats the same related task (e.g. several is_child_of edges all pointing
	// at the same parent), and this keeps it to one GetByID per distinct task.
	cache := make(map[uuid.UUID]*domain.Task)
	get := func(id uuid.UUID) (*domain.Task, error) {
		if t, ok := cache[id]; ok {
			return t, nil
		}
		t, err := s.taskRepo.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		cache[id] = t
		return t, nil
	}

	outgoing, err = enrichDependencies(outDeps, func(d domain.TaskDependency) uuid.UUID { return d.DependsOnTaskID }, get)
	if err != nil {
		return nil, nil, err
	}
	incoming, err = enrichDependencies(inDeps, func(d domain.TaskDependency) uuid.UUID { return d.TaskID }, get)
	if err != nil {
		return nil, nil, err
	}
	return outgoing, incoming, nil
}

// enrichDependencies attaches the related task's title/status to each dependency,
// where relatedID picks the OTHER task in the edge (the one that is not the task
// the list was fetched for).
func enrichDependencies(
	deps []domain.TaskDependency,
	relatedID func(domain.TaskDependency) uuid.UUID,
	get func(uuid.UUID) (*domain.Task, error),
) ([]domain.EnrichedTaskDependency, error) {
	out := make([]domain.EnrichedTaskDependency, len(deps))
	for i, d := range deps {
		out[i] = domain.EnrichedTaskDependency{TaskDependency: d}
		t, err := get(relatedID(d))
		if err != nil {
			return nil, err
		}
		if t != nil {
			title := t.Title
			status := t.StatusID
			out[i].RelatedTaskTitle = &title
			out[i].RelatedTaskStatusID = &status
		}
	}
	return out, nil
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
