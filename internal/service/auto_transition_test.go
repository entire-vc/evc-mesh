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
// Auto-transition service tests.
//
// Contract:
//   - When ALL subtasks of a parent task reach "done" or "cancelled" category,
//     the parent task automatically moves to "review" (or "done" if no review).
//   - Only triggers if the parent is currently "in_progress".
//   - When a blocking dependency is resolved (moved to "done"),
//     dependent tasks in "backlog" are moved to "todo".
//   - No transition occurs when some subtasks are still in progress.
//   - No transition occurs when the parent is already in a terminal state.
// ---------------------------------------------------------------------------

// buildAutoTransitionFixture creates a fresh set of mocks and a wired AutoTransitionService
// along with a TaskService (so MoveTask actually writes to the mock task repo).
func buildAutoTransitionFixture() (
	AutoTransitionService,
	*MockTaskRepository,
	*MockTaskStatusRepository,
	*MockTaskDependencyRepository,
) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()

	// Create a real taskService so that MoveTask writes back to taskRepo.
	taskSvc := NewTaskService(taskRepo, statusRepo, depRepo, activityRepo)
	atSvc := NewAutoTransitionService(taskRepo, statusRepo, depRepo, taskSvc)

	return atSvc, taskRepo, statusRepo, depRepo
}

// seedStatus creates and stores a TaskStatus in the mock repo.
func seedStatus(repo *MockTaskStatusRepository, projectID uuid.UUID, category domain.StatusCategory, name string) *domain.TaskStatus {
	s := &domain.TaskStatus{
		ID:        uuid.New(),
		ProjectID: projectID,
		Category:  category,
		Name:      name,
	}
	repo.items[s.ID] = s
	return s
}

// seedTask creates and stores a Task in the mock repo.
func seedTask(repo *MockTaskRepository, projectID uuid.UUID, statusID uuid.UUID, parentID *uuid.UUID, title string) *domain.Task {
	t := &domain.Task{
		ID:           uuid.New(),
		ProjectID:    projectID,
		StatusID:     statusID,
		Title:        title,
		ParentTaskID: parentID,
		AssigneeType: domain.AssigneeTypeUnassigned,
	}
	repo.items[t.ID] = t
	return t
}

// ---------------------------------------------------------------------------
// CheckSubtaskCompletion tests
// ---------------------------------------------------------------------------

func TestAutoTransition_AllSubtasksDone_TriggersParentMove(t *testing.T) {
	ctx := context.Background()
	svc, taskRepo, statusRepo, _ := buildAutoTransitionFixture()

	projectID := uuid.New()
	inProgressStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryInProgress, "In Progress")
	doneStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryDone, "Done")
	reviewStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryReview, "Review")

	// Parent task in "in_progress".
	parent := seedTask(taskRepo, projectID, inProgressStatus.ID, nil, "Parent Task")

	// Three subtasks all in "done".
	seedTask(taskRepo, projectID, doneStatus.ID, &parent.ID, "Subtask A")
	seedTask(taskRepo, projectID, doneStatus.ID, &parent.ID, "Subtask B")
	seedTask(taskRepo, projectID, doneStatus.ID, &parent.ID, "Subtask C")

	err := svc.CheckSubtaskCompletion(ctx, parent.ID)
	require.NoError(t, err)

	// Parent should have moved to "review".
	updated := taskRepo.items[parent.ID]
	require.NotNil(t, updated)
	assert.Equal(t, reviewStatus.ID, updated.StatusID, "parent should move to review when all subtasks are done")
}

func TestAutoTransition_AllSubtasksDone_FallsToDoneWhenNoReview(t *testing.T) {
	ctx := context.Background()
	svc, taskRepo, statusRepo, _ := buildAutoTransitionFixture()

	projectID := uuid.New()
	inProgressStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryInProgress, "In Progress")
	doneStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryDone, "Done")
	// No "review" status in this project.

	parent := seedTask(taskRepo, projectID, inProgressStatus.ID, nil, "Parent Task")
	seedTask(taskRepo, projectID, doneStatus.ID, &parent.ID, "Subtask A")

	err := svc.CheckSubtaskCompletion(ctx, parent.ID)
	require.NoError(t, err)

	updated := taskRepo.items[parent.ID]
	assert.Equal(t, doneStatus.ID, updated.StatusID, "should fall back to done when no review status exists")
}

