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

// parkHarness extends the reaper harness with a mid-pipeline rules source and a
// backlog status, so a test can say "this project parks" in one line.
type parkHarness struct {
	*reaperHarness
	rules     *stubRulesSvc
	projectID uuid.UUID
	backlogID uuid.UUID
}

func newParkHarness(t *testing.T, cfg *domain.MidPipelineConfig) *parkHarness {
	t.Helper()
	h := newReaperHarness()
	rules := &stubRulesSvc{cfg: cfg}
	h.reaper.(*checkoutLeaseReaper).rulesSvc = rules

	projectID := uuid.New()
	h.addTodoStatus(t, projectID)
	backlog := &domain.TaskStatus{
		ID:        uuid.New(),
		ProjectID: projectID,
		Name:      "backlog",
		Category:  domain.StatusCategoryBacklog,
	}
	if err := h.statusRepo.Create(context.Background(), backlog); err != nil {
		t.Fatalf("create backlog status: %v", err)
	}
	return &parkHarness{reaperHarness: h, rules: rules, projectID: projectID, backlogID: backlog.ID}
}

func (h *parkHarness) stored(t *testing.T, id uuid.UUID) *domain.Task {
	t.Helper()
	got, err := h.taskRepo.GetByID(context.Background(), id)
	if err != nil || got == nil {
		t.Fatalf("task %s not readable after sweep: %v", id, err)
	}
	return got
}

func (h *parkHarness) commentBodies() []string {
	h.commentRepo.mu.Lock()
	defer h.commentRepo.mu.Unlock()
	var out []string
	for _, c := range h.commentRepo.items {
		out = append(out, c.Body)
	}
	return out
}

// ── the behaviour change, and its off-switch ──────────────────────────────

// The whole point of the flag: with it ON a stalled card goes to backlog with an
// alarm, not back into the feed.
func TestPark_Enabled_ParksToBacklogWithDueDateAndMonitorLabel(t *testing.T) {
	h := newParkHarness(t, &domain.MidPipelineConfig{AutoParkStalled: true})
	task := h.addUnleasedTask(t, h.projectID, 4*time.Hour)

	// Read the same clock the park does — this package's tests swap timeNow
	// for a frozen one, so wall time is not a safe reference here.
	before := timeNow()
	n, err := h.reaper.SweepUnleasedInProgress(context.Background(), DefaultUnleasedGrace)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 task handled, got %d", n)
	}

	if len(h.mover.movesSeen) != 1 || h.mover.movesSeen[0] != task.ID {
		t.Fatalf("MoveTask not called for the stalled task: %v", h.mover.movesSeen)
	}

	got := h.stored(t, task.ID)

	// The alarm. Without a due_date the park has no wake-up path at all.
	if got.DueDate == nil {
		t.Fatal("parked task has no due_date — nothing will ever wake it; this is the nine-day failure")
	}
	wantLo := before.Add(23*time.Hour + 55*time.Minute)
	wantHi := before.Add(24*time.Hour + 5*time.Minute)
	if got.DueDate.Before(wantLo) || got.DueDate.After(wantHi) {
		t.Errorf("due_date %v is not ~24h out (expected between %v and %v)", got.DueDate, wantLo, wantHi)
	}

	// The label. MonitorPromotionService filters on this exact string, so a park
	// without it is invisible to the only thing that could bring it back.
	if !containsInStringArray(got.Labels, parkMonitorLabel) {
		t.Fatalf("parked task is missing the %q label — monitor-promotion will never pick it up; labels=%v",
			parkMonitorLabel, got.Labels)
	}
}

// The off-switch, and the guarantee that every project which has not opted in
// keeps the behaviour it has today.
func TestPark_Disabled_StillReturnsToTodo(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  *domain.MidPipelineConfig
	}{
		{"no mid_pipeline block", nil},
		{"block present, flag off", &domain.MidPipelineConfig{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newParkHarness(t, tc.cfg)
			task := h.addUnleasedTask(t, h.projectID, 4*time.Hour)

			if _, err := h.reaper.SweepUnleasedInProgress(context.Background(), DefaultUnleasedGrace); err != nil {
				t.Fatalf("sweep: %v", err)
			}
			got := h.stored(t, task.ID)
			if got.DueDate != nil {
				t.Error("a task in a non-parking project got a due_date")
			}
			if containsInStringArray(got.Labels, parkMonitorLabel) {
				t.Errorf("a task in a non-parking project got the %q label", parkMonitorLabel)
			}
		})
	}
}

