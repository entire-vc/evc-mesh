package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ---------------------------------------------------------------------------
// Park-alarm gate (#559270cf).
//
// A task parked in backlog under a passive-wait label with no FUTURE due_date has no
// wake-up path: no agent feed polls backlog, and the promotion sweeper fires on
// due_date. The audit counted 34 such cards live, 11 of them already classified "parked
// with no exit". The gate refuses the two writes that create one — the move into
// backlog, and the PATCH that adds the label or removes the date on a card already
// there — and refuses nothing else.
//
// The clock is frozen (setupTaskService), so "future" and "past" here are relative to
// frozenTime, not to wall time. That matters: hasArmedAlarm reads timeNow(), the same
// seam, so these tests exercise the production comparison rather than a parallel one.
// ---------------------------------------------------------------------------

func parkTestDue(offset time.Duration) *time.Time {
	d := frozenTime.Add(offset)
	return &d
}

// alarmGateHarness builds a task service with a backlog and a todo status in one project.
type alarmGateHarness struct {
	svc      *taskService
	taskRepo *MockTaskRepository
	project  uuid.UUID
	backlog  *domain.TaskStatus
	todo     *domain.TaskStatus
}

func newAlarmGateHarness(t *testing.T) *alarmGateHarness {
	t.Helper()
	svc, taskRepo, statusRepo := setupTaskService()
	project := uuid.New()

	mk := func(cat domain.StatusCategory) *domain.TaskStatus {
		st := &domain.TaskStatus{ID: uuid.New(), ProjectID: project, Name: string(cat), Category: cat}
		require.NoError(t, statusRepo.Create(context.Background(), st))
		return st
	}
	h := &alarmGateHarness{svc: svc, taskRepo: taskRepo, project: project}
	h.backlog = mk(domain.StatusCategoryBacklog)
	h.todo = mk(domain.StatusCategoryTodo)
	return h
}

func (h *alarmGateHarness) seed(t *testing.T, statusID uuid.UUID, labels []string, due *time.Time) *domain.Task {
	t.Helper()
	task := &domain.Task{
		ID:        uuid.New(),
		ProjectID: h.project,
		StatusID:  statusID,
		Title:     "card",
		Labels:    labels,
		DueDate:   due,
	}
	require.NoError(t, h.taskRepo.Create(context.Background(), task))
	return task
}

func (h *alarmGateHarness) moveToBacklog(task *domain.Task) error {
	return h.svc.MoveTask(context.Background(), task.ID, MoveTaskInput{StatusID: &h.backlog.ID})
}

// --- MoveTask side ---------------------------------------------------------

func TestParkAlarm_MoveToBacklog_LabelledNoDueDate_Refused(t *testing.T) {
	for _, label := range []string{"kind:monitor", "phase:verify"} {
		t.Run(label, func(t *testing.T) {
			h := newAlarmGateHarness(t)
			task := h.seed(t, h.todo.ID, []string{label}, nil)

			err := h.moveToBacklog(task)
			require.Error(t, err, "a park with no wake-up was accepted")

			var parkErr *ParkAlarmError
			require.ErrorAs(t, err, &parkErr)
			assert.Equal(t, label, parkErr.Label, "the refusal must name the label that caused it")

			stored, getErr := h.taskRepo.GetByID(context.Background(), task.ID)
			require.NoError(t, getErr)
			assert.Equal(t, h.todo.ID, stored.StatusID, "the move was applied despite the refusal")
		})
	}
}

// A due_date that has already fired is not an alarm. Accepting one would produce a park
// the very next sweeper tick undoes — reported to the caller as success.
func TestParkAlarm_MoveToBacklog_LabelledPastDueDate_Refused(t *testing.T) {
	h := newAlarmGateHarness(t)
	task := h.seed(t, h.todo.ID, []string{"kind:monitor"}, parkTestDue(-time.Hour))

	err := h.moveToBacklog(task)
	var parkErr *ParkAlarmError
	require.ErrorAs(t, err, &parkErr, "an already-fired due_date is not a wake-up")
}

