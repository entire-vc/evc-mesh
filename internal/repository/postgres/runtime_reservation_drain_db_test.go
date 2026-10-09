package postgres

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Carry-over controls from R1 against execution admission: grant revocation,
// disable/drain and concurrent desired/apply CAS, on real PostgreSQL.

type grantScope struct {
	receiver, project, status uuid.UUID
	ref                       string
	key                       RuntimeActor
}

func (f reservationFixture) scopeA() grantScope {
	return grantScope{receiver: f.receiver, project: f.project, status: f.status, ref: "worker-b", key: f.worker}
}

// addSecondGrant gives the same worker identity a second receiving workspace
// (grant B) with its own binding "worker-c", admitted by that workspace's owner.
func (f *reservationFixture) addSecondGrant(t *testing.T) grantScope {
	t.Helper()
	ctx := context.Background()
	receiverB := seedGrantWorkspace(t, f.db)
	t.Cleanup(func() { _, _ = f.db.Exec(`DELETE FROM workspaces WHERE id=$1`, receiverB) })
	grantB := insertGrant(t, f.db, f.worker.AgentID, receiverB, "member", "runtime-worker-b", "synthetic-worker-b-hash", nil)
	b := f.catalog.Bindings["worker-b"]
	b.Binding = domain.RuntimeIdentity{AgentID: f.worker.AgentID, WorkspaceID: receiverB, GrantID: grantB}
	f.catalog.Bindings["worker-c"] = b
	data, err := json.Marshal(f.catalog)
	require.NoError(t, err)
	f.input.Config = data
	f.input.IfRevision = 1
	_, err = f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	// New catalog content requires renewed receiving approval for worker-b too.
	enabled := true
	_, err = f.repo.Admit(ctx, f.owner, f.receiver, "worker-b", f.receiverActor, RuntimeAdmissionInput{IfRevision: 1, Enabled: &enabled, PermittedProfiles: b.PermittedProfiles})
	require.NoError(t, err)
	var ownerB uuid.UUID
	require.NoError(t, f.db.Get(&ownerB, `SELECT owner_id FROM workspaces WHERE id=$1`, receiverB))
	_, err = f.repo.Admit(ctx, f.owner, receiverB, "worker-c", RuntimeActor{UserID: ownerB}, RuntimeAdmissionInput{Enabled: &enabled, PermittedProfiles: b.PermittedProfiles})
	require.NoError(t, err)
	p := &domain.Project{ID: uuid.New(), WorkspaceID: receiverB, Name: "Runtime B", Slug: "runtime-b-" + uuid.NewString(), DefaultAssigneeType: domain.DefaultAssigneeNone}
	require.NoError(t, NewProjectRepo(f.db).Create(ctx, p))
	status := &domain.TaskStatus{ID: uuid.New(), ProjectID: p.ID, Name: "Todo", Slug: "todo", Category: domain.StatusCategoryTodo, IsDefault: true, Color: "#000000"}
	require.NoError(t, NewTaskStatusRepo(f.db).Create(ctx, status))
	return grantScope{receiver: receiverB, project: p.ID, status: status.ID, ref: "worker-c", key: RuntimeActor{AgentID: f.worker.AgentID, AuthWorkspaceID: receiverB}}
}

func (f reservationFixture) taskIn(t *testing.T, s grantScope) (taskID, writerRequest uuid.UUID) {
	t.Helper()
	task := &domain.Task{ID: uuid.New(), ProjectID: s.project, StatusID: s.status, Title: "runtime fixture", AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium, CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, NewTaskRepo(f.db).Create(context.Background(), task, nil))
	request := uuid.New()
	_, err := f.db.Exec(`UPDATE tasks SET checked_out_by=$2,checkout_token=$3,checkout_expires=now()+interval '1 hour',checkout_generation=1,checkout_request_id=$4 WHERE id=$1`, task.ID, f.worker.AgentID, uuid.New(), request)
	require.NoError(t, err)
	return task.ID, request
}