// A reaper built with no rules source at all must behave exactly as it did
// before this option existed.
func TestPark_NoRulesSource_NeverParks(t *testing.T) {
	h := newReaperHarness()
	projectID := uuid.New()
	h.addTodoStatus(t, projectID)
	task := h.addUnleasedTask(t, projectID, 4*time.Hour)

	if _, err := h.reaper.SweepUnleasedInProgress(context.Background(), DefaultUnleasedGrace); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	got, _ := h.taskRepo.GetByID(context.Background(), task.ID)
	if got.DueDate != nil || containsInStringArray(got.Labels, parkMonitorLabel) {
		t.Fatal("reaper parked a task with no rules source wired — it cannot know any project opted in")
	}
}

// An unreadable config must leave the pre-existing behaviour in place. Parking is
// the stronger action (it hides a card for a day); guessing at it on a failed
// read is the wrong direction to fail.
func TestPark_RulesReadError_FallsBackToTodo(t *testing.T) {
	h := newParkHarness(t, nil)
	h.rules.err = errors.New("rules unavailable")
	task := h.addUnleasedTask(t, h.projectID, 4*time.Hour)

	if _, err := h.reaper.SweepUnleasedInProgress(context.Background(), DefaultUnleasedGrace); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	got := h.stored(t, task.ID)
	if got.DueDate != nil {
		t.Fatal("reaper parked a task on an unreadable config — it must degrade to the old behaviour, not to the stronger one")
	}
	if len(h.mover.movesSeen) != 1 {
		t.Fatalf("task was neither parked nor returned to todo: moves=%v", h.mover.movesSeen)
	}
}

// ── ordering: the alarm is armed before the move ──────────────────────────

// If the park could move first and arm second, a failure of the second step
// would leave the card in backlog with no due_date and no label: invisible to
// the feed AND to monitor-promotion, i.e. asleep until a human finds it. Arming
// first means a failure leaves the card exactly where it was, to be retried next
// tick. This test drives that failure and asserts the card did NOT move.
func TestPark_ArmFailure_LeavesTaskWhereItWasRatherThanStrandedInBacklog(t *testing.T) {
	h := newParkHarness(t, &domain.MidPipelineConfig{AutoParkStalled: true})
	task := h.addUnleasedTask(t, h.projectID, 4*time.Hour)

	// Sweep first (so the find succeeds), then break the write the arm needs.
	tasks, err := h.taskRepo.FindStaleUnleasedInProgress(context.Background(), DefaultUnleasedGrace)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("precondition: expected 1 stale task, got %d (%v)", len(tasks), err)
	}
	h.taskRepo.errToReturn = errors.New("task store unavailable")

	parked := h.reaper.(*checkoutLeaseReaper).parkTask(context.Background(), &tasks[0], 24)

	if parked {
		t.Fatal("parkTask reported success while the alarm write failed")
	}
	if len(h.mover.movesSeen) != 0 {
		t.Fatalf("task was MOVED to backlog even though arming its alarm failed — that strands it with no wake-up path; moves=%v", h.mover.movesSeen)
	}
	_ = task
}

// ── degenerate project shapes ─────────────────────────────────────────────

// A project that opted into parking but has no backlog column must still get its
// stalled cards handed back, not left in_progress forever.
func TestPark_NoBacklogStatus_FallsBackToTodo(t *testing.T) {
	h := newReaperHarness()
	h.reaper.(*checkoutLeaseReaper).rulesSvc = &stubRulesSvc{
		cfg: &domain.MidPipelineConfig{AutoParkStalled: true},
	}
	projectID := uuid.New()
	h.addTodoStatus(t, projectID) // todo exists, backlog deliberately does not
	task := h.addUnleasedTask(t, projectID, 4*time.Hour)

	n, err := h.reaper.SweepUnleasedInProgress(context.Background(), DefaultUnleasedGrace)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("task with no backlog column was dropped entirely; handled=%d", n)
	}
	if len(h.mover.movesSeen) != 1 || h.mover.movesSeen[0] != task.ID {
		t.Fatalf("expected fallback move to todo: %v", h.mover.movesSeen)
	}
}

