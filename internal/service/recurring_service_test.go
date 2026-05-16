package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// ---------------------------------------------------------------------------
// MockRecurringRepository — history-aware mock for createInstance / getPrevious tests
// ---------------------------------------------------------------------------

type MockRecurringRepository struct {
	mu        sync.Mutex
	schedules map[uuid.UUID]*domain.RecurringSchedule
	history   map[uuid.UUID][]domain.RecurringInstanceSummary

	// Observation counters.
	advanceNextRunCalls  int
	recordFailureCalls   int
	quarantineCalls      int
	resetFailureCalls    int
	incrementCalls       int
	updateCalls          int
}

func NewMockRecurringRepository() *MockRecurringRepository {
	return &MockRecurringRepository{
		schedules: make(map[uuid.UUID]*domain.RecurringSchedule),
		history:   make(map[uuid.UUID][]domain.RecurringInstanceSummary),
	}
}

func (m *MockRecurringRepository) Create(_ context.Context, s *domain.RecurringSchedule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *s
	m.schedules[s.ID] = &cp
	return nil
}

func (m *MockRecurringRepository) GetByID(_ context.Context, id uuid.UUID) (*domain.RecurringSchedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.schedules[id]
	if !ok {
		return nil, nil
	}
	cp := *s
	return &cp, nil
}

func (m *MockRecurringRepository) Update(_ context.Context, s *domain.RecurringSchedule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateCalls++
	cp := *s
	m.schedules[s.ID] = &cp
	return nil
}

func (m *MockRecurringRepository) Delete(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.schedules, id)
	return nil
}

func (m *MockRecurringRepository) ListByProject(_ context.Context, _ uuid.UUID, pg pagination.Params) (*pagination.Page[domain.RecurringSchedule], error) {
	return pagination.NewPage[domain.RecurringSchedule](nil, 0, pg), nil
}

func (m *MockRecurringRepository) FindDue(_ context.Context) ([]domain.RecurringSchedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []domain.RecurringSchedule
	now := time.Now()
	for _, s := range m.schedules {
		if s.IsActive && s.NextRunAt != nil && s.NextRunAt.Before(now) {
			if s.LastTriggeredAt == nil || s.LastTriggeredAt.Before(*s.NextRunAt) {
				result = append(result, *s)
			}
		}
	}
	return result, nil
}

func (m *MockRecurringRepository) IncrementInstance(_ context.Context, id uuid.UUID, nextRunAt *time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.incrementCalls++
	s, ok := m.schedules[id]
	if !ok {
		return errors.New("schedule not found")
	}
	s.InstanceCount++
	now := time.Now()
	s.LastTriggeredAt = &now
	s.NextRunAt = nextRunAt
	return nil
}

func (m *MockRecurringRepository) AdvanceNextRun(_ context.Context, id uuid.UUID, nextRunAt *time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.advanceNextRunCalls++
	s, ok := m.schedules[id]
	if !ok {
		return errors.New("schedule not found")
	}
	s.NextRunAt = nextRunAt
	return nil
}

func (m *MockRecurringRepository) RecordFailure(_ context.Context, id uuid.UUID, nextRunAt *time.Time, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recordFailureCalls++
	s, ok := m.schedules[id]
	if !ok {
		return errors.New("schedule not found")
	}
	s.ConsecutiveFailures++
	s.NextRunAt = nextRunAt
	return nil
}

func (m *MockRecurringRepository) Quarantine(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.quarantineCalls++
	s, ok := m.schedules[id]
	if !ok {
		return errors.New("schedule not found")
	}
	s.IsActive = false
	now := time.Now()
	s.QuarantinedAt = &now
	return nil
}

func (m *MockRecurringRepository) ResetConsecutiveFailures(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resetFailureCalls++
	s, ok := m.schedules[id]
	if !ok {
		return errors.New("schedule not found")
	}
	s.ConsecutiveFailures = 0
	s.LastError = nil
	return nil
}

