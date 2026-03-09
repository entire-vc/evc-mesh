package service

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// AutoTransitionService checks and applies automatic status transitions.
type AutoTransitionService interface {
	// EvaluateOnTaskMove checks and applies any auto-transition rules triggered
	// by a task being moved to a new status category.
	EvaluateOnTaskMove(ctx context.Context, taskID uuid.UUID, newStatusCategory domain.StatusCategory) error
	// CheckSubtaskCompletion checks if all subtasks of a parent task are done.
	// If so, moves the parent to the first "review" status (or "done" if no review exists).
	CheckSubtaskCompletion(ctx context.Context, parentTaskID uuid.UUID) error
	// CheckDependencyResolution checks if all blocking dependencies of dependent tasks
	// are resolved and moves them from "backlog" to "todo" accordingly.
	CheckDependencyResolution(ctx context.Context, resolvedTaskID uuid.UUID) error
	// ListRules returns all auto-transition rules for a project.
	ListRules(ctx context.Context, projectID uuid.UUID) ([]domain.AutoTransitionRule, error)
	// CreateRule creates a new auto-transition rule.
	CreateRule(ctx context.Context, rule *domain.AutoTransitionRule) error
	// UpdateRule updates an existing auto-transition rule.
	UpdateRule(ctx context.Context, rule *domain.AutoTransitionRule) error
	// DeleteRule removes an auto-transition rule.
	DeleteRule(ctx context.Context, ruleID uuid.UUID) error
}

// autoTransitionService implements AutoTransitionService.
type autoTransitionService struct {
	taskRepo   repository.TaskRepository
	statusRepo repository.TaskStatusRepository
	depRepo    repository.TaskDependencyRepository
	taskSvc    TaskService
	ruleRepo   repository.AutoTransitionRuleRepository
}

// NewAutoTransitionService creates a new AutoTransitionService.
// ruleRepo may be nil for backwards compatibility (falls back to hardcoded category lookup).
func NewAutoTransitionService(
	taskRepo repository.TaskRepository,
	statusRepo repository.TaskStatusRepository,
	depRepo repository.TaskDependencyRepository,
	taskSvc TaskService,
	ruleRepo repository.AutoTransitionRuleRepository,
) AutoTransitionService {
	return &autoTransitionService{
		taskRepo:   taskRepo,
		statusRepo: statusRepo,
		depRepo:    depRepo,
		taskSvc:    taskSvc,
		ruleRepo:   ruleRepo,
	}
}

// EvaluateOnTaskMove is the main entry point called after a task status change.
// It checks all relevant auto-transition conditions based on the new status category.
func (s *autoTransitionService) EvaluateOnTaskMove(ctx context.Context, taskID uuid.UUID, newStatusCategory domain.StatusCategory) error {
	// When a task is moved to "done" or "cancelled":
	// 1. Check if its parent should be transitioned (all siblings done).
	// 2. Check if tasks that depend on this task can be unblocked.
	if newStatusCategory == domain.StatusCategoryDone || newStatusCategory == domain.StatusCategoryCancelled {
		task, err := s.taskRepo.GetByID(ctx, taskID)
		if err != nil {
			return err
		}
		if task == nil {
			return nil
		}

		// Check parent subtask completion.
		if task.ParentTaskID != nil {
			if err := s.CheckSubtaskCompletion(ctx, *task.ParentTaskID); err != nil {
				log.Printf("[auto-transition] WARNING: CheckSubtaskCompletion for parent %s failed: %v", *task.ParentTaskID, err)
			}
		}

		// Check if tasks that depend on this task can now be unblocked.
		if err := s.CheckDependencyResolution(ctx, taskID); err != nil {
			log.Printf("[auto-transition] WARNING: CheckDependencyResolution for task %s failed: %v", taskID, err)
		}
	}
	return nil
}

