package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ---------------------------------------------------------------------------
// HumanGateDefaultTimeoutService tests (task #060ccaae). The DB-level structural
// proofs (hard/no-default exclusion, the handoff away from FindSoftTimedOutGates) live
// in task_repo_default_timeout_db_test.go (real Postgres). These cover the SERVICE's
// own orchestration on top of whatever the repo hands it: it must record the right
// decision input, return the task to todo when appropriate, notify, and tolerate a
// single candidate's failure without dropping the rest.
// ---------------------------------------------------------------------------

// fakeDecisionRecorder captures every RecordHumanGateDecisionInput it receives instead
// of touching a real ledger, so tests can assert on exactly what the sweep asked to be
// recorded (DecidedBy, Provenance, Channel, Quote, CanonicalKey) — the actual
// record-then-release behavior is already proven end to end against
// comment_service_human_gate_decisions_test.go's harness.
type fakeDecisionRecorder struct {
	mu      sync.Mutex
	calls   []domain.RecordHumanGateDecisionInput
	failFor map[uuid.UUID]bool
}

func newFakeDecisionRecorder() *fakeDecisionRecorder {
	return &fakeDecisionRecorder{failFor: map[uuid.UUID]bool{}}
}

func (f *fakeDecisionRecorder) RecordHumanGateDecision(_ context.Context, in domain.RecordHumanGateDecisionInput) (*domain.HumanGateDecision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	if f.failFor[in.TaskID] {
		return nil, errInjectedTest
	}
	return &domain.HumanGateDecision{ID: uuid.New(), TaskID: in.TaskID}, nil
}

func (f *fakeDecisionRecorder) callFor(taskID uuid.UUID) (domain.RecordHumanGateDecisionInput, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.TaskID == taskID {
			return c, true
		}
	}
	return domain.RecordHumanGateDecisionInput{}, false
}

// fakeDefaultTimeoutTaskMover is a minimal in-memory defaultTimeoutTaskMover — the
// sweep's status-move needs, nothing else (mirrors leaseTaskMover's narrowing).
type fakeDefaultTimeoutTaskMover struct {
	mu        sync.Mutex
	tasks     map[uuid.UUID]*domain.Task
	moveCalls []dtMoveCall
	moveErr   error
}

type dtMoveCall struct {
	TaskID   uuid.UUID
	StatusID uuid.UUID
}

func newFakeDefaultTimeoutTaskMover() *fakeDefaultTimeoutTaskMover {
	return &fakeDefaultTimeoutTaskMover{tasks: map[uuid.UUID]*domain.Task{}}
}

func (f *fakeDefaultTimeoutTaskMover) seed(task *domain.Task) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks[task.ID] = task
}

func (f *fakeDefaultTimeoutTaskMover) GetByID(_ context.Context, id uuid.UUID) (*domain.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[id]
	if !ok {
		return nil, nil
	}
	cp := *t
	return &cp, nil
}

func (f *fakeDefaultTimeoutTaskMover) MoveTask(_ context.Context, taskID uuid.UUID, in MoveTaskInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.moveErr != nil {
		return f.moveErr
	}
	if in.StatusID != nil {
		if t, ok := f.tasks[taskID]; ok {
			t.StatusID = *in.StatusID
		}
		f.moveCalls = append(f.moveCalls, dtMoveCall{TaskID: taskID, StatusID: *in.StatusID})
	}
	return nil
}

func (f *fakeDefaultTimeoutTaskMover) moveCallsFor(taskID uuid.UUID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.moveCalls {
		if c.TaskID == taskID {
			n++
		}
	}
	return n
}

type defaultTimeoutHarness struct {
	taskRepo    *MockTaskRepository
	statusRepo  *MockTaskStatusRepository
	projectRepo *MockProjectRepository
	decisions   *fakeDecisionRecorder
	mover       *fakeDefaultTimeoutTaskMover
	notify      *MockNotificationService
	svc         HumanGateDefaultTimeoutService
}

// freezeTimeNow pins the package-level timeNow (task_service.go) to frozenTime for the
// duration of the test and restores it on cleanup. Necessary because several OTHER
// tests in this package set timeNow directly without restoring it (a pre-existing gap,
// not introduced here) — without this, whether SweepExpiredDefaultGates sees "now" as
// the real clock or some earlier test's frozen value depends on execution order.
func freezeTimeNow(t *testing.T) {
	t.Helper()
	original := timeNow
	timeNow = func() time.Time { return frozenTime }
	t.Cleanup(func() { timeNow = original })
}

func newDefaultTimeoutHarness(t *testing.T) *defaultTimeoutHarness {
	freezeTimeNow(t)
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	projRepo := NewMockProjectRepository()
	decisions := newFakeDecisionRecorder()
	mover := newFakeDefaultTimeoutTaskMover()
	notify := NewMockNotificationService()
	svc := NewHumanGateDefaultTimeoutService(taskRepo, statusRepo, projRepo, decisions, mover, notify)
	return &defaultTimeoutHarness{
		taskRepo: taskRepo, statusRepo: statusRepo, projectRepo: projRepo,
		decisions: decisions, mover: mover, notify: notify, svc: svc,
	}
}

