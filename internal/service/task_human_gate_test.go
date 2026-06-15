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
