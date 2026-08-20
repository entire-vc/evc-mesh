package service

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// checkoutBoard is a project with a todo and an in_progress status and one task
// sitting in todo — the shape the board bug lives in.
type checkoutBoard struct {
	svc          *taskService
	taskRepo     *MockTaskRepository
	statusRepo   *MockTaskStatusRepository
	projectID    uuid.UUID
	taskID       uuid.UUID
	todoID       uuid.UUID
	inProgressID uuid.UUID
}

func checkoutBoardFixture(t *testing.T) checkoutBoard {
	t.Helper()
	svc, taskRepo, statusRepo, _ := setupCheckoutTaskService()

	projectID := uuid.New()
	todoID := uuid.New()
	inProgressID := uuid.New()
	taskID := uuid.New()

	statusRepo.items[todoID] = &domain.TaskStatus{
		ID: todoID, ProjectID: projectID, Category: domain.StatusCategoryTodo, Name: "Todo", Position: 1,
	}
	statusRepo.items[inProgressID] = &domain.TaskStatus{
		ID: inProgressID, ProjectID: projectID, Category: domain.StatusCategoryInProgress, Name: "In Progress", Position: 2,
	}
	taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: projectID, StatusID: todoID, Title: "A todo task",
	}
	return checkoutBoard{
		svc:          svc,
		taskRepo:     taskRepo,
		statusRepo:   statusRepo,
		projectID:    projectID,
		taskID:       taskID,
		todoID:       todoID,
		inProgressID: inProgressID,
	}
}

// The bug this file exists for: a card taken into work was indistinguishable on the
// board from an untouched one, because checkout is a lock and never changed status.
// 12 cards fleet-wide held live checkouts while In Progress showed 1 (measured
// 2026-08-20, task #5f9c5117).
func TestCheckoutTask_MovesTodoCardIntoInProgress(t *testing.T) {
	b := checkoutBoardFixture(t)

	_, err := b.svc.CheckoutTask(agentContext(uuid.New()), b.taskID, 30, nil)
	require.NoError(t, err)

	assert.Equal(t, b.inProgressID, b.taskRepo.items[b.taskID].StatusID,
		"a checked-out todo card must be visible in In Progress")
}

// The fan-out case: one lane holding several cards at once must show ALL of them,
// not just the first. This is the acceptance criterion the capacity rule would
// have silently broken had the move been made under the agent's own actor.
func TestCheckoutTask_FanOutShowsEveryCard(t *testing.T) {
	b := checkoutBoardFixture(t)
	agentID := uuid.New()

	ids := make([]uuid.UUID, 0, 5)
	for i := 0; i < 5; i++ {
		id := uuid.New()
		b.taskRepo.items[id] = &domain.Task{
			ID: id, ProjectID: b.projectID, StatusID: b.todoID, Title: "fanned card",
		}
		ids = append(ids, id)
	}

	for _, id := range ids {
		_, err := b.svc.CheckoutTask(agentContext(agentID), id, 30, nil)
		require.NoError(t, err)
	}

	for i, id := range ids {
		assert.Equal(t, b.inProgressID, b.taskRepo.items[id].StatusID,
			"fanned card %d of %d must be visible in In Progress", i+1, len(ids))
	}
}

// Only todo is auto-advanced. A card checked out from backlog or review (e.g. a
// verifier picking up a review card) must keep its status — yanking it into
// In Progress would misreport where it is in the flow.
func TestCheckoutTask_NonTodoStatusIsLeftAlone(t *testing.T) {
	for _, cat := range []domain.StatusCategory{
		domain.StatusCategoryBacklog,
		domain.StatusCategoryReview,
		domain.StatusCategoryTriage,
		domain.StatusCategoryInProgress,
	} {
		t.Run(string(cat), func(t *testing.T) {
			svc, taskRepo, statusRepo, _ := setupCheckoutTaskService()
			projectID := uuid.New()
			statusID := uuid.New()
			inProgressID := uuid.New()
			taskID := uuid.New()

			statusRepo.items[statusID] = &domain.TaskStatus{
				ID: statusID, ProjectID: projectID, Category: cat, Name: string(cat), Position: 1,
			}
			statusRepo.items[inProgressID] = &domain.TaskStatus{
				ID: inProgressID, ProjectID: projectID, Category: domain.StatusCategoryInProgress,
				Name: "In Progress", Position: 2,
			}
			taskRepo.items[taskID] = &domain.Task{
				ID: taskID, ProjectID: projectID, StatusID: statusID, Title: "a card",
			}

			_, err := svc.CheckoutTask(agentContext(uuid.New()), taskID, 30, nil)
			require.NoError(t, err)
			assert.Equal(t, statusID, taskRepo.items[taskID].StatusID,
				"checkout from %s must not change status", cat)
		})
	}
}

