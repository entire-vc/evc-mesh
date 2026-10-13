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

// fakeOpenDependents answers HasOpenBlockedDependents from real task rows in the
// harness's mock repos, so the "is the dependent still open" decision is driven
// by the dependent's status category exactly as the SQL does it, not by a
// hard-coded boolean. The SQL itself is proved against Postgres in
// TestTaskDependencyRepo_HasOpenBlockedDependents.
type fakeOpenDependents struct {
	h     *parkHarness
	edges []domain.TaskDependency
	err   error
	calls int
}

func (f *fakeOpenDependents) HasOpenBlockedDependents(ctx context.Context, blockerID uuid.UUID) (bool, error) {
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	for _, e := range f.edges {
		if e.DependsOnTaskID != blockerID || e.DependencyType != domain.DependencyTypeBlocks {
			continue
		}
		dep, err := f.h.taskRepo.GetByID(ctx, e.TaskID)
		if err != nil || dep == nil {
			continue
		}
		st, err := f.h.statusRepo.GetByID(ctx, dep.StatusID)
		if err != nil || st == nil {
			continue
		}
		if st.Category != domain.StatusCategoryDone && st.Category != domain.StatusCategoryCancelled {
			return true, nil
		}
	}
	return false, nil
}

func (h *parkHarness) addStatus(t *testing.T, cat domain.StatusCategory) uuid.UUID {
	t.Helper()
	st := &domain.TaskStatus{ID: uuid.New(), ProjectID: h.projectID, Name: string(cat), Category: cat}
	if err := h.statusRepo.Create(context.Background(), st); err != nil {
		t.Fatalf("create %s status: %v", cat, err)
	}
	return st.ID
}

func (h *parkHarness) addDependent(t *testing.T, statusID uuid.UUID) uuid.UUID {
	t.Helper()
	// Fresh UpdatedAt keeps the dependent itself out of the mock's stale sweep,
	// so only the blocker is a candidate.
	task := &domain.Task{ID: uuid.New(), ProjectID: h.projectID, StatusID: statusID, Title: "dependent", UpdatedAt: time.Now()}
	if err := h.taskRepo.Create(context.Background(), task, nil); err != nil {
		t.Fatalf("create dependent: %v", err)
	}
	return task.ID
}

func newBlockerHarness(t *testing.T) (*parkHarness, *fakeOpenDependents) {
	t.Helper()
	h := newParkHarness(t, &domain.MidPipelineConfig{AutoParkStalled: true})
	deps := &fakeOpenDependents{h: h}
	h.reaper.(*checkoutLeaseReaper).dependents = deps
	return h, deps
}

func blocks(blocker, dependent uuid.UUID) domain.TaskDependency {
	return domain.TaskDependency{ID: uuid.New(), TaskID: dependent, DependsOnTaskID: blocker, DependencyType: domain.DependencyTypeBlocks}
}

// #53f30091 → #14580276, 11.10: the stale card formally blocked an open UI card.
// Parking it took it out of every feed and the dependent stalled ~16 h. A card
// that open work is waiting on goes back to its own assignee's todo instead.
func TestPark_BlockerOfOpenDependent_ReturnedToTodoNotParked(t *testing.T) {
	h, deps := newBlockerHarness(t)
	task := h.addUnleasedTask(t, h.projectID, 4*time.Hour)
	deps.edges = append(deps.edges, blocks(task.ID, h.addDependent(t, h.addStatus(t, domain.StatusCategoryTodo))))
	assignee := *task.AssigneeID

	n, err := h.reaper.SweepUnleasedInProgress(context.Background(), DefaultUnleasedGrace)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 task handled, got %d", n)
	}

	got := h.stored(t, task.ID)
	st, _ := h.statusRepo.GetByID(context.Background(), got.StatusID)
	if st == nil || st.Category != domain.StatusCategoryTodo {
		t.Fatalf("blocker of an open dependent landed in %v, want todo", st)
	}
	if got.DueDate != nil || containsInStringArray(got.Labels, parkMonitorLabel) {
		t.Fatalf("blocker of an open dependent was armed as a park: due=%v labels=%v", got.DueDate, got.Labels)
	}
	if got.AssigneeID == nil || *got.AssigneeID != assignee {
		t.Fatalf("assignee changed: %v, want %v", got.AssigneeID, assignee)
	}
	bodies := h.commentBodies()
	if len(bodies) != 1 || bodies[0] != blockerReturnedComment {
		t.Fatalf("expected exactly the blocker-return note, got %q", bodies)
	}
	if codes := h.commentCodes(); len(codes) != 1 || codes[0] != SysCodeLeaseBlockerReturned {
		t.Fatalf("blocker-return system_code = %v, want [%s]", codes, SysCodeLeaseBlockerReturned)
	}
	for _, body := range bodies {
		if strings.HasPrefix(body, "🅿") {
			t.Fatalf("park comment posted on a card that was not parked: %q", body)
		}
	}
	if len(h.notify.notified) != 1 || h.notify.notified[0] != assignee {
		t.Fatalf("assignee not notified: %v", h.notify.notified)
	}
	acts := h.leaseActivities()
	if len(acts) != 1 || acts[0].Action != activityCheckoutUnleasedReturned {
		t.Fatalf("expected one %s activity, got %+v", activityCheckoutUnleasedReturned, acts)
	}
}

