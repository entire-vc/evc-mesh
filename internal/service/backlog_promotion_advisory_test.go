package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ---------------------------------------------------------------------------
// Backlog promotion advisory rule tests (task #9f3f4064, parent #00327dc6).
//
// Contract under test — mirroring bob/scripts/mesh-intake-sweep.py:
//   - A task DEMOTED into backlog from a working status, with NO dependencies, is a
//     deliberate park and must NOT be advised as promotable (#b832d451: vacuously-true
//     empty-dep-set undid a park in 26 minutes).
//   - A task BORN in backlog (never moved), with no dependencies, IS promotable —
//     that is genuine intake, the positive control.
//   - The demotion-park guard must NOT protect a task that HAS dependencies, even one
//     with a parent_task_id and a real (cleared) dependency — was_deliberately_parked's
//     own dep_ids-empty gate means such a card is judged on dependency-clear alone
//     (#bbf3db92: a naive "last transition = demotion" check WITHOUT this gate would
//     wrongly protect this exact class of card, diverging from the live sweep, which
//     re-promoted it twice over 7.5h before the label route was added).
//   - Advisory only: SweepAdvisory never calls MoveTask (there is no mover wired in at
//     all — the type doesn't expose one).
// ---------------------------------------------------------------------------

type backlogHarness struct {
	taskRepo     *MockTaskRepository
	statusRepo   *MockTaskStatusRepository
	depRepo      *MockTaskDependencyRepository
	activityRepo *MockActivityLogRepository
	svc          BacklogPromotionAdvisoryService
}

func newBacklogHarness() *backlogHarness {
	h := &backlogHarness{
		taskRepo:     NewMockTaskRepository(),
		statusRepo:   NewMockTaskStatusRepository(),
		depRepo:      NewMockTaskDependencyRepository(),
		activityRepo: NewMockActivityLogRepository(),
	}
	h.taskRepo.WithStatusCategoryLookup(h.statusRepo)
	h.svc = NewBacklogPromotionAdvisoryService(h.taskRepo, h.statusRepo, h.depRepo, h.activityRepo)
	return h
}

func (h *backlogHarness) addStatus(t *testing.T, projectID uuid.UUID, name string, category domain.StatusCategory) *domain.TaskStatus {
	t.Helper()
	st := &domain.TaskStatus{ID: uuid.New(), ProjectID: projectID, Name: name, Category: category}
	if err := h.statusRepo.Create(context.Background(), st); err != nil {
		t.Fatalf("addStatus: %v", err)
	}
	return st
}

func (h *backlogHarness) addTask(t *testing.T, projectID, statusID uuid.UUID) *domain.Task {
	t.Helper()
	task := &domain.Task{
		ID:           uuid.New(),
		ProjectID:    projectID,
		StatusID:     statusID,
		Title:        "backlog task",
		AssigneeType: domain.AssigneeTypeAgent,
	}
	if err := h.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("addTask: %v", err)
	}
	return task
}

// addMove records a task.moved activity-log entry (old status name -> new status name).
func (h *backlogHarness) addMove(t *testing.T, task *domain.Task, oldName, newName string, when time.Time) {
	t.Helper()
	changes, err := json.Marshal(map[string]any{
		"status": map[string]string{"old": oldName, "new": newName},
	})
	if err != nil {
		t.Fatalf("marshal changes: %v", err)
	}
	entry := &domain.ActivityLog{
		ID:         uuid.New(),
		EntityType: "task",
		EntityID:   task.ID,
		Action:     "task.moved",
		ActorType:  domain.ActorTypeSystem,
		Changes:    changes,
		CreatedAt:  when,
	}
	if err := h.activityRepo.Create(context.Background(), entry); err != nil {
		t.Fatalf("addMove: %v", err)
	}
}

func (h *backlogHarness) addDependency(t *testing.T, taskID, dependsOnID uuid.UUID) {
	t.Helper()
	dep := &domain.TaskDependency{
		ID:              uuid.New(),
		TaskID:          taskID,
		DependsOnTaskID: dependsOnID,
		DependencyType:  domain.DependencyTypeBlocks,
		CreatedAt:       time.Now(),
	}
	if err := h.depRepo.Create(context.Background(), dep); err != nil {
		t.Fatalf("addDependency: %v", err)
	}
}