// seedExpiredGate seeds a task on both the mock TaskRepository (so FindExpiredDefaultGates
// picks it up) and the fake task mover (so GetByID/MoveTask work), in a given status
// category, and returns its id.
func (h *defaultTimeoutHarness) seedExpiredGate(t *testing.T, category domain.StatusCategory) uuid.UUID {
	t.Helper()
	taskID := uuid.New()
	author := uuid.New()
	armedAt := frozenTime.Add(-100 * time.Hour)
	deadline := frozenTime.Add(-time.Hour)
	statusID := uuid.New()
	projectID := uuid.New()
	recDefault := "merge as-is"

	require.NoError(t, h.statusRepo.Create(context.Background(), &domain.TaskStatus{
		ID: statusID, ProjectID: projectID, Category: category, Slug: string(category),
	}))
	require.NoError(t, h.taskRepo.Create(context.Background(), &domain.Task{
		ID: taskID, ProjectID: projectID, StatusID: statusID, Title: "test task",
		HumanGate: true, HumanGateClass: domain.HumanGateClassSoft, HumanGateArmedAt: &armedAt,
		GateAuthor: &author, GateAuthorType: actorTypePtr(domain.ActorTypeAgent),
		RecommendedDefault: &recDefault, GateDeadline: &deadline,
	}))
	h.mover.seed(&domain.Task{ID: taskID, ProjectID: projectID, StatusID: statusID, Title: "test task"})
	return taskID
}

func actorTypePtr(a domain.ActorType) *domain.ActorType { return &a }