// CheckSubtaskCompletion checks if all subtasks of a parent task are done/cancelled.
// If so, and the parent is in "in_progress" category, it moves the parent to "review"
// (or "done" if no "review" status exists in the project).
func (s *autoTransitionService) CheckSubtaskCompletion(ctx context.Context, parentTaskID uuid.UUID) error {
	// 1. Get the parent task.
	parent, err := s.taskRepo.GetByID(ctx, parentTaskID)
	if err != nil {
		return err
	}
	if parent == nil {
		return nil
	}

	// 2. Get the parent's current status category.
	parentStatus, err := s.statusRepo.GetByID(ctx, parent.StatusID)
	if err != nil {
		return err
	}
	if parentStatus == nil {
		return nil
	}

	// Only transition parents that are currently "in_progress".
	if parentStatus.Category != domain.StatusCategoryInProgress {
		return nil
	}

	// 3. Get all subtasks.
	subtasks, err := s.taskRepo.ListSubtasks(ctx, parentTaskID)
	if err != nil {
		return err
	}
	if len(subtasks) == 0 {
		return nil // no subtasks — rule does not apply
	}

	// 4. Build a category map for statuses we encounter.
	categoryByStatusID := make(map[uuid.UUID]domain.StatusCategory)
	for _, sub := range subtasks {
		if _, seen := categoryByStatusID[sub.StatusID]; !seen {
			st, err := s.statusRepo.GetByID(ctx, sub.StatusID)
			if err != nil {
				return err
			}
			if st != nil {
				categoryByStatusID[sub.StatusID] = st.Category
			}
		}
	}

	// 5. Check if all subtasks are done or cancelled.
	if !allSubtasksTerminal(subtasks, categoryByStatusID) {
		return nil
	}

	// 6. Check if a configured rule overrides the target status.
	targetStatusID, err := s.resolveTargetFromRule(ctx, parent.ProjectID, domain.TriggerAllSubtasksDone)
	if err != nil {
		return err
	}

	// Fallback: prefer "review", fall back to "done".
	if targetStatusID == uuid.Nil {
		targetStatusID, err = s.findTargetStatus(ctx, parent.ProjectID, domain.StatusCategoryReview, domain.StatusCategoryDone)
		if err != nil {
			return err
		}
	}
	if targetStatusID == uuid.Nil {
		return nil // no suitable target status found
	}

	log.Printf("[auto-transition] Moving parent task %s to review/done because all subtasks are complete", parentTaskID)
	return s.taskSvc.MoveTask(ctx, parentTaskID, MoveTaskInput{StatusID: &targetStatusID})
}

// CheckDependencyResolution checks if tasks that depend on resolvedTaskID can now be
// unblocked. For each dependent task: if ALL its blocking dependencies are now done,
// and the dependent task is in "backlog" category, it moves it to "todo".
func (s *autoTransitionService) CheckDependencyResolution(ctx context.Context, resolvedTaskID uuid.UUID) error {
	// 1. Get all tasks that depend ON this task (reverse lookup).
	dependents, err := s.depRepo.ListDependents(ctx, resolvedTaskID)
	if err != nil {
		return err
	}

	for _, dep := range dependents {
		// Only handle "blocks" dependency type.
		if dep.DependencyType != domain.DependencyTypeBlocks {
			continue
		}

		if err := s.tryUnblockTask(ctx, dep.TaskID); err != nil {
			log.Printf("[auto-transition] WARNING: tryUnblockTask for task %s failed: %v", dep.TaskID, err)
		}
	}
	return nil
}

// tryUnblockTask checks if a specific task can be moved from "backlog" to "todo".
func (s *autoTransitionService) tryUnblockTask(ctx context.Context, taskID uuid.UUID) error {
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return nil
	}

	// Only unblock tasks that are in "backlog".
	currentStatus, err := s.statusRepo.GetByID(ctx, task.StatusID)
	if err != nil {
		return err
	}
	if currentStatus == nil || currentStatus.Category != domain.StatusCategoryBacklog {
		return nil
	}

	// Check if ALL blocking dependencies are now done.
	allDeps, err := s.depRepo.ListByTask(ctx, taskID)
	if err != nil {
		return err
	}

	// Build category map for blocker tasks.
	categoryByTaskID := make(map[uuid.UUID]domain.StatusCategory)
	for _, dep := range allDeps {
		if dep.DependencyType != domain.DependencyTypeBlocks {
			continue
		}
		if _, seen := categoryByTaskID[dep.DependsOnTaskID]; !seen {
			blocker, err := s.taskRepo.GetByID(ctx, dep.DependsOnTaskID)
			if err != nil {
				return err
			}
			if blocker == nil {
				continue
			}
			blockerStatus, err := s.statusRepo.GetByID(ctx, blocker.StatusID)
			if err != nil {
				return err
			}
			if blockerStatus != nil {
				categoryByTaskID[dep.DependsOnTaskID] = blockerStatus.Category
			}
		}
	}

	if hasUnresolvedBlockers(allDeps, categoryByTaskID) {
		return nil // still blocked
	}

	// Check if a configured rule overrides the target status.
	targetStatusID, err := s.resolveTargetFromRule(ctx, task.ProjectID, domain.TriggerBlockingDepResolved)
	if err != nil {
		return err
	}

	// Fallback: move to "todo".
	if targetStatusID == uuid.Nil {
		targetStatusID, err = s.findTargetStatus(ctx, task.ProjectID, domain.StatusCategoryTodo)
		if err != nil {
			return err
		}
	}
	if targetStatusID == uuid.Nil {
		return nil
	}

	log.Printf("[auto-transition] Unblocking task %s (all blocking deps resolved) → moving to todo", taskID)
	return s.taskSvc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &targetStatusID})
}

