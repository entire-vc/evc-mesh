package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// The lease reaper may take back only a stage the checkout itself set.
// Proto and CRM cards moved to In Progress / Proposal by a person or a business
// agent never take a checkout, and the unleased sweep returned them to the first
// todo status (CRM: New) two hours later. Each case below runs the real sweeps
// against Postgres after more than the 120 min grace of logical time (updated_at
// pushed back three hours), next to a positive orphan-recovery control.

type stageFixture struct {
	repo                 *postgres.TaskRepo
	svc                  TaskService
	reaper               CheckoutLeaseReaper
	todo, active, prop   uuid.UUID
	owner, projectID     uuid.UUID
	exec                 func(q string, args ...any)
	newTask              func(title string) uuid.UUID
	status, stageMarker  func(id uuid.UUID) uuid.UUID
	ageQuiet             func(id uuid.UUID)
	checkedOutBy         func(id uuid.UUID) *uuid.UUID
	sweepUnleased, sweep func()
}

func newStageFixture(t *testing.T) *stageFixture {
	t.Helper()
	f, repo, svc, _, owner := newCheckoutM1Fixture(t)
	sf := &stageFixture{repo: repo, svc: svc, todo: f.statusID, owner: owner, projectID: f.projectID}
	sf.exec = func(q string, args ...any) {
		t.Helper()
		_, err := f.db.Exec(q, args...)
		require.NoError(t, err)
	}
	var cat string
	require.NoError(t, f.db.Get(&cat, `SELECT category FROM task_statuses WHERE id=$1`, f.statusID))
	require.Equal(t, "todo", cat, "fixture base status must be todo for the checkout auto-progression")
	sf.active, sf.prop = uuid.New(), uuid.New()
	sf.exec(`INSERT INTO task_statuses(id,project_id,name,slug,category,position) VALUES($1,$2,'In Progress','in-progress-stage','in_progress',10)`, sf.active, f.projectID)
	sf.exec(`INSERT INTO task_statuses(id,project_id,name,slug,category,position) VALUES($1,$2,'Proposal','proposal-stage','in_progress',11)`, sf.prop, f.projectID)
	sf.reaper = NewCheckoutLeaseReaper(repo, postgres.NewTaskStatusRepo(f.db), nil, svc, nil, nil, nil, nil, nil)
	sysCtx := actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)
	sf.newTask = func(title string) uuid.UUID {
		task := &domain.Task{ID: uuid.New(), ProjectID: f.projectID, StatusID: f.statusID, Title: title, AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium, CreatedByType: domain.ActorTypeSystem}
		require.NoError(t, svc.Create(sysCtx, task))
		return task.ID
	}
	sf.status = func(id uuid.UUID) uuid.UUID {
		var s uuid.UUID
		require.NoError(t, f.db.Get(&s, `SELECT status_id FROM tasks WHERE id=$1`, id))
		return s
	}
	sf.stageMarker = func(id uuid.UUID) uuid.UUID {
		var s *uuid.UUID
		require.NoError(t, f.db.Get(&s, `SELECT checkout_stage_status_id FROM tasks WHERE id=$1`, id))
		if s == nil {
			return uuid.Nil
		}
		return *s
	}
	sf.checkedOutBy = func(id uuid.UUID) *uuid.UUID {
		var s *uuid.UUID
		require.NoError(t, f.db.Get(&s, `SELECT checked_out_by FROM tasks WHERE id=$1`, id))
		return s
	}
	// Logical clock: everything the quiet window reads is pushed past the grace.
	sf.ageQuiet = func(id uuid.UUID) {
		sf.exec(`UPDATE tasks SET updated_at=now()-interval '3 hours' WHERE id=$1`, id)
		sf.exec(`UPDATE comments SET created_at=now()-interval '3 hours' WHERE task_id=$1`, id)
	}
	sf.sweepUnleased = func() {
		_, err := sf.reaper.SweepUnleasedInProgress(context.Background(), DefaultUnleasedGrace)
		require.NoError(t, err)
	}
	sf.sweep = func() {
		_, err := sf.reaper.SweepExpiredLeases(context.Background())
		require.NoError(t, err)
		_, err = repo.ReleaseExpiredCheckouts(context.Background())
		require.NoError(t, err)
		sf.sweepUnleased()
	}
	return sf
}

func (sf *stageFixture) moveAs(t *testing.T, ctx context.Context, id, status uuid.UUID) {
	t.Helper()
	require.NoError(t, sf.svc.MoveTask(ctx, id, MoveTaskInput{StatusID: &status, Source: "api"}))
}

