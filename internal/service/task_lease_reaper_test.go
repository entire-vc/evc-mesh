package service

import (
	"context"
	"errors"
	"strings"
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
	taskRepo     *MockTaskRepository
	statusRepo   *MockTaskStatusRepository
	commentRepo  *MockCommentRepository
	activityRepo *MockActivityLogRepository
	projectRepo  *MockProjectRepository
	mover        *mockLeaseTaskMover
	notify       *mockLeaseNotify
	reaper       CheckoutLeaseReaper
}

func newReaperHarness() *reaperHarness {
	h := &reaperHarness{
		taskRepo:     NewMockTaskRepository(),
		statusRepo:   NewMockTaskStatusRepository(),
		commentRepo:  NewMockCommentRepository(),
		activityRepo: NewMockActivityLogRepository(),
		// WithDefaultWorkspace: every test here mints an ad-hoc projectID via
		// uuid.New() without registering a Project row (they predate
		// logLeaseActivity's need for one) — a single default tenant lets
		// logLeaseActivity resolve workspace_id for all of them without
		// touching every existing test's setup.
		projectRepo: NewMockProjectRepository().WithDefaultWorkspace(uuid.New()),
		mover:       &mockLeaseTaskMover{},
		notify:      &mockLeaseNotify{},
	}
	h.reaper = &checkoutLeaseReaper{
		taskRepo:       h.taskRepo,
		statusRepo:     h.statusRepo,
		commentRepo:    h.commentRepo,
		taskMover:      h.mover,
		agentNotifySvc: h.notify,
		activityRepo:   h.activityRepo,
		projectRepo:    h.projectRepo,
	}
	return h
}

// leaseActivities returns all activity_log entries the reaper recorded.
func (h *reaperHarness) leaseActivities() []domain.ActivityLog {
	h.activityRepo.mu.RLock()
	defer h.activityRepo.mu.RUnlock()
	out := make([]domain.ActivityLog, 0, len(h.activityRepo.items))
	for _, e := range h.activityRepo.items {
		out = append(out, *e)
	}
	return out
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

	// Audit §1.2: this class of outcome is recorded to activity_log, not as a
	// task-thread comment (2,205 of these were pure boilerplate, 7.1% of the
	// fleet's total comments) — notifyAssignee's separate event already tells
	// the holder directly.
	h.commentRepo.mu.Lock()
	numComments := len(h.commentRepo.items)
	h.commentRepo.mu.Unlock()
	if numComments != 0 {
		t.Errorf("expected 0 system comments (moved to activity_log), got %d", numComments)
	}
	activities := h.leaseActivities()
	if len(activities) != 1 {
		t.Fatalf("expected 1 activity_log entry, got %d", len(activities))
	}
	if activities[0].Action != activityCheckoutLeaseExpired {
		t.Errorf("expected action %q, got %q", activityCheckoutLeaseExpired, activities[0].Action)
	}
	if activities[0].EntityID != task.ID {
		t.Errorf("activity logged against the wrong task: got %s, want %s", activities[0].EntityID, task.ID)
	}

	if len(h.notify.notified) != 1 || h.notify.notified[0] != *task.AssigneeID {
		t.Errorf("expected agent notification to task assignee")
	}
}

