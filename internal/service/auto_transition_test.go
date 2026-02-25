package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ---------------------------------------------------------------------------
// Auto-transition rule tests.
//
// Contract for Phase 5-OSS auto-transitions:
//   - When all subtasks of a parent task reach the "done" category,
//     the parent task automatically moves to a configured target status.
//   - When a blocking dependency is resolved (moved to "done"),
//     the dependent task becomes unblocked (moved out of "blocked" status).
//   - No transition occurs when some subtasks are still in progress.
//   - No transition occurs when no auto-transition rule is configured.
//   - Rules are scoped per-project and stored in a dedicated table.
//
// Implementation note:
//   The auto-transition logic will be called:
//   a) After every TaskService.MoveTask() call.
//   b) By a background worker polling for rule triggers.
//
// Tests that require implementation are marked t.Skip().
// Tests validating domain logic that can run now are fully implemented.
// ---------------------------------------------------------------------------

// AutoTransitionTrigger defines what event activates the rule.
type AutoTransitionTrigger string

const (
	// TriggerAllSubtasksDone fires when all direct subtasks reach the "done" category.
	TriggerAllSubtasksDone AutoTransitionTrigger = "all_subtasks_done"
	// TriggerBlockingDepResolved fires when a blocking dependency moves to "done".
	TriggerBlockingDepResolved AutoTransitionTrigger = "blocking_dependency_resolved"
)

// AutoTransitionRule defines a project-level rule for automatic task transitions.
type AutoTransitionRule struct {
	ID              uuid.UUID             `json:"id"`
	ProjectID       uuid.UUID             `json:"project_id"`
	Trigger         AutoTransitionTrigger `json:"trigger"`
	TargetStatusID  uuid.UUID             `json:"target_status_id"`
	IsEnabled       bool                  `json:"is_enabled"`
}

// ---------------------------------------------------------------------------
// AutoTransitionService interface (to be added to service/interfaces.go)
// ---------------------------------------------------------------------------

