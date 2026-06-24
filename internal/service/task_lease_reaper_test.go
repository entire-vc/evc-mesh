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

// ── test harness ──────────────────────────────────────────────────────────

type reaperHarness struct {
	taskRepo    *MockTaskRepository
	statusRepo  *MockTaskStatusRepository
	commentRepo *MockCommentRepository
	mover       *mockLeaseTaskMover
	notify      *mockLeaseNotify
	reaper      CheckoutLeaseReaper
}

func newReaperHarness() *reaperHarness {
	h := &reaperHarness{
		taskRepo:    NewMockTaskRepository(),
		statusRepo:  NewMockTaskStatusRepository(),
		commentRepo: NewMockCommentRepository(),
		mover:       &mockLeaseTaskMover{},
		notify:      &mockLeaseNotify{},
	}
	h.reaper = &checkoutLeaseReaper{
		taskRepo:       h.taskRepo,
		statusRepo:     h.statusRepo,
		commentRepo:    h.commentRepo,
		taskMover:      h.mover,
		agentNotifySvc: h.notify,
	}
	return h
}

func (h *reaperHarness) addTodoStatus(t *testing.T, projectID uuid.UUID) *domain.TaskStatus {
	t.Helper()
	st := &domain.TaskStatus{
		ID:        uuid.New(),
		ProjectID: projectID,
		Name:      "todo",
		Category:  domain.StatusCategoryTodo,
	}
	if err := h.statusRepo.Create(context.Background(), st); err != nil {
		t.Fatalf("addTodoStatus: %v", err)
	}
	return st
}

func (h *reaperHarness) addExpiredTask(t *testing.T, projectID uuid.UUID, at domain.AssigneeType) *domain.Task {
	t.Helper()
	past := time.Now().Add(-10 * time.Minute)
	aid := uuid.New()
	task := &domain.Task{
		ID:              uuid.New(),
		ProjectID:       projectID,
		StatusID:        uuid.New(),
		Title:           "wedged task",
		AssigneeID:      &aid,
		AssigneeType:    at,
		CheckoutExpires: &past,
	}
	if err := h.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("addExpiredTask: %v", err)
	}
	return task
}

// ── tests ──────────────────────────────────────────────────────────────────

func TestLeaseReaper_HappyPath_MovesToTodo(t *testing.T) {
	h := newReaperHarness()
	projectID := uuid.New()
	h.addTodoStatus(t, projectID)
	task := h.addExpiredTask(t, projectID, domain.AssigneeTypeAgent)

	n, err := h.reaper.SweepExpiredLeases(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 moved, got %d", n)
	}
	if len(h.mover.movesSeen) != 1 || h.mover.movesSeen[0] != task.ID {
		t.Errorf("MoveTask not called with correct task ID")
	}

	h.commentRepo.mu.Lock()
	numComments := len(h.commentRepo.items)
	h.commentRepo.mu.Unlock()
	if numComments != 1 {
		t.Errorf("expected 1 system comment, got %d", numComments)
	}

	if len(h.notify.notified) != 1 || h.notify.notified[0] != *task.AssigneeID {
		t.Errorf("expected agent notification to task assignee")
	}
}

func TestLeaseReaper_NoExpiredTasks_ReturnsZero(t *testing.T) {
	h := newReaperHarness()

	n, err := h.reaper.SweepExpiredLeases(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0, got %d", n)
	}
	if len(h.mover.movesSeen) != 0 {
		t.Errorf("MoveTask should not have been called")
	}
}

func TestLeaseReaper_NoTodoStatus_SkipsTask(t *testing.T) {
	h := newReaperHarness()
	projectID := uuid.New()
	// No todo status registered.
	h.addExpiredTask(t, projectID, domain.AssigneeTypeAgent)

	n, err := h.reaper.SweepExpiredLeases(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 (skipped), got %d", n)
	}
	if len(h.mover.movesSeen) != 0 {
		t.Errorf("MoveTask should not have been called")
	}
}

func TestLeaseReaper_MoveTaskFails_SkipsCount(t *testing.T) {
	h := newReaperHarness()
	projectID := uuid.New()
	h.addTodoStatus(t, projectID)
	h.addExpiredTask(t, projectID, domain.AssigneeTypeAgent)
	h.mover.moveErr = errors.New("simulated move failure")

	n, err := h.reaper.SweepExpiredLeases(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 (move failed), got %d", n)
	}
	h.commentRepo.mu.Lock()
	numComments := len(h.commentRepo.items)
	h.commentRepo.mu.Unlock()
	if numComments != 0 {
		t.Errorf("expected no comments on failed move")
	}
}

func TestLeaseReaper_MultipleTasksSameProject_TodoStatusCached(t *testing.T) {
	h := newReaperHarness()
	projectID := uuid.New()
	h.addTodoStatus(t, projectID)
	h.addExpiredTask(t, projectID, domain.AssigneeTypeAgent)
	h.addExpiredTask(t, projectID, domain.AssigneeTypeAgent)

	n, err := h.reaper.SweepExpiredLeases(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2, got %d", n)
	}
	if len(h.mover.movesSeen) != 2 {
		t.Errorf("expected 2 MoveTask calls, got %d", len(h.mover.movesSeen))
	}
	h.statusRepo.mu.Lock()
	calls := h.statusRepo.listByProjectCalls
	h.statusRepo.mu.Unlock()
	if calls != 1 {
		t.Errorf("expected 1 ListByProject call (cache hit), got %d", calls)
	}
}

func TestLeaseReaper_UserAssignee_NoAgentNotification(t *testing.T) {
	h := newReaperHarness()
	projectID := uuid.New()
	h.addTodoStatus(t, projectID)
	h.addExpiredTask(t, projectID, domain.AssigneeTypeUser)

	_, _ = h.reaper.SweepExpiredLeases(context.Background())
	if len(h.notify.notified) != 0 {
		t.Errorf("expected no agent notification for user-assigned task")
	}
}

func TestLeaseReaper_FutureCheckoutExpiry_NotSwept(t *testing.T) {
	h := newReaperHarness()
	projectID := uuid.New()
	h.addTodoStatus(t, projectID)

	future := time.Now().Add(10 * time.Minute)
	aid := uuid.New()
	active := &domain.Task{
		ID:              uuid.New(),
		ProjectID:       projectID,
		StatusID:        uuid.New(),
		Title:           "active task",
		AssigneeID:      &aid,
		AssigneeType:    domain.AssigneeTypeAgent,
		CheckoutExpires: &future,
	}
	if err := h.taskRepo.Create(context.Background(), active); err != nil {
		t.Fatalf("create active task: %v", err)
	}

	n, err := h.reaper.SweepExpiredLeases(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("active task must not be swept, got n=%d", n)
	}
	if len(h.mover.movesSeen) != 0 {
		t.Errorf("MoveTask must not be called for active tasks")
	}
}
