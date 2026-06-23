package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ── minimal mock leaseTaskMover ────────────────────────────────────────────

type mockLeaseTaskMover struct {
	moveErr   error
	movesSeen []uuid.UUID
}

func (m *mockLeaseTaskMover) MoveTask(_ context.Context, taskID uuid.UUID, _ MoveTaskInput) error {
	m.movesSeen = append(m.movesSeen, taskID)
	return m.moveErr
}

// ── minimal mock AgentNotifyService ───────────────────────────────────────

type mockLeaseNotify struct {
	notified []uuid.UUID
}

func (m *mockLeaseNotify) NotifyAgent(_ context.Context, agentID uuid.UUID, _ AgentNotification) {
	m.notified = append(m.notified, agentID)
}

// ── helpers ────────────────────────────────────────────────────────────────

func reaperSetup(t *testing.T) (
	*MockTaskRepository,
	*MockTaskStatusRepository,
	*MockCommentRepository,
	*mockLeaseTaskMover,
	*mockLeaseNotify,
	CheckoutLeaseReaper,
) {
	t.Helper()
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	commentRepo := NewMockCommentRepository()
	mover := &mockLeaseTaskMover{}
	notify := &mockLeaseNotify{}

	// Wire using the internal constructor that accepts leaseTaskMover directly.
	reaper := &checkoutLeaseReaper{
		taskRepo:       taskRepo,
		statusRepo:     statusRepo,
		commentRepo:    commentRepo,
		taskMover:      mover,
		agentNotifySvc: notify,
	}
	return taskRepo, statusRepo, commentRepo, mover, notify, reaper
}

func addTodoStatus(t *testing.T, statusRepo *MockTaskStatusRepository, projectID uuid.UUID) *domain.TaskStatus {
	t.Helper()
	st := &domain.TaskStatus{
		ID:        uuid.New(),
		ProjectID: projectID,
		Name:      "todo",
		Category:  domain.StatusCategoryTodo,
	}
	if err := statusRepo.Create(context.Background(), st); err != nil {
		t.Fatalf("addTodoStatus: %v", err)
	}
	return st
}

func addExpiredInProgressTask(t *testing.T, taskRepo *MockTaskRepository, projectID uuid.UUID, assigneeType domain.AssigneeType) *domain.Task {
	t.Helper()
	past := time.Now().Add(-10 * time.Minute)
	assigneeID := uuid.New()
	task := &domain.Task{
		ID:              uuid.New(),
		ProjectID:       projectID,
		StatusID:        uuid.New(), // arbitrary in_progress status ID
		Title:           "wedged task",
		AssigneeID:      &assigneeID,
		AssigneeType:    assigneeType,
		CheckoutExpires: &past,
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("addExpiredInProgressTask: %v", err)
	}
	return task
}

// ── tests ──────────────────────────────────────────────────────────────────

func TestLeaseReaper_HappyPath_MovesToTodo(t *testing.T) {
	taskRepo, statusRepo, commentRepo, mover, notify, reaper := reaperSetup(t)

	projectID := uuid.New()
	addTodoStatus(t, statusRepo, projectID)
	task := addExpiredInProgressTask(t, taskRepo, projectID, domain.AssigneeTypeAgent)

	n, err := reaper.SweepExpiredLeases(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 moved, got %d", n)
	}
	if len(mover.movesSeen) != 1 || mover.movesSeen[0] != task.ID {
		t.Errorf("MoveTask not called with correct task ID")
	}

	commentRepo.mu.Lock()
	numComments := len(commentRepo.items)
	commentRepo.mu.Unlock()
	if numComments != 1 {
		t.Errorf("expected 1 system comment, got %d", numComments)
	}

	if len(notify.notified) != 1 || notify.notified[0] != *task.AssigneeID {
		t.Errorf("expected agent notification to task assignee")
	}
}

func TestLeaseReaper_NoExpiredTasks_ReturnsZero(t *testing.T) {
	_, _, _, mover, _, reaper := reaperSetup(t)

	n, err := reaper.SweepExpiredLeases(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0, got %d", n)
	}
	if len(mover.movesSeen) != 0 {
		t.Errorf("MoveTask should not have been called")
	}
}