func decisionFor(t *testing.T, decisions []BacklogPromotionDecision, taskID uuid.UUID) BacklogPromotionDecision {
	t.Helper()
	for _, d := range decisions {
		if d.TaskID == taskID {
			return d
		}
	}
	t.Fatalf("no decision logged for task %s", taskID)
	return BacklogPromotionDecision{}
}

// --- Positive control: born in backlog, no deps → promotable ---------------------

func TestBacklogPromotionAdvisory_BornInBacklog_NoDeps_Promotable(t *testing.T) {
	h := newBacklogHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, "Backlog", domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, "Todo", domain.StatusCategoryTodo)

	task := h.addTask(t, projectID, backlog.ID)
	// No activity-log entries at all — genuinely never moved.

	decisions, err := h.svc.SweepAdvisory(context.Background())
	if err != nil {
		t.Fatalf("SweepAdvisory: %v", err)
	}
	got := decisionFor(t, decisions, task.ID)
	if !got.Promote {
		t.Fatalf("expected promote=true (born in backlog, no deps), got false, reason=%q", got.Reason)
	}
}

// --- Negative control 1: parked via demotion, no deps → NOT promotable -----------

func TestBacklogPromotionAdvisory_DemotedNoDeps_NotPromotable(t *testing.T) {
	h := newBacklogHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, "Backlog", domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, "In Progress", domain.StatusCategoryInProgress)
	h.addStatus(t, projectID, "Todo", domain.StatusCategoryTodo)

	task := h.addTask(t, projectID, backlog.ID)
	h.addMove(t, task, "In Progress", "Backlog", time.Now().Add(-26*time.Minute))
	// No dependencies — all_deps_cleared([]) would be vacuously true; this is
	// EXACTLY the #b832d451 shape (demoted, zero deps).

	decisions, err := h.svc.SweepAdvisory(context.Background())
	if err != nil {
		t.Fatalf("SweepAdvisory: %v", err)
	}
	got := decisionFor(t, decisions, task.ID)
	if got.Promote {
		t.Fatalf("expected promote=false (demoted, no deps = deliberate park), got true, reason=%q", got.Reason)
	}
}

// --- Negative control 2 (the #bbf3db92 trap): demoted + parented + real cleared
// dependency → the demotion-park guard must NOT fire; the card is judged on its
// (cleared) dependency alone, matching the live sweep's actual behaviour. This test
// specifically guards against a NAIVE port that checks "last transition = demotion"
// without was_deliberately_parked's own dep_ids-empty short-circuit — such a port
// would wrongly return promote=false here, diverging from the sweep. ---------------

func TestBacklogPromotionAdvisory_DemotedParentedWithClearedDep_StillPromotable(t *testing.T) {
	h := newBacklogHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, "Backlog", domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, "In Progress", domain.StatusCategoryInProgress)
	done := h.addStatus(t, projectID, "Done", domain.StatusCategoryDone)

	parent := h.addTask(t, projectID, backlog.ID)
	blocker := h.addTask(t, projectID, done.ID) // already cleared

	task := h.addTask(t, projectID, backlog.ID)
	task.ParentTaskID = &parent.ID
	if err := h.taskRepo.Update(context.Background(), task); err != nil {
		t.Fatalf("set parent: %v", err)
	}
	h.addDependency(t, task.ID, blocker.ID)
	// Demoted from a working status, same shape as the plain park case above — but
	// THIS card has a parent AND a real (cleared) dependency.
	h.addMove(t, task, "In Progress", "Backlog", time.Now().Add(-7*time.Hour))

	decisions, err := h.svc.SweepAdvisory(context.Background())
	if err != nil {
		t.Fatalf("SweepAdvisory: %v", err)
	}
	got := decisionFor(t, decisions, task.ID)
	if !got.Promote {
		t.Fatalf("expected promote=true (dependency-bearing card judged by dep-clear, not demotion) — "+
			"a false here means the demotion-park guard incorrectly fired on a card WITH dependencies "+
			"(the #bbf3db92 trap), got false, reason=%q", got.Reason)
	}
}

