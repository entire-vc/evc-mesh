package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ---------------------------------------------------------------------------
// INTAKE brakes of the backlog→todo advisory rule (an internal ticket, from an internal ticket).
// Source of truth for the semantics: scripts/fleet-balancer.py intake_team +
// fleet_intake.evaluate + human_gate.dep_freeze_reason. Each brake has a red case
// (held, reason checked) and a green control (same card without the brake promotes).
// ---------------------------------------------------------------------------

type brakeHarness struct {
	*backlogHarness
	agentRepo *MockAgentRepository
	projectID uuid.UUID
	backlog   *domain.TaskStatus
	todo      *domain.TaskStatus
	progress  *domain.TaskStatus
}

func newBrakeHarness(t *testing.T, devCap int) *brakeHarness {
	t.Helper()
	h := &brakeHarness{backlogHarness: newBacklogHarness(), agentRepo: NewMockAgentRepository(), projectID: uuid.New()}
	h.backlog = h.addStatus(t, h.projectID, "Backlog", domain.StatusCategoryBacklog)
	h.todo = h.addStatus(t, h.projectID, "Todo", domain.StatusCategoryTodo)
	h.progress = h.addStatus(t, h.projectID, "In Progress", domain.StatusCategoryInProgress)
	h.svc = NewBacklogPromotionAdvisoryService(h.taskRepo, h.statusRepo, h.depRepo, h.activityRepo, h.projectRepo,
		WithLaneAvailability(h.agentRepo), WithCapacityBrake(devCap))
	return h
}

