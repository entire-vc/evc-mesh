package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ---------------------------------------------------------------------------
// Backlog due-date promotion tests.
//
// Contract as of #559270cf (was: task ff9431fe, parent bug e1b46862 — kind:monitor
// only). A backlog task whose due_date has passed is promoted to the project's
// todo-category status, WHATEVER its labels, unless one of four guards says no:
//
//   - a freeze-class label (freeze / no-intake-promote / no-promote / golden /
//     eval-harness) — a date does not outrank a human's "stay in backlog";
//   - human_gate armed — a system actor may not move it, and the human IS the wake-up;
//   - is_shipped — likewise unmovable by a system actor off done;
//   - an open `blocks` dependency — the event it waits on has not happened.
//
// Tasks outside backlog are never touched whatever their due_date: due_date remains an
// ordinary deadline field on a card that is already being worked.
//
// The label-negative-control this file used to carry ("past due + no kind:monitor ⇒ not
// promoted") is deliberately GONE — it asserted exactly the behaviour #559270cf
// removed. It was written with a `freeze` label, so it would have stayed green after
// the widening while testing something else entirely; keeping it would have meant
// carrying a passing test whose stated reason was false. Its two real halves live on
// below as PastDueUnlabelled_Promoted (the widening) and PastDueFreezeLabel_NotPromoted
// (the guard that actually kept it green).
// ---------------------------------------------------------------------------

type monitorHarness struct {
	taskRepo     *MockTaskRepository
	statusRepo   *MockTaskStatusRepository
	commentRepo  *MockCommentRepository
	depRepo      *MockTaskDependencyRepository
	activityRepo *MockActivityLogRepository
	mover        *mockLeaseTaskMover
	svc          MonitorPromotionService
}

func newMonitorHarness() *monitorHarness {
	h := &monitorHarness{
		taskRepo:     NewMockTaskRepository(),
		statusRepo:   NewMockTaskStatusRepository(),
		commentRepo:  NewMockCommentRepository(),
		depRepo:      NewMockTaskDependencyRepository(),
		activityRepo: NewMockActivityLogRepository(),
		mover:        &mockLeaseTaskMover{},
	}
	h.taskRepo.WithStatusCategoryLookup(h.statusRepo)
	h.svc = &monitorPromotionService{
		taskRepo:     h.taskRepo,
		statusRepo:   h.statusRepo,
		commentRepo:  h.commentRepo,
		depRepo:      h.depRepo,
		activityRepo: h.activityRepo,
		taskMover:    h.mover,
	}
	return h
}

// addMove records a task.moved activity-log entry (old status name -> new status
// name), for exercising the demotion-into-backlog guard. Mirrors
// backlog_promotion_advisory_test.go's addMove — same event shape, same reader.
func (h *monitorHarness) addMove(t *testing.T, task *domain.Task, oldName, newName string, when time.Time) {
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

func (h *monitorHarness) addStatus(t *testing.T, projectID uuid.UUID, category domain.StatusCategory) *domain.TaskStatus {
	t.Helper()
	st := &domain.TaskStatus{
		ID:        uuid.New(),
		ProjectID: projectID,
		Name:      string(category),
		Category:  category,
	}
	if err := h.statusRepo.Create(context.Background(), st); err != nil {
		t.Fatalf("addStatus: %v", err)
	}
	return st
}

func (h *monitorHarness) addTask(t *testing.T, projectID, statusID uuid.UUID, labels []string, dueDate *time.Time) *domain.Task {
	t.Helper()
	task := &domain.Task{
		ID:        uuid.New(),
		ProjectID: projectID,
		StatusID:  statusID,
		Title:     "parked task",
		Labels:    labels,
		DueDate:   dueDate,
	}
	if err := h.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("addTask: %v", err)
	}
	return task
}