// --- Negative control variant: same shape, but the dependency is NOT yet cleared —
// must still not-promote, for the ordinary reason (unresolved dependency), not via
// the park guard. Distinguishes "correctly blocked" from "blocked for the wrong
// reason" (the two must not be conflated). ------------------------------------------

func TestBacklogPromotionAdvisory_DemotedParentedWithOpenDep_NotPromotable(t *testing.T) {
	h := newBacklogHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, "Backlog", domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, "In Progress", domain.StatusCategoryInProgress)

	parent := h.addTask(t, projectID, backlog.ID)
	blocker := h.addTask(t, projectID, backlog.ID) // still open, not cleared

	task := h.addTask(t, projectID, backlog.ID)
	task.ParentTaskID = &parent.ID
	if err := h.taskRepo.Update(context.Background(), task); err != nil {
		t.Fatalf("set parent: %v", err)
	}
	h.addDependency(t, task.ID, blocker.ID)
	h.addMove(t, task, "In Progress", "Backlog", time.Now().Add(-7*time.Hour))

	decisions, err := h.svc.SweepAdvisory(context.Background())
	if err != nil {
		t.Fatalf("SweepAdvisory: %v", err)
	}
	got := decisionFor(t, decisions, task.ID)
	if got.Promote {
		t.Fatalf("expected promote=false (open dependency), got true, reason=%q", got.Reason)
	}
}

// --- Label-based park (freeze/no-promote/etc.) is respected even with no move
// history at all — the other real-world park mechanism, and how #bbf3db92 was
// actually remediated (labels added to the card). -----------------------------------

func TestBacklogPromotionAdvisory_FreezeLabel_NotPromotable(t *testing.T) {
	h := newBacklogHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, "Backlog", domain.StatusCategoryBacklog)

	task := h.addTask(t, projectID, backlog.ID)
	task.Labels = []string{"freeze"}
	if err := h.taskRepo.Update(context.Background(), task); err != nil {
		t.Fatalf("set labels: %v", err)
	}

	decisions, err := h.svc.SweepAdvisory(context.Background())
	if err != nil {
		t.Fatalf("SweepAdvisory: %v", err)
	}
	got := decisionFor(t, decisions, task.ID)
	if got.Promote {
		t.Fatalf("expected promote=false (freeze label), got true, reason=%q", got.Reason)
	}
}

// --- Human-gate is respected, read directly off the task (server-authoritative). ---

func TestBacklogPromotionAdvisory_HumanGate_NotPromotable(t *testing.T) {
	h := newBacklogHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, "Backlog", domain.StatusCategoryBacklog)

	task := h.addTask(t, projectID, backlog.ID)
	task.HumanGate = true
	if err := h.taskRepo.Update(context.Background(), task); err != nil {
		t.Fatalf("set human_gate: %v", err)
	}

	decisions, err := h.svc.SweepAdvisory(context.Background())
	if err != nil {
		t.Fatalf("SweepAdvisory: %v", err)
	}
	got := decisionFor(t, decisions, task.ID)
	if got.Promote {
		t.Fatalf("expected promote=false (human_gate armed), got true, reason=%q", got.Reason)
	}
}

// --- Terminal-blocker-via-cancelled also clears the dependency (kept in lockstep
// with auto_transition.go's hasUnresolvedBlockers per the canonical dep-clear rule). -

func TestBacklogPromotionAdvisory_CancelledBlocker_ClearsDep(t *testing.T) {
	h := newBacklogHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, "Backlog", domain.StatusCategoryBacklog)
	cancelled := h.addStatus(t, projectID, "Cancelled", domain.StatusCategoryCancelled)

	blocker := h.addTask(t, projectID, cancelled.ID)
	task := h.addTask(t, projectID, backlog.ID)
	h.addDependency(t, task.ID, blocker.ID)

	decisions, err := h.svc.SweepAdvisory(context.Background())
	if err != nil {
		t.Fatalf("SweepAdvisory: %v", err)
	}
	got := decisionFor(t, decisions, task.ID)
	if !got.Promote {
		t.Fatalf("expected promote=true (cancelled blocker clears the dependency), got false, reason=%q", got.Reason)
	}
}