func (f reservationFixture) inputIn(t *testing.T, s grantScope, task, request uuid.UUID, worker, key string) domain.RuntimeAcquireInput {
	t.Helper()
	ctx := context.Background()
	view, err := f.repo.Binding(ctx, f.owner, s.receiver, s.ref, s.key)
	require.NoError(t, err)
	connection, err := runtimeConnection(ctx, f.db, f.owner, false)
	require.NoError(t, err)
	catalog, err := domain.ParseRuntimeCatalog(connection.Config)
	require.NoError(t, err)
	pools, ok := catalog.RuntimeProfilePools(f.owner, "preferred")
	require.True(t, ok)
	return domain.RuntimeAcquireInput{IdempotencyKey: key, ProfileID: "preferred", TaskID: task, CheckoutGeneration: 1, CheckoutRequestID: &request,
		WorkerRef: worker, ExpectedCatalogRevision: view.Revision, ExpectedCatalogDigest: view.Digest, ExpectedAdmissionRevision: view.Admission.Revision,
		ExpectedProfileRevision: domain.RuntimeProfileRevision(view.Revision), ExpectedPoolSetDigest: domain.RuntimePoolSetDigest(pools), TTLSeconds: 60}
}

func (f reservationFixture) acquireIn(s grantScope, key RuntimeActor, in domain.RuntimeAcquireInput) (*domain.RuntimeReservation, error) {
	r, _, err := f.repo.AcquireReservation(context.Background(), f.owner, s.receiver, s.ref, key, in)
	return r, err
}

func (f reservationFixture) consumeIn(s grantScope, r *domain.RuntimeReservation) (*domain.RuntimeReservation, error) {
	return f.repo.ConsumeReservation(context.Background(), f.owner, s.receiver, s.ref, r.ReservationID, s.key, domain.RuntimeConsumeInput{Fence: r.Fence, CheckoutGeneration: 1, RunLeaseSeconds: 600})
}

func (f reservationFixture) releaseIn(s grantScope, r *domain.RuntimeReservation, request uuid.UUID, proof string) (*domain.RuntimeReservation, error) {
	return f.repo.ReleaseReservation(context.Background(), f.owner, s.receiver, s.ref, r.ReservationID, s.key,
		domain.RuntimeReleaseInput{Fence: r.Fence, CheckoutRequestID: &request, CheckoutGeneration: 1, Stopped: true, Proof: domain.RuntimeReleaseProof{Kind: proof}})
}

// Revoking grant A blocks only new work on A. Grant B of the same identity keeps
// working, key A cannot act for B, the home key never substitutes for either,
// and A's running reservation stays occupied (it counts against the global
// identity cap) until its own writer releases it.
func TestRuntimeReservationGrantRevocationIsolatesGrants(t *testing.T) {
	f := seedReservation(t, roomyPools)
	f.setCap(t, 3)
	a := f.scopeA()
	b := f.addSecondGrant(t)
	f.reportControllers(t, false)

	taskRun, reqRun := f.taskIn(t, a)
	running, err := f.acquireIn(a, a.key, f.inputIn(t, a, taskRun, reqRun, "a-run", "a-run"))
	require.NoError(t, err)
	_, err = f.consumeIn(a, running)
	require.NoError(t, err)
	taskPending, reqPending := f.taskIn(t, a)
	pending, err := f.acquireIn(a, a.key, f.inputIn(t, a, taskPending, reqPending, "a-pending", "a-pending"))
	require.NoError(t, err)
	taskNew, reqNew := f.taskIn(t, a)
	newA := f.inputIn(t, a, taskNew, reqNew, "a-new", "a-new")

	_, err = f.db.Exec(`UPDATE agent_workspace_grants SET revoked_at=now() WHERE id=$1`, f.grant)
	require.NoError(t, err)

	_, err = f.acquireIn(a, a.key, newA)
	requireReason(t, err, http.StatusForbidden, "")
	_, err = f.consumeIn(a, pending)
	requireReason(t, err, http.StatusForbidden, "")
	require.Equal(t, 2, f.activeCount(t), "revocation never frees an active reservation by itself")
	require.Equal(t, 2, f.claimCount(t, running.ReservationID))

	// Grant B is independent of A's revocation.
	taskB, reqB := f.taskIn(t, b)
	inB := f.inputIn(t, b, taskB, reqB, "b-1", "b-1")
	// Key A, the identity's home key, or B's key on A's binding never cross over.
	homeKey := RuntimeActor{AgentID: f.worker.AgentID, AuthWorkspaceID: f.owner}
	for name, key := range map[string]RuntimeActor{"key A": a.key, "home key": homeKey} {
		_, err = f.acquireIn(b, key, inB)
		requireReason(t, err, http.StatusForbidden, "")
		require.Equal(t, 2, f.activeCount(t), name)
	}
	_, err = f.acquireIn(a, homeKey, newA)
	requireReason(t, err, http.StatusForbidden, "")
	_, err = f.acquireIn(a, b.key, newA)
	requireReason(t, err, http.StatusForbidden, "")
	onB, err := f.acquireIn(b, b.key, inB)
	require.NoError(t, err)
	_, err = f.consumeIn(b, onB)
	require.NoError(t, err)

	// A's occupied slots still count against the identity-global cap of 3.
	taskB2, reqB2 := f.taskIn(t, b)
	inB2 := f.inputIn(t, b, taskB2, reqB2, "b-2", "b-2")
	_, err = f.acquireIn(b, b.key, inB2)
	requireReason(t, err, http.StatusConflict, "identity_cap_reached")

	// Key B cannot release A's reservation; A's own writer still can after revocation.
	_, err = f.repo.ReleaseReservation(context.Background(), f.owner, a.receiver, a.ref, running.ReservationID, b.key,
		domain.RuntimeReleaseInput{Fence: running.Fence, CheckoutRequestID: &reqRun, CheckoutGeneration: 1, Stopped: true, Proof: domain.RuntimeReleaseProof{Kind: domain.RuntimeProofNoChild}})
	requireReason(t, err, http.StatusNotFound, "")
	_, err = f.releaseIn(a, running, reqRun, domain.RuntimeProofNoChild)
	require.NoError(t, err)
	_, err = f.releaseIn(a, pending, reqPending, domain.RuntimeProofNotStarted)
	require.NoError(t, err)
	require.Zero(t, f.claimCount(t, running.ReservationID))
	_, err = f.acquireIn(b, b.key, inB2)
	require.NoError(t, err, "released A capacity becomes available to B")
	_, err = f.acquireIn(a, a.key, newA)
	requireReason(t, err, http.StatusForbidden, "")
}