func TestAutoTransition_NotAllSubtasksDone_NoTransition(t *testing.T) {
	ctx := context.Background()
	svc, taskRepo, statusRepo, _ := buildAutoTransitionFixture()

	projectID := uuid.New()
	inProgressStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryInProgress, "In Progress")
	doneStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryDone, "Done")
	todoStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryTodo, "To Do")
	seedStatus(statusRepo, projectID, domain.StatusCategoryReview, "Review")

	parent := seedTask(taskRepo, projectID, inProgressStatus.ID, nil, "Parent Task")
	seedTask(taskRepo, projectID, doneStatus.ID, &parent.ID, "Subtask A")
	seedTask(taskRepo, projectID, todoStatus.ID, &parent.ID, "Subtask B") // not done

	originalStatusID := parent.StatusID

	err := svc.CheckSubtaskCompletion(ctx, parent.ID)
	require.NoError(t, err)

	updated := taskRepo.items[parent.ID]
	assert.Equal(t, originalStatusID, updated.StatusID, "parent should NOT move when some subtasks are still pending")
}

func TestAutoTransition_CancelledSubtasksCountAsDone(t *testing.T) {
	ctx := context.Background()
	svc, taskRepo, statusRepo, _ := buildAutoTransitionFixture()

	projectID := uuid.New()
	inProgressStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryInProgress, "In Progress")
	doneStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryDone, "Done")
	cancelledStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryCancelled, "Cancelled")
	reviewStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryReview, "Review")

	parent := seedTask(taskRepo, projectID, inProgressStatus.ID, nil, "Parent Task")
	seedTask(taskRepo, projectID, doneStatus.ID, &parent.ID, "Subtask A")
	seedTask(taskRepo, projectID, cancelledStatus.ID, &parent.ID, "Subtask B") // cancelled counts as terminal

	err := svc.CheckSubtaskCompletion(ctx, parent.ID)
	require.NoError(t, err)

	updated := taskRepo.items[parent.ID]
	assert.Equal(t, reviewStatus.ID, updated.StatusID, "cancelled subtasks should count as terminal for auto-transition")
}

func TestAutoTransition_ParentAlreadyDone_NoTransition(t *testing.T) {
	ctx := context.Background()
	svc, taskRepo, statusRepo, _ := buildAutoTransitionFixture()

	projectID := uuid.New()
	doneStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryDone, "Done")
	seedStatus(statusRepo, projectID, domain.StatusCategoryReview, "Review")

	// Parent already in "done" — should not be touched.
	parent := seedTask(taskRepo, projectID, doneStatus.ID, nil, "Parent Task")
	seedTask(taskRepo, projectID, doneStatus.ID, &parent.ID, "Subtask A")

	err := svc.CheckSubtaskCompletion(ctx, parent.ID)
	require.NoError(t, err)

	updated := taskRepo.items[parent.ID]
	assert.Equal(t, doneStatus.ID, updated.StatusID, "parent already done — should not be moved")
}

func TestAutoTransition_NoSubtasks_NoTransition(t *testing.T) {
	ctx := context.Background()
	svc, taskRepo, statusRepo, _ := buildAutoTransitionFixture()

	projectID := uuid.New()
	inProgressStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryInProgress, "In Progress")
	seedStatus(statusRepo, projectID, domain.StatusCategoryReview, "Review")

	parent := seedTask(taskRepo, projectID, inProgressStatus.ID, nil, "Leaf Task")
	// No subtasks.

	err := svc.CheckSubtaskCompletion(ctx, parent.ID)
	require.NoError(t, err)

	updated := taskRepo.items[parent.ID]
	assert.Equal(t, inProgressStatus.ID, updated.StatusID, "task with no subtasks should not be auto-transitioned")
}