// A supervised task in todo is refused checkout entirely (pre-existing gate), so it
// must never be advanced as a side effect of a refused call.
func TestCheckoutTask_SupervisedTodoNotAdvanced(t *testing.T) {
	b := checkoutBoardFixture(t)
	b.taskRepo.items[b.taskID].DelegationLevel = domain.DelegationLevelSupervised

	_, err := b.svc.CheckoutTask(agentContext(uuid.New()), b.taskID, 30, nil)
	require.Error(t, err)
	assert.Equal(t, b.todoID, b.taskRepo.items[b.taskID].StatusID,
		"a refused supervised checkout must leave the card in todo")
}

// Fail-open contract. The lock is the load-bearing half; the status change only
// reports it. A project with no in_progress column, or a move that a rule or a
// transient error refuses, must still yield a usable checkout — otherwise a
// display fix would become a way to stop agents working.
func TestCheckoutTask_SucceedsWhenProjectHasNoInProgressColumn(t *testing.T) {
	svc, taskRepo, statusRepo, _ := setupCheckoutTaskService()
	projectID := uuid.New()
	todoID := uuid.New()
	taskID := uuid.New()
	statusRepo.items[todoID] = &domain.TaskStatus{
		ID: todoID, ProjectID: projectID, Category: domain.StatusCategoryTodo, Name: "Todo", Position: 1,
	}
	taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: projectID, StatusID: todoID, Title: "a card"}

	result, err := svc.CheckoutTask(agentContext(uuid.New()), taskID, 30, nil)
	require.NoError(t, err, "checkout must not fail because the board has no In Progress column")
	require.NotNil(t, result)
	assert.NotEqual(t, uuid.Nil, result.CheckoutToken, "the lock must still be usable")
	assert.Equal(t, todoID, taskRepo.items[taskID].StatusID)
}

func TestCheckoutTask_SucceedsWhenTheMoveItselfIsRefused(t *testing.T) {
	// A genuinely-attempted, genuinely-refused move. `IsShipped` makes MoveTask
	// reject any transition to a non-done category, so the auto-move is reached and
	// fails inside MoveTask — as opposed to being skipped before it is tried, which
	// is what a broken status read would produce and which would prove nothing.
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	activityRepo := NewMockActivityLogRepository()
	svc := newTestTaskService(taskRepo, statusRepo, NewMockTaskDependencyRepository(), activityRepo,
		WithTaskAgentRepo(NewMockAgentRepository()),
	).(*taskService)
	timeNow = func() time.Time { return frozenTime }

	projectID := uuid.New()
	todoID := uuid.New()
	inProgressID := uuid.New()
	taskID := uuid.New()
	statusRepo.items[todoID] = &domain.TaskStatus{
		ID: todoID, ProjectID: projectID, Category: domain.StatusCategoryTodo, Name: "Todo", Position: 1,
	}
	statusRepo.items[inProgressID] = &domain.TaskStatus{
		ID: inProgressID, ProjectID: projectID, Category: domain.StatusCategoryInProgress,
		Name: "In Progress", Position: 2,
	}
	taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: projectID, StatusID: todoID, Title: "a card", IsShipped: true,
	}

	result, err := svc.CheckoutTask(agentContext(uuid.New()), taskID, 30, nil)
	require.NoError(t, err, "a refused auto-move must not fail the checkout")
	require.NotNil(t, result)
	assert.NotEqual(t, uuid.Nil, result.CheckoutToken, "the lock must still be usable")
	assert.Equal(t, todoID, taskRepo.items[taskID].StatusID)

	// Non-vacuity guard: prove the refusal was reached and recorded, rather than the
	// auto-move having been skipped somewhere earlier.
	var sawFailureEntry bool
	for _, e := range activityRepo.items {
		if e.Action == "task.checkout_auto_progress_failed" {
			sawFailureEntry = true
		}
	}
	assert.True(t, sawFailureEntry,
		"the refused auto-move must be recorded, otherwise this test asserts nothing")
}
