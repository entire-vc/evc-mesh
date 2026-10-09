package service

// Contract under test (task #eab3a995): when a human_gate comes down, a task sitting in
// triage goes to the project's todo status, never to in_progress, whichever door the
// release came through. Everything not in triage is left exactly where it is.

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

type gateReturnEnv struct {
	ts     *taskService
	repo   *MockTaskRepository
	status map[domain.StatusCategory]uuid.UUID
	projID uuid.UUID
}

func newGateReturnEnv(t *testing.T, skip ...domain.StatusCategory) gateReturnEnv {
	t.Helper()
	ts, repo, statusRepo := setupTaskService()
	env := gateReturnEnv{ts: ts, repo: repo, status: map[domain.StatusCategory]uuid.UUID{}, projID: uuid.New()}
	skipSet := map[domain.StatusCategory]bool{}
	for _, c := range skip {
		skipSet[c] = true
	}
	for _, cat := range []domain.StatusCategory{
		domain.StatusCategoryBacklog, domain.StatusCategoryTodo, domain.StatusCategoryInProgress,
		domain.StatusCategoryReview, domain.StatusCategoryTriage, domain.StatusCategoryDone,
		domain.StatusCategoryCancelled,
	} {
		if skipSet[cat] {
			continue
		}
		id := uuid.New()
		statusRepo.items[id] = &domain.TaskStatus{ID: id, ProjectID: env.projID, Category: cat}
		env.status[cat] = id
	}
	return env
}

func (e gateReturnEnv) seed(cat domain.StatusCategory, statusID uuid.UUID, gated bool) *domain.Task {
	id := uuid.New()
	startAfter := frozenTime.Add(48 * time.Hour)
	task := &domain.Task{
		ID: id, ProjectID: e.projID, StatusID: statusID, Title: "gated", HumanGate: gated,
		StartAfter: &startAfter,
	}
	e.repo.items[id] = task
	return task
}

// releasers are the entry points a release can arrive through at the service layer.
// (Comment, withdrawal and decision paths call SetHumanGate; the button/DELETE calls
// ClearHumanGate; PATCH/UI calls Update.)
func (e gateReturnEnv) releasers() map[string]func(*domain.Task) error {
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeUser)
	return map[string]func(*domain.Task) error{
		"SetHumanGate(false)": func(tk *domain.Task) error { return e.ts.SetHumanGate(ctx, tk.ID, false) },
		"ClearHumanGate":      func(tk *domain.Task) error { return e.ts.ClearHumanGate(ctx, tk.ID) },
		"Update(PATCH/UI)": func(tk *domain.Task) error {
			upd := *tk
			upd.HumanGate = false
			return e.ts.Update(ctx, &upd)
		},
	}
}

func TestGateClear_TriageGoesToTodo_EveryEntryPoint(t *testing.T) {
	for name := range newGateReturnEnv(t).releasers() {
		t.Run(name, func(t *testing.T) {
			env := newGateReturnEnv(t)
			task := env.seed(domain.StatusCategoryTriage, env.status[domain.StatusCategoryTriage], true)
			startAfter := *task.StartAfter

			require.NoError(t, env.releasers()[name](task))

			got := env.repo.items[task.ID]
			assert.False(t, got.HumanGate)
			assert.Equal(t, env.status[domain.StatusCategoryTodo], got.StatusID, "triage must return to todo")
			assert.NotEqual(t, env.status[domain.StatusCategoryInProgress], got.StatusID)
			require.NotNil(t, got.StartAfter, "start_after must survive the release")
			assert.True(t, startAfter.Equal(*got.StartAfter))
		})
	}
}

func TestGateClear_NonTriageStatusesAreLeftAlone(t *testing.T) {
	cats := []domain.StatusCategory{
		domain.StatusCategoryInProgress, domain.StatusCategoryTodo, domain.StatusCategoryDone,
		domain.StatusCategoryCancelled, domain.StatusCategoryBacklog, domain.StatusCategoryReview,
	}
	for _, cat := range cats {
		t.Run(string(cat), func(t *testing.T) {
			env := newGateReturnEnv(t)
			task := env.seed(cat, env.status[cat], true)
			require.NoError(t, env.ts.SetHumanGate(context.Background(), task.ID, false))
			assert.Equal(t, env.status[cat], env.repo.items[task.ID].StatusID)
			assert.False(t, env.repo.items[task.ID].HumanGate)
		})
	}
}