func TestHumanGateDefaultTimeoutService_NoCandidates_NoOp(t *testing.T) {
	h := newDefaultTimeoutHarness(t)
	n, err := h.svc.SweepExpiredDefaultGates(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestHumanGateDefaultTimeoutService_FindError_Propagates(t *testing.T) {
	h := newDefaultTimeoutHarness(t)
	h.taskRepo.errToReturn = errInjectedTest
	_, err := h.svc.SweepExpiredDefaultGates(context.Background())
	require.ErrorIs(t, err, errInjectedTest)
}

// TestHumanGateDefaultTimeoutService_RecordsCorrectDecisionInput is the core
// orchestration proof: the sweep must record DecidedBy=the gate's own author (not a
// system sentinel — applying the default enacts THEIR predeclared answer), the new
// default_applied provenance, channel=mesh, and the recommended_default text as the
// quote.
func TestHumanGateDefaultTimeoutService_RecordsCorrectDecisionInput(t *testing.T) {
	h := newDefaultTimeoutHarness(t)
	// Need the author id back out — seed manually instead of via the helper so the
	// test can assert against it.
	taskID := uuid.New()
	author := uuid.New()
	armedAt := frozenTime.Add(-100 * time.Hour)
	deadline := frozenTime.Add(-time.Hour)
	statusID := uuid.New()
	projectID := uuid.New()
	recDefault := "merge; gateway inactive"
	require.NoError(t, h.statusRepo.Create(context.Background(), &domain.TaskStatus{
		ID: statusID, ProjectID: projectID, Category: domain.StatusCategoryInProgress,
	}))
	require.NoError(t, h.taskRepo.Create(context.Background(), &domain.Task{
		ID: taskID, ProjectID: projectID, StatusID: statusID,
		HumanGate: true, HumanGateClass: domain.HumanGateClassSoft, HumanGateArmedAt: &armedAt,
		GateAuthor: &author, GateAuthorType: actorTypePtr(domain.ActorTypeAgent),
		RecommendedDefault: &recDefault, GateDeadline: &deadline,
	}))
	h.mover.seed(&domain.Task{ID: taskID, ProjectID: projectID, StatusID: statusID, Title: "t"})

	n, err := h.svc.SweepExpiredDefaultGates(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	call, ok := h.decisions.callFor(taskID)
	require.True(t, ok, "must have called RecordHumanGateDecision for this task")
	assert.Equal(t, author, call.DecidedBy)
	assert.Equal(t, domain.HumanGateProvenanceDefaultApplied, call.Provenance)
	assert.Equal(t, domain.HumanGateChannelMesh, call.Channel)
	require.NotNil(t, call.Quote)
	assert.Equal(t, recDefault, *call.Quote)
	require.NotNil(t, call.CanonicalKey)
	assert.Contains(t, *call.CanonicalKey, taskID.String())
}

func TestHumanGateDefaultTimeoutService_MovesTriageTaskToTodo(t *testing.T) {
	h := newDefaultTimeoutHarness(t)
	taskID := h.seedExpiredGate(t, domain.StatusCategoryTriage)

	todoStatusID := uuid.New()
	task, _ := h.mover.GetByID(context.Background(), taskID)
	require.NoError(t, h.statusRepo.Create(context.Background(), &domain.TaskStatus{
		ID: todoStatusID, ProjectID: task.ProjectID, Category: domain.StatusCategoryTodo,
	}))

	n, err := h.svc.SweepExpiredDefaultGates(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 1, h.mover.moveCallsFor(taskID), "a task frozen in triage must be moved to todo when its default applies")
}

func TestHumanGateDefaultTimeoutService_AlreadyTodo_NoMoveCall(t *testing.T) {
	h := newDefaultTimeoutHarness(t)
	taskID := h.seedExpiredGate(t, domain.StatusCategoryTodo)

	n, err := h.svc.SweepExpiredDefaultGates(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 0, h.mover.moveCallsFor(taskID), "already todo — moving would be a no-op status change and a spurious activity-log entry")
}

func TestHumanGateDefaultTimeoutService_DoneTask_NotReopened(t *testing.T) {
	h := newDefaultTimeoutHarness(t)
	taskID := h.seedExpiredGate(t, domain.StatusCategoryDone)

	n, err := h.svc.SweepExpiredDefaultGates(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n, "the decision is still recorded and the gate still released even though status is left alone")
	assert.Equal(t, 0, h.mover.moveCallsFor(taskID), "applying a default is not a reason to reopen work a human already finished")
}

func TestHumanGateDefaultTimeoutService_CancelledTask_NotReopened(t *testing.T) {
	h := newDefaultTimeoutHarness(t)
	taskID := h.seedExpiredGate(t, domain.StatusCategoryCancelled)

	n, err := h.svc.SweepExpiredDefaultGates(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 0, h.mover.moveCallsFor(taskID))
}

func TestHumanGateDefaultTimeoutService_NotifiesWorkspaceBroadcast(t *testing.T) {
	h := newDefaultTimeoutHarness(t)
	taskID := h.seedExpiredGate(t, domain.StatusCategoryInProgress)
	task, _ := h.mover.GetByID(context.Background(), taskID)
	wsID := uuid.New()
	require.NoError(t, h.projectRepo.Create(context.Background(), &domain.Project{ID: task.ProjectID, WorkspaceID: wsID}))

	_, err := h.svc.SweepExpiredDefaultGates(context.Background())
	require.NoError(t, err)

	require.Len(t, h.notify.calls, 1)
	ev := h.notify.calls[0]
	assert.Equal(t, "task.human_gate_default_applied", ev.EventType)
	assert.Equal(t, wsID, ev.WorkspaceID)
	assert.Nil(t, ev.TargetUserID, "broadcast, not targeted — reaches Pavel without the sweep needing to know his user id")
	require.NotNil(t, ev.TaskID)
	assert.Equal(t, taskID, *ev.TaskID)
}

// TestHumanGateDefaultTimeoutService_OneFailureDoesNotDropTheRest proves the
// best-effort-per-row posture: a single candidate's RecordHumanGateDecision failure is
// logged and skipped, never stops the sweep from applying every OTHER expired gate in
// the same batch.
func TestHumanGateDefaultTimeoutService_OneFailureDoesNotDropTheRest(t *testing.T) {
	h := newDefaultTimeoutHarness(t)
	failingID := h.seedExpiredGate(t, domain.StatusCategoryInProgress)
	okID := h.seedExpiredGate(t, domain.StatusCategoryInProgress)
	h.decisions.failFor[failingID] = true

	n, err := h.svc.SweepExpiredDefaultGates(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only the successful candidate counts")

	_, failingWasCalled := h.decisions.callFor(failingID)
	_, okWasCalled := h.decisions.callFor(okID)
	assert.True(t, failingWasCalled, "the failing candidate must still have been attempted")
	assert.True(t, okWasCalled, "the OTHER candidate must not be skipped just because an earlier one failed")
}

// TestHumanGateDefaultTimeoutService_NilOptionalDeps_StillRecordsDecision proves the
// service degrades gracefully rather than panicking when notifySvc/projectRepo are nil
// (the documented optional-dependency posture, matching commentService's own).
func TestHumanGateDefaultTimeoutService_NilOptionalDeps_StillRecordsDecision(t *testing.T) {
	freezeTimeNow(t)
	taskRepo := NewMockTaskRepository()
	decisions := newFakeDecisionRecorder()
	mover := newFakeDefaultTimeoutTaskMover()
	svc := NewHumanGateDefaultTimeoutService(taskRepo, nil, nil, decisions, mover, nil)

	taskID := uuid.New()
	author := uuid.New()
	armedAt := frozenTime.Add(-100 * time.Hour)
	deadline := frozenTime.Add(-time.Hour)
	recDefault := "merge as-is"
	require.NoError(t, taskRepo.Create(context.Background(), &domain.Task{
		ID: taskID, HumanGate: true, HumanGateClass: domain.HumanGateClassSoft,
		HumanGateArmedAt: &armedAt, GateAuthor: &author, GateAuthorType: actorTypePtr(domain.ActorTypeAgent),
		RecommendedDefault: &recDefault, GateDeadline: &deadline,
	}))
	mover.seed(&domain.Task{ID: taskID})

	require.NotPanics(t, func() {
		n, err := svc.SweepExpiredDefaultGates(context.Background())
		require.NoError(t, err)
		assert.Equal(t, 1, n)
	})
	_, called := decisions.callFor(taskID)
	assert.True(t, called)
}