// ── configuration ─────────────────────────────────────────────────────────

func TestPark_ConfiguredDueHoursIsHonoured(t *testing.T) {
	h := newParkHarness(t, &domain.MidPipelineConfig{AutoParkStalled: true, AutoParkDueHours: 4})
	task := h.addUnleasedTask(t, h.projectID, 4*time.Hour)

	before := timeNow()
	if _, err := h.reaper.SweepUnleasedInProgress(context.Background(), DefaultUnleasedGrace); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	got := h.stored(t, task.ID)
	if got.DueDate == nil {
		t.Fatal("no due_date set")
	}
	if got.DueDate.After(before.Add(4*time.Hour + 5*time.Minute)) {
		t.Errorf("configured auto_park_due_hours=4 ignored; due_date=%v", got.DueDate)
	}
}

func TestMidPipelineConfig_NilSafeAccessors(t *testing.T) {
	var nilCfg *domain.MidPipelineConfig
	if nilCfg.ReviewStrict() || nilCfg.ParkStalled() {
		t.Error("nil config must read as all-off")
	}
	if nilCfg.AutoParkDue() != domain.DefaultAutoParkDueHours {
		t.Errorf("nil config due hours = %d, want default %d", nilCfg.AutoParkDue(), domain.DefaultAutoParkDueHours)
	}
	if (&domain.MidPipelineConfig{AutoParkDueHours: -3}).AutoParkDue() != domain.DefaultAutoParkDueHours {
		t.Error("a negative due-hours must fall back to the default, not schedule a wake-up in the past")
	}
}

// ── the label must not be duplicated on a re-park ─────────────────────────

func TestPark_ExistingMonitorLabelNotDuplicated(t *testing.T) {
	h := newParkHarness(t, &domain.MidPipelineConfig{AutoParkStalled: true})
	task := h.addUnleasedTask(t, h.projectID, 4*time.Hour)
	task.Labels = []string{parkMonitorLabel, "phase:verify"}
	if err := h.taskRepo.Update(context.Background(), task); err != nil {
		t.Fatalf("seed labels: %v", err)
	}

	if _, err := h.reaper.SweepUnleasedInProgress(context.Background(), DefaultUnleasedGrace); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	got := h.stored(t, task.ID)
	var count int
	for _, l := range got.Labels {
		if l == parkMonitorLabel {
			count++
		}
	}
	if count != 1 {
		t.Errorf("%q appears %d times after re-park, want exactly 1: %v", parkMonitorLabel, count, got.Labels)
	}
	if !containsInStringArray(got.Labels, "phase:verify") {
		t.Errorf("park clobbered a pre-existing label: %v", got.Labels)
	}
}

// ── the audit comment must say which of the two things happened ───────────

func TestPark_CommentExplainsParkNotTodoReturn(t *testing.T) {
	h := newParkHarness(t, &domain.MidPipelineConfig{AutoParkStalled: true})
	h.addUnleasedTask(t, h.projectID, 4*time.Hour)

	if _, err := h.reaper.SweepUnleasedInProgress(context.Background(), DefaultUnleasedGrace); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	bodies := h.commentBodies()
	if len(bodies) != 1 {
		t.Fatalf("expected exactly 1 system comment, got %d", len(bodies))
	}
	body := bodies[0]
	for _, want := range []string{"backlog", "due_date", parkMonitorLabel} {
		if !strings.Contains(body, want) {
			t.Errorf("park comment does not mention %q, so a reader cannot tell what happened or how it wakes: %s", want, body)
		}
	}
	if strings.Contains(body, "возвращена в todo") {
		t.Errorf("park comment claims the task went back to todo, which is the OTHER outcome: %s", body)
	}
}

// ── retrying a park whose move is permanently refused ─────────────────────

