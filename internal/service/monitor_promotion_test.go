package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ---------------------------------------------------------------------------
// Monitor promotion service tests.
//
// Contract (task ff9431fe, parent bug e1b46862):
//   - A backlog task labelled "kind:monitor" with a past due_date is promoted
//     to the project's todo-category status.
//   - A backlog task labelled "kind:monitor" with a future due_date is left alone.
//   - A backlog task with a past due_date but WITHOUT "kind:monitor" is left
//     alone (negative control — proves we didn't gate on due_date alone).
//   - Tasks outside "backlog" (e.g. todo/in_progress) are never touched by this
//     sweep, regardless of due_date — existing deadline semantics are preserved.
// ---------------------------------------------------------------------------

type monitorHarness struct {
	taskRepo    *MockTaskRepository
	statusRepo  *MockTaskStatusRepository
	commentRepo *MockCommentRepository
	mover       *mockLeaseTaskMover
	svc         MonitorPromotionService
}

func newMonitorHarness() *monitorHarness {
	h := &monitorHarness{
		taskRepo:    NewMockTaskRepository(),
		statusRepo:  NewMockTaskStatusRepository(),
		commentRepo: NewMockCommentRepository(),
		mover:       &mockLeaseTaskMover{},
	}
	h.taskRepo.WithStatusCategoryLookup(h.statusRepo)
	h.svc = &monitorPromotionService{
		taskRepo:    h.taskRepo,
		statusRepo:  h.statusRepo,
		commentRepo: h.commentRepo,
		taskMover:   h.mover,
	}
	return h
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
		Title:     "monitor task",
		Labels:    labels,
		DueDate:   dueDate,
	}
	if err := h.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("addTask: %v", err)
	}
	return task
}

func TestMonitorPromotion_PastDueKindMonitor_PromotedToTodo(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, domain.StatusCategoryTodo)

	past := time.Now().Add(-1 * time.Hour)
	task := h.addTask(t, projectID, backlog.ID, []string{"kind:monitor"}, &past)

	n, err := h.svc.SweepDueMonitorTasks(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
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

func TestMonitorPromotion_FutureDueKindMonitor_NotPromoted(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, domain.StatusCategoryTodo)

	future := time.Now().Add(1 * time.Hour)
	h.addTask(t, projectID, backlog.ID, []string{"kind:monitor"}, &future)

	n, err := h.svc.SweepDueMonitorTasks(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("future due_date must not be promoted, got n=%d", n)
	}
	if len(h.mover.movesSeen) != 0 {
		t.Errorf("MoveTask should not have been called")
	}
}

func TestMonitorPromotion_PastDueNoKindMonitorLabel_NotPromoted(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	h.addStatus(t, projectID, domain.StatusCategoryTodo)

	past := time.Now().Add(-1 * time.Hour)
	// No "kind:monitor" label — an ordinary backlog park (or freeze/no-promote
	// park) must NOT be gated on due_date alone.
	h.addTask(t, projectID, backlog.ID, []string{"freeze"}, &past)

	n, err := h.svc.SweepDueMonitorTasks(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("backlog task without kind:monitor must not be auto-promoted, got n=%d", n)
	}
	if len(h.mover.movesSeen) != 0 {
		t.Errorf("MoveTask should not have been called")
	}
}

func TestMonitorPromotion_TodoAndInProgressTasks_Unaffected(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	todo := h.addStatus(t, projectID, domain.StatusCategoryTodo)
	inProgress := h.addStatus(t, projectID, domain.StatusCategoryInProgress)

	past := time.Now().Add(-1 * time.Hour)
	h.addTask(t, projectID, todo.ID, []string{"kind:monitor"}, &past)
	h.addTask(t, projectID, inProgress.ID, []string{"kind:monitor"}, &past)

	n, err := h.svc.SweepDueMonitorTasks(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("sweep must only ever touch backlog-category tasks, got n=%d", n)
	}
	if len(h.mover.movesSeen) != 0 {
		t.Errorf("MoveTask should not have been called for todo/in_progress tasks")
	}
}

func TestMonitorPromotion_NoTodoStatus_SkipsTask(t *testing.T) {
	h := newMonitorHarness()
	projectID := uuid.New()
	backlog := h.addStatus(t, projectID, domain.StatusCategoryBacklog)
	// No todo status registered in this project.

	past := time.Now().Add(-1 * time.Hour)
	h.addTask(t, projectID, backlog.ID, []string{"kind:monitor"}, &past)

	n, err := h.svc.SweepDueMonitorTasks(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 (skipped, no todo status), got %d", n)
	}
	if len(h.mover.movesSeen) != 0 {
		t.Errorf("MoveTask should not have been called")
	}
}

func TestMonitorPromotion_NoDueTasks_ReturnsZero(t *testing.T) {
	h := newMonitorHarness()
	n, err := h.svc.SweepDueMonitorTasks(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0, got %d", n)
	}
}