func TestLeaseReaperStage_ManualStagesSurviveGrace(t *testing.T) {
	sf := newStageFixture(t)
	user := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeUser)
	agent := agentContext(sf.owner)

	manual := sf.newTask("human sets In Progress without a checkout")
	sf.moveAs(t, user, manual, sf.active)
	proposal := sf.newTask("CRM deal in Proposal (custom in_progress stage)")
	sf.moveAs(t, user, proposal, sf.prop)
	agentBusiness := sf.newTask("business agent moves its deal to In Progress")
	sf.moveAs(t, agent, agentBusiness, sf.active)

	// Positive control: a checkout-made stage whose run was lost.
	orphan := sf.newTask("agent run lost after checkout")
	lease, err := sf.svc.CheckoutTask(agent, orphan, 30, nil, checkoutScope())
	require.NoError(t, err)
	require.Equal(t, sf.active, sf.status(orphan), "checkout auto-progression moves todo to the first in_progress")
	require.Equal(t, sf.active, sf.stageMarker(orphan), "checkout auto-progression must mark its own stage")
	require.NoError(t, sf.svc.ReleaseCheckout(agent, orphan, lease.CheckoutToken))

	for _, id := range []uuid.UUID{manual, proposal, agentBusiness, orphan} {
		sf.ageQuiet(id)
	}
	sf.sweep()

	require.Equal(t, sf.active, sf.status(manual), "a manual In Progress was reset by the reaper")
	require.Equal(t, sf.prop, sf.status(proposal), "a Proposal stage was reset by the reaper")
	require.Equal(t, sf.active, sf.status(agentBusiness), "an agent-managed business stage was reset by the reaper")
	require.Equal(t, sf.todo, sf.status(orphan), "the lost checkout run was NOT recovered — orphan recovery is broken")
	require.Equal(t, uuid.Nil, sf.stageMarker(orphan), "the reaper's own move must clear the stage marker")

	// A second and third sweep must not touch anything again.
	sf.ageQuiet(orphan)
	sf.sweep()
	sf.sweep()
	require.Equal(t, sf.todo, sf.status(orphan))
	require.Equal(t, sf.active, sf.status(manual))
	require.Equal(t, sf.prop, sf.status(proposal))
}

// A checkout-made stage stops being the reaper's the moment anyone moves the card.
func TestLeaseReaperStage_LaterMoveMakesStageIntentional(t *testing.T) {
	sf := newStageFixture(t)
	agent := agentContext(sf.owner)
	id := sf.newTask("checked out, then moved to Proposal by its agent")
	lease, err := sf.svc.CheckoutTask(agent, id, 30, nil, checkoutScope())
	require.NoError(t, err)
	require.Equal(t, sf.active, sf.stageMarker(id))
	sf.moveAs(t, agent, id, sf.prop)
	require.Equal(t, uuid.Nil, sf.stageMarker(id), "an explicit move must clear the checkout stage marker")
	require.NoError(t, sf.svc.ReleaseCheckout(agent, id, lease.CheckoutToken))
	sf.ageQuiet(id)
	sf.sweep()
	require.Equal(t, sf.prop, sf.status(id))
}

// Expired lease on a manual stage: the lease goes, the stage stays. On a
// checkout-made stage phase 1 still returns the card (positive control).
func TestLeaseReaperStage_ExpiredLease(t *testing.T) {
	sf := newStageFixture(t)
	user := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeUser)
	agent := agentContext(sf.owner)

	manual := sf.newTask("manual Proposal, an agent's lease on it expired")
	sf.moveAs(t, user, manual, sf.prop)
	_, err := sf.repo.AcquireCheckout(context.Background(), manual, sf.owner, uuid.New(), time.Now().Add(-time.Minute), checkoutScope())
	require.NoError(t, err)

	owned := sf.newTask("checkout-made stage, lease expired")
	_, err = sf.svc.CheckoutTask(agent, owned, 30, nil, checkoutScope())
	require.NoError(t, err)
	sf.exec(`UPDATE tasks SET checkout_expires=now()-interval '1 minute' WHERE id=$1`, owned)

	sf.sweep()
	require.Equal(t, sf.prop, sf.status(manual), "an expired lease reset a manual stage")
	require.Nil(t, sf.checkedOutBy(manual), "the expired lease on a manual stage must still be released")
	require.Equal(t, sf.todo, sf.status(owned), "an expired checkout run was NOT recovered")
	require.Nil(t, sf.checkedOutBy(owned))
}

// The guarded write repeats the stage predicate: a stage made intentional after
// the candidate SELECT refuses the move even with a matching version.
func TestLeaseReaperStage_StageChangedAfterSelect(t *testing.T) {
	sf := newStageFixture(t)
	agent := agentContext(sf.owner)
	id := sf.newTask("candidate whose stage becomes intentional mid-sweep")
	lease, err := sf.svc.CheckoutTask(agent, id, 30, nil, checkoutScope())
	require.NoError(t, err)
	require.NoError(t, sf.svc.ReleaseCheckout(agent, id, lease.CheckoutToken))
	sf.ageQuiet(id)

	candidates, err := sf.repo.FindStaleUnleasedInProgress(context.Background(), DefaultUnleasedGrace)
	require.NoError(t, err)
	var snap *domain.Task
	for i := range candidates {
		if candidates[i].ID == id {
			snap = &candidates[i]
		}
	}
	require.NotNil(t, snap, "positive control: the orphan is a candidate before the change")

	// Clear only the marker; then re-read so version and status still match the
	// write, isolating the stage predicate from the version CAS.
	sf.exec(`UPDATE tasks SET checkout_stage_status_id=NULL WHERE id=$1`, id)
	fresh, err := sf.repo.GetByID(context.Background(), id)
	require.NoError(t, err)
	sysCtx := actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)
	err = sf.svc.MoveTask(sysCtx, id, reaperMoveInput(fresh, &sf.todo, activityCheckoutUnleasedReturned))
	var conflict *domain.TaskConflict
	require.ErrorAs(t, err, &conflict, "the guarded reaper write ignored the stage marker")
	require.Equal(t, sf.active, sf.status(id))
}
