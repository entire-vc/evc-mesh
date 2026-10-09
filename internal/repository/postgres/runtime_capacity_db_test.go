package postgres

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func (f reservationFixture) capacity(t *testing.T) domain.RuntimeAgentCapacity {
	t.Helper()
	agent := f.worker.AgentID
	got, err := f.repo.Capacity(context.Background(), f.receiver, f.worker, &agent)
	require.NoError(t, err)
	require.Len(t, got.Agents, 1)
	require.Equal(t, domain.RuntimeCapacitySourceStore, got.Source)
	require.WithinDuration(t, time.Now(), got.ObservedAt, time.Minute)
	return got.Agents[0]
}

func (f reservationFixture) assignedTask(t *testing.T, status uuid.UUID, mutate string, args ...any) uuid.UUID {
	t.Helper()
	agent := f.worker.AgentID
	task := &domain.Task{ID: uuid.New(), ProjectID: f.project, StatusID: status, Title: "capacity fixture", AssigneeID: &agent, AssigneeType: domain.AssigneeTypeAgent, Priority: domain.PriorityMedium, CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, NewTaskRepo(f.db).Create(context.Background(), task, nil))
	if mutate != "" {
		_, err := f.db.Exec(`UPDATE tasks SET `+mutate+` WHERE id=$1`, append([]any{task.ID}, args...)...)
		require.NoError(t, err)
	}
	return task.ID
}

func (f reservationFixture) checkout(t *testing.T, task uuid.UUID) uuid.UUID {
	t.Helper()
	request := uuid.New()
	_, err := f.db.Exec(`UPDATE tasks SET checked_out_by=$2,checkout_token=$3,checkout_expires=now()+interval '1 hour',checkout_generation=1,checkout_request_id=$4 WHERE id=$1`, task, f.worker.AgentID, uuid.New(), request)
	require.NoError(t, err)
	return request
}

