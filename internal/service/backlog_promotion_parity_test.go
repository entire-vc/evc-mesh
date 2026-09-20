package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Parity tests (#c15c503a): one per condition the intake sweep enforces and the
// advisory rule used to ignore. Each states the sweep function it mirrors.

type parityEnv struct {
	*backlogHarness
	projectID uuid.UUID
	backlog   *domain.TaskStatus
}

func newParityEnv(t *testing.T) *parityEnv {
	t.Helper()
	h := newBacklogHarness()
	pid := uuid.New()
	b := h.addStatus(t, pid, "Backlog", domain.StatusCategoryBacklog)
	h.addStatus(t, pid, "Todo", domain.StatusCategoryTodo)
	return &parityEnv{backlogHarness: h, projectID: pid, backlog: b}
}

func (e *parityEnv) task(t *testing.T, mut func(*domain.Task)) *domain.Task {
	t.Helper()
	task := e.addTask(t, e.projectID, e.backlog.ID)
	if mut != nil {
		mut(task)
		if err := e.taskRepo.Update(context.Background(), task); err != nil {
			t.Fatalf("update: %v", err)
		}
	}
	return task
}

func (e *parityEnv) verdict(t *testing.T, task *domain.Task) BacklogPromotionDecision {
	t.Helper()
	ds, err := e.svc.SweepAdvisory(context.Background())
	if err != nil {
		t.Fatalf("SweepAdvisory: %v", err)
	}
	return decisionFor(t, ds, task.ID)
}

func (e *parityEnv) wantHold(t *testing.T, task *domain.Task) {
	t.Helper()
	if d := e.verdict(t, task); d.Promote {
		t.Fatalf("expected hold, got promote (%q)", d.Reason)
	}
}

func (e *parityEnv) wantPromote(t *testing.T, task *domain.Task) {
	t.Helper()
	if d := e.verdict(t, task); !d.Promote {
		t.Fatalf("expected promote, got hold (%q)", d.Reason)
	}
}

// --- is_human_gated (cheap path) -------------------------------------------------

func TestParity_HumanGate_AssigneeUser(t *testing.T) {
	e := newParityEnv(t)
	e.wantHold(t, e.task(t, func(x *domain.Task) { x.AssigneeType = domain.AssigneeTypeUser }))
}

func TestParity_HumanGate_Supervised(t *testing.T) {
	e := newParityEnv(t)
	e.wantHold(t, e.task(t, func(x *domain.Task) { x.DelegationLevel = domain.DelegationLevelSupervised }))
}

func TestParity_HumanGate_GateLabelSpellings(t *testing.T) {
	// `blocked-on-pavel` (hyphenated, what agents emit) and `blocked:pavel` must both
	// hold: norm_label makes the spelling irrelevant.
	for _, l := range []string{"human-verify", "kind:human-verify", "blocked-on-pavel", "blocked:pavel", "Needs:Pavel", "host:macbook"} {
		e := newParityEnv(t)
		e.wantHold(t, e.task(t, func(x *domain.Task) { x.Labels = []string{l} }))
	}
}

func TestParity_HumanGate_DescriptionAskHoldsButQuotedMarkerDoesNot(t *testing.T) {
	e := newParityEnv(t)
	armed := e.task(t, func(x *domain.Task) { x.Description = "context\n\n❓ **Blocking @pavel**: approve?" })
	quoted := e.task(t, func(x *domain.Task) { x.Description = "the marker is written as `❓ Blocking @pavel` in code" })
	ds, _ := e.svc.SweepAdvisory(context.Background())
	if decisionFor(t, ds, armed.ID).Promote {
		t.Fatal("a marker at the start of a description line is an open ask and must hold")
	}
	if !decisionFor(t, ds, quoted.ID).Promote {
		t.Fatal("a marker quoted as code documents the mechanism and must not arm it (#ce053513)")
	}
}

func TestParity_HumanGate_ServerFlagStillHolds(t *testing.T) {
	e := newParityEnv(t)
	e.wantHold(t, e.task(t, func(x *domain.Task) { x.HumanGate = true }))
}

// --- parent_awaits_human / parent_is_eval_fixture --------------------------------

func TestParity_ParentGate(t *testing.T) {
	cases := map[string]func(*domain.Task){
		"supervised":   func(p *domain.Task) { p.DelegationLevel = domain.DelegationLevelSupervised },
		"human_gate":   func(p *domain.Task) { p.HumanGate = true },
		"freeze":       func(p *domain.Task) { p.Labels = []string{"freeze"} },
		"no-promote":   func(p *domain.Task) { p.Labels = []string{"no-promote"} },
		"eval fixture": func(p *domain.Task) { p.Labels = []string{"eval-harness"} },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			e := newParityEnv(t)
			parent := e.task(t, mut)
			child := e.task(t, func(x *domain.Task) { x.ParentTaskID = &parent.ID })
			e.wantHold(t, child)
		})
	}
}