// Disabling the integration denies every new admission and every new start,
// while existing reservations drain through their own writer: a consumed run is
// still visible, replayable and releasable, an unconsumed one can only be
// released. Re-enabling restores admission without any environment fallback.
func TestRuntimeReservationDisableDrainsExisting(t *testing.T) {
	ctx := context.Background()
	f := seedReservation(t, roomyPools)
	a := f.scopeA()
	taskRun, reqRun := f.taskIn(t, a)
	running, err := f.acquireIn(a, a.key, f.inputIn(t, a, taskRun, reqRun, "run", "run"))
	require.NoError(t, err)
	consumed, err := f.consumeIn(a, running)
	require.NoError(t, err)
	taskPending, reqPending := f.taskIn(t, a)
	pending, err := f.acquireIn(a, a.key, f.inputIn(t, a, taskPending, reqPending, "pending", "pending"))
	require.NoError(t, err)
	taskNew, reqNew := f.taskIn(t, a)
	staleInput := f.inputIn(t, a, taskNew, reqNew, "new", "new")

	disabled := false
	f.input.IfRevision = 1
	f.input.Enabled = &disabled
	_, err = f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	view, err := f.repo.Binding(ctx, f.owner, a.receiver, a.ref, a.key)
	require.NoError(t, err)
	require.False(t, view.Enabled)
	require.True(t, view.DrainRequested)

	fresh := f.inputIn(t, a, taskNew, reqNew, "new", "new-2")
	for _, in := range []domain.RuntimeAcquireInput{staleInput, fresh} {
		_, err = f.acquireIn(a, a.key, in)
		requireReason(t, err, http.StatusLocked, "runtime_disabled")
	}
	_, err = f.consumeIn(a, pending)
	requireReason(t, err, http.StatusLocked, "runtime_disabled")
	replay, err := f.consumeIn(a, running)
	require.NoError(t, err, "a consumed run is already started: its replay stays answerable while draining")
	require.Equal(t, consumed.ConsumeReceipt.ReceiptID, replay.ConsumeReceipt.ReceiptID)
	require.Equal(t, 2, f.activeCount(t), "drain keeps both occupied until released")

	got, err := f.repo.GetReservation(ctx, f.owner, a.receiver, a.ref, running.ReservationID, a.key)
	require.NoError(t, err)
	require.Equal(t, domain.RuntimeReservationConsumed, got.State)
	_, err = f.releaseIn(a, running, reqRun, domain.RuntimeProofNoChild)
	require.NoError(t, err)
	_, err = f.releaseIn(a, pending, reqPending, domain.RuntimeProofNotStarted)
	require.NoError(t, err)
	require.Zero(t, f.activeCount(t))

	enabled := true
	f.input.IfRevision = 2
	f.input.Enabled = &enabled
	_, err = f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	f.reportControllers(t, false) // controllers re-apply the new revision before admitting
	_, err = f.acquireIn(a, a.key, fresh)
	requireReason(t, err, http.StatusConflict, "stale_revision")
	_, err = f.acquireIn(a, a.key, f.inputIn(t, a, taskNew, reqNew, "new", "new-3"))
	require.NoError(t, err, "re-enable with unchanged content keeps receiver approval")
}