// The capacity projection derives ready/running/waiting from durable state:
// waiting work (gate, triage, parked wait, open blocker, start_after) consumes
// nothing once it holds no checkout or reservation; a real reservation or a
// held checkout counts whatever the task status or gate; stale/unknown
// occupancy is never reported as free.
func TestRuntimeCapacityProjection(t *testing.T) {
	ctx := context.Background()
	f := seedReservation(t, roomyPools)
	triage := &domain.TaskStatus{ID: uuid.New(), ProjectID: f.project, Name: "Waiting for human", Slug: "waiting-human", Category: domain.StatusCategoryTriage, Color: "#000000", Position: 1}
	require.NoError(t, NewTaskStatusRepo(f.db).Create(ctx, triage))
	inProgress := &domain.TaskStatus{ID: uuid.New(), ProjectID: f.project, Name: "In Progress", Slug: "in-progress", Category: domain.StatusCategoryInProgress, Color: "#000000", Position: 2}
	require.NoError(t, NewTaskStatusRepo(f.db).Create(ctx, inProgress))

	ready := f.assignedTask(t, f.status, "")
	gated := f.assignedTask(t, inProgress.ID, "human_gate=true")
	f.assignedTask(t, triage.ID, "")
	blocker := &domain.Task{ID: uuid.New(), ProjectID: f.project, StatusID: f.status, Title: "blocker", AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium, CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser}
	require.NoError(t, NewTaskRepo(f.db).Create(ctx, blocker, nil))
	blocked := f.assignedTask(t, f.status, "")
	_, err := f.db.Exec(`INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type) VALUES($1,$2,$3,'blocks')`, uuid.New(), blocked, blocker.ID)
	require.NoError(t, err)
	f.assignedTask(t, f.status, "start_after=now()+interval '1 day'")
	parked := f.assignedTask(t, inProgress.ID, "")
	_, err = f.db.Exec(`INSERT INTO parked_waits(id,task_id,wait_comment_id,plan,registered_by,registered_by_type,result) VALUES($1,$2,$3,'{}',$4,'agent','{}')`, uuid.New(), parked, uuid.New(), f.worker.AgentID)
	require.NoError(t, err)

	c := f.capacity(t)
	require.Equal(t, 2, c.Configured)
	require.Equal(t, 2, c.Effective)
	require.Zero(t, c.Occupied)
	require.Equal(t, 2, c.Ready, "waiting work without a writer or reservation consumes nothing")
	require.Equal(t, domain.RuntimeCapacityAvailable, c.Reason)
	require.Equal(t, 1, c.Tasks.Ready)
	require.Equal(t, 5, c.Tasks.Waiting)
	require.Equal(t, domain.RuntimeTaskWaitReasons{HumanGate: 1, Triage: 1, ParkedWait: 1, Dependencies: 1, StartAfter: 1}, c.Tasks.WaitingBy)

	// A gated in-progress task with a real, consumed reservation is running.
	gatedRequest := f.checkout(t, gated)
	res, _, err := f.acquire(f.acquireInput(t, "preferred", gated, gatedRequest, "w1", "k1"))
	require.NoError(t, err)
	_, err = f.repo.ConsumeReservation(ctx, f.owner, f.receiver, "worker-b", res.ReservationID, f.worker, domain.RuntimeConsumeInput{Fence: res.Fence, CheckoutGeneration: 1, RunLeaseSeconds: 600})
	require.NoError(t, err)
	c = f.capacity(t)
	require.Equal(t, 1, c.Running)
	require.Zero(t, c.Writers, "the checkout behind a reservation is not counted twice")
	require.Equal(t, 1, c.Ready)
	require.Zero(t, c.Tasks.WaitingBy.HumanGate, "a gate never hides a live run")

	// A held checkout without a reservation (direct mode) is a writer.
	f.checkout(t, ready)
	c = f.capacity(t)
	require.Equal(t, 1, c.Writers)
	require.Equal(t, 2, c.Occupied)
	require.Zero(t, c.Ready)
	require.Equal(t, domain.RuntimeCapacityAtCapacity, c.Reason)
	require.Zero(t, c.Tasks.Ready)

	// Stale lease and a consumed run past its lease: unknown, still occupied.
	_, err = f.db.Exec(`UPDATE tasks SET checkout_expires=now()-interval '1 minute' WHERE id=$1`, ready)
	require.NoError(t, err)
	_, err = f.db.Exec(`UPDATE runtime_reservations SET expires_at=now()-interval '1 second' WHERE id=$1`, res.ReservationID)
	require.NoError(t, err)
	c = f.capacity(t)
	require.Equal(t, 1, c.StaleWriters)
	require.Equal(t, 1, c.Reconcile)
	require.Zero(t, c.Running)
	require.Equal(t, 2, c.Unknown)
	require.Equal(t, 2, c.Occupied)
	require.Zero(t, c.Ready, "unknown occupancy is never free")

	// Quiesce: proven release plus released checkout returns the slot; the task
	// is waiting on its gate again and does not consume capacity.
	_, err = f.repo.ReleaseReservation(ctx, f.owner, f.receiver, "worker-b", res.ReservationID, f.worker,
		domain.RuntimeReleaseInput{Fence: res.Fence, CheckoutRequestID: &gatedRequest, CheckoutGeneration: 1, Stopped: true, Proof: domain.RuntimeReleaseProof{Kind: domain.RuntimeProofNoChild}})
	require.NoError(t, err)
	c = f.capacity(t)
	require.Equal(t, 1, c.Writers, "released reservation leaves its still-held checkout counted as a writer")
	require.Equal(t, 1, c.StaleWriters)
	require.Equal(t, 2, c.Occupied)
	_, err = f.db.Exec(`UPDATE tasks SET checked_out_by=NULL,checkout_expires=NULL WHERE id IN ($1,$2)`, gated, ready)
	require.NoError(t, err)
	c = f.capacity(t)
	require.Zero(t, c.Occupied)
	require.Equal(t, 2, c.Ready)
	require.Equal(t, 1, c.Tasks.WaitingBy.HumanGate)

	f.setCap(t, 0)
	c = f.capacity(t)
	require.Zero(t, c.Effective)
	require.Zero(t, c.Ready)
	require.Equal(t, domain.RuntimeCapacityCapUnset, c.Reason)
}

func TestRuntimeCapacityAccess(t *testing.T) {
	ctx := context.Background()
	f := seedReservation(t, roomyPools)
	// Receiving workspace member (owner) sees the granted identity; the owner
	// workspace lists its home agents.
	got, err := f.repo.Capacity(ctx, f.receiver, f.receiverActor, nil)
	require.NoError(t, err)
	require.Len(t, got.Agents, 1)
	require.Equal(t, f.worker.AgentID, got.Agents[0].AgentID)
	got, err = f.repo.Capacity(ctx, f.owner, f.ownerActor, nil)
	require.NoError(t, err)
	require.Len(t, got.Agents, 2)

	homeKey := RuntimeActor{AgentID: f.worker.AgentID, AuthWorkspaceID: f.owner}
	_, err = f.repo.Capacity(ctx, f.receiver, homeKey, nil)
	requireReason(t, err, http.StatusForbidden, "")
	connector := f.receiverActor
	connector.Connector = true
	_, err = f.repo.Capacity(ctx, f.receiver, connector, nil)
	requireReason(t, err, http.StatusForbidden, "")
	_, err = f.repo.Capacity(ctx, f.receiver, f.ownerActor, nil)
	requireReason(t, err, http.StatusNotFound, "")
	stranger := uuid.New()
	_, err = f.repo.Capacity(ctx, f.receiver, f.worker, &stranger)
	requireReason(t, err, http.StatusNotFound, "")

	// Revoked grant: the identity leaves the receiving workspace's projection.
	_, err = f.db.Exec(`UPDATE agent_workspace_grants SET revoked_at=now() WHERE id=$1`, f.grant)
	require.NoError(t, err)
	got, err = f.repo.Capacity(ctx, f.receiver, f.receiverActor, nil)
	require.NoError(t, err)
	require.Empty(t, got.Agents)
}