// resolveTargetFromRule looks up a configured, enabled rule for the given trigger and
// returns its target_status_id. Returns uuid.Nil if ruleRepo is nil or no matching
// enabled rule exists.
func (s *autoTransitionService) resolveTargetFromRule(ctx context.Context, projectID uuid.UUID, trigger domain.AutoTransitionTrigger) (uuid.UUID, error) {
	if s.ruleRepo == nil {
		return uuid.Nil, nil
	}
	rules, err := s.ruleRepo.List(ctx, projectID)
	if err != nil {
		return uuid.Nil, err
	}
	for _, r := range rules {
		if r.Trigger == trigger && r.IsEnabled {
			return r.TargetStatusID, nil
		}
	}
	return uuid.Nil, nil
}

// findTargetStatus returns the first status in a project matching any of the given
// categories (in priority order). Returns uuid.Nil if none found.
func (s *autoTransitionService) findTargetStatus(ctx context.Context, projectID uuid.UUID, categories ...domain.StatusCategory) (uuid.UUID, error) {
	statuses, err := s.statusRepo.ListByProject(ctx, projectID)
	if err != nil {
		return uuid.Nil, err
	}

	// Build a map from category to first matching status ID.
	categoryToStatus := make(map[domain.StatusCategory]uuid.UUID)
	for _, st := range statuses {
		if _, exists := categoryToStatus[st.Category]; !exists {
			categoryToStatus[st.Category] = st.ID
		}
	}

	for _, cat := range categories {
		if id, ok := categoryToStatus[cat]; ok {
			return id, nil
		}
	}
	return uuid.Nil, nil
}

// allSubtasksTerminal returns true if every subtask has a "done" or "cancelled" category.
func allSubtasksTerminal(subtasks []domain.Task, categoryByStatusID map[uuid.UUID]domain.StatusCategory) bool {
	if len(subtasks) == 0 {
		return false
	}
	for _, sub := range subtasks {
		cat, ok := categoryByStatusID[sub.StatusID]
		if !ok {
			return false // unknown status — treat as not done
		}
		if cat != domain.StatusCategoryDone && cat != domain.StatusCategoryCancelled {
			return false
		}
	}
	return true
}

// hasUnresolvedBlockers returns true if any "blocks" dependency points to a task
// that is NOT in the "done" category.
func hasUnresolvedBlockers(deps []domain.TaskDependency, categoryByTaskID map[uuid.UUID]domain.StatusCategory) bool {
	for _, dep := range deps {
		if dep.DependencyType != domain.DependencyTypeBlocks {
			continue
		}
		cat, ok := categoryByTaskID[dep.DependsOnTaskID]
		if !ok || cat != domain.StatusCategoryDone {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Rule management — backed by AutoTransitionRuleRepository
// ---------------------------------------------------------------------------

// ListRules returns all auto-transition rules for a project.
func (s *autoTransitionService) ListRules(ctx context.Context, projectID uuid.UUID) ([]domain.AutoTransitionRule, error) {
	if s.ruleRepo == nil {
		return []domain.AutoTransitionRule{}, nil
	}
	return s.ruleRepo.List(ctx, projectID)
}

// CreateRule creates a new auto-transition rule.
func (s *autoTransitionService) CreateRule(ctx context.Context, rule *domain.AutoTransitionRule) error {
	if rule.ID == uuid.Nil {
		rule.ID = uuid.New()
	}
	now := time.Now()
	if rule.CreatedAt.IsZero() {
		rule.CreatedAt = now
	}
	if rule.UpdatedAt.IsZero() {
		rule.UpdatedAt = now
	}
	if s.ruleRepo == nil {
		return nil // no-op for backward compat
	}
	return s.ruleRepo.Create(ctx, rule)
}

// UpdateRule persists changes to an existing auto-transition rule.
func (s *autoTransitionService) UpdateRule(ctx context.Context, rule *domain.AutoTransitionRule) error {
	if s.ruleRepo == nil {
		return nil
	}
	return s.ruleRepo.Update(ctx, rule)
}

// DeleteRule removes an auto-transition rule.
func (s *autoTransitionService) DeleteRule(ctx context.Context, ruleID uuid.UUID) error {
	if s.ruleRepo == nil {
		return nil
	}
	return s.ruleRepo.Delete(ctx, ruleID)
}