// A park whose MoveTask is refused must not re-arm the alarm on every retry.
//
// parkTask arms before it moves, deliberately (a half-park with no due_date and
// no label is invisible to everything and sleeps until a human finds it). But the
// original code re-armed unconditionally, so a MoveTask that fails *permanently*
// — a human-gated task, a shipped task, a project rule — turned the 60s reaper
// tick into a write loop that pushed due_date another dueHours out every pass.
// The wake-up the park depends on therefore receded once per minute and could
// never arrive. Measured on prod 2026-09-06, #2921ff07: 145 attempts, due_date
// rewritten to now+24h each time.
//
// The clock is ADVANCED between the two attempts on purpose. Under this package's
// usual frozen clock both attempts would compute the same due_date and the test
// would pass against the unfixed code — a control that cannot fail.
func TestPark_RetryAfterRefusedMove_DoesNotSlideTheAlarm(t *testing.T) {
	h := newParkHarness(t, &domain.MidPipelineConfig{AutoParkStalled: true})
	task := h.addUnleasedTask(t, h.projectID, 4*time.Hour)

	realNow := timeNow
	defer func() { timeNow = realNow }()
	clock := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	timeNow = func() time.Time { return clock }

	// MoveTask refuses the way a human-gated task refuses: the same way, every time.
	h.mover.moveErr = errors.New("task is human-gated: awaiting human sign-off")

	reaper := h.reaper.(*checkoutLeaseReaper)

	load := func() *domain.Task { return h.stored(t, task.ID) }

	// Tick 1.
	if reaper.parkTask(context.Background(), load(), 24) {
		t.Fatal("parkTask reported success while MoveTask refused the move")
	}
	afterFirst := load()
	if afterFirst.DueDate == nil {
		t.Fatal("first attempt did not arm the alarm at all — the pre-arm ordering is gone")
	}
	firstDue := *afterFirst.DueDate

	// A minute passes, as it does between reaper ticks.
	clock = clock.Add(1 * time.Minute)

	// Tick 2, on the task as it now stands in the store.
	if reaper.parkTask(context.Background(), load(), 24) {
		t.Fatal("parkTask reported success on retry while MoveTask still refused")
	}
	afterSecond := load()
	if afterSecond.DueDate == nil {
		t.Fatal("retry cleared the due_date that the first attempt armed")
	}

	if !afterSecond.DueDate.Equal(firstDue) {
		t.Errorf("retry SLID the park alarm: %v → %v (+%v). Every refused tick pushes the wake-up further away, so it never fires",
			firstDue, *afterSecond.DueDate, afterSecond.DueDate.Sub(firstDue))
	}

	// The label must survive the retry too — monitor-promotion filters on it.
	if !containsInStringArray(afterSecond.Labels, parkMonitorLabel) {
		t.Errorf("retry lost the %q label; labels=%v", parkMonitorLabel, afterSecond.Labels)
	}
}

// The companion to the test above: a park alarm that has already FIRED (due_date
// in the past) must be re-armed, not treated as still-armed. Otherwise a card that
// parked, woke on its due_date, was worked, and stalled again would inherit a
// deadline that is already behind it and never wake a second time.
func TestPark_ExpiredAlarmIsRearmedNotTreatedAsArmed(t *testing.T) {
	h := newParkHarness(t, &domain.MidPipelineConfig{AutoParkStalled: true})
	task := h.addUnleasedTask(t, h.projectID, 4*time.Hour)

	realNow := timeNow
	defer func() { timeNow = realNow }()
	clock := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	timeNow = func() time.Time { return clock }

	// A stale alarm from a previous park: label present, deadline long past.
	stale := clock.Add(-72 * time.Hour)
	seed := h.stored(t, task.ID)
	seed.DueDate = &stale
	seed.Labels = append(seed.Labels, parkMonitorLabel)
	if err := h.taskRepo.Update(context.Background(), seed); err != nil {
		t.Fatalf("seed stale alarm: %v", err)
	}

	if !h.reaper.(*checkoutLeaseReaper).parkTask(context.Background(), h.stored(t, task.ID), 24) {
		t.Fatal("parkTask failed on a task carrying an expired alarm")
	}

	got := h.stored(t, task.ID)
	if got.DueDate == nil || !got.DueDate.After(clock) {
		t.Fatalf("expired alarm was treated as still-armed: due_date=%v is not in the future relative to %v — the card would never wake",
			got.DueDate, clock)
	}
}