func (m *MockRecurringRepository) GetInstanceHistory(_ context.Context, scheduleID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.RecurringInstanceSummary], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := m.history[scheduleID]
	return pagination.NewPage(items, len(items), pg), nil
}

var _ repository.RecurringRepository = (*MockRecurringRepository)(nil)

// ---------------------------------------------------------------------------
// StubTaskService — only implements methods called by recurring_service.
// ---------------------------------------------------------------------------

type StubTaskService struct {
	mu            sync.Mutex
	created       []*domain.Task
	createErr     error
	defaultStatus *domain.TaskStatus
}

func NewStubTaskService() *StubTaskService {
	return &StubTaskService{
		defaultStatus: &domain.TaskStatus{
			ID:       uuid.New(),
			Category: "todo",
		},
	}
}

func (s *StubTaskService) Create(_ context.Context, t *domain.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return s.createErr
	}
	cp := *t
	s.created = append(s.created, &cp)
	return nil
}

func (s *StubTaskService) GetDefaultStatus(_ context.Context, _ uuid.UUID) (*domain.TaskStatus, error) {
	return s.defaultStatus, nil
}

func (s *StubTaskService) GetByID(_ context.Context, _ uuid.UUID) (*domain.Task, error) {
	panic("StubTaskService.GetByID not implemented")
}
func (s *StubTaskService) Update(_ context.Context, _ *domain.Task) error {
	panic("StubTaskService.Update not implemented")
}
func (s *StubTaskService) Delete(_ context.Context, _ uuid.UUID) error {
	panic("StubTaskService.Delete not implemented")
}
func (s *StubTaskService) List(_ context.Context, _ uuid.UUID, _ repository.TaskFilter, _ pagination.Params) (*pagination.Page[domain.Task], error) {
	panic("StubTaskService.List not implemented")
}
func (s *StubTaskService) MoveTask(_ context.Context, _ uuid.UUID, _ MoveTaskInput) error {
	panic("StubTaskService.MoveTask not implemented")
}
func (s *StubTaskService) AssignTask(_ context.Context, _ uuid.UUID, _ AssignTaskInput) error {
	panic("StubTaskService.AssignTask not implemented")
}
func (s *StubTaskService) CreateSubtask(_ context.Context, _ uuid.UUID, _ CreateSubtaskInput) (*domain.Task, error) {
	panic("StubTaskService.CreateSubtask not implemented")
}
func (s *StubTaskService) ListSubtasks(_ context.Context, _ uuid.UUID) ([]domain.Task, error) {
	panic("StubTaskService.ListSubtasks not implemented")
}
func (s *StubTaskService) GetMyTasks(_ context.Context, _ uuid.UUID, _ domain.AssigneeType) ([]domain.Task, error) {
	panic("StubTaskService.GetMyTasks not implemented")
}
func (s *StubTaskService) BulkUpdate(_ context.Context, _ uuid.UUID, _ BulkUpdateTasksInput) BulkUpdateTasksResult {
	panic("StubTaskService.BulkUpdate not implemented")
}
func (s *StubTaskService) CheckoutTask(_ context.Context, _ uuid.UUID, _ int) (*CheckoutResult, error) {
	panic("StubTaskService.CheckoutTask not implemented")
}
func (s *StubTaskService) ReleaseCheckout(_ context.Context, _, _ uuid.UUID) error {
	panic("StubTaskService.ReleaseCheckout not implemented")
}
func (s *StubTaskService) ExtendCheckout(_ context.Context, _, _ uuid.UUID, _ int) (*CheckoutResult, error) {
	panic("StubTaskService.ExtendCheckout not implemented")
}
func (s *StubTaskService) MoveToProject(_ context.Context, _, _ uuid.UUID) (*domain.Task, error) {
	panic("StubTaskService.MoveToProject not implemented")
}

var _ TaskService = (*StubTaskService)(nil)