// ---------------------------------------------------------------------------
// CheckDependencyResolution tests
// ---------------------------------------------------------------------------

func TestAutoTransition_BlockingDepResolved_UnblocksDependent(t *testing.T) {
	ctx := context.Background()
	svc, taskRepo, statusRepo, depRepo := buildAutoTransitionFixture()

	projectID := uuid.New()
	doneStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryDone, "Done")
	backlogStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryBacklog, "Backlog")
	todoStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryTodo, "To Do")

	// Task A (blocker) is now done.
	taskA := seedTask(taskRepo, projectID, doneStatus.ID, nil, "Task A (blocker)")
	// Task B depends on Task A and is in backlog.
	taskB := seedTask(taskRepo, projectID, backlogStatus.ID, nil, "Task B (blocked)")

	// Dependency: B blocks depends on A.
	dep := &domain.TaskDependency{
		ID:              uuid.New(),
		TaskID:          taskB.ID,
		DependsOnTaskID: taskA.ID,
		DependencyType:  domain.DependencyTypeBlocks,
	}
	depRepo.items[dep.ID] = dep

	err := svc.CheckDependencyResolution(ctx, taskA.ID)
	require.NoError(t, err)

	updated := taskRepo.items[taskB.ID]
	require.NotNil(t, updated)
	assert.Equal(t, todoStatus.ID, updated.StatusID, "task B should move to todo when its blocker is resolved")
}

func TestAutoTransition_PartialDepResolved_NoUnblock(t *testing.T) {
	ctx := context.Background()
	svc, taskRepo, statusRepo, depRepo := buildAutoTransitionFixture()

	projectID := uuid.New()
	doneStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryDone, "Done")
	inProgressStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryInProgress, "In Progress")
	backlogStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryBacklog, "Backlog")
	seedStatus(statusRepo, projectID, domain.StatusCategoryTodo, "To Do")

	// Task A is done, Task C is still in progress.
	taskA := seedTask(taskRepo, projectID, doneStatus.ID, nil, "Task A")
	taskC := seedTask(taskRepo, projectID, inProgressStatus.ID, nil, "Task C")
	// Task B depends on BOTH A and C.
	taskB := seedTask(taskRepo, projectID, backlogStatus.ID, nil, "Task B")

	dep1 := &domain.TaskDependency{
		ID:             uuid.New(),
		TaskID:         taskB.ID,
		DependsOnTaskID: taskA.ID,
		DependencyType: domain.DependencyTypeBlocks,
	}
	dep2 := &domain.TaskDependency{
		ID:             uuid.New(),
		TaskID:         taskB.ID,
		DependsOnTaskID: taskC.ID,
		DependencyType: domain.DependencyTypeBlocks,
	}
	depRepo.items[dep1.ID] = dep1
	depRepo.items[dep2.ID] = dep2

	// Resolving A alone should NOT unblock B (C is still in progress).
	err := svc.CheckDependencyResolution(ctx, taskA.ID)
	require.NoError(t, err)

	updated := taskRepo.items[taskB.ID]
	assert.Equal(t, backlogStatus.ID, updated.StatusID, "task B should remain in backlog while task C is still blocking")
}

func TestAutoTransition_RelatesToDepResolved_NoUnblock(t *testing.T) {
	ctx := context.Background()
	svc, taskRepo, statusRepo, depRepo := buildAutoTransitionFixture()

	projectID := uuid.New()
	doneStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryDone, "Done")
	backlogStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryBacklog, "Backlog")
	seedStatus(statusRepo, projectID, domain.StatusCategoryTodo, "To Do")

	taskA := seedTask(taskRepo, projectID, doneStatus.ID, nil, "Task A")
	taskB := seedTask(taskRepo, projectID, backlogStatus.ID, nil, "Task B")

	// relates_to dependency should NOT trigger unblocking.
	dep := &domain.TaskDependency{
		ID:              uuid.New(),
		TaskID:          taskB.ID,
		DependsOnTaskID: taskA.ID,
		DependencyType:  domain.DependencyTypeRelatesTo,
	}
	depRepo.items[dep.ID] = dep

	err := svc.CheckDependencyResolution(ctx, taskA.ID)
	require.NoError(t, err)

	updated := taskRepo.items[taskB.ID]
	assert.Equal(t, backlogStatus.ID, updated.StatusID, "relates_to deps should not trigger unblocking")
}

