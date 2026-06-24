package service

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// setupShippedTask creates a project, a done status, a todo status, and an
// in_progress status so tests can exercise the shipped guard without boilerplate.
type shippedEnv struct {
	svc        TaskService
	taskRepo   *MockTaskRepository
	statusRepo *MockTaskStatusRepository
	projectID  uuid.UUID
	doneID     uuid.UUID
	todoID     uuid.UUID
	inProgID   uuid.UUID
}

func newShippedEnv(t *testing.T) *shippedEnv {
	t.Helper()
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	projectID := uuid.New()

	makeStatus := func(cat domain.StatusCategory) uuid.UUID {
		st := &domain.TaskStatus{ID: uuid.New(), ProjectID: projectID, Category: cat, Name: string(cat)}
		if err := statusRepo.Create(context.Background(), st); err != nil {
			t.Fatalf("create status %s: %v", cat, err)
		}
		return st.ID
	}
	doneID := makeStatus(domain.StatusCategoryDone)
	todoID := makeStatus(domain.StatusCategoryTodo)
	inProgID := makeStatus(domain.StatusCategoryInProgress)

	svc := NewTaskService(taskRepo, statusRepo, nil, nil)
	return &shippedEnv{
		svc: svc, taskRepo: taskRepo, statusRepo: statusRepo,
		projectID: projectID, doneID: doneID, todoID: todoID, inProgID: inProgID,
	}
}

func (e *shippedEnv) createTask(t *testing.T, shipped bool, statusID uuid.UUID) *domain.Task {
	t.Helper()
	task := &domain.Task{
		ID:        uuid.New(),
		ProjectID: e.projectID,
		StatusID:  statusID,
		Title:     "test task",
		IsShipped: shipped,
	}
	if err := e.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return task
}

// TestShipped_BlocksMoveToNonDone verifies that a shipped task cannot be moved to todo.
func TestShipped_BlocksMoveToNonDone(t *testing.T) {
	e := newShippedEnv(t)
	task := e.createTask(t, true, e.doneID)

	err := e.svc.MoveTask(context.Background(), task.ID, MoveTaskInput{StatusID: &e.todoID})
	if err == nil {
		t.Fatal("expected TaskShippedError, got nil")
	}
	var shippedErr *TaskShippedError
	if !isError[*TaskShippedError](err, &shippedErr) {
		t.Fatalf("expected TaskShippedError, got %T: %v", err, err)
	}
}

// TestShipped_BlocksMoveToInProgress verifies a shipped task cannot go to in_progress.
func TestShipped_BlocksMoveToInProgress(t *testing.T) {
	e := newShippedEnv(t)
	task := e.createTask(t, true, e.doneID)

	err := e.svc.MoveTask(context.Background(), task.ID, MoveTaskInput{StatusID: &e.inProgID})
	if err == nil {
		t.Fatal("expected TaskShippedError, got nil")
	}
	var shippedErr *TaskShippedError
	if !isError[*TaskShippedError](err, &shippedErr) {
		t.Fatalf("expected TaskShippedError, got %T: %v", err, err)
	}
}

// TestShipped_AllowsMoveToAnotherDoneStatus verifies that a shipped task CAN move
// between done-category statuses (e.g. done → done-archived).
func TestShipped_AllowsMoveToAnotherDoneStatus(t *testing.T) {
	e := newShippedEnv(t)
	done2 := &domain.TaskStatus{ID: uuid.New(), ProjectID: e.projectID, Category: domain.StatusCategoryDone, Name: "done-archived"}
	if err := e.statusRepo.Create(context.Background(), done2); err != nil {
		t.Fatal(err)
	}
	task := e.createTask(t, true, e.doneID)

	if err := e.svc.MoveTask(context.Background(), task.ID, MoveTaskInput{StatusID: &done2.ID}); err != nil {
		t.Fatalf("unexpected error moving shipped task between done statuses: %v", err)
	}
}

// TestNotShipped_MovesFreely verifies that an unshipped task is unaffected.
func TestNotShipped_MovesFreely(t *testing.T) {
	e := newShippedEnv(t)
	task := e.createTask(t, false, e.doneID)

	if err := e.svc.MoveTask(context.Background(), task.ID, MoveTaskInput{StatusID: &e.todoID}); err != nil {
		t.Fatalf("unexpected error moving non-shipped task: %v", err)
	}
}

// TestShipTask_SetsFlag verifies that ShipTask(true) marks the task as shipped.
func TestShipTask_SetsFlag(t *testing.T) {
	e := newShippedEnv(t)
	task := e.createTask(t, false, e.doneID)

	if err := e.svc.ShipTask(context.Background(), task.ID, true); err != nil {
		t.Fatalf("ShipTask(true) failed: %v", err)
	}
	got, _ := e.taskRepo.GetByID(context.Background(), task.ID)
	if got == nil || !got.IsShipped {
		t.Error("expected task.IsShipped=true after ShipTask(true)")
	}
}

// TestShipTask_ClearsFlag verifies that ShipTask(false) unships the task.
func TestShipTask_ClearsFlag(t *testing.T) {
	e := newShippedEnv(t)
	task := e.createTask(t, true, e.doneID)

	if err := e.svc.ShipTask(context.Background(), task.ID, false); err != nil {
		t.Fatalf("ShipTask(false) failed: %v", err)
	}
	got, _ := e.taskRepo.GetByID(context.Background(), task.ID)
	if got == nil || got.IsShipped {
		t.Error("expected task.IsShipped=false after ShipTask(false)")
	}
}

// TestUnship_ThenMove verifies that after clearing the shipped flag, the task can be reopened.
func TestUnship_ThenMove(t *testing.T) {
	e := newShippedEnv(t)
	task := e.createTask(t, true, e.doneID)

	if err := e.svc.ShipTask(context.Background(), task.ID, false); err != nil {
		t.Fatalf("ShipTask(false): %v", err)
	}
	if err := e.svc.MoveTask(context.Background(), task.ID, MoveTaskInput{StatusID: &e.todoID}); err != nil {
		t.Fatalf("MoveTask after unship: %v", err)
	}
}

// isError is a tiny generic helper to avoid importing errors in each test.
func isError[T error](err error, target *T) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(T); ok {
		*target = e
		return true
	}
	return false
}