// addBlocks records a `blocks` edge: task depends on blocker.
func (h *monitorHarness) addBlocks(t *testing.T, taskID, blockerID uuid.UUID) {
	t.Helper()
	dep := &domain.TaskDependency{
		ID:              uuid.New(),
		TaskID:          taskID,
		DependsOnTaskID: blockerID,
		DependencyType:  domain.DependencyTypeBlocks,
	}
	if err := h.depRepo.Create(context.Background(), dep); err != nil {
		t.Fatalf("addBlocks: %v", err)
	}
}

func (h *monitorHarness) sweep(t *testing.T) int {
	t.Helper()
	n, err := h.svc.SweepDueBacklogTasks(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return n
}

func hourAgo() *time.Time   { d := time.Now().Add(-1 * time.Hour); return &d }
func hourAhead() *time.Time { d := time.Now().Add(1 * time.Hour); return &d }

func TestBacklogPromotion_PastDueKindMonitor_PromotedToTodo(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, domain.StatusCategoryTodo)

	task := h.addTask(t, projectID, backlog.ID, []string{"kind:monitor"}, hourAgo())

	if n := h.sweep(t); n != 1 {
		t.Fatalf("expected 1 promoted, got %d", n)
	}
	if len(h.mover.movesSeen) != 1 || h.mover.movesSeen[0] != task.ID {
		t.Errorf("MoveTask not called with correct task ID, got %v", h.mover.movesSeen)
	}

	h.commentRepo.mu.Lock()
	numComments := len(h.commentRepo.items)
	h.commentRepo.mu.Unlock()
	if numComments != 1 {
		t.Errorf("expected 1 auto-unpark comment, got %d", numComments)
	}
}

// The widening itself. Before #559270cf this card was invisible to the sweeper: it
// carries a due_date that has passed and nothing at all listened to it. This is the
// case the audit counted 8 of on prod.
func TestBacklogPromotion_PastDueUnlabelled_Promoted(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, domain.StatusCategoryTodo)

	task := h.addTask(t, projectID, backlog.ID, nil, hourAgo())

	if n := h.sweep(t); n != 1 {
		t.Fatalf("a dated backlog card with no labels must be promoted, got n=%d", n)
	}
	if len(h.mover.movesSeen) != 1 || h.mover.movesSeen[0] != task.ID {
		t.Errorf("MoveTask not called for the unlabelled task, got %v", h.mover.movesSeen)
	}
}

func TestBacklogPromotion_PastDuePhaseVerify_Promoted(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, domain.StatusCategoryTodo)

	h.addTask(t, projectID, backlog.ID, []string{"phase:verify"}, hourAgo())

	if n := h.sweep(t); n != 1 {
		t.Fatalf("phase:verify park with a passed due_date must be promoted, got n=%d", n)
	}
}

func TestBacklogPromotion_FutureDue_NotPromoted(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, domain.StatusCategoryTodo)

	h.addTask(t, projectID, backlog.ID, []string{"kind:monitor"}, hourAhead())

	if n := h.sweep(t); n != 0 {
		t.Errorf("future due_date must not be promoted, got n=%d", n)
	}
	if len(h.mover.movesSeen) != 0 {
		t.Errorf("MoveTask should not have been called")
	}
}

// Guard 1. A human freeze outranks the clock. Each freeze-class label is asserted
// separately: they are one map in the source, and a subset check would pass while the
// rest of the map was silently dropped.
func TestBacklogPromotion_PastDueFreezeLabel_NotPromoted(t *testing.T) {
	for _, label := range []string{"freeze", "no-promote", "no-intake-promote", "golden", "eval-harness"} {
		t.Run(label, func(t *testing.T) {
			h := newMonitorHarness()
			projectID := uuid.New()
			backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
			h.addStatus(t, projectID, domain.StatusCategoryTodo)

			h.addTask(t, projectID, backlog.ID, []string{label}, hourAgo())

			if n := h.sweep(t); n != 0 {
				t.Errorf("%q must survive a passed due_date, got n=%d", label, n)
			}
			if len(h.mover.movesSeen) != 0 {
				t.Errorf("MoveTask should not have been called for %q", label)
			}
		})
	}
}