// The positive control. Without it, a gate that refused EVERY move into backlog would
// be indistinguishable from this one.
func TestParkAlarm_MoveToBacklog_LabelledFutureDueDate_Allowed(t *testing.T) {
	h := newAlarmGateHarness(t)
	task := h.seed(t, h.todo.ID, []string{"kind:monitor"}, parkTestDue(24*time.Hour))

	require.NoError(t, h.moveToBacklog(task))

	stored, err := h.taskRepo.GetByID(context.Background(), task.ID)
	require.NoError(t, err)
	assert.Equal(t, h.backlog.ID, stored.StatusID, "an armed park must go through")
}

// Second positive control: an ordinary backlog card is not a passive-wait park and
// needs no date. The gate keys on the label, not on the destination alone.
func TestParkAlarm_MoveToBacklog_Unlabelled_Allowed(t *testing.T) {
	h := newAlarmGateHarness(t)
	task := h.seed(t, h.todo.ID, []string{"audit-2026-09"}, nil)

	require.NoError(t, h.moveToBacklog(task), "an ordinary backlog move must not need a due_date")
}

// The gate is about backlog specifically — backlog is the status nothing polls.
func TestParkAlarm_MoveToTodo_LabelledNoDueDate_Allowed(t *testing.T) {
	h := newAlarmGateHarness(t)
	task := h.seed(t, h.backlog.ID, []string{"kind:monitor"}, nil)

	err := h.svc.MoveTask(context.Background(), task.ID, MoveTaskInput{StatusID: &h.todo.ID})
	require.NoError(t, err, "moving a labelled card OUT of backlog must never be gated")
}

// Explicit opt-out. Proves the flag is actually read, not just declared.
func TestParkAlarm_MoveToBacklog_ProjectOptedOut_Allowed(t *testing.T) {
	h := newAlarmGateHarness(t)
	off := false
	mockRules := NewMockRulesService(nil).WithWorkflowRules(&domain.WorkflowRulesResponse{
		WorkflowRulesConfig: domain.WorkflowRulesConfig{
			MidPipeline: &domain.MidPipelineConfig{RequireParkAlarm: &off},
		},
	})
	h.svc.rulesConfigSvc = mockRules

	task := h.seed(t, h.todo.ID, []string{"kind:monitor"}, nil)
	require.NoError(t, h.moveToBacklog(task), "require_park_alarm=false must disable the gate")
}

// Nil-defaults-ON, and it stays on when the rules service cannot answer. This is the
// deliberate divergence from midPipelineConfig's fail-OPEN contract, and it is asserted
// here rather than only described in a comment.
func TestParkAlarm_MoveToBacklog_RulesServiceUnreadable_StillGated(t *testing.T) {
	h := newAlarmGateHarness(t)
	broken := NewMockRulesService(nil)
	broken.errToReturn = errors.New("rules service down")
	h.svc.rulesConfigSvc = broken

	task := h.seed(t, h.todo.ID, []string{"kind:monitor"}, nil)

	var parkErr *ParkAlarmError
	require.ErrorAs(t, h.moveToBacklog(task), &parkErr,
		"an unreadable rules config must leave this gate ON, not silently off")
}

// --- Update (PATCH) side ---------------------------------------------------

func (h *alarmGateHarness) update(t *testing.T, task *domain.Task, mutate func(*domain.Task)) error {
	t.Helper()
	stored, err := h.taskRepo.GetByID(context.Background(), task.ID)
	require.NoError(t, err)
	next := *stored
	mutate(&next)
	return h.svc.Update(context.Background(), &next)
}

func TestParkAlarm_Update_AddsLabelToDatelessBacklogCard_Refused(t *testing.T) {
	h := newAlarmGateHarness(t)
	task := h.seed(t, h.backlog.ID, nil, nil)

	err := h.update(t, task, func(x *domain.Task) { x.Labels = []string{"kind:monitor"} })

	var parkErr *ParkAlarmError
	require.ErrorAs(t, err, &parkErr, "adding a passive-wait label to a dateless backlog card creates the exact broken state")
}