// ---------------------------------------------------------------------------
// Helper — minimal schedule for history-based tests (distinct from newTestSchedule)
// ---------------------------------------------------------------------------

func newScheduleWithHistory(cronExpr string, nextRunAt time.Time) *domain.RecurringSchedule {
	id := uuid.New()
	nr := nextRunAt
	return &domain.RecurringSchedule{
		ID:                  id,
		WorkspaceID:         uuid.New(),
		ProjectID:           uuid.New(),
		TitleTemplate:       "Test task {{.Number}}",
		DescriptionTemplate: "PrevSummary: {{.PrevSummary}}",
		Frequency:           domain.RecurringFrequencyCustom,
		CronExpr:            cronExpr,
		Timezone:            "UTC",
		AssigneeType:        domain.AssigneeTypeAgent,
		Priority:            domain.PriorityMedium,
		Labels:              pq.StringArray{},
		IsActive:            true,
		StartsAt:            nextRunAt.Add(-24 * time.Hour),
		NextRunAt:           &nr,
		InstanceCount:       0,
		CreatedBy:           uuid.New(),
		CreatedByType:       domain.ActorTypeUser,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}
}

// ---------------------------------------------------------------------------
// AC#1 extra — truncateRuneSafe edge cases (exact boundary, emoji)
//   Complementary to TestTruncateRuneSafe_MultibyteRunes in recurring_scheduler_test.go
// ---------------------------------------------------------------------------

func TestTruncateRuneSafe_ExactBoundary(t *testing.T) {
	// String is exactly maxRunes long — must be returned unchanged.
	s := ""
	for i := 0; i < 500; i++ {
		s += "Ж" // 2-byte Cyrillic
	}
	got := truncateRuneSafe(s, 500)
	if got != s {
		t.Fatalf("expected unchanged string for exact-boundary input")
	}
	if !utf8.ValidString(got) {
		t.Fatal("exact-boundary result not valid UTF-8")
	}
}

func TestTruncateRuneSafe_EmojiExact501(t *testing.T) {
	// 501 emoji = 501 runes, each 4 bytes. [:500] (byte-index 500) would land mid-emoji.
	s := ""
	for i := 0; i < 501; i++ {
		s += "🔥"
	}
	got := truncateRuneSafe(s, 500)
	if !utf8.ValidString(got) {
		t.Fatal("result not valid UTF-8")
	}
	if utf8.RuneCountInString(got) != 500 {
		t.Fatalf("expected 500 runes, got %d", utf8.RuneCountInString(got))
	}
}

// ---------------------------------------------------------------------------
// AC#1 — getPreviousInstanceSummary: rune-safe truncation of LastComment
// ---------------------------------------------------------------------------

func TestGetPreviousInstanceSummary_RuneSafeTruncation(t *testing.T) {
	repo := NewMockRecurringRepository()
	taskSvc := NewStubTaskService()
	svc := NewRecurringService(repo, taskSvc).(*recurringService)

	scheduleID := uuid.New()
	// 600 Cyrillic chars = 600 runes, 1200 bytes. Naive [:500] lands mid-rune at byte 500.
	longComment := ""
	for i := 0; i < 600; i++ {
		longComment += "Ж"
	}
	repo.history[scheduleID] = []domain.RecurringInstanceSummary{
		{
			TaskID:         uuid.New(),
			InstanceNumber: 1,
			Title:          "T",
			StatusCategory: "done",
			LastComment:    &longComment,
			CreatedAt:      time.Now(),
		},
	}

	summary := svc.getPreviousInstanceSummary(context.Background(), scheduleID)
	if summary == nil || summary.LastComment == nil {
		t.Fatal("expected summary with LastComment")
	}

	got := *summary.LastComment
	if !utf8.ValidString(got) {
		t.Fatalf("truncated LastComment is not valid UTF-8: %q", got)
	}
	if utf8.RuneCountInString(got) != prevSummaryMaxRunes {
		t.Fatalf("expected %d runes, got %d", prevSummaryMaxRunes, utf8.RuneCountInString(got))
	}
}