// Guard 1b. A freeze label wins even when kind:monitor is also present — the card was
// parked by the reaper and THEN frozen by a human, and the human's word is later.
func TestBacklogPromotion_FreezeBeatsKindMonitor(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, domain.StatusCategoryTodo)

	h.addTask(t, projectID, backlog.ID, []string{"kind:monitor", "freeze"}, hourAgo())

	if n := h.sweep(t); n != 0 {
		t.Errorf("freeze must outrank kind:monitor, got n=%d", n)
	}
}

// Guard 2. A system actor is FORBIDDEN by MoveTask to move a human_gated task to
// backlog/done/cancelled, and selecting one here would retry forever — the shape that
// made the lease reaper fail to park #2921ff07 145 times.
func TestBacklogPromotion_HumanGateArmed_NotPromoted(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, domain.StatusCategoryTodo)

	task := h.addTask(t, projectID, backlog.ID, []string{"kind:monitor"}, hourAgo())
	task.HumanGate = true
	if err := h.taskRepo.Update(context.Background(), task); err != nil {
		t.Fatalf("arm gate: %v", err)
	}

	if n := h.sweep(t); n != 0 {
		t.Errorf("human_gate armed must not be promoted, got n=%d", n)
	}
	if len(h.mover.movesSeen) != 0 {
		t.Errorf("MoveTask must not even be attempted on a gated card")
	}
}

// Guard 3. Same reasoning as the gate: MoveTask refuses a shipped task anywhere but
// done, so attempting the move would spin rather than terminate.
func TestBacklogPromotion_Shipped_NotPromoted(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, domain.StatusCategoryTodo)

	task := h.addTask(t, projectID, backlog.ID, nil, hourAgo())
	task.IsShipped = true
	if err := h.taskRepo.Update(context.Background(), task); err != nil {
		t.Fatalf("mark shipped: %v", err)
	}

	if n := h.sweep(t); n != 0 {
		t.Errorf("shipped task must not be promoted, got n=%d", n)
	}
	if len(h.mover.movesSeen) != 0 {
		t.Errorf("MoveTask must not even be attempted on a shipped card")
	}
}

// Guard 4. An open blocker means the event the card waits on has not happened; the
// date is only a backstop.
func TestBacklogPromotion_OpenBlocksDependency_NotPromoted(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	todo := h.addStatus(t, projectID, domain.StatusCategoryTodo)

	blocker := h.addTask(t, projectID, todo.ID, nil, nil) // still open
	task := h.addTask(t, projectID, backlog.ID, []string{"kind:monitor"}, hourAgo())
	h.addBlocks(t, task.ID, blocker.ID)

	if n := h.sweep(t); n != 0 {
		t.Errorf("an open blocks dependency must hold the card, got n=%d", n)
	}
	if len(h.mover.movesSeen) != 0 {
		t.Errorf("MoveTask should not have been called")
	}
}

// Positive control for guard 4: the SAME card, with the SAME edge, promotes once the
// blocker closes. Without this arm, a guard that refused every card carrying any
// dependency would be indistinguishable from one that reads the blocker's status.
func TestBacklogPromotion_ClosedBlocksDependency_Promoted(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, domain.StatusCategoryTodo)
	done := h.addStatus(t, projectID, domain.StatusCategoryDone)

	blocker := h.addTask(t, projectID, done.ID, nil, nil)
	task := h.addTask(t, projectID, backlog.ID, []string{"kind:monitor"}, hourAgo())
	h.addBlocks(t, task.ID, blocker.ID)

	if n := h.sweep(t); n != 1 {
		t.Fatalf("a cleared blocker must not hold the card, got n=%d", n)
	}
}