type AutoTransitionService interface {
	// EvaluateOnTaskMove checks and applies any auto-transition rules triggered
	// by a task being moved to a new status.
	EvaluateOnTaskMove(ctx context.Context, taskID uuid.UUID, newStatusCategory domain.StatusCategory) error
	// ListRules returns all auto-transition rules for a project.
	ListRules(ctx context.Context, projectID uuid.UUID) ([]AutoTransitionRule, error)
	// CreateRule creates a new auto-transition rule.
	CreateRule(ctx context.Context, rule *AutoTransitionRule) error
	// DeleteRule removes an auto-transition rule.
	DeleteRule(ctx context.Context, ruleID uuid.UUID) error
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestAutoTransition_AllSubtasksDone_TriggersParentMove(t *testing.T) {
	t.Skip("TODO: implement AutoTransitionService in internal/service/auto_transition_service.go")
	// Setup:
	//   - Parent task P in status "in_progress".
	//   - Project has rule: TriggerAllSubtasksDone → target status "done".
	//   - Subtask A is already "done".
	//   - Subtask B is "in_progress".
	// When:
	//   - Subtask B is moved to "done" (MoveTask called).
	// Then:
	//   - AutoTransitionService.EvaluateOnTaskMove is triggered.
	//   - Parent task P is moved to the configured target "done" status.
}

func TestAutoTransition_NotAllSubtasksDone_NoTransition(t *testing.T) {
	t.Skip("TODO: implement AutoTransitionService in internal/service/auto_transition_service.go")
	// Setup:
	//   - Parent task P in status "in_progress".
	//   - Project has rule: TriggerAllSubtasksDone → target "done".
	//   - Subtask A is "done", Subtask B is "in_progress", Subtask C is "todo".
	// When:
	//   - Subtask A is moved to "done" (already done, but A was previously "in_progress").
	// Then:
	//   - Parent task P is NOT moved (B and C are still not done).
}

func TestAutoTransition_NoRuleConfigured_NoTransition(t *testing.T) {
	t.Skip("TODO: implement AutoTransitionService in internal/service/auto_transition_service.go")
	// Setup:
	//   - Parent task P with subtasks all in "done".
	//   - Project has NO auto-transition rules.
	// When:
	//   - Last subtask is moved to "done".
	// Then:
	//   - Parent task P status is unchanged.
}

func TestAutoTransition_BlockingDepResolved_UnblocksDependent(t *testing.T) {
	t.Skip("TODO: implement AutoTransitionService in internal/service/auto_transition_service.go")
	// Setup:
	//   - Task B has dependency: B is_blocked_by A (DependencyTypeBlocks).
	//   - Task B is in status "blocked" (a custom status with category "blocked").
	//   - Project has rule: TriggerBlockingDepResolved → target status "todo".
	// When:
	//   - Task A is moved to "done".
	// Then:
	//   - AutoTransitionService detects that A's dependency is resolved.
	//   - Task B is moved to status "todo" (or the configured target).
}

func TestAutoTransition_DisabledRule_NoTransition(t *testing.T) {
	t.Skip("TODO: implement AutoTransitionService in internal/service/auto_transition_service.go")
	// Setup:
	//   - Rule exists but IsEnabled = false.
	// When:
	//   - Trigger condition is met.
	// Then:
	//   - No auto-transition occurs.
}

func TestAutoTransition_TaskWithNoSubtasks_NoTransition(t *testing.T) {
	t.Skip("TODO: implement AutoTransitionService in internal/service/auto_transition_service.go")
	// Setup:
	//   - Task P has no subtasks.
	//   - Rule: TriggerAllSubtasksDone configured.
	// When:
	//   - Task P itself is moved.
	// Then:
	//   - No recursive transition (task is its own leaf, rule does not apply).
}

// ---------------------------------------------------------------------------
// Tests: AutoTransitionRule domain logic (run NOW — no service needed)
// ---------------------------------------------------------------------------

func TestAutoTransitionRule_DomainLogic(t *testing.T) {
	t.Run("rule with valid trigger and target is valid", func(t *testing.T) {
		rule := AutoTransitionRule{
			ID:             uuid.New(),
			ProjectID:      uuid.New(),
			Trigger:        TriggerAllSubtasksDone,
			TargetStatusID: uuid.New(),
			IsEnabled:      true,
		}
		assert.Equal(t, TriggerAllSubtasksDone, rule.Trigger)
		assert.True(t, rule.IsEnabled)
		assert.NotEqual(t, uuid.Nil, rule.ID)
		assert.NotEqual(t, uuid.Nil, rule.ProjectID)
		assert.NotEqual(t, uuid.Nil, rule.TargetStatusID)
	})

	t.Run("disabled rule has IsEnabled=false", func(t *testing.T) {
		rule := AutoTransitionRule{
			ID:             uuid.New(),
			ProjectID:      uuid.New(),
			Trigger:        TriggerBlockingDepResolved,
			TargetStatusID: uuid.New(),
			IsEnabled:      false,
		}
		assert.False(t, rule.IsEnabled)
	})
}

// ---------------------------------------------------------------------------
// Tests: Subtask completion check — pure logic (runs NOW)
// ---------------------------------------------------------------------------

// allSubtasksDone is the pure function that the auto-transition service will use.
// It checks whether every subtask in the list has a "done" category status.
func allSubtasksDone(subtasks []domain.Task, statusCategoryByID map[uuid.UUID]domain.StatusCategory) bool {
	if len(subtasks) == 0 {
		return false // no subtasks → rule does not apply
	}
	for _, st := range subtasks {
		cat, ok := statusCategoryByID[st.StatusID]
		if !ok || cat != domain.StatusCategoryDone {
			return false
		}
	}
	return true
}

func TestAllSubtasksDone_Logic(t *testing.T) {
	doneStatusID := uuid.New()
	inProgressStatusID := uuid.New()
	todoStatusID := uuid.New()

	categoryMap := map[uuid.UUID]domain.StatusCategory{
		doneStatusID:       domain.StatusCategoryDone,
		inProgressStatusID: domain.StatusCategoryInProgress,
		todoStatusID:       domain.StatusCategoryTodo,
	}

	t.Run("all done returns true", func(t *testing.T) {
		subtasks := []domain.Task{
			{ID: uuid.New(), StatusID: doneStatusID},
			{ID: uuid.New(), StatusID: doneStatusID},
			{ID: uuid.New(), StatusID: doneStatusID},
		}
		assert.True(t, allSubtasksDone(subtasks, categoryMap))
	})

	t.Run("some in_progress returns false", func(t *testing.T) {
		subtasks := []domain.Task{
			{ID: uuid.New(), StatusID: doneStatusID},
			{ID: uuid.New(), StatusID: inProgressStatusID},
		}
		assert.False(t, allSubtasksDone(subtasks, categoryMap))
	})

	t.Run("all todo returns false", func(t *testing.T) {
		subtasks := []domain.Task{
			{ID: uuid.New(), StatusID: todoStatusID},
		}
		assert.False(t, allSubtasksDone(subtasks, categoryMap))
	})

	t.Run("empty subtask list returns false (no trigger)", func(t *testing.T) {
		assert.False(t, allSubtasksDone([]domain.Task{}, categoryMap))
	})

	t.Run("single done subtask returns true", func(t *testing.T) {
		subtasks := []domain.Task{
			{ID: uuid.New(), StatusID: doneStatusID},
		}
		assert.True(t, allSubtasksDone(subtasks, categoryMap))
	})
}

// ---------------------------------------------------------------------------
// Tests: hasUnresolvedBlockingDeps — pure logic (runs NOW)
// ---------------------------------------------------------------------------

// hasUnresolvedBlockingDeps checks whether a task still has unresolved "blocks" dependencies.
func hasUnresolvedBlockingDeps(deps []domain.TaskDependency, statusCategoryByTaskID map[uuid.UUID]domain.StatusCategory) bool {
	for _, dep := range deps {
		if dep.DependencyType != domain.DependencyTypeBlocks {
			continue
		}
		cat, ok := statusCategoryByTaskID[dep.DependsOnTaskID]
		if !ok || cat != domain.StatusCategoryDone {
			return true // blocking task is not done yet
		}
	}
	return false
}

func TestHasUnresolvedBlockingDeps_Logic(t *testing.T) {
	blockerDoneID := uuid.New()
	blockerInProgressID := uuid.New()

	categoryMap := map[uuid.UUID]domain.StatusCategory{
		blockerDoneID:       domain.StatusCategoryDone,
		blockerInProgressID: domain.StatusCategoryInProgress,
	}

	t.Run("all blockers done returns false (no unresolved deps)", func(t *testing.T) {
		deps := []domain.TaskDependency{
			{ID: uuid.New(), DependsOnTaskID: blockerDoneID, DependencyType: domain.DependencyTypeBlocks},
		}
		assert.False(t, hasUnresolvedBlockingDeps(deps, categoryMap))
	})

	t.Run("one blocker still in_progress returns true", func(t *testing.T) {
		deps := []domain.TaskDependency{
			{ID: uuid.New(), DependsOnTaskID: blockerDoneID, DependencyType: domain.DependencyTypeBlocks},
			{ID: uuid.New(), DependsOnTaskID: blockerInProgressID, DependencyType: domain.DependencyTypeBlocks},
		}
		assert.True(t, hasUnresolvedBlockingDeps(deps, categoryMap))
	})

	t.Run("relates_to dep does not block", func(t *testing.T) {
		deps := []domain.TaskDependency{
			{ID: uuid.New(), DependsOnTaskID: blockerInProgressID, DependencyType: domain.DependencyTypeRelatesTo},
		}
		// relates_to never blocks, even if not done.
		assert.False(t, hasUnresolvedBlockingDeps(deps, categoryMap))
	})

	t.Run("no deps returns false", func(t *testing.T) {
		assert.False(t, hasUnresolvedBlockingDeps([]domain.TaskDependency{}, categoryMap))
	})
}

// ---------------------------------------------------------------------------
// Tests: Integration sketch using existing mock repos (runs NOW)
// ---------------------------------------------------------------------------

func TestAutoTransition_WithExistingMocks_SubtaskStatusCheck(t *testing.T) {
	// This test validates that the existing mock repos support the data queries
	// needed for auto-transition. No new service needed.
	ctx := context.Background()
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()

	projectID := uuid.New()
	doneStatusID := uuid.New()
	todoStatusID := uuid.New()

	// Seed statuses.
	statusRepo.items[doneStatusID] = &domain.TaskStatus{
		ID:        doneStatusID,
		ProjectID: projectID,
		Category:  domain.StatusCategoryDone,
		Name:      "Done",
	}
	statusRepo.items[todoStatusID] = &domain.TaskStatus{
		ID:        todoStatusID,
		ProjectID: projectID,
		Category:  domain.StatusCategoryTodo,
		Name:      "To Do",
	}

	// Create parent task.
	parentID := uuid.New()
	taskRepo.items[parentID] = &domain.Task{
		ID:        parentID,
		ProjectID: projectID,
		StatusID:  todoStatusID,
		Title:     "Parent Task",
	}

	// Create subtasks — all done.
	for i := 0; i < 3; i++ {
		childID := uuid.New()
		taskRepo.items[childID] = &domain.Task{
			ID:           childID,
			ProjectID:    projectID,
			StatusID:     doneStatusID,
			Title:        "Subtask",
			ParentTaskID: &parentID,
		}
	}

	// Retrieve subtasks using the mock repo.
	subtasks, err := taskRepo.ListSubtasks(ctx, parentID)
	require.NoError(t, err)
	assert.Len(t, subtasks, 3, "should have 3 subtasks")

	// Build category map from status repo.
	statusByID := map[uuid.UUID]domain.StatusCategory{}
	for id, s := range statusRepo.items {
		statusByID[id] = s.Category
	}

	// All subtasks should be "done".
	result := allSubtasksDone(subtasks, statusByID)
	assert.True(t, result, "all subtasks are done — auto-transition should fire")

	// Now make one subtask not done.
	for id, t2 := range taskRepo.items {
		if t2.ParentTaskID != nil && *t2.ParentTaskID == parentID {
			taskRepo.items[id].StatusID = todoStatusID
			break
		}
	}

	subtasks2, err := taskRepo.ListSubtasks(ctx, parentID)
	require.NoError(t, err)
	result2 := allSubtasksDone(subtasks2, statusByID)
	assert.False(t, result2, "not all subtasks done — no auto-transition should fire")
}