// ---------------------------------------------------------------------------
// AC#2 / AC#3 — createInstance: ToValidUTF8 backstop on invalid PrevSummary
// ---------------------------------------------------------------------------

func TestCreateInstance_DescriptionBackstop(t *testing.T) {
	// Inject a raw invalid UTF-8 sequence via PrevSummary (simulates a byte-truncated
	// Cyrillic comment fetched from DB before Fix #1 was applied).
	invalidPrev := "prefix \xd0 suffix" // lone 0xD0 lead byte — invalid UTF-8

	repo := NewMockRecurringRepository()
	taskSvc := NewStubTaskService()
	svc := NewRecurringService(repo, taskSvc).(*recurringService)

	scheduleID := uuid.New()
	schedule := &domain.RecurringSchedule{
		ID:                  scheduleID,
		ProjectID:           uuid.New(),
		WorkspaceID:         uuid.New(),
		TitleTemplate:       "Task {{.Number}}",
		DescriptionTemplate: "Prev: {{.PrevSummary}}",
		Frequency:           domain.RecurringFrequencyCustom,
		CronExpr:            "0 9 * * *",
		Timezone:            "UTC",
		AssigneeType:        domain.AssigneeTypeAgent,
		Priority:            domain.PriorityMedium,
		Labels:              pq.StringArray{},
		IsActive:            true,
		StartsAt:            time.Now().Add(-time.Hour),
		InstanceCount:       1,
		CreatedBy:           uuid.New(),
		CreatedByType:       domain.ActorTypeUser,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}

	lastComment := invalidPrev
	repo.history[scheduleID] = []domain.RecurringInstanceSummary{
		{
			TaskID:         uuid.New(),
			InstanceNumber: 1,
			Title:          "Task 1",
			StatusCategory: "done",
			LastComment:    &lastComment,
			CreatedAt:      time.Now().Add(-time.Hour),
		},
	}

	_, err := svc.createInstance(context.Background(), schedule, time.Now())
	if err != nil {
		t.Fatalf("createInstance returned error: %v", err)
	}

	taskSvc.mu.Lock()
	created := taskSvc.created
	taskSvc.mu.Unlock()

	if len(created) == 0 {
		t.Fatal("expected taskSvc.Create to be called")
	}
	desc := created[0].Description
	if !utf8.ValidString(desc) {
		t.Fatalf("description passed to taskSvc.Create is not valid UTF-8: %q", desc)
	}
}

// AC#3 — repro: schedule with byte-truncated PrevSummary must complete without error
//        and next_run_at must advance.
func TestRunOneSchedule_InvalidPrevSummaryDoesNotFail(t *testing.T) {
	// Simulate a comment stored with a mid-rune cut (pre-Fix #1 byte truncation).
	invalidComment := ""
	for i := 0; i < 249; i++ {
		invalidComment += "А" // 2-byte Cyrillic
	}
	invalidComment += "\xd0" // lone Cyrillic lead byte — invalid UTF-8

	repo := NewMockRecurringRepository()
	taskSvc := NewStubTaskService()
	svc := NewRecurringService(repo, taskSvc).(*recurringService)

	past := time.Now().Add(-time.Minute)
	schedule := newScheduleWithHistory("* * * * *", past)
	schedule.InstanceCount = 1 // enable PrevSummary fetch
	repo.schedules[schedule.ID] = schedule
	repo.history[schedule.ID] = []domain.RecurringInstanceSummary{
		{
			TaskID:         uuid.New(),
			InstanceNumber: 1,
			Title:          "Prev",
			StatusCategory: "done",
			LastComment:    &invalidComment,
			CreatedAt:      time.Now().Add(-2 * time.Minute),
		},
	}

	created, err := svc.runOneSchedule(context.Background(), schedule)
	if err != nil {
		t.Fatalf("runOneSchedule returned error: %v", err)
	}
	if !created {
		t.Fatal("expected instance to be created")
	}
	if repo.incrementCalls != 1 {
		t.Fatalf("expected IncrementInstance called once, got %d", repo.incrementCalls)
	}

	stored := repo.schedules[schedule.ID]
	if stored.NextRunAt == nil {
		t.Fatal("next_run_at is nil after successful run")
	}
	if !stored.NextRunAt.After(time.Now().Add(-time.Second)) {
		t.Fatalf("next_run_at %v should be a future occurrence", stored.NextRunAt)
	}
}