// A relates_to edge is not a blocking relationship and must not hold a woken card
// down — the narrower `blocks` filter is the point, not an accident.
func TestBacklogPromotion_RelatesToDependency_DoesNotBlock(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	todo := h.addStatus(t, projectID, domain.StatusCategoryTodo)

	other := h.addTask(t, projectID, todo.ID, nil, nil) // open, but only related
	task := h.addTask(t, projectID, backlog.ID, nil, hourAgo())
	dep := &domain.TaskDependency{
		ID:              uuid.New(),
		TaskID:          task.ID,
		DependsOnTaskID: other.ID,
		DependencyType:  domain.DependencyTypeRelatesTo,
	}
	if err := h.depRepo.Create(context.Background(), dep); err != nil {
		t.Fatalf("create relates_to: %v", err)
	}

	if n := h.sweep(t); n != 1 {
		t.Errorf("relates_to must not block promotion, got n=%d", n)
	}
}

// ---------------------------------------------------------------------------
// Guard 5: demotion-into-backlog (#559270cf amendment, #1eb4fd7d).
// ---------------------------------------------------------------------------

// A card with no blocks dependency, demoted into backlog from a working status, must
// NOT be promoted just because "no open blockers" is vacuously true for a card that
// never had any (#b832d451: the July incident this guard exists to prevent — the
// original sweep undid such a park in 26 minutes).
func TestBacklogPromotion_DemotedNoDeps_NotPromoted(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, domain.StatusCategoryTodo)

	task := h.addTask(t, projectID, backlog.ID, nil, hourAgo())
	h.addMove(t, task, "review", "backlog", time.Now().Add(-2*time.Hour))

	if n := h.sweep(t); n != 0 {
		t.Errorf("a demoted, dependency-less card must not be promoted, got n=%d", n)
	}
	if len(h.mover.movesSeen) != 0 {
		t.Errorf("MoveTask should not have been called")
	}
}

// Positive control: a card that was NEVER moved (born in backlog) is genuine intake,
// not a deliberate park, and must still promote on a passed due_date. Without this
// arm, a guard that refused every backlog card would be indistinguishable from one
// that actually reads move history.
func TestBacklogPromotion_NeverMoved_Promoted(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, domain.StatusCategoryTodo)

	h.addTask(t, projectID, backlog.ID, nil, hourAgo())

	if n := h.sweep(t); n != 1 {
		t.Errorf("a card born in backlog must promote on due_date, got n=%d", n)
	}
}

// The #bbf3db92 trap, ported: a demoted card that ALSO carries a `blocks` edge must be
// judged by whether that edge is cleared, never by its move history — once the edge
// clears, that IS the informative event this due-date sweep exists to catch when the
// event-triggered auto-transition (tryUnblockTask) missed it because the blocker
// closed, or the edge was added, before the card was parked. A demotion-check that
// ran unconditionally here would silently re-park this class of card forever.
func TestBacklogPromotion_DemotedWithClearedBlocker_Promoted(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, domain.StatusCategoryTodo)
	done := h.addStatus(t, projectID, domain.StatusCategoryDone)

	blocker := h.addTask(t, projectID, done.ID, nil, nil)
	task := h.addTask(t, projectID, backlog.ID, nil, hourAgo())
	h.addBlocks(t, task.ID, blocker.ID)
	h.addMove(t, task, "review", "backlog", time.Now().Add(-2*time.Hour))

	if n := h.sweep(t); n != 1 {
		t.Errorf("a demoted card with a cleared blocker must still promote, got n=%d", n)
	}
}

// A demoted card with an OPEN blocker is caught by guard 4 (open blockers) before
// guard 5 is ever reached — the two guards must not fight over the same card in a way
// that changes the outcome depending on evaluation order.
func TestBacklogPromotion_DemotedWithOpenBlocker_NotPromoted(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	todo := h.addStatus(t, projectID, domain.StatusCategoryTodo)

	blocker := h.addTask(t, projectID, todo.ID, nil, nil) // still open
	task := h.addTask(t, projectID, backlog.ID, nil, hourAgo())
	h.addBlocks(t, task.ID, blocker.ID)
	h.addMove(t, task, "review", "backlog", time.Now().Add(-2*time.Hour))

	if n := h.sweep(t); n != 0 {
		t.Errorf("an open blocker must hold the card regardless of move history, got n=%d", n)
	}
}