func TestAutoTransition_DependentNotInBacklog_NoUnblock(t *testing.T) {
	ctx := context.Background()
	svc, taskRepo, statusRepo, depRepo := buildAutoTransitionFixture()

	projectID := uuid.New()
	doneStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryDone, "Done")
	inProgressStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryInProgress, "In Progress")
	seedStatus(statusRepo, projectID, domain.StatusCategoryTodo, "To Do")

	taskA := seedTask(taskRepo, projectID, doneStatus.ID, nil, "Task A")
	// Task B is in "in_progress" (not "backlog") — should not be touched.
	taskB := seedTask(taskRepo, projectID, inProgressStatus.ID, nil, "Task B")

	dep := &domain.TaskDependency{
		ID:              uuid.New(),
		TaskID:          taskB.ID,
		DependsOnTaskID: taskA.ID,
		DependencyType:  domain.DependencyTypeBlocks,
	}
	depRepo.items[dep.ID] = dep

	err := svc.CheckDependencyResolution(ctx, taskA.ID)
	require.NoError(t, err)

	updated := taskRepo.items[taskB.ID]
	assert.Equal(t, inProgressStatus.ID, updated.StatusID, "dependent task not in backlog should not be moved")
}

// ---------------------------------------------------------------------------
// EvaluateOnTaskMove integration test
// ---------------------------------------------------------------------------

func TestAutoTransition_EvaluateOnTaskMove_TriggersBothChecks(t *testing.T) {
	ctx := context.Background()
	svc, taskRepo, statusRepo, depRepo := buildAutoTransitionFixture()

	projectID := uuid.New()
	inProgressStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryInProgress, "In Progress")
	doneStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryDone, "Done")
	backlogStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryBacklog, "Backlog")
	reviewStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryReview, "Review")
	todoStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryTodo, "To Do")

	// Parent with one subtask (the task being moved).
	parent := seedTask(taskRepo, projectID, inProgressStatus.ID, nil, "Parent")
	subtask := seedTask(taskRepo, projectID, doneStatus.ID, &parent.ID, "The subtask")

	// Dependent task blocked by the subtask.
	dependent := seedTask(taskRepo, projectID, backlogStatus.ID, nil, "Dependent")
	dep := &domain.TaskDependency{
		ID:              uuid.New(),
		TaskID:          dependent.ID,
		DependsOnTaskID: subtask.ID,
		DependencyType:  domain.DependencyTypeBlocks,
	}
	depRepo.items[dep.ID] = dep

	// EvaluateOnTaskMove should trigger both parent and dependency checks.
	err := svc.EvaluateOnTaskMove(ctx, subtask.ID, domain.StatusCategoryDone)
	require.NoError(t, err)

	// Parent should move to review.
	updatedParent := taskRepo.items[parent.ID]
	assert.Equal(t, reviewStatus.ID, updatedParent.StatusID, "parent should move to review")

	// Dependent should move to todo.
	updatedDependent := taskRepo.items[dependent.ID]
	assert.Equal(t, todoStatus.ID, updatedDependent.StatusID, "dependent should move to todo")
}

func TestAutoTransition_EvaluateOnTaskMove_NonDoneCategory_NoTrigger(t *testing.T) {
	ctx := context.Background()
	svc, taskRepo, statusRepo, _ := buildAutoTransitionFixture()

	projectID := uuid.New()
	inProgressStatus := seedStatus(statusRepo, projectID, domain.StatusCategoryInProgress, "In Progress")
	seedStatus(statusRepo, projectID, domain.StatusCategoryReview, "Review")

	parent := seedTask(taskRepo, projectID, inProgressStatus.ID, nil, "Parent")
	subtask := seedTask(taskRepo, projectID, inProgressStatus.ID, &parent.ID, "Subtask")

	// Moving to "in_progress" should not trigger any auto-transition.
	err := svc.EvaluateOnTaskMove(ctx, subtask.ID, domain.StatusCategoryInProgress)
	require.NoError(t, err)

	updatedParent := taskRepo.items[parent.ID]
	assert.Equal(t, inProgressStatus.ID, updatedParent.StatusID, "parent should not move when subtask is not done")
}