// Concurrent desired-state applies of the same revision: exactly one winner, for
// the owner catalog and for the receiving admission alike. A controller report
// racing a save can never mark the newer revision applied.
func TestRuntimeRepoConcurrentApplySameRevision(t *testing.T) {
	ctx := context.Background()
	f := seedRuntime(t)
	_, err := f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	race := func(n int, op func() error, loser string) int32 {
		var wins atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if opErr := op(); opErr == nil {
					wins.Add(1)
				} else {
					require.ErrorContains(t, opErr, loser)
				}
			}()
		}
		close(start)
		wg.Wait()
		return wins.Load()
	}
	apply := f.input
	apply.IfRevision = 1
	require.EqualValues(t, 1, race(8, func() error { _, e := f.repo.Save(ctx, f.owner, f.ownerActor, apply); return e }, "revision changed"))
	snapshot, err := f.repo.Inventory(ctx, f.owner, f.ownerActor)
	require.NoError(t, err)
	require.EqualValues(t, 2, snapshot.Revision, "eight concurrent applies of revision 1 produce exactly revision 2")

	enabled := true
	admit := RuntimeAdmissionInput{Enabled: &enabled, PermittedProfiles: []string{"preferred"}}
	require.EqualValues(t, 1, race(8, func() error {
		_, e := f.repo.Admit(ctx, f.owner, f.receiver, "worker-b", f.receiverActor, admit)
		return e
	}, "revision changed"))

	seeded, err := f.repo.Inventory(ctx, f.owner, f.ownerActor)
	require.NoError(t, err)
	require.NoError(t, f.repo.Report(ctx, f.owner, "runner-a", f.reporter, domain.RuntimeReport{SchemaVersion: 2, Revision: seeded.Revision, Digest: seeded.Digest, Status: "applied", Capabilities: f.catalog.Controllers["runner-a"].Capabilities, Pools: map[string]domain.RuntimePoolObservation{}}))
	for i := range 10 {
		current, currentErr := f.repo.Inventory(ctx, f.owner, f.ownerActor)
		require.NoError(t, currentErr)
		report := domain.RuntimeReport{SchemaVersion: 2, Revision: current.Revision, Digest: current.Digest, Status: "applied", Capabilities: f.catalog.Controllers["runner-a"].Capabilities, Pools: map[string]domain.RuntimePoolObservation{}}
		next := f.input
		next.IfRevision = current.Revision
		var wg sync.WaitGroup
		var reportErr, saveErr error
		wg.Add(2)
		go func() { defer wg.Done(); reportErr = f.repo.Report(ctx, f.owner, "runner-a", f.reporter, report) }()
		go func() { defer wg.Done(); _, saveErr = f.repo.Save(ctx, f.owner, f.ownerActor, next) }()
		wg.Wait()
		require.NoError(t, saveErr, "iteration %d", i)
		if reportErr != nil {
			require.ErrorContains(t, reportErr, "current revision and digest")
		}
		after, afterErr := f.repo.Inventory(ctx, f.owner, f.ownerActor)
		require.NoError(t, afterErr)
		require.Equal(t, current.Revision+1, after.Revision)
		require.Len(t, after.Controllers, 1)
		require.False(t, after.Controllers[0].Current, "iteration %d: a report of revision %d cannot apply revision %d", i, current.Revision, after.Revision)
	}
}
