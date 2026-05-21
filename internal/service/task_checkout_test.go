package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	pgRepo "github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// setupCheckoutTaskService returns a taskService wired with an agent repo so
// 409 conflict responses can resolve holder names. Time is frozen.
func setupCheckoutTaskService() (*taskService, *MockTaskRepository, *MockTaskStatusRepository, *MockAgentRepository) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()
	agentRepo := NewMockAgentRepository()
	svc := NewTaskService(taskRepo, statusRepo, depRepo, activityRepo,
		WithTaskAgentRepo(agentRepo),
	).(*taskService)
	timeNow = func() time.Time { return frozenTime }
	return svc, taskRepo, statusRepo, agentRepo
}

// agentContext wraps a context with an actor identity for checkout calls.
func agentContext(agentID uuid.UUID) context.Context {
	return actorctx.WithActor(context.Background(), agentID, domain.ActorTypeAgent)
}

func TestCheckoutTask_RequiresAgentActor(t *testing.T) {
	svc, _, _, _ := setupCheckoutTaskService()

	_, err := svc.CheckoutTask(context.Background(), uuid.New(), 0, nil)
	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.Code)
}

func TestCheckoutTask_SuccessWithExplicitTTL(t *testing.T) {
	svc, taskRepo, _, _ := setupCheckoutTaskService()
	projectID := uuid.New()
	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: projectID, Title: "A task"}
	agentID := uuid.New()

	result, err := svc.CheckoutTask(agentContext(agentID), taskID, 30, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, taskID, result.TaskID)
	assert.Equal(t, agentID, result.CheckedOutBy)
	assert.Equal(t, frozenTime.Add(30*time.Minute), result.ExpiresAt)
	assert.NotEqual(t, uuid.Nil, result.CheckoutToken)
}

func TestCheckoutTask_TTLDefaults(t *testing.T) {
	svc, taskRepo, _, _ := setupCheckoutTaskService()
	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: uuid.New(), Title: "A task"}

	result, err := svc.CheckoutTask(agentContext(uuid.New()), taskID, 0, nil)
	require.NoError(t, err)
	assert.Equal(t, frozenTime.Add(15*time.Minute), result.ExpiresAt)
}

func TestCheckoutTask_TTLClampedToMax(t *testing.T) {
	svc, taskRepo, _, _ := setupCheckoutTaskService()
	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: uuid.New(), Title: "A task"}

	result, err := svc.CheckoutTask(agentContext(uuid.New()), taskID, 99999, nil)
	require.NoError(t, err)
	assert.Equal(t, frozenTime.Add(240*time.Minute), result.ExpiresAt)
}

func TestCheckoutTask_ConflictReturnsHolderName(t *testing.T) {
	svc, taskRepo, _, agentRepo := setupCheckoutTaskService()

	holderID := uuid.New()
	agentRepo.items[holderID] = &domain.Agent{ID: holderID, Name: "Linus"}

	taskID := uuid.New()
	expires := frozenTime.Add(10 * time.Minute)
	tokenID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{
		ID:              taskID,
		ProjectID:       uuid.New(),
		Title:           "Locked task",
		CheckedOutBy:    &holderID,
		CheckoutToken:   &tokenID,
		CheckoutExpires: &expires,
	}

	_, err := svc.CheckoutTask(agentContext(uuid.New()), taskID, 0, nil)
	require.Error(t, err)

	var conflict *CheckoutConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, holderID, conflict.CheckedOutBy)
	assert.Equal(t, "Linus", conflict.CheckedOutByName, "holder name should be resolved via agent repo")
	assert.Equal(t, "agent", conflict.CheckedOutByKind)
	assert.Equal(t, expires, conflict.ExpiresAt)
}

func TestCheckoutTask_ConflictOmitsNameWhenAgentMissing(t *testing.T) {
	svc, taskRepo, _, _ := setupCheckoutTaskService()

	holderID := uuid.New()
	taskID := uuid.New()
	expires := frozenTime.Add(10 * time.Minute)
	tokenID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{
		ID:              taskID,
		ProjectID:       uuid.New(),
		Title:           "Locked task",
		CheckedOutBy:    &holderID,
		CheckoutToken:   &tokenID,
		CheckoutExpires: &expires,
	}

	_, err := svc.CheckoutTask(agentContext(uuid.New()), taskID, 0, nil)
	require.Error(t, err)

	var conflict *CheckoutConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Empty(t, conflict.CheckedOutByName, "name stays empty when agent record cannot be found")
	assert.Equal(t, "agent", conflict.CheckedOutByKind)
}

func TestCheckoutTask_SameAgentReclaimRefreshes(t *testing.T) {
	svc, taskRepo, _, _ := setupCheckoutTaskService()
	taskID := uuid.New()
	agentID := uuid.New()
	oldToken := uuid.New()
	oldExpires := frozenTime.Add(5 * time.Minute)
	taskRepo.items[taskID] = &domain.Task{
		ID:              taskID,
		ProjectID:       uuid.New(),
		Title:           "Already mine",
		CheckedOutBy:    &agentID,
		CheckoutToken:   &oldToken,
		CheckoutExpires: &oldExpires,
	}

	result, err := svc.CheckoutTask(agentContext(agentID), taskID, 30, nil)
	require.NoError(t, err, "same-agent re-checkout must be idempotent")
	assert.Equal(t, agentID, result.CheckedOutBy)
	assert.Equal(t, frozenTime.Add(30*time.Minute), result.ExpiresAt)
}