// A move that landed IN backlog from another backlog-category status (e.g. one
// project's two backlog-shaped statuses) is not a demotion — the source was already
// backlog — and must not be read as one.
func TestBacklogPromotion_MovedBacklogToBacklog_Promoted(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, domain.StatusCategoryTodo)

	task := h.addTask(t, projectID, backlog.ID, nil, hourAgo())
	h.addMove(t, task, "backlog", "backlog", time.Now().Add(-2*time.Hour))

	if n := h.sweep(t); n != 1 {
		t.Errorf("a backlog-to-backlog move is not a demotion, got n=%d", n)
	}
}

// Fail-closed: no activity log repository wired at all must read as "possibly
// demoted", not "never demoted" — same reasoning as the depRepo==nil branch.
func TestBacklogPromotion_NilActivityRepo_NotPromoted(t *testing.T) {
	h := newMonitorHarness()
	h.svc = &monitorPromotionService{
		taskRepo:     h.taskRepo,
		statusRepo:   h.statusRepo,
		commentRepo:  h.commentRepo,
		depRepo:      h.depRepo,
		activityRepo: nil,
		taskMover:    h.mover,
	}
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, domain.StatusCategoryTodo)
	h.addTask(t, projectID, backlog.ID, nil, hourAgo())

	if n := h.sweep(t); n != 0 {
		t.Errorf("an unwired activity log repo must fail closed, got n=%d", n)
	}
}

// Fail-closed: an unreadable dependency repository must read as "blocked", never as
// "clear". "Could not look" is not "looked and saw nothing".
func TestBacklogPromotion_DependencyLookupFails_NotPromoted(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, domain.StatusCategoryTodo)
	h.addTask(t, projectID, backlog.ID, []string{"kind:monitor"}, hourAgo())

	h.depRepo.errToReturn = errors.New("db down")

	if n := h.sweep(t); n != 0 {
		t.Errorf("unreadable dependencies must fail closed, got n=%d", n)
	}
	if len(h.mover.movesSeen) != 0 {
		t.Errorf("MoveTask should not have been called")
	}
}

// Fail-closed: no dependency repository wired at all is the same answer.
func TestBacklogPromotion_NilDependencyRepo_NotPromoted(t *testing.T) {
	h := newMonitorHarness()
	h.svc = &monitorPromotionService{
		taskRepo:    h.taskRepo,
		statusRepo:  h.statusRepo,
		commentRepo: h.commentRepo,
		depRepo:     nil,
		taskMover:   h.mover,
	}
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, domain.StatusCategoryTodo)
	h.addTask(t, projectID, backlog.ID, []string{"kind:monitor"}, hourAgo())

	if n := h.sweep(t); n != 0 {
		t.Errorf("an unwired dependency repo must fail closed, got n=%d", n)
	}
}

func TestBacklogPromotion_TodoAndInProgressTasks_Unaffected(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	todo := h.addStatus(t, projectID, domain.StatusCategoryTodo)
	inProgress := h.addStatus(t, projectID, domain.StatusCategoryInProgress)

	h.addTask(t, projectID, todo.ID, []string{"kind:monitor"}, hourAgo())
	h.addTask(t, projectID, inProgress.ID, []string{"kind:monitor"}, hourAgo())

	if n := h.sweep(t); n != 0 {
		t.Errorf("sweep must only ever touch backlog-category tasks, got n=%d", n)
	}
	if len(h.mover.movesSeen) != 0 {
		t.Errorf("MoveTask should not have been called for todo/in_progress tasks")
	}
}

