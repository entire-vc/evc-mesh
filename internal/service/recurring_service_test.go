package service

import (
	"context"
	"errors"
	"strings"
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
	advanceNextRunCalls int
	recordFailureCalls  int
	quarantineCalls     int
	resetFailureCalls   int
	incrementCalls      int
	updateCalls         int
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
	// validateAssigneeErr, when set, makes ValidateAssigneeForProject refuse —
	// tests exercising the tenancy-refusal path set this instead of building a
	// real cross-workspace fixture, matching how createErr is used above.
	validateAssigneeErr error
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
func (s *StubTaskService) GetStatusByID(_ context.Context, _ uuid.UUID) (*domain.TaskStatus, error) {
	panic("StubTaskService.GetStatusByID not implemented")
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
func (s *StubTaskService) GetMyTasks(_ context.Context, _, _ uuid.UUID, _ domain.AssigneeType, _ repository.AssigneeTaskFilter) ([]domain.Task, int, error) {
	panic("StubTaskService.GetMyTasks not implemented")
}
func (s *StubTaskService) GetUserActiveTasks(_ context.Context, _, _ uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	panic("StubTaskService.GetUserActiveTasks not implemented")
}
func (s *StubTaskService) BulkUpdate(_ context.Context, _ uuid.UUID, _ BulkUpdateTasksInput) BulkUpdateTasksResult {
	panic("StubTaskService.BulkUpdate not implemented")
}
func (s *StubTaskService) CheckoutTask(_ context.Context, _ uuid.UUID, _ int, _ map[string]interface{}) (*CheckoutResult, error) {
	panic("StubTaskService.CheckoutTask not implemented")
}
func (s *StubTaskService) ReleaseCheckout(_ context.Context, _, _ uuid.UUID) error {
	panic("StubTaskService.ReleaseCheckout not implemented")
}
func (s *StubTaskService) SelfReleaseCheckout(_ context.Context, _ uuid.UUID) error {
	panic("StubTaskService.SelfReleaseCheckout not implemented")
}
func (s *StubTaskService) ExtendCheckout(_ context.Context, _, _ uuid.UUID, _ int) (*CheckoutResult, error) {
	panic("StubTaskService.ExtendCheckout not implemented")
}
func (s *StubTaskService) ForceReleaseCheckout(_ context.Context, _ uuid.UUID) error {
	panic("StubTaskService.ForceReleaseCheckout not implemented")
}
func (s *StubTaskService) MoveToProject(_ context.Context, _, _ uuid.UUID) (*domain.Task, error) {
	panic("StubTaskService.MoveToProject not implemented")
}
func (s *StubTaskService) GetByShortID(_ context.Context, _ string) (*domain.Task, error) {
	panic("StubTaskService.GetByShortID not implemented")
}
func (s *StubTaskService) Search(_ context.Context, _ uuid.UUID, _ repository.TaskFilter, _ pagination.Params) (*pagination.Page[domain.Task], error) {
	panic("StubTaskService.Search not implemented")
}

func (s *StubTaskService) SupersedeRecurringInstances(_ context.Context, _, _ uuid.UUID) error {
	return nil
}

func (s *StubTaskService) ValidateAssigneeForProject(_ context.Context, _ uuid.UUID, assigneeID *uuid.UUID, assigneeType domain.AssigneeType) (domain.AssigneeType, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if assigneeID == nil || *assigneeID == uuid.Nil {
		return domain.AssigneeTypeUnassigned, nil
	}
	if s.validateAssigneeErr != nil {
		return assigneeType, s.validateAssigneeErr
	}
	return assigneeType, nil
}

func (s *StubTaskService) SetHumanGate(_ context.Context, _ uuid.UUID, _ bool) error { return nil }
func (s *StubTaskService) SetHumanGateClass(_ context.Context, _ uuid.UUID, _ domain.HumanGateClass) error {
	return nil
}
func (s *StubTaskService) ShipTask(_ context.Context, _ uuid.UUID, _ bool) error { return nil }
func (s *StubTaskService) SetDodCheck(_ context.Context, _ uuid.UUID, _, _, _ string) error {
	return nil
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
	// No whitespace anywhere in the 600-'Ж' input, so softenTruncationBoundary
	// has nothing to back off to — the cut stays at exactly prevSummaryMaxRunes
	// runes, plus the visible truncation marker.
	if !strings.HasSuffix(got, prevSummaryTruncatedSuffix) {
		t.Fatalf("expected truncated result to end with %q, got %q", prevSummaryTruncatedSuffix, got)
	}
	wantRunes := prevSummaryMaxRunes + utf8.RuneCountInString(prevSummaryTruncatedSuffix)
	if utf8.RuneCountInString(got) != wantRunes {
		t.Fatalf("expected %d runes, got %d", wantRunes, utf8.RuneCountInString(got))
	}
}

// AC#1 extra — softenTruncationBoundary must not cut a real word in half when a
// whitespace boundary exists within the lookback window (task c9fa3b4b: prior
// behavior cut "...дефектом ПОДАЧИ ... вместо ложного f" mid-word).
func TestGetPreviousInstanceSummary_TruncationBacksOffToWordBoundary(t *testing.T) {
	repo := NewMockRecurringRepository()
	taskSvc := NewStubTaskService()
	svc := NewRecurringService(repo, taskSvc).(*recurringService)

	scheduleID := uuid.New()
	// 495 filler runes + a word that straddles the 500-rune cut (the naive cut
	// lands inside "overflowingword", at "...x over"). The word boundary is
	// well within the 10%-of-500=50-rune lookback, so it should back off
	// cleanly to just the filler, not leave a dangling word fragment.
	longComment := strings.Repeat("x", 495) + " overflowingword more text past the limit"
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
	want := strings.Repeat("x", 495) + prevSummaryTruncatedSuffix
	if got != want {
		t.Fatalf("expected clean word-boundary cut %q, got %q", want, got)
	}
	if strings.Contains(got, "overflowingwor") {
		t.Fatalf("truncation landed mid-word: %q", got)
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

// Task c9fa3b4b — the injected previous-instance report must be clearly framed
// as historical context, never as an unlabeled continuation of the current
// instance's instructions. Repro: instance 30's description ended with a bare
// "…Recall@episodic оказался дефектом ПОДАЧИ … вместо ложного f" — the raw
// last_comment of instance 29 with no marker showing where it came from.
func TestCreateInstance_PrevSummaryIsFramedAsContext(t *testing.T) {
	repo := NewMockRecurringRepository()
	taskSvc := NewStubTaskService()
	svc := NewRecurringService(repo, taskSvc).(*recurringService)

	scheduleID := uuid.New()
	schedule := &domain.RecurringSchedule{
		ID:                  scheduleID,
		ProjectID:           uuid.New(),
		WorkspaceID:         uuid.New(),
		TitleTemplate:       "Task {{.Number}}",
		DescriptionTemplate: "Do the daily sweep.\n{{.PrevSummary}}",
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

	lastComment := "Done. Rolled out fix X, verified Y."
	prevCreatedAt := time.Now().Add(-time.Hour)
	repo.history[scheduleID] = []domain.RecurringInstanceSummary{
		{
			TaskID:         uuid.New(),
			InstanceNumber: 7,
			Title:          "Task 7",
			StatusCategory: "done",
			LastComment:    &lastComment,
			CreatedAt:      prevCreatedAt,
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

	header := prevSummaryHeader(7, prevCreatedAt)
	if !strings.Contains(desc, header) {
		t.Fatalf("expected description to contain the framing header %q, got %q", header, desc)
	}
	if !strings.Contains(desc, prevSummaryFooter) {
		t.Fatalf("expected description to contain the closing marker %q, got %q", prevSummaryFooter, desc)
	}
	headerIdx := strings.Index(desc, header)
	commentIdx := strings.Index(desc, lastComment)
	footerIdx := strings.Index(desc, prevSummaryFooter)
	if commentIdx == -1 {
		t.Fatalf("expected description to still contain the previous comment text, got %q", desc)
	}
	if headerIdx == -1 || headerIdx > commentIdx {
		t.Fatalf("expected framing header to precede the previous comment, got %q", desc)
	}
	if footerIdx == -1 || footerIdx < commentIdx {
		t.Fatalf("expected closing marker to follow the previous comment, got %q", desc)
	}
	instructionsIdx := strings.Index(desc, "Do the daily sweep.")
	if instructionsIdx == -1 || instructionsIdx > headerIdx {
		t.Fatalf("expected this run's own instructions to precede the framed prior-report block, got %q", desc)
	}
}

// Riker's review on PR #358 (task c9fa3b4b): two of six live schedules put
// {{.PrevSummary}} mid-template, not at the end. Without a closing marker,
// everything after the placeholder falls inside the "NOT instructions for
// this run" block — an inversion of the original bug: real instructions get
// mislabeled as historical noise instead of the other way round. This test
// reproduces that exact template shape and asserts the text AFTER the
// placeholder sits OUTSIDE the framed block.
func TestCreateInstance_PrevSummaryMidTemplate_LaterStepsStayOutsideFrame(t *testing.T) {
	repo := NewMockRecurringRepository()
	taskSvc := NewStubTaskService()
	svc := NewRecurringService(repo, taskSvc).(*recurringService)

	scheduleID := uuid.New()
	const step2Marker = "Step 2 — must stay outside the framed block."
	schedule := &domain.RecurringSchedule{
		ID:                  scheduleID,
		ProjectID:           uuid.New(),
		WorkspaceID:         uuid.New(),
		TitleTemplate:       "Task {{.Number}}",
		DescriptionTemplate: "Step 1.\n{{.PrevSummary}}\n" + step2Marker,
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

	lastComment := "Done. Yesterday's report."
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

	footerIdx := strings.Index(desc, prevSummaryFooter)
	step2Idx := strings.Index(desc, step2Marker)
	if footerIdx == -1 {
		t.Fatalf("expected description to contain the closing marker, got %q", desc)
	}
	if step2Idx == -1 {
		t.Fatalf("expected description to still contain Step 2's text, got %q", desc)
	}
	if step2Idx < footerIdx {
		t.Fatalf("Step 2 lands INSIDE the framed prior-report block (before the closing marker) — "+
			"real instructions would read as historical context, got %q", desc)
	}
}

// A schedule whose template does not reference {{.PrevSummary}} must render a
// description byte-for-byte equal to a plain-text template — no leakage from
// the previous instance, no framing header inserted where none was asked for.
func TestCreateInstance_NoPrevSummaryPlaceholder_DescriptionMatchesTemplate(t *testing.T) {
	repo := NewMockRecurringRepository()
	taskSvc := NewStubTaskService()
	svc := NewRecurringService(repo, taskSvc).(*recurringService)

	scheduleID := uuid.New()
	const tmpl = "Fixed description, no template variables at all."
	schedule := &domain.RecurringSchedule{
		ID:                  scheduleID,
		ProjectID:           uuid.New(),
		WorkspaceID:         uuid.New(),
		TitleTemplate:       "Task {{.Number}}",
		DescriptionTemplate: tmpl,
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

	lastComment := "Done. Some unrelated report from the previous instance."
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
	if got := created[0].Description; got != tmpl {
		t.Fatalf("expected description to be byte-for-byte equal to the template %q, got %q", tmpl, got)
	}
}

// AC#3 — repro: schedule with byte-truncated PrevSummary must complete without error
//
//	and next_run_at must advance.
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

// ---------------------------------------------------------------------------
// Fix 2 — createInstance strips user assignee
// ---------------------------------------------------------------------------

func newMinimalSchedule() *domain.RecurringSchedule {
	nr := time.Now().Add(-time.Minute)
	return &domain.RecurringSchedule{
		ID:            uuid.New(),
		ProjectID:     uuid.New(),
		WorkspaceID:   uuid.New(),
		TitleTemplate: "Task {{.Number}}",
		Frequency:     domain.RecurringFrequencyCustom,
		CronExpr:      "0 9 * * *",
		Timezone:      "UTC",
		AssigneeType:  domain.AssigneeTypeUnassigned,
		Priority:      domain.PriorityMedium,
		Labels:        pq.StringArray{},
		IsActive:      true,
		StartsAt:      time.Now().Add(-time.Hour),
		NextRunAt:     &nr,
		InstanceCount: 0,
		CreatedBy:     uuid.New(),
		CreatedByType: domain.ActorTypeUser,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
}

func TestCreateInstance_StripsUserAssignee(t *testing.T) {
	userID := uuid.New()
	schedule := newMinimalSchedule()
	schedule.AssigneeID = &userID
	schedule.AssigneeType = domain.AssigneeTypeUser

	repo := NewMockRecurringRepository()
	_ = repo.Create(context.Background(), schedule)

	taskSvc := NewStubTaskService()
	svc := NewRecurringService(repo, taskSvc).(*recurringService)

	_, err := svc.createInstance(context.Background(), schedule, time.Now())
	if err != nil {
		t.Fatalf("createInstance failed: %v", err)
	}

	taskSvc.mu.Lock()
	defer taskSvc.mu.Unlock()
	if len(taskSvc.created) != 1 {
		t.Fatalf("expected 1 task, got %d", len(taskSvc.created))
	}
	task := taskSvc.created[0]
	if task.AssigneeType != domain.AssigneeTypeUnassigned {
		t.Errorf("AssigneeType = %q, want %q", task.AssigneeType, domain.AssigneeTypeUnassigned)
	}
	if task.AssigneeID != nil {
		t.Errorf("AssigneeID = %v, want nil", task.AssigneeID)
	}
}

func TestCreateInstance_PreservesAgentAssignee(t *testing.T) {
	agentID := uuid.New()
	schedule := newMinimalSchedule()
	schedule.AssigneeID = &agentID
	schedule.AssigneeType = domain.AssigneeTypeAgent

	repo := NewMockRecurringRepository()
	_ = repo.Create(context.Background(), schedule)

	taskSvc := NewStubTaskService()
	svc := NewRecurringService(repo, taskSvc).(*recurringService)

	_, err := svc.createInstance(context.Background(), schedule, time.Now())
	if err != nil {
		t.Fatalf("createInstance failed: %v", err)
	}

	taskSvc.mu.Lock()
	defer taskSvc.mu.Unlock()
	task := taskSvc.created[0]
	if task.AssigneeType != domain.AssigneeTypeAgent {
		t.Errorf("AssigneeType = %q, want agent", task.AssigneeType)
	}
	if task.AssigneeID == nil || *task.AssigneeID != agentID {
		t.Errorf("AssigneeID = %v, want %v", task.AssigneeID, agentID)
	}
}

func TestCreateInstance_SetsAssignedBySystem(t *testing.T) {
	schedule := newMinimalSchedule()

	repo := NewMockRecurringRepository()
	_ = repo.Create(context.Background(), schedule)

	taskSvc := NewStubTaskService()
	svc := NewRecurringService(repo, taskSvc).(*recurringService)

	_, err := svc.createInstance(context.Background(), schedule, time.Now())
	if err != nil {
		t.Fatalf("createInstance failed: %v", err)
	}

	taskSvc.mu.Lock()
	defer taskSvc.mu.Unlock()
	if len(taskSvc.created) != 1 {
		t.Fatalf("expected 1 task created, got %d", len(taskSvc.created))
	}
	if got := taskSvc.created[0].AssignedBy; got != domain.AssignmentSourceSystem {
		t.Errorf("AssignedBy = %q, want %q", got, domain.AssignmentSourceSystem)
	}
}

// ---------------------------------------------------------------------------
// Fix 1 — runOneSchedule calls SupersedeRecurringInstances
// ---------------------------------------------------------------------------

// spyTaskService wraps StubTaskService and records SupersedeRecurringInstances calls.
type spyTaskService struct {
	*StubTaskService
	mu             sync.Mutex
	supersedeCalls []struct{ scheduleID, newTaskID uuid.UUID }
}

func newSpyTaskService() *spyTaskService {
	return &spyTaskService{StubTaskService: NewStubTaskService()}
}

func (s *spyTaskService) SupersedeRecurringInstances(_ context.Context, scheduleID, newTaskID uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.supersedeCalls = append(s.supersedeCalls, struct{ scheduleID, newTaskID uuid.UUID }{scheduleID, newTaskID})
	return nil
}

func TestRunOneSchedule_CallsSupersede(t *testing.T) {
	schedule := newMinimalSchedule()

	repo := NewMockRecurringRepository()
	_ = repo.Create(context.Background(), schedule)

	spy := newSpyTaskService()
	svc := NewRecurringService(repo, spy).(*recurringService)

	created, err := svc.runOneSchedule(context.Background(), schedule)
	if err != nil {
		t.Fatalf("runOneSchedule failed: %v", err)
	}
	if !created {
		t.Fatal("expected instance created")
	}

	spy.mu.Lock()
	calls := spy.supersedeCalls
	spy.mu.Unlock()

	if len(calls) != 1 {
		t.Fatalf("SupersedeRecurringInstances called %d times, want 1", len(calls))
	}
	if calls[0].scheduleID != schedule.ID {
		t.Errorf("supersede scheduleID = %v, want %v", calls[0].scheduleID, schedule.ID)
	}
	// The newTaskID must match the task that was just created.
	spy.StubTaskService.mu.Lock()
	createdTask := spy.created[0]
	spy.StubTaskService.mu.Unlock()
	if calls[0].newTaskID != createdTask.ID {
		t.Errorf("supersede newTaskID = %v, want %v", calls[0].newTaskID, createdTask.ID)
	}
}

// ---------------------------------------------------------------------------
// Assignee tenancy wiring — Create/Update must call taskSvc.ValidateAssigneeForProject
// and propagate its refusal, the same funnel task write paths go through.
// ---------------------------------------------------------------------------

func TestRecurringService_Create_RefusesWhenFunnelRefuses(t *testing.T) {
	stub := NewStubTaskService()
	stub.validateAssigneeErr = &AssigneeNotInWorkspaceError{Reason: "agent belongs to a different workspace"}
	repo := NewMockRecurringRepository()
	svc := NewRecurringService(repo, stub)

	foreignAgent := uuid.New()
	_, err := svc.Create(context.Background(), CreateRecurringInput{
		ProjectID: uuid.New(), TitleTemplate: "x", Frequency: domain.RecurringFrequencyDaily,
		AssigneeID: &foreignAgent, AssigneeType: domain.AssigneeTypeAgent,
	})

	var refused *AssigneeNotInWorkspaceError
	if !errors.As(err, &refused) {
		t.Fatalf("Create() error = %v, want *AssigneeNotInWorkspaceError", err)
	}
	if len(repo.schedules) != 0 {
		t.Fatalf("refused Create must not persist a row, found %d", len(repo.schedules))
	}
}

func TestRecurringService_Create_NativeAssigneeStillPersists(t *testing.T) {
	stub := NewStubTaskService() // no validateAssigneeErr set: funnel says OK
	repo := NewMockRecurringRepository()
	svc := NewRecurringService(repo, stub)

	nativeAgent := uuid.New()
	sched, err := svc.Create(context.Background(), CreateRecurringInput{
		ProjectID: uuid.New(), TitleTemplate: "x", Frequency: domain.RecurringFrequencyDaily,
		AssigneeID: &nativeAgent, AssigneeType: domain.AssigneeTypeAgent,
	})
	if err != nil {
		t.Fatalf("Create() unexpected error: %v", err)
	}
	if sched.AssigneeID == nil || *sched.AssigneeID != nativeAgent {
		t.Fatalf("Create() assignee_id = %v, want %v", sched.AssigneeID, nativeAgent)
	}
}

func TestRecurringService_Create_NoAssignee_SkipsTheFunnel(t *testing.T) {
	stub := NewStubTaskService()
	stub.validateAssigneeErr = &AssigneeNotInWorkspaceError{Reason: "should never be reached"}
	repo := NewMockRecurringRepository()
	svc := NewRecurringService(repo, stub)

	_, err := svc.Create(context.Background(), CreateRecurringInput{
		ProjectID: uuid.New(), TitleTemplate: "x", Frequency: domain.RecurringFrequencyDaily,
	})
	if err != nil {
		t.Fatalf("Create() unexpected error for an unassigned schedule: %v", err)
	}
}

// TestRecurringService_Update_RefusesWhenFunnelRefuses covers the PATCH path,
// and specifically the case where only AssigneeID changes on the request —
// AssigneeID and AssigneeType are independent optional fields on
// UpdateRecurringInput, so the check has to validate the MERGED schedule state
// (existing AssigneeType + new AssigneeID), not just whichever field the PATCH
// happened to name.
func TestRecurringService_Update_RefusesWhenFunnelRefuses(t *testing.T) {
	stub := NewStubTaskService()
	repo := NewMockRecurringRepository()
	svc := NewRecurringService(repo, stub)

	nativeAgent := uuid.New()
	created, err := svc.Create(context.Background(), CreateRecurringInput{
		ProjectID: uuid.New(), TitleTemplate: "x", Frequency: domain.RecurringFrequencyDaily,
		AssigneeID: &nativeAgent, AssigneeType: domain.AssigneeTypeAgent,
	})
	if err != nil {
		t.Fatalf("setup Create() failed: %v", err)
	}

	stub.validateAssigneeErr = &AssigneeNotInWorkspaceError{Reason: "agent belongs to a different workspace"}
	foreignAgent := uuid.New()

	_, err = svc.Update(context.Background(), created.ID, UpdateRecurringInput{AssigneeID: &foreignAgent})

	var refused *AssigneeNotInWorkspaceError
	if !errors.As(err, &refused) {
		t.Fatalf("Update() error = %v, want *AssigneeNotInWorkspaceError", err)
	}
	stored, _ := repo.GetByID(context.Background(), created.ID)
	if stored.AssigneeID == nil || *stored.AssigneeID != nativeAgent {
		t.Fatalf("refused Update must leave the original assignee in place, got %v, want %v", stored.AssigneeID, nativeAgent)
	}
}

func TestRecurringService_Update_UnrelatedFieldSkipsTheFunnel(t *testing.T) {
	stub := NewStubTaskService()
	repo := NewMockRecurringRepository()
	svc := NewRecurringService(repo, stub)

	created, err := svc.Create(context.Background(), CreateRecurringInput{
		ProjectID: uuid.New(), TitleTemplate: "x", Frequency: domain.RecurringFrequencyDaily,
	})
	if err != nil {
		t.Fatalf("setup Create() failed: %v", err)
	}

	stub.validateAssigneeErr = &AssigneeNotInWorkspaceError{Reason: "should never be reached"}
	newTitle := "renamed"
	_, err = svc.Update(context.Background(), created.ID, UpdateRecurringInput{TitleTemplate: &newTitle})
	if err != nil {
		t.Fatalf("Update() unexpected error for a PATCH that never touches assignee_id: %v", err)
	}
}

// TestRecurringService_Update_NativeAssigneeSucceeds_RealTaskService is the
// success-path counterpart to TestRecurringService_Update_RefusesWhenFunnelRefuses,
// run against the REAL taskService (setupTenancyEnv), not a stub — proving the
// wiring works end to end through the actual resolveAssigneeType +
// assertAssigneeInProjectWorkspace funnel.
func TestRecurringService_Update_NativeAssigneeSucceeds_RealTaskService(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	repo := NewMockRecurringRepository()
	svc := NewRecurringService(repo, env.svc)

	created, err := svc.Create(context.Background(), CreateRecurringInput{
		ProjectID: env.projectID, TitleTemplate: "x", Frequency: domain.RecurringFrequencyDaily,
	})
	if err != nil {
		t.Fatalf("setup Create() failed: %v", err)
	}

	updated, err := svc.Update(context.Background(), created.ID, UpdateRecurringInput{
		AssigneeID: &env.nativeAgent,
	})
	if err != nil {
		t.Fatalf("Update() unexpected error for a native-workspace agent: %v", err)
	}
	if updated.AssigneeID == nil || *updated.AssigneeID != env.nativeAgent {
		t.Fatalf("Update() assignee_id = %v, want %v", updated.AssigneeID, env.nativeAgent)
	}
	if updated.AssigneeType != domain.AssigneeTypeAgent {
		t.Fatalf("Update() assignee_type = %v, want agent (resolved from directory)", updated.AssigneeType)
	}
}