// ---------------------------------------------------------------------------
// Rule management tests
// ---------------------------------------------------------------------------

func TestAutoTransition_CreateAndListRules(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _ := buildAutoTransitionFixture()

	projectID := uuid.New()
	rule := &AutoTransitionRule{
		ProjectID:      projectID,
		Trigger:        TriggerAllSubtasksDone,
		TargetStatusID: uuid.New(),
		IsEnabled:      true,
	}

	err := svc.CreateRule(ctx, rule)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, rule.ID, "rule ID should be auto-generated")

	rules, err := svc.ListRules(ctx, projectID)
	require.NoError(t, err)
	assert.Len(t, rules, 1)
	assert.Equal(t, TriggerAllSubtasksDone, rules[0].Trigger)
}

func TestAutoTransition_DeleteRule(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _ := buildAutoTransitionFixture()

	projectID := uuid.New()
	rule := &AutoTransitionRule{
		ProjectID:      projectID,
		Trigger:        TriggerBlockingDepResolved,
		TargetStatusID: uuid.New(),
		IsEnabled:      true,
	}
	require.NoError(t, svc.CreateRule(ctx, rule))

	// Delete it.
	err := svc.DeleteRule(ctx, rule.ID)
	require.NoError(t, err)

	rules, err := svc.ListRules(ctx, projectID)
	require.NoError(t, err)
	assert.Empty(t, rules, "rule should be deleted")
}

// ---------------------------------------------------------------------------
// AutoTransitionRule domain logic (pure, no service needed)
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
// Pure logic helper tests (run without service)
// ---------------------------------------------------------------------------

// allSubtasksDone mirrors the logic used in CheckSubtaskCompletion for testing.
func allSubtasksDone(subtasks []domain.Task, statusCategoryByID map[uuid.UUID]domain.StatusCategory) bool {
	if len(subtasks) == 0 {
		return false
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

// hasUnresolvedBlockingDeps mirrors the hasUnresolvedBlockers logic for testing.
func hasUnresolvedBlockingDeps(deps []domain.TaskDependency, statusCategoryByTaskID map[uuid.UUID]domain.StatusCategory) bool {
	for _, dep := range deps {
		if dep.DependencyType != domain.DependencyTypeBlocks {
			continue
		}
		cat, ok := statusCategoryByTaskID[dep.DependsOnTaskID]
		if !ok || cat != domain.StatusCategoryDone {
			return true
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
		assert.False(t, hasUnresolvedBlockingDeps(deps, categoryMap))
	})

	t.Run("no deps returns false", func(t *testing.T) {
		assert.False(t, hasUnresolvedBlockingDeps([]domain.TaskDependency{}, categoryMap))
	})
}

// ---------------------------------------------------------------------------
// Integration sketch using existing mock repos
// ---------------------------------------------------------------------------

func TestAutoTransition_WithExistingMocks_SubtaskStatusCheck(t *testing.T) {
	ctx := context.Background()
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()

	projectID := uuid.New()
	doneStatusID := uuid.New()
	todoStatusID := uuid.New()

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

	parentID := uuid.New()
	taskRepo.items[parentID] = &domain.Task{
		ID:        parentID,
		ProjectID: projectID,
		StatusID:  todoStatusID,
		Title:     "Parent Task",
	}

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

	subtasks, err := taskRepo.ListSubtasks(ctx, parentID)
	require.NoError(t, err)
	assert.Len(t, subtasks, 3, "should have 3 subtasks")

	statusByID := map[uuid.UUID]domain.StatusCategory{}
	for id, s := range statusRepo.items {
		statusByID[id] = s.Category
	}

	result := allSubtasksDone(subtasks, statusByID)
	assert.True(t, result, "all subtasks are done — auto-transition should fire")

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