func TestGateClear_LiveCheckoutIsNotRewritten(t *testing.T) {
	env := newGateReturnEnv(t)
	task := env.seed(domain.StatusCategoryTriage, env.status[domain.StatusCategoryTriage], true)
	holder := uuid.New()
	exp := frozenTime.Add(30 * time.Minute)
	task.CheckedOutBy, task.CheckoutExpires = &holder, &exp

	require.NoError(t, env.ts.SetHumanGate(context.Background(), task.ID, false))
	assert.Equal(t, env.status[domain.StatusCategoryTriage], env.repo.items[task.ID].StatusID)
}

func TestGateClear_LiveCheckoutWithoutExpiry_IsNotRewritten(t *testing.T) {
	env := newGateReturnEnv(t)
	task := env.seed(domain.StatusCategoryTriage, env.status[domain.StatusCategoryTriage], true)
	holder := uuid.New()
	task.CheckedOutBy = &holder // no expiry: still held

	require.NoError(t, env.ts.SetHumanGate(context.Background(), task.ID, false))
	assert.Equal(t, env.status[domain.StatusCategoryTriage], env.repo.items[task.ID].StatusID)
}

func TestGateClear_ExpiredCheckoutDoesNotBlockReturn(t *testing.T) {
	env := newGateReturnEnv(t)
	task := env.seed(domain.StatusCategoryTriage, env.status[domain.StatusCategoryTriage], true)
	holder := uuid.New()
	past := frozenTime.Add(-time.Hour)
	task.CheckedOutBy, task.CheckoutExpires = &holder, &past

	require.NoError(t, env.ts.SetHumanGate(context.Background(), task.ID, false))
	assert.Equal(t, env.status[domain.StatusCategoryTodo], env.repo.items[task.ID].StatusID)
}

func TestGateClear_NoTodoStatus_StaysInTriage_NoInProgressFallback(t *testing.T) {
	env := newGateReturnEnv(t, domain.StatusCategoryTodo)
	task := env.seed(domain.StatusCategoryTriage, env.status[domain.StatusCategoryTriage], true)

	require.NoError(t, env.ts.SetHumanGate(context.Background(), task.ID, false))
	got := env.repo.items[task.ID]
	assert.False(t, got.HumanGate, "the gate itself still comes down")
	assert.Equal(t, env.status[domain.StatusCategoryTriage], got.StatusID)
}

func TestGateClear_Idempotent_SecondClearDoesNotMoveAgain(t *testing.T) {
	env := newGateReturnEnv(t)
	task := env.seed(domain.StatusCategoryTriage, env.status[domain.StatusCategoryTriage], true)
	ctx := context.Background()

	require.NoError(t, env.ts.ClearHumanGate(ctx, task.ID))
	require.Equal(t, env.status[domain.StatusCategoryTodo], env.repo.items[task.ID].StatusID)

	// A real checkout moves it to in_progress; a repeated clear must not undo that.
	env.repo.items[task.ID].StatusID = env.status[domain.StatusCategoryInProgress]
	require.NoError(t, env.ts.ClearHumanGate(ctx, task.ID))
	assert.Equal(t, env.status[domain.StatusCategoryInProgress], env.repo.items[task.ID].StatusID)
}

func TestGateClear_RearmedBeforeMove_IsNoOp(t *testing.T) {
	env := newGateReturnEnv(t)
	task := env.seed(domain.StatusCategoryTriage, env.status[domain.StatusCategoryTriage], true)

	// Direct call while the flag is (still/again) up: the helper must refuse to move.
	assert.False(t, env.ts.returnTriageToTodoAfterGateClear(context.Background(), task.ID))
	assert.Equal(t, env.status[domain.StatusCategoryTriage], env.repo.items[task.ID].StatusID)
}

