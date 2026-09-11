package service

// Tests for the human_gate sticky freeze flag (audit P0 #3, 2026-06-15).
//
// Rules under test:
//  1. Agent actor → MoveTask to done/backlog/cancelled is blocked with HumanGateFrozenError.
//  2. System actor → same block.
//  3. User actor → allowed through (Pavel can resolve the gate).
//  4. HumanGate=true does NOT block moves to review or triage.

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// buildHumanGateEnv creates a taskService with a gated task and statuses in each category.
func buildHumanGateEnv(t *testing.T) (svc *taskService, taskID uuid.UUID, statusByCategory map[domain.StatusCategory]uuid.UUID) {
	t.Helper()
	ts, taskRepo, statusRepo := setupTaskService()

	projID := uuid.New()
	taskID = uuid.New()

	statusByCategory = make(map[domain.StatusCategory]uuid.UUID)
	for _, cat := range []domain.StatusCategory{
		domain.StatusCategoryBacklog,
		domain.StatusCategoryTodo,
		domain.StatusCategoryInProgress,
		domain.StatusCategoryReview,
		domain.StatusCategoryTriage,
		domain.StatusCategoryDone,
		domain.StatusCategoryCancelled,
	} {
		sid := uuid.New()
		statusRepo.items[sid] = &domain.TaskStatus{ID: sid, ProjectID: projID, Category: cat}
		statusByCategory[cat] = sid
	}

	taskRepo.items[taskID] = &domain.Task{
		ID:        taskID,
		ProjectID: projID,
		StatusID:  statusByCategory[domain.StatusCategoryReview],
		Title:     "gated task",
		HumanGate: true,
	}

	return ts, taskID, statusByCategory
}

func TestMoveTask_HumanGate_AgentBlockedOnDone(t *testing.T) {
	ts, taskID, cats := buildHumanGateEnv(t)
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)

	doneID := cats[domain.StatusCategoryDone]
	err := ts.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &doneID})

	require.Error(t, err)
	var gateErr *HumanGateFrozenError
	assert.ErrorAs(t, err, &gateErr, "agent must not move human-gated task to done")
}

func TestMoveTask_HumanGate_AgentBlockedOnBacklog(t *testing.T) {
	ts, taskID, cats := buildHumanGateEnv(t)
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)

	backlogID := cats[domain.StatusCategoryBacklog]
	err := ts.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &backlogID})

	require.Error(t, err)
	var gateErr *HumanGateFrozenError
	assert.ErrorAs(t, err, &gateErr, "agent must not move human-gated task to backlog")
}

func TestMoveTask_HumanGate_SystemBlockedOnCancelled(t *testing.T) {
	ts, taskID, cats := buildHumanGateEnv(t)
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeSystem)

	cancelledID := cats[domain.StatusCategoryCancelled]
	err := ts.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &cancelledID})

	require.Error(t, err)
	var gateErr *HumanGateFrozenError
	assert.ErrorAs(t, err, &gateErr, "system actor must not move human-gated task to cancelled")
}

func TestMoveTask_HumanGate_UserAllowedOnDone(t *testing.T) {
	ts, taskID, cats := buildHumanGateEnv(t)
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeUser)

	doneID := cats[domain.StatusCategoryDone]
	err := ts.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &doneID})

	// User (Pavel) may always resolve the gate.
	require.NoError(t, err, "user must be allowed to move human-gated task to done")
}

func TestMoveTask_HumanGate_GateAuthorAllowedOnBacklog(t *testing.T) {
	ts, taskID, cats := buildHumanGateEnv(t)

	authorID := uuid.New()
	authorType := domain.ActorTypeAgent
	ts.taskRepo.(*MockTaskRepository).items[taskID].GateAuthor = &authorID
	ts.taskRepo.(*MockTaskRepository).items[taskID].GateAuthorType = &authorType

	ctx := actorctx.WithActor(context.Background(), authorID, domain.ActorTypeAgent)

	backlogID := cats[domain.StatusCategoryBacklog]
	err := ts.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &backlogID})

	require.NoError(t, err, "the gate's own author must be allowed to park the still-open ask in backlog")

	parked, getErr := ts.taskRepo.GetByID(context.Background(), taskID)
	require.NoError(t, getErr)
	assert.True(t, parked.HumanGate, "parking must not clear the gate — a human still has to resolve it")
}

func TestMoveTask_HumanGate_GateAuthorStillBlockedOnDone(t *testing.T) {
	ts, taskID, cats := buildHumanGateEnv(t)

	authorID := uuid.New()
	authorType := domain.ActorTypeAgent
	ts.taskRepo.(*MockTaskRepository).items[taskID].GateAuthor = &authorID
	ts.taskRepo.(*MockTaskRepository).items[taskID].GateAuthorType = &authorType

	ctx := actorctx.WithActor(context.Background(), authorID, domain.ActorTypeAgent)

	doneID := cats[domain.StatusCategoryDone]
	err := ts.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &doneID})

	require.Error(t, err)
	var gateErr *HumanGateFrozenError
	assert.ErrorAs(t, err, &gateErr, "the gate author's own-ask exception covers backlog only, never done")
}

func TestMoveTask_HumanGate_GateAuthorStillBlockedOnCancelled(t *testing.T) {
	ts, taskID, cats := buildHumanGateEnv(t)

	authorID := uuid.New()
	authorType := domain.ActorTypeAgent
	ts.taskRepo.(*MockTaskRepository).items[taskID].GateAuthor = &authorID
	ts.taskRepo.(*MockTaskRepository).items[taskID].GateAuthorType = &authorType

	ctx := actorctx.WithActor(context.Background(), authorID, domain.ActorTypeAgent)

	cancelledID := cats[domain.StatusCategoryCancelled]
	err := ts.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &cancelledID})

	require.Error(t, err)
	var gateErr *HumanGateFrozenError
	assert.ErrorAs(t, err, &gateErr, "the gate author's own-ask exception covers backlog only, never cancelled")
}

func TestMoveTask_HumanGate_NonAuthorAgentStillBlockedOnBacklog(t *testing.T) {
	ts, taskID, cats := buildHumanGateEnv(t)

	authorID := uuid.New()
	authorType := domain.ActorTypeAgent
	ts.taskRepo.(*MockTaskRepository).items[taskID].GateAuthor = &authorID
	ts.taskRepo.(*MockTaskRepository).items[taskID].GateAuthorType = &authorType

	// A DIFFERENT agent than the one who raised the gate.
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)

	backlogID := cats[domain.StatusCategoryBacklog]
	err := ts.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &backlogID})

	require.Error(t, err)
	var gateErr *HumanGateFrozenError
	assert.ErrorAs(t, err, &gateErr, "only the gate's own author gets the backlog exception, not any agent")
}

func TestMoveTask_HumanGate_AgentAllowedToReview(t *testing.T) {
	ts, taskID, cats := buildHumanGateEnv(t)

	// Move from review to triage (not a blocked category)
	ts.taskRepo.(*MockTaskRepository).items[taskID].StatusID = cats[domain.StatusCategoryInProgress]
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)

	reviewID := cats[domain.StatusCategoryReview]
	err := ts.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &reviewID})

	// Moving to review is allowed even for gated tasks — that's the hold point.
	require.NoError(t, err, "agent must be allowed to move human-gated task to review")
}