func TestLeaseReaper_NoTodoStatus_SkipsTask(t *testing.T) {
	taskRepo, _, _, mover, _, reaper := reaperSetup(t)

	projectID := uuid.New()
	// No todo status registered — status repo has nothing for this project.
	addExpiredInProgressTask(t, taskRepo, projectID, domain.AssigneeTypeAgent)

	n, err := reaper.SweepExpiredLeases(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 (skipped), got %d", n)
	}
	if len(mover.movesSeen) != 0 {
		t.Errorf("MoveTask should not have been called")
	}
}

func TestLeaseReaper_MoveTaskFails_SkipsCount(t *testing.T) {
	taskRepo, statusRepo, commentRepo, mover, _, reaper := reaperSetup(t)

	projectID := uuid.New()
	addTodoStatus(t, statusRepo, projectID)
	addExpiredInProgressTask(t, taskRepo, projectID, domain.AssigneeTypeAgent)

	mover.moveErr = errors.New("simulated move failure")

	n, err := reaper.SweepExpiredLeases(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 (move failed), got %d", n)
	}
	commentRepo.mu.Lock()
	numComments := len(commentRepo.items)
	commentRepo.mu.Unlock()
	if numComments != 0 {
		t.Errorf("expected no comments on failed move")
	}
}

func TestLeaseReaper_MultipleTasksSameProject_TodoStatusCached(t *testing.T) {
	taskRepo, statusRepo, _, mover, _, reaper := reaperSetup(t)

	projectID := uuid.New()
	addTodoStatus(t, statusRepo, projectID)
	addExpiredInProgressTask(t, taskRepo, projectID, domain.AssigneeTypeAgent)
	addExpiredInProgressTask(t, taskRepo, projectID, domain.AssigneeTypeAgent)

	n, err := reaper.SweepExpiredLeases(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2, got %d", n)
	}
	if len(mover.movesSeen) != 2 {
		t.Errorf("expected 2 MoveTask calls, got %d", len(mover.movesSeen))
	}
	// ListByProject should have been called only once (second task hits cache).
	statusRepo.mu.Lock()
	calls := statusRepo.listByProjectCalls
	statusRepo.mu.Unlock()
	if calls != 1 {
		t.Errorf("expected 1 ListByProject call (cache hit), got %d", calls)
	}
}

func TestLeaseReaper_UserAssignee_NoAgentNotification(t *testing.T) {
	taskRepo, statusRepo, _, _, notify, reaper := reaperSetup(t)

	projectID := uuid.New()
	addTodoStatus(t, statusRepo, projectID)
	addExpiredInProgressTask(t, taskRepo, projectID, domain.AssigneeTypeUser)

	_, _ = reaper.SweepExpiredLeases(context.Background())
	if len(notify.notified) != 0 {
		t.Errorf("expected no agent notification for user-assigned task")
	}
}

func TestLeaseReaper_FutureCheckoutExpiry_NotSwept(t *testing.T) {
	taskRepo, statusRepo, _, mover, _, reaper := reaperSetup(t)

	projectID := uuid.New()
	addTodoStatus(t, statusRepo, projectID)

	// Task with checkout expiry in the future (active heartbeat).
	future := time.Now().Add(10 * time.Minute)
	assigneeID := uuid.New()
	active := &domain.Task{
		ID:              uuid.New(),
		ProjectID:       projectID,
		StatusID:        uuid.New(),
		Title:           "active task",
		AssigneeID:      &assigneeID,
		AssigneeType:    domain.AssigneeTypeAgent,
		CheckoutExpires: &future,
	}
	if err := taskRepo.Create(context.Background(), active); err != nil {
		t.Fatalf("create active task: %v", err)
	}

	n, err := reaper.SweepExpiredLeases(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("active task must not be swept, got n=%d", n)
	}
	if len(mover.movesSeen) != 0 {
		t.Errorf("MoveTask must not be called for active tasks")
	}
}