func (h *brakeHarness) addAgent(t *testing.T, caps string) uuid.UUID {
	t.Helper()
	a := &domain.Agent{ID: uuid.New(), Capabilities: json.RawMessage(caps)}
	if err := h.agentRepo.Create(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	return a.ID
}

func (h *brakeHarness) owned(t *testing.T, status, agent uuid.UUID) *domain.Task {
	t.Helper()
	task := h.addTask(t, h.projectID, status)
	task.AssigneeID = &agent
	return task
}

func (h *brakeHarness) decide(t *testing.T, id uuid.UUID) BacklogPromotionDecision {
	t.Helper()
	ds, err := h.svc.SweepAdvisory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return decisionFor(t, ds, id)
}

func wantHeld(t *testing.T, d BacklogPromotionDecision, sub string) {
	t.Helper()
	if d.Promote || !strings.Contains(d.Reason, sub) {
		t.Fatalf("want held with %q, got promote=%v reason=%q", sub, d.Promote, d.Reason)
	}
}

func wantPromote(t *testing.T, d BacklogPromotionDecision) {
	t.Helper()
	if !d.Promote {
		t.Fatalf("want promote, got held: %q", d.Reason)
	}
}

func TestIntakeBrakes_StartAfter(t *testing.T) {
	h := newBrakeHarness(t, 4)
	future := h.addTask(t, h.projectID, h.backlog.ID)
	at := time.Now().Add(2 * time.Hour)
	future.StartAfter = &at
	past := h.addTask(t, h.projectID, h.backlog.ID)
	was := time.Now().Add(-time.Hour)
	past.StartAfter = &was

	wantHeld(t, h.decide(t, future.ID), "start_after")
	wantPromote(t, h.decide(t, past.ID))
}

func TestIntakeBrakes_Leased(t *testing.T) {
	h := newBrakeHarness(t, 4)
	task := h.addTask(t, h.projectID, h.backlog.ID)
	holder := uuid.New()
	task.CheckedOutBy = &holder
	wantHeld(t, h.decide(t, task.ID), "leased")

	live := h.addTask(t, h.projectID, h.backlog.ID)
	live.CheckedOutBy = &holder
	until := time.Now().Add(time.Hour)
	live.CheckoutExpires = &until
	wantHeld(t, h.decide(t, live.ID), "leased")

	expired := h.addTask(t, h.projectID, h.backlog.ID)
	expired.CheckedOutBy = &holder
	gone := time.Now().Add(-time.Minute)
	expired.CheckoutExpires = &gone
	wantPromote(t, h.decide(t, expired.ID))
}

func TestIntakeBrakes_LanePaused(t *testing.T) {
	h := newBrakeHarness(t, 4)
	paused := h.addAgent(t, `{"fleet_paused":true}`)
	paused2 := h.addAgent(t, `["fleet-paused"]`)
	live := h.addAgent(t, `{"fleet_paused":false}`)
	a := h.owned(t, h.backlog.ID, paused)
	b := h.owned(t, h.backlog.ID, paused2)
	c := h.owned(t, h.backlog.ID, live)
	gone := h.owned(t, h.backlog.ID, uuid.New())

	ds, err := h.svc.SweepAdvisory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantHeld(t, decisionFor(t, ds, a.ID), "fleet paused")
	wantHeld(t, decisionFor(t, ds, b.ID), "fleet paused")
	wantPromote(t, decisionFor(t, ds, c.ID))
	wantHeld(t, decisionFor(t, ds, gone.ID), "agent not found")
}

func TestIntakeBrakes_Capacity(t *testing.T) {
	h := newBrakeHarness(t, 2)
	busy := h.addAgent(t, `{}`)
	h.owned(t, h.todo.ID, busy)
	h.owned(t, h.progress.ID, busy)
	card := h.owned(t, h.backlog.ID, busy)
	wantHeld(t, h.decide(t, card.ID), "capacity 2>=2")

	free := h.addAgent(t, `{}`)
	h.owned(t, h.todo.ID, free)
	ok := h.owned(t, h.backlog.ID, free)
	wantPromote(t, h.decide(t, ok.ID))
}

// The balancer bumps load[aid] after each promotion in a run: with one free slot only
// the first of two cards for the same lane is advised.
func TestIntakeBrakes_CapacityCountsPromotionsOfThisTick(t *testing.T) {
	h := newBrakeHarness(t, 1)
	agent := h.addAgent(t, `{}`)
	h.owned(t, h.backlog.ID, agent)
	h.owned(t, h.backlog.ID, agent)
	ds, err := h.svc.SweepAdvisory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	promoted := 0
	for _, d := range ds {
		if d.Promote {
			promoted++
		} else if !strings.Contains(d.Reason, "capacity") {
			t.Fatalf("unexpected hold reason %q", d.Reason)
		}
	}
	if promoted != 1 {
		t.Fatalf("want exactly 1 promotion, got %d", promoted)
	}
}

func TestIntakeBrakes_CapacityOffWithoutOption(t *testing.T) {
	h := newBrakeHarness(t, 0)
	agent := h.addAgent(t, `{}`)
	h.owned(t, h.todo.ID, agent)
	card := h.owned(t, h.backlog.ID, agent)
	wantPromote(t, h.decide(t, card.ID))
}

// dep_freeze_reason looks at `blocks` edges only: an open relates_to edge is no blocker.
func TestIntakeBrakes_DepFreeze_BlocksOnly(t *testing.T) {
	h := newBrakeHarness(t, 4)
	open := h.addTask(t, h.projectID, h.progress.ID)

	blocked := h.addTask(t, h.projectID, h.backlog.ID)
	h.addDependency(t, blocked.ID, open.ID)

	related := h.addTask(t, h.projectID, h.backlog.ID)
	if err := h.depRepo.Create(context.Background(), &domain.TaskDependency{
		ID: uuid.New(), TaskID: related.ID, DependsOnTaskID: open.ID,
		DependencyType: domain.DependencyTypeRelatesTo, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	ds, err := h.svc.SweepAdvisory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantHeld(t, decisionFor(t, ds, blocked.ID), "unresolved dependencies")
	wantPromote(t, decisionFor(t, ds, related.ID))
}