// End to end through the comment service with the REAL task service: this is the
// ContentHub #1498c950 repro (Pavel comments on a gated triage card).
func TestReleaseHumanGate_RealTaskService_PavelReplyLandsInTodo(t *testing.T) {
	ts, repo, statusRepo := setupTaskService()
	env := setupTriageEnvWithOptions(t, true, WithCommentTaskService(ts))
	// share the repos so the comment service and the task service see the same rows
	ts.taskRepo, ts.statusRepo = env.taskRepo, env.statusRepo
	_ = repo
	_ = statusRepo
	todoID := uuid.New()
	env.statusRepo.items[todoID] = &domain.TaskStatus{ID: todoID, ProjectID: env.projID, Category: domain.StatusCategoryTodo}

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: env.projID, StatusID: env.triageID, Title: "Gated in triage", HumanGate: true,
	}
	env.seedBlockingComment(taskID)

	pavelID := uuid.New()
	ctx := actorctx.WithActor(context.Background(), pavelID, domain.ActorTypeUser)
	require.NoError(t, env.svc.Create(ctx, &domain.Comment{
		TaskID: taskID, AuthorID: pavelID, AuthorType: domain.ActorTypeUser, Body: "Ок, делайте.",
	}))

	got := env.taskRepo.items[taskID]
	assert.False(t, got.HumanGate)
	assert.Equal(t, todoID, got.StatusID, "must land in todo, not in_progress")
	sys := env.systemComments()
	require.Len(t, sys, 1)
	assert.Contains(t, sys[0].Body, "triage → todo")
	assert.NotContains(t, sys[0].Body, "in_progress")
}

func TestReleaseHumanGateOnWithdrawal_RealTaskService_LandsInTodo(t *testing.T) {
	ts, _, _ := setupTaskService()
	env := setupTriageEnvWithOptions(t, true, WithCommentTaskService(ts))
	ts.taskRepo, ts.statusRepo = env.taskRepo, env.statusRepo
	todoID := uuid.New()
	env.statusRepo.items[todoID] = &domain.TaskStatus{ID: todoID, ProjectID: env.projID, Category: domain.StatusCategoryTodo}

	taskID := env.seedGatedTask(env.triageID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	require.NoError(t, env.svc.Create(ctx, &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: "Blocker самоустранился, ask не нужен — снимаю.",
	}))

	got := env.taskRepo.items[taskID]
	assert.False(t, got.HumanGate)
	assert.Equal(t, todoID, got.StatusID)
	sys := env.systemComments()
	require.Len(t, sys, 1)
	assert.Contains(t, sys[0].Body, "triage → todo")
}

// The audit text may only claim "-> todo" when the card really ended in todo.
func TestMovedByGateRelease_OnlyTodoCounts(t *testing.T) {
	ts, _, _ := setupTaskService()
	env := setupTriageEnvWithOptions(t, true, WithCommentTaskService(ts))
	ts.taskRepo, ts.statusRepo = env.taskRepo, env.statusRepo
	todoID := uuid.New()
	env.statusRepo.items[todoID] = &domain.TaskStatus{ID: todoID, ProjectID: env.projID, Category: domain.StatusCategoryTodo}
	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID, StatusID: env.triageID}
	ctx := context.Background()

	assert.False(t, env.svc.movedByGateRelease(ctx, taskID, env.triageID), "unchanged status is not a move")

	env.taskRepo.items[taskID].StatusID = env.inProgressID // a racing checkout won
	assert.False(t, env.svc.movedByGateRelease(ctx, taskID, env.triageID), "move to in_progress is not the release move")

	env.taskRepo.items[taskID].StatusID = todoID
	assert.True(t, env.svc.movedByGateRelease(ctx, taskID, env.triageID))
}

// Clearing a gate that is not up must not move a card parked in triage for other reasons.
func TestGateClear_AlreadyClear_TriageCardNotMoved(t *testing.T) {
	for name, release := range newGateReturnEnv(t).releasers() {
		_ = release
		t.Run(name, func(t *testing.T) {
			env := newGateReturnEnv(t)
			task := env.seed(domain.StatusCategoryTriage, env.status[domain.StatusCategoryTriage], false)
			require.NoError(t, env.releasers()[name](task))
			assert.Equal(t, env.status[domain.StatusCategoryTriage], env.repo.items[task.ID].StatusID)
		})
	}
}