func TestBacklogPromotion_NoTodoStatus_SkipsTask(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	// No todo status registered in this project.

	h.addTask(t, projectID, backlog.ID, []string{"kind:monitor"}, hourAgo())

	if n := h.sweep(t); n != 0 {
		t.Errorf("expected 0 (skipped, no todo status), got %d", n)
	}
	if len(h.mover.movesSeen) != 0 {
		t.Errorf("MoveTask should not have been called")
	}
}

func TestBacklogPromotion_NoDueTasks_ReturnsZero(t *testing.T) {
	h := newMonitorHarness()
	if n := h.sweep(t); n != 0 {
		t.Errorf("expected 0, got %d", n)
	}
}

// ---------------------------------------------------------------------------
// start_after independence (sub4 of #e9ce6b91, blocked on sub1 #246b8fcc which
// added the column). FindDueBacklogTasks's SQL selects on due_date alone, and
// skipReason's five guards never read StartAfter either — this sweep was never
// wired to the field. These tests exist to catch a FUTURE regression that wires
// it in by accident, not to test anything currently branching on it.
// ---------------------------------------------------------------------------

// addTaskWithStartAfter mirrors addTask but also sets StartAfter, to exercise the
// sweep against a task carrying both time fields.
func (h *monitorHarness) addTaskWithStartAfter(t *testing.T, projectID, statusID uuid.UUID, labels []string, dueDate, startAfter *time.Time) *domain.Task {
	t.Helper()
	task := &domain.Task{
		ID:         uuid.New(),
		ProjectID:  projectID,
		StatusID:   statusID,
		Title:      "parked task",
		Labels:     labels,
		DueDate:    dueDate,
		StartAfter: startAfter,
	}
	if err := h.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("addTaskWithStartAfter: %v", err)
	}
	return task
}

// Red control: a FUTURE start_after must not hold a past-due backlog card down. If
// this sweep ever started treating start_after as a second due-date gate (the
// natural-looking but wrong move, since StartAfter is a "not-before" field
// elsewhere in the system), this is the test that would fail.
func TestBacklogPromotion_PastDueFutureStartAfter_Promoted(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, domain.StatusCategoryTodo)

	task := h.addTaskWithStartAfter(t, projectID, backlog.ID, nil, hourAgo(), hourAhead())

	if n := h.sweep(t); n != 1 {
		t.Fatalf("a future start_after must not hold a past-due backlog card, got n=%d", n)
	}
	if len(h.mover.movesSeen) != 1 || h.mover.movesSeen[0] != task.ID {
		t.Errorf("MoveTask not called for the task, got %v", h.mover.movesSeen)
	}
}

// Positive control, same shape but with start_after already in the past too —
// carrying both fields set must not break the ordinary promote path either.
func TestBacklogPromotion_PastDuePastStartAfter_Promoted(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, domain.StatusCategoryTodo)

	h.addTaskWithStartAfter(t, projectID, backlog.ID, nil, hourAgo(), hourAgo())

	if n := h.sweep(t); n != 1 {
		t.Fatalf("a past-due backlog card with a past start_after must still promote, got n=%d", n)
	}
}

// start_after with NO due_date at all must not be promoted — FindDueBacklogTasks's
// candidate query requires due_date IS NOT NULL; start_after is not a substitute
// wake-up trigger for this sweep (it is only a gate other lanes read, per sub1's
// doc comment on domain.Task.StartAfter).
func TestBacklogPromotion_StartAfterOnlyNoDueDate_NotPromoted(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, domain.StatusCategoryTodo)

	h.addTaskWithStartAfter(t, projectID, backlog.ID, nil, nil, hourAgo())

	if n := h.sweep(t); n != 0 {
		t.Errorf("start_after alone (no due_date) must not trigger promotion, got n=%d", n)
	}
	if len(h.mover.movesSeen) != 0 {
		t.Errorf("MoveTask should not have been called")
	}
}