// ---------------------------------------------------------------------------
// AC4a — createInstance error → RecordFailure called, next_run_at advances
// ---------------------------------------------------------------------------

func TestRunOneSchedule_FailureCallsRecordFailure(t *testing.T) {
	repo := NewMockRecurringRepository()
	taskSvc := NewStubTaskService()
	taskSvc.createErr = errors.New("simulated INSERT failure pq 22021")
	svc := NewRecurringService(repo, taskSvc).(*recurringService)

	past := time.Now().Add(-time.Minute)
	schedule := newScheduleWithHistory("* * * * *", past)
	repo.schedules[schedule.ID] = schedule

	_, err := svc.runOneSchedule(context.Background(), schedule)
	if err == nil {
		t.Fatal("expected error from runOneSchedule")
	}
	if repo.recordFailureCalls != 1 {
		t.Fatalf("expected RecordFailure called once, got %d", repo.recordFailureCalls)
	}
	stored := repo.schedules[schedule.ID]
	if stored.NextRunAt == nil || !stored.NextRunAt.After(time.Now().Add(-time.Second)) {
		t.Fatalf("next_run_at %v should point to a future occurrence after failure", stored.NextRunAt)
	}
}

// ---------------------------------------------------------------------------
// AC4b — 3 consecutive failures → quarantine + alert fired exactly once
// ---------------------------------------------------------------------------

func TestRunOneSchedule_QuarantineAfterThreeFailures(t *testing.T) {
	repo := NewMockRecurringRepository()
	taskSvc := NewStubTaskService()
	taskSvc.createErr = errors.New("simulated pq 22021")

	var alertedID uuid.UUID
	alertCalled := 0
	svc := NewRecurringService(repo, taskSvc,
		WithAlertFunc(func(id uuid.UUID, _ error) {
			alertedID = id
			alertCalled++
		}),
	).(*recurringService)

	past := time.Now().Add(-time.Minute)
	schedule := newScheduleWithHistory("* * * * *", past)
	repo.schedules[schedule.ID] = schedule
	scheduleID := schedule.ID

	// Simulate maxConsecutiveFailures ticks. On each tick the schedule struct
	// reflects what the DB would return (consecutive_failures incremented by previous ticks).
	for i := 0; i < maxConsecutiveFailures; i++ {
		s := repo.schedules[scheduleID]
		pastAgain := time.Now().Add(-time.Minute)
		s.NextRunAt = &pastAgain
		s.ConsecutiveFailures = i // DB value before this tick
		svc.runOneSchedule(context.Background(), s)
	}

	stored := repo.schedules[scheduleID]
	if stored.IsActive {
		t.Fatal("schedule should be quarantined (is_active=false)")
	}
	if repo.quarantineCalls != 1 {
		t.Fatalf("expected Quarantine called once, got %d", repo.quarantineCalls)
	}
	if alertCalled != 1 {
		t.Fatalf("expected alert called once (on 3rd failure), got %d", alertCalled)
	}
	if alertedID != scheduleID {
		t.Fatal("alert schedule ID mismatch")
	}
}

// ---------------------------------------------------------------------------
// AC4c — success after failures resets the consecutive-failure counter
// ---------------------------------------------------------------------------