func TestParity_ParentGate_PassiveLabelOnParentDoesNotFreezeChildren(t *testing.T) {
	// 08.09.2026 narrowing: kind:monitor / awaiting-window on a master mean "watch
	// card", not "freeze the work under me".
	e := newParityEnv(t)
	parent := e.task(t, func(p *domain.Task) { p.Labels = []string{"kind:monitor"} })
	child := e.task(t, func(x *domain.Task) { x.ParentTaskID = &parent.ID })
	e.wantPromote(t, child)
}

func TestParity_ParentGate_MissingParentFailsClosed(t *testing.T) {
	e := newParityEnv(t)
	ghost := uuid.New()
	e.wantHold(t, e.task(t, func(x *domain.Task) { x.ParentTaskID = &ghost }))
}

// --- ABSOLUTE_NO_PROMOTE_LABELS / due_wake_overrides_park / wake:<type> ----------

func TestParity_AbsoluteLabelsHoldEvenWhenDuePassed(t *testing.T) {
	past := time.Now().Add(-48 * time.Hour)
	for _, l := range []string{"freeze", "no-intake-promote", "no-promote", "golden", "eval-harness"} {
		e := newParityEnv(t)
		e.wantHold(t, e.task(t, func(x *domain.Task) { x.Labels = []string{l}; x.DueDate = &past }))
	}
}

func TestParity_PassiveLabelWithPassedDueDateIsWoken(t *testing.T) {
	past := time.Now().Add(-48 * time.Hour)
	e := newParityEnv(t)
	e.wantPromote(t, e.task(t, func(x *domain.Task) { x.Labels = []string{"awaiting-window"}; x.DueDate = &past }))
}

func TestParity_PassiveLabelWithFutureOrNoDueDateStaysParked(t *testing.T) {
	future := time.Now().Add(48 * time.Hour)
	e := newParityEnv(t)
	e.wantHold(t, e.task(t, func(x *domain.Task) { x.Labels = []string{"awaiting-window"}; x.DueDate = &future }))
	e.wantHold(t, e.task(t, func(x *domain.Task) { x.Labels = []string{"awaiting-window"} }))
}

func TestParity_WakeDateLicencesOverrideOfAbsoluteLabel(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	e := newParityEnv(t)
	e.wantPromote(t, e.task(t, func(x *domain.Task) { x.Labels = []string{"no-promote", "wake:date"}; x.DueDate = &past }))
}

func TestParity_WakeDateNeedsTheDateToHaveArrived(t *testing.T) {
	future := time.Now().Add(time.Hour)
	e := newParityEnv(t)
	e.wantHold(t, e.task(t, func(x *domain.Task) { x.Labels = []string{"no-promote", "wake:date"}; x.DueDate = &future }))
}

func TestParity_WakeManualAndDeleteAfterNeverPromote(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	for _, l := range []string{"wake:manual", "wake:delete_after"} {
		e := newParityEnv(t)
		// No passive label at all: the sweep still refuses these terminal wake types.
		e.wantHold(t, e.task(t, func(x *domain.Task) { x.Labels = []string{l}; x.DueDate = &past }))
	}
}

func TestParity_WakeConditionTypesFailClosed(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	e := newParityEnv(t)
	e.wantHold(t, e.task(t, func(x *domain.Task) { x.Labels = []string{"freeze", "wake:all_children_closed"}; x.DueDate = &past }))
}

// --- the class the sweep does not see (#4c9da018, #6d2aba40, #76fa130e) ----------

// The sweep scans one hard-wired workspace and only the projects its key belongs to
// (6 of 12 in the Entire VC workspace on 2026-09-20; KidCash and Editorial sit in
// other workspaces). The server rule scans every backlog card. Decision: the rule
// applies the SAME guards to them — nothing is silently dropped for being outside the
// sweep's reach — and whether to ENFORCE there is a cutover decision, not parity.
func TestParity_CardsOutsideSweepScopeGetTheSameGuards(t *testing.T) {
	e := newParityEnv(t)
	otherProject := uuid.New() // a project no sweep identity belongs to
	b := e.addStatus(t, otherProject, "Backlog", domain.StatusCategoryBacklog)
	e.addStatus(t, otherProject, "Todo", domain.StatusCategoryTodo)

	gated := e.addTask(t, otherProject, b.ID)
	gated.AssigneeType = domain.AssigneeTypeUser
	if err := e.taskRepo.Update(context.Background(), gated); err != nil {
		t.Fatal(err)
	}
	open := e.addTask(t, otherProject, b.ID)

	ds, err := e.svc.SweepAdvisory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if decisionFor(t, ds, gated.ID).Promote {
		t.Fatal("a Pavel-assigned card outside the sweep's scope must be held like any other")
	}
	if !decisionFor(t, ds, open.ID).Promote {
		t.Fatal("an ungated card outside the sweep's scope is evaluated normally, not dropped")
	}
}