// TestLeaseReaper_ExpiredLease_DoesNotBlameHeartbeat guards against the
// misdiagnosis fixed in task fe8ddfa0: FindExpiredInProgressCheckouts
// (task_repo.go) sweeps purely on checkout_expires < now() and never reads
// heartbeat/last_heartbeat, so nothing this sweep records may name heartbeat
// as the cause. Since audit §1.2 moved this outcome off the task thread
// entirely (see TestLeaseReaper_HappyPath_MovesToTodo above), there is no
// comment body left to misdiagnose — this checks the one text surface that
// remains: the activity_log action name itself.
func TestLeaseReaper_ExpiredLease_DoesNotBlameHeartbeat(t *testing.T) {
	h := newReaperHarness()
	projectID := uuid.New()
	h.addTodoStatus(t, projectID)
	h.addExpiredTask(t, projectID, domain.AssigneeTypeAgent)

	_, err := h.reaper.SweepExpiredLeases(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	activities := h.leaseActivities()
	if len(activities) != 1 {
		t.Fatalf("expected 1 activity_log entry, got %d", len(activities))
	}
	action := activities[0].Action
	if strings.Contains(action, "heartbeat") {
		t.Errorf("activity action still blames heartbeat, got: %q", action)
	}
	if !strings.Contains(action, "lease_expired") {
		t.Errorf("activity action must name lease/TTL expiry as the cause, got: %q", action)
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

// ---------------------------------------------------------------------------
// Phase 3 — in_progress with NO lease at all.
//
// SweepExpiredLeases keys on `checkout_expires < now()`, so it can only ever see
// a lease that EXPIRED. A lease that was CLEARED leaves checkout_expires NULL and
// is invisible to it — and equally invisible to the agent feed, which polls
// status_category=todo. Nothing returned such a task to circulation; measured on
// prod 2026-08-27, 4 of 7 in_progress tasks were in this state, the oldest idle
// 245h (#8e2e1c0e).
//
// The negative control is the load-bearing half here: the fix must not rob a task
// from an agent that is genuinely holding it. Without that case, a sweep that
// returned EVERY in_progress task to todo would pass the positive test.
// ---------------------------------------------------------------------------

// addUnleasedTask builds an in_progress task holding no checkout at all, last
// touched idleFor ago.
func (h *reaperHarness) addUnleasedTask(t *testing.T, projectID uuid.UUID, idleFor time.Duration) *domain.Task {
	t.Helper()
	aid := uuid.New()
	task := &domain.Task{
		ID:           uuid.New(),
		ProjectID:    projectID,
		StatusID:     uuid.New(),
		Title:        "unleased in_progress task",
		AssigneeID:   &aid,
		AssigneeType: domain.AssigneeTypeAgent,
		UpdatedAt:    time.Now().Add(-idleFor),
		// CheckedOutBy / CheckoutExpires deliberately nil — this IS the state.
	}
	if err := h.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("addUnleasedTask: %v", err)
	}
	return task
}

func TestLeaseReaper_UnleasedInProgress_ReturnedToTodo(t *testing.T) {
	h := newReaperHarness()
	projectID := uuid.New()
	h.addTodoStatus(t, projectID)
	task := h.addUnleasedTask(t, projectID, 4*time.Hour)

	n, err := h.reaper.SweepUnleasedInProgress(context.Background(), DefaultUnleasedGrace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 task returned to todo, got %d", n)
	}
	if len(h.mover.movesSeen) != 1 || h.mover.movesSeen[0] != task.ID {
		t.Fatalf("MoveTask not called for the unleased task; moves=%v", h.mover.movesSeen)
	}
}

// The negative control the fix exists to respect: a LIVE checkout means an agent
// is holding the task, and the sweep must leave it alone. Confirms the sweep
// discriminates on the lease rather than on the status category.
func TestLeaseReaper_UnleasedInProgress_LiveCheckoutUntouched(t *testing.T) {
	h := newReaperHarness()
	projectID := uuid.New()
	h.addTodoStatus(t, projectID)

	future := time.Now().Add(90 * time.Minute)
	holder := uuid.New()
	aid := uuid.New()
	held := &domain.Task{
		ID:              uuid.New(),
		ProjectID:       projectID,
		StatusID:        uuid.New(),
		Title:           "an agent is working on this right now",
		AssigneeID:      &aid,
		AssigneeType:    domain.AssigneeTypeAgent,
		CheckedOutBy:    &holder,
		CheckoutExpires: &future,
		UpdatedAt:       time.Now().Add(-9 * time.Hour), // idle, but LEASED
	}
	if err := h.taskRepo.Create(context.Background(), held); err != nil {
		t.Fatalf("create: %v", err)
	}

	n, err := h.reaper.SweepUnleasedInProgress(context.Background(), DefaultUnleasedGrace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Fatalf("sweep took a task that is under a live checkout — it must not; moved=%d", n)
	}
	if len(h.mover.movesSeen) != 0 {
		t.Fatalf("MoveTask called on a live-checkout task: %v", h.mover.movesSeen)
	}
}

// Second negative control: inside the grace window the task is left alone, so an
// agent that released its lock and is about to move the card itself is not robbed
// mid-handoff. Without this, the grace parameter would be decorative.
func TestLeaseReaper_UnleasedInProgress_WithinGraceUntouched(t *testing.T) {
	h := newReaperHarness()
	projectID := uuid.New()
	h.addTodoStatus(t, projectID)
	h.addUnleasedTask(t, projectID, 5*time.Minute) // well inside the 120m grace

	n, err := h.reaper.SweepUnleasedInProgress(context.Background(), DefaultUnleasedGrace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Fatalf("sweep acted inside the grace window; moved=%d", n)
	}
}

// The two sweeps must stay distinguishable in the audit trail: an operator
// reading a task's comments has to be able to tell "your lease expired" from
// "you held no lease at all", because the corrective action differs.
func TestLeaseReaper_UnleasedInProgress_ActivityDistinctFromExpiredLease(t *testing.T) {
	h := newReaperHarness()
	projectID := uuid.New()
	h.addTodoStatus(t, projectID)
	h.addUnleasedTask(t, projectID, 4*time.Hour)

	if _, err := h.reaper.SweepUnleasedInProgress(context.Background(), DefaultUnleasedGrace); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Audit §1.2: no comment either, same as the expired-lease case above.
	h.commentRepo.mu.Lock()
	numComments := len(h.commentRepo.items)
	h.commentRepo.mu.Unlock()
	if numComments != 0 {
		t.Errorf("expected 0 system comments (moved to activity_log), got %d", numComments)
	}

	activities := h.leaseActivities()
	if len(activities) != 1 {
		t.Fatalf("expected exactly 1 activity_log entry, got %d", len(activities))
	}
	action := activities[0].Action
	if action != activityCheckoutUnleasedReturned {
		t.Errorf("expected action %q, got %q", activityCheckoutUnleasedReturned, action)
	}
	if action == activityCheckoutLeaseExpired {
		t.Errorf("unleased sweep recorded the expired-TTL action — the two cases are indistinguishable: %q", action)
	}
}

// A repo error must surface, not be silently reported as "nothing to do" — the
// failure mode where a broken sweep is indistinguishable from a clean estate.
func TestLeaseReaper_UnleasedInProgress_RepoErrorSurfaces(t *testing.T) {
	h := newReaperHarness()
	h.taskRepo.errToReturn = errors.New("db down")

	n, err := h.reaper.SweepUnleasedInProgress(context.Background(), DefaultUnleasedGrace)
	if err == nil {
		t.Fatalf("repo error swallowed — sweep reported %d moved and no error", n)
	}
	if n != 0 {
		t.Errorf("expected 0 moved on error, got %d", n)
	}
}