func TestParkAlarm_Update_ClearsDueDateOnLabelledBacklogCard_Refused(t *testing.T) {
	h := newAlarmGateHarness(t)
	task := h.seed(t, h.backlog.ID, []string{"kind:monitor"}, parkTestDue(24*time.Hour))

	err := h.update(t, task, func(x *domain.Task) { x.DueDate = nil })

	var parkErr *ParkAlarmError
	require.ErrorAs(t, err, &parkErr, "removing the only wake-up from a parked card must be refused")
}

func TestParkAlarm_Update_AddsLabelWithFutureDueDate_Allowed(t *testing.T) {
	h := newAlarmGateHarness(t)
	task := h.seed(t, h.backlog.ID, nil, nil)

	err := h.update(t, task, func(x *domain.Task) {
		x.Labels = []string{"phase:verify"}
		x.DueDate = parkTestDue(48 * time.Hour)
	})
	require.NoError(t, err, "label and alarm set in one PATCH must be accepted")
}

// The hostage clause. 34 such cards exist right now; every one of them has to stay
// editable, or the only route to fixing a title runs through a field the caller is not
// touching.
func TestParkAlarm_Update_AlreadyAlarmlessCard_StaysEditable(t *testing.T) {
	h := newAlarmGateHarness(t)
	task := h.seed(t, h.backlog.ID, []string{"kind:monitor"}, nil)

	err := h.update(t, task, func(x *domain.Task) { x.Title = "renamed while still alarmless" })
	require.NoError(t, err, "an existing alarmless park must not become uneditable")

	stored, getErr := h.taskRepo.GetByID(context.Background(), task.ID)
	require.NoError(t, getErr)
	assert.Equal(t, "renamed while still alarmless", stored.Title)
}

// ...and fixing it is exactly what the gate should welcome.
func TestParkAlarm_Update_ArmingAnAlarmlessCard_Allowed(t *testing.T) {
	h := newAlarmGateHarness(t)
	task := h.seed(t, h.backlog.ID, []string{"kind:monitor"}, nil)

	err := h.update(t, task, func(x *domain.Task) { x.DueDate = parkTestDue(72 * time.Hour) })
	require.NoError(t, err, "setting the missing due_date must be accepted")
}

// Dropping the label is the other legitimate fix: the card is ready to be picked up.
func TestParkAlarm_Update_DroppingLabel_Allowed(t *testing.T) {
	h := newAlarmGateHarness(t)
	task := h.seed(t, h.backlog.ID, []string{"kind:monitor"}, nil)

	err := h.update(t, task, func(x *domain.Task) { x.Labels = []string{"audit-2026-09"} })
	require.NoError(t, err, "removing the passive-wait label must be accepted")
}

// A labelled card outside backlog is not a park at all — due_date there is an ordinary
// deadline and clearing it is an ordinary edit.
func TestParkAlarm_Update_LabelledCardInTodo_NotGated(t *testing.T) {
	h := newAlarmGateHarness(t)
	task := h.seed(t, h.todo.ID, []string{"kind:monitor"}, parkTestDue(24*time.Hour))

	err := h.update(t, task, func(x *domain.Task) { x.DueDate = nil })
	require.NoError(t, err, "the gate must only apply to cards resting in backlog")
}

// --- Unit-level predicate checks -------------------------------------------

func TestParkAlarm_AlarmStateUnchanged(t *testing.T) {
	future := parkTestDue(time.Hour)
	past := parkTestDue(-time.Hour)

	cases := []struct {
		name      string
		oldLabels []string
		oldDue    *time.Time
		newLabels []string
		newDue    *time.Time
		want      bool
	}{
		{"both fine", nil, nil, nil, nil, true},
		{"both alarmless, same label", []string{"kind:monitor"}, nil, []string{"kind:monitor"}, nil, true},
		{"both alarmless, past date still counts as none", []string{"kind:monitor"}, past, []string{"kind:monitor"}, nil, true},
		{"label swapped between two alarmless states", []string{"kind:monitor"}, nil, []string{"phase:verify"}, nil, false},
		{"newly broken", nil, nil, []string{"kind:monitor"}, nil, false},
		{"newly fixed", []string{"kind:monitor"}, nil, []string{"kind:monitor"}, future, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := alarmStateUnchanged(tc.oldLabels, tc.oldDue, tc.newLabels, tc.newDue)
			assert.Equal(t, tc.want, got)
		})
	}
}