// The control: once every dependent is done or cancelled nothing waits on the
// card any more, and it parks exactly as before.
func TestPark_BlockerOfClosedDependentsOnly_StillParked(t *testing.T) {
	h, deps := newBlockerHarness(t)
	task := h.addUnleasedTask(t, h.projectID, 4*time.Hour)
	deps.edges = append(deps.edges,
		blocks(task.ID, h.addDependent(t, h.addStatus(t, domain.StatusCategoryDone))),
		blocks(task.ID, h.addDependent(t, h.addStatus(t, domain.StatusCategoryCancelled))),
	)

	if _, err := h.reaper.SweepUnleasedInProgress(context.Background(), DefaultUnleasedGrace); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if deps.calls == 0 {
		t.Fatal("the dependents source was never consulted — the control proves nothing")
	}
	got := h.stored(t, task.ID)
	if got.StatusID != h.backlogID {
		t.Fatalf("card with only closed dependents was not parked: status %s, want backlog %s", got.StatusID, h.backlogID)
	}
	if got.DueDate == nil || !containsInStringArray(got.Labels, parkMonitorLabel) {
		t.Fatalf("parked card is missing its alarm: due=%v labels=%v", got.DueDate, got.Labels)
	}
}

// Unreadable dependency state decides nothing: the card is neither parked (it
// may strand a dependent for a day) nor returned (a DB outage would re-feed every
// stale card in the workspace). It stays put and the next tick asks again.
func TestPark_DependentsUnreadable_LeftForNextTick(t *testing.T) {
	h, deps := newBlockerHarness(t)
	deps.err = errors.New("db down")
	task := h.addUnleasedTask(t, h.projectID, 4*time.Hour)

	n, err := h.reaper.SweepUnleasedInProgress(context.Background(), DefaultUnleasedGrace)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 0 || len(h.mover.movesSeen) != 0 {
		t.Fatalf("card moved although its dependents could not be read: n=%d moves=%v", n, h.mover.movesSeen)
	}
	got := h.stored(t, task.ID)
	if got.StatusID != task.StatusID || got.DueDate != nil {
		t.Fatalf("card changed although its dependents could not be read: %+v", got)
	}
	if len(h.commentBodies()) != 0 {
		t.Fatalf("a comment was posted on a skipped card: %q", h.commentBodies())
	}
}

// The handoff path keeps precedence: a card whose last live word belongs to
// someone else goes back to its executor with the handoff note, and the
// dependency source is not even asked.
func TestPark_HandoffWinsOverBlockerCheck(t *testing.T) {
	h, deps := newBlockerHarness(t)
	task := h.addUnleasedTask(t, h.projectID, 4*time.Hour)
	deps.edges = append(deps.edges, blocks(task.ID, h.addDependent(t, h.addStatus(t, domain.StatusCategoryTodo))))
	h.say(t, task, uuid.New(), domain.ActorTypeAgent, "VERDICT: DO-NOT-SHIP", 3*time.Hour, "")

	h.sweepOne(t)

	if got := h.category(t, task.ID); got != domain.StatusCategoryTodo {
		t.Fatalf("handed-off blocker must return to todo, got %v", got)
	}
	if deps.calls != 0 {
		t.Fatalf("dependents consulted %d times on a handoff", deps.calls)
	}
	var handoffNote bool
	for _, b := range h.commentBodies() {
		if b == blockerReturnedComment {
			t.Fatal("blocker note posted on a handoff return")
		}
		handoffNote = handoffNote || strings.Contains(b, "[handoff:")
	}
	if !handoffNote {
		t.Fatal("handoff note missing")
	}
	if codes := h.commentCodes(); len(codes) != 1 || codes[0] != SysCodeLeaseHandoff {
		t.Fatalf("handoff system_code = %v, want [%s]", codes, SysCodeLeaseHandoff)
	}
}

// A reaper wired without a dependents source parks a blocker exactly as before.
func TestPark_NoDependentsSource_ParksAsBefore(t *testing.T) {
	h := newParkHarness(t, &domain.MidPipelineConfig{AutoParkStalled: true})
	task := h.addUnleasedTask(t, h.projectID, 4*time.Hour)

	h.sweepOne(t)

	if got := h.stored(t, task.ID); got.StatusID != h.backlogID {
		t.Fatalf("with no dependents source the card must park, status %s", got.StatusID)
	}
}

// main.go wires the dependency repository through the constructor; a field
// dropped there would silently restore the old park-everything behaviour.
func TestNewCheckoutLeaseReaper_WiresDependents(t *testing.T) {
	deps := &fakeOpenDependents{}
	r := NewCheckoutLeaseReaper(nil, nil, nil, nil, nil, nil, nil, nil, deps)
	if got := r.(*checkoutLeaseReaper).dependents; got != deps {
		t.Fatalf("dependents not wired: %v", got)
	}
}