func TestReleaseCheckout_WrongTokenIsForbidden(t *testing.T) {
	svc, taskRepo, _, _ := setupCheckoutTaskService()
	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: uuid.New(), Title: "A task"}
	taskRepo.errToReturn = pgRepo.ErrInvalidCheckoutToken
	defer func() { taskRepo.errToReturn = nil }()

	err := svc.ReleaseCheckout(agentContext(uuid.New()), taskID, uuid.New())
	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusForbidden, apiErr.Code)
}

func TestExtendCheckout_WrongTokenIsForbidden(t *testing.T) {
	svc, taskRepo, _, _ := setupCheckoutTaskService()
	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: uuid.New(), Title: "A task"}
	taskRepo.errToReturn = pgRepo.ErrInvalidCheckoutToken
	defer func() { taskRepo.errToReturn = nil }()

	_, err := svc.ExtendCheckout(agentContext(uuid.New()), taskID, uuid.New(), 0)
	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusForbidden, apiErr.Code)
}

func runMoveTaskAutoReleaseCase(t *testing.T, category domain.StatusCategory, expectReleased bool) {
	t.Helper()
	svc, taskRepo, statusRepo, _ := setupCheckoutTaskService()

	projectID := uuid.New()
	taskID := uuid.New()
	oldStatusID := uuid.New()
	newStatusID := uuid.New()
	holderID := uuid.New()
	tokenID := uuid.New()
	expires := frozenTime.Add(10 * time.Minute)

	statusRepo.items[oldStatusID] = &domain.TaskStatus{ID: oldStatusID, ProjectID: projectID, Category: domain.StatusCategoryInProgress, Name: "In progress"}
	statusRepo.items[newStatusID] = &domain.TaskStatus{ID: newStatusID, ProjectID: projectID, Category: category, Name: string(category)}
	taskRepo.items[taskID] = &domain.Task{
		ID:              taskID,
		ProjectID:       projectID,
		StatusID:        oldStatusID,
		Title:           "Locked task",
		CheckedOutBy:    &holderID,
		CheckoutToken:   &tokenID,
		CheckoutExpires: &expires,
	}

	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &newStatusID})
	require.NoError(t, err)

	updated := taskRepo.items[taskID]
	require.NotNil(t, updated)

	if expectReleased {
		assert.Nil(t, updated.CheckedOutBy, "checkout holder should be cleared after move to %s", category)
		assert.Nil(t, updated.CheckoutToken, "checkout token should be cleared after move to %s", category)
		assert.Nil(t, updated.CheckoutExpires, "checkout expiry should be cleared after move to %s", category)
	} else {
		require.NotNil(t, updated.CheckedOutBy, "checkout should be preserved after move to %s", category)
		assert.Equal(t, holderID, *updated.CheckedOutBy)
	}
}

func TestMoveTask_AutoReleasesCheckoutOnDone(t *testing.T) {
	runMoveTaskAutoReleaseCase(t, domain.StatusCategoryDone, true)
}

func TestMoveTask_AutoReleasesCheckoutOnReview(t *testing.T) {
	runMoveTaskAutoReleaseCase(t, domain.StatusCategoryReview, true)
}

func TestMoveTask_AutoReleasesCheckoutOnCancelled(t *testing.T) {
	runMoveTaskAutoReleaseCase(t, domain.StatusCategoryCancelled, true)
}

func TestMoveTask_DoesNotReleaseCheckoutOnTodo(t *testing.T) {
	runMoveTaskAutoReleaseCase(t, domain.StatusCategoryTodo, false)
}

func TestMoveTask_DoesNotReleaseCheckoutOnInProgress(t *testing.T) {
	runMoveTaskAutoReleaseCase(t, domain.StatusCategoryInProgress, false)
}

func TestForceReleaseCheckout_ClearsHolderWithoutToken(t *testing.T) {
	svc, taskRepo, _, _ := setupCheckoutTaskService()
	taskID := uuid.New()
	holderID := uuid.New()
	tokenID := uuid.New()
	expires := frozenTime.Add(10 * time.Minute)
	taskRepo.items[taskID] = &domain.Task{
		ID:              taskID,
		ProjectID:       uuid.New(),
		Title:           "Locked task",
		CheckedOutBy:    &holderID,
		CheckoutToken:   &tokenID,
		CheckoutExpires: &expires,
	}

	err := svc.ForceReleaseCheckout(context.Background(), taskID)
	require.NoError(t, err)

	updated := taskRepo.items[taskID]
	require.NotNil(t, updated)
	assert.Nil(t, updated.CheckedOutBy)
	assert.Nil(t, updated.CheckoutToken)
	assert.Nil(t, updated.CheckoutExpires)
}

func TestForceReleaseCheckout_NoOpOnUnlockedTask(t *testing.T) {
	svc, taskRepo, _, _ := setupCheckoutTaskService()
	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: uuid.New(), Title: "Unlocked"}

	require.NoError(t, svc.ForceReleaseCheckout(context.Background(), taskID))
}

func TestCheckoutConflictError_AsTarget(t *testing.T) {
	original := &CheckoutConflictError{
		CheckedOutBy:     uuid.New(),
		CheckedOutByName: "Linus",
		CheckedOutByKind: "agent",
		ExpiresAt:        frozenTime,
	}
	err := error(original)
	var conflict *CheckoutConflictError
	require.True(t, errors.As(err, &conflict))
	assert.Equal(t, original.CheckedOutByName, conflict.CheckedOutByName)
	assert.Contains(t, original.Error(), "Linus")
}