func TestRunOneSchedule_ConsecutiveFailureCountResetOnSuccess(t *testing.T) {
	repo := NewMockRecurringRepository()
	taskSvc := NewStubTaskService()
	taskSvc.createErr = errors.New("temporary error")

	alertCalled := 0
	svc := NewRecurringService(repo, taskSvc,
		WithAlertFunc(func(_ uuid.UUID, _ error) { alertCalled++ }),
	).(*recurringService)

	past := time.Now().Add(-time.Minute)
	schedule := newScheduleWithHistory("* * * * *", past)
	repo.schedules[schedule.ID] = schedule

	// Two failures.
	for i := 0; i < 2; i++ {
		s := repo.schedules[schedule.ID]
		pastAgain := time.Now().Add(-time.Minute)
		s.NextRunAt = &pastAgain
		s.ConsecutiveFailures = i
		svc.runOneSchedule(context.Background(), s)
	}

	// Success — should reset ConsecutiveFailures.
	taskSvc.createErr = nil
	s := repo.schedules[schedule.ID]
	pastAgain := time.Now().Add(-time.Minute)
	s.NextRunAt = &pastAgain
	s.ConsecutiveFailures = 2 // what the DB returns after 2 failures
	created, err := svc.runOneSchedule(context.Background(), s)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if !created {
		t.Fatal("expected instance created on success")
	}
	if repo.resetFailureCalls == 0 {
		t.Fatal("expected ResetConsecutiveFailures called after success with ConsecutiveFailures > 0")
	}

	// Two more failures starting from 0 — should NOT quarantine (need 3 consecutive).
	taskSvc.createErr = errors.New("another error")
	for i := 0; i < 2; i++ {
		s2 := repo.schedules[schedule.ID]
		pastAgain2 := time.Now().Add(-time.Minute)
		s2.NextRunAt = &pastAgain2
		s2.ConsecutiveFailures = i
		svc.runOneSchedule(context.Background(), s2)
	}

	stored := repo.schedules[schedule.ID]
	if !stored.IsActive {
		t.Fatal("schedule should still be active after only 2 post-reset failures")
	}
	if alertCalled != 0 {
		t.Fatalf("alert should not have fired, got %d calls", alertCalled)
	}
}

// ---------------------------------------------------------------------------
// AC5 — 3 days in past (hourly cron) → exactly 1 instance, future next_run_at
// ---------------------------------------------------------------------------

func TestRunOneSchedule_CatchUpExactlyOneInstance(t *testing.T) {
	repo := NewMockRecurringRepository()
	taskSvc := NewStubTaskService()
	svc := NewRecurringService(repo, taskSvc).(*recurringService)

	// Hourly cron, next_run_at = 3 days ago → 72 missed occurrences.
	threeDaysAgo := time.Now().Add(-72 * time.Hour)
	schedule := newScheduleWithHistory("0 * * * *", threeDaysAgo)
	repo.schedules[schedule.ID] = schedule

	created, err := svc.runOneSchedule(context.Background(), schedule)
	if err != nil {
		t.Fatalf("runOneSchedule returned error: %v", err)
	}
	if !created {
		t.Fatal("expected exactly 1 catch-up instance")
	}
	// IncrementInstance called once — proof that only 1 task was created.
	if repo.incrementCalls != 1 {
		t.Fatalf("expected IncrementInstance called once (1 catch-up not 72), got %d", repo.incrementCalls)
	}
	// next_run_at must be strictly in the future — not back-dated.
	stored := repo.schedules[schedule.ID]
	if stored.NextRunAt == nil || !stored.NextRunAt.After(time.Now()) {
		t.Fatalf("next_run_at %v should be strictly in the future", stored.NextRunAt)
	}
	// Exactly 1 task created.
	taskSvc.mu.Lock()
	n := len(taskSvc.created)
	taskSvc.mu.Unlock()
	if n != 1 {
		t.Fatalf("expected 1 task created (not 72), got %d", n)
	}
}
