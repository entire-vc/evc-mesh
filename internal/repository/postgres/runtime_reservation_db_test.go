package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Real PostgreSQL controls for execution admission (R3). They skip without a
// reachable database, like the rest of runtime_repo_db_test.go.

type reservationFixture struct {
	runtimeFixture
	project, status uuid.UUID
}

func seedReservation(t *testing.T, mutate func(*domain.RuntimeCatalog)) reservationFixture {
	t.Helper()
	f := seedRuntime(t)
	ctx := context.Background()
	// A second controller with a profile on the same account: its pools are the
	// same canonical resources, so capacity must be shared across controllers.
	runnerB := f.catalog.Controllers["runner-a"]
	runnerB.Host = "prepared-host-b"
	f.catalog.Controllers["runner-b"] = runnerB
	preferredB := f.catalog.Profiles["preferred"]
	preferredB.ControllerRef = "runner-b"
	f.catalog.Profiles["preferred-b"] = preferredB
	b := f.catalog.Bindings["worker-b"]
	b.PermittedProfiles = append(b.PermittedProfiles, "preferred-b")
	f.catalog.Bindings["worker-b"] = b
	if mutate != nil {
		mutate(f.catalog)
	}
	data, err := json.Marshal(f.catalog)
	require.NoError(t, err)
	f.input.Config = data
	_, err = f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	enabled := true
	_, err = f.repo.Admit(ctx, f.owner, f.receiver, "worker-b", f.receiverActor, RuntimeAdmissionInput{Enabled: &enabled, PermittedProfiles: b.PermittedProfiles})
	require.NoError(t, err)
	p := &domain.Project{ID: uuid.New(), WorkspaceID: f.receiver, Name: "Runtime", Slug: "runtime-" + uuid.NewString(), DefaultAssigneeType: domain.DefaultAssigneeNone}
	require.NoError(t, NewProjectRepo(f.db).Create(ctx, p))
	status := &domain.TaskStatus{ID: uuid.New(), ProjectID: p.ID, Name: "Todo", Slug: "todo", Category: domain.StatusCategoryTodo, IsDefault: true, Color: "#000000"}
	require.NoError(t, NewTaskStatusRepo(f.db).Create(ctx, status))
	f.setCap(t, 2)
	// Never leave a held (possibly expired) checkout behind: repository-wide
	// sweeps such as ReleaseExpiredCheckouts in other tests count every row.
	t.Cleanup(func() {
		_, _ = f.db.Exec(`UPDATE tasks SET checked_out_by=NULL,checkout_expires=NULL WHERE checked_out_by=$1`, f.worker.AgentID)
	})
	return reservationFixture{runtimeFixture: f, project: p.ID, status: status.ID}
}

func (f runtimeFixture) setCap(t *testing.T, n int) {
	t.Helper()
	_, err := f.db.Exec(`UPDATE agents SET max_concurrent_tasks=$2 WHERE id=$1`, f.worker.AgentID, n)
	require.NoError(t, err)
}

// checkedOutTask creates a task in the receiving workspace held by the worker
// agent with a live lease, generation 1 and the returned writer request.
func (f reservationFixture) checkedOutTask(t *testing.T) (taskID, writerRequest uuid.UUID) {
	t.Helper()
	task := &domain.Task{ID: uuid.New(), ProjectID: f.project, StatusID: f.status, Title: "runtime fixture", AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium, CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, NewTaskRepo(f.db).Create(context.Background(), task, nil))
	request := uuid.New()
	_, err := f.db.Exec(`UPDATE tasks SET checked_out_by=$2,checkout_token=$3,checkout_expires=now()+interval '1 hour',checkout_generation=1,checkout_request_id=$4 WHERE id=$1`, task.ID, f.worker.AgentID, uuid.New(), request)
	require.NoError(t, err)
	return task.ID, request
}

func (f reservationFixture) acquireInput(t *testing.T, profile string, task, request uuid.UUID, worker, key string) domain.RuntimeAcquireInput {
	t.Helper()
	view, err := f.repo.Binding(context.Background(), f.owner, f.receiver, "worker-b", f.worker)
	require.NoError(t, err)
	connection, err := runtimeConnection(context.Background(), f.db, f.owner, false)
	require.NoError(t, err)
	catalog, err := domain.ParseRuntimeCatalog(connection.Config)
	require.NoError(t, err)
	pools, ok := catalog.RuntimeProfilePools(f.owner, profile)
	require.True(t, ok)
	return domain.RuntimeAcquireInput{IdempotencyKey: key, ProfileID: profile, TaskID: task, CheckoutGeneration: 1, CheckoutRequestID: &request,
		WorkerRef: worker, ExpectedCatalogRevision: view.Revision, ExpectedCatalogDigest: view.Digest, ExpectedAdmissionRevision: view.Admission.Revision,
		ExpectedProfileRevision: domain.RuntimeProfileRevision(view.Revision), ExpectedPoolSetDigest: domain.RuntimePoolSetDigest(pools), TTLSeconds: 60}
}

func (f reservationFixture) acquire(in domain.RuntimeAcquireInput) (*domain.RuntimeReservation, bool, error) {
	return f.repo.AcquireReservation(context.Background(), f.owner, f.receiver, "worker-b", f.worker, in)
}

func requireReason(t *testing.T, err error, code int, reason string) {
	t.Helper()
	var apiErr *apierror.Error
	require.True(t, errors.As(err, &apiErr), "expected API error %d/%s, got %v", code, reason, err)
	require.Equal(t, code, apiErr.Code, "%v", err)
	if reason != "" {
		require.Equal(t, reason, apiErr.Details, "%v", err)
	}
}

func (f reservationFixture) activeCount(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM runtime_reservations WHERE agent_id=$1 AND state IN ('reserved','consumed')`, f.worker.AgentID))
	return n
}

func (f reservationFixture) claimCount(t *testing.T, id uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM runtime_reservation_pool_claims WHERE reservation_id=$1`, id))
	return n
}

func roomyPools(c *domain.RuntimeCatalog) {
	for name, p := range c.Pools {
		p.MaxConcurrency = 50
		c.Pools[name] = p
	}
}

// Two controllers race many acquires for one identity with cap=2: exactly two
// win, whatever controller or task they came through.
func TestRuntimeReservationIdentityCapRaceAcrossControllers(t *testing.T) {
	f := seedReservation(t, roomyPools)
	const racers = 8
	inputs := make([]domain.RuntimeAcquireInput, racers)
	for i := range inputs {
		task, request := f.checkedOutTask(t)
		profile := []string{"preferred", "preferred-b"}[i%2]
		inputs[i] = f.acquireInput(t, profile, task, request, fmt.Sprintf("worker-%d", i), fmt.Sprintf("attempt-%d", i))
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]error, racers)
	for i := range inputs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, results[i] = f.acquire(inputs[i])
		}()
	}
	close(start)
	wg.Wait()
	won := 0
	for _, err := range results {
		if err == nil {
			won++
			continue
		}
		requireReason(t, err, http.StatusConflict, "identity_cap_reached")
	}
	require.Equal(t, 2, won, "global identity cap=2 admits exactly two concurrent reservations")
	require.Equal(t, 2, f.activeCount(t))
	// Lowering the cap never revokes an active reservation; it only refuses new ones.
	f.setCap(t, 1)
	require.Equal(t, 2, f.activeCount(t))
	task, request := f.checkedOutTask(t)
	_, _, err := f.acquire(f.acquireInput(t, "preferred", task, request, "worker-late", "attempt-late"))
	requireReason(t, err, http.StatusConflict, "identity_cap_reached")
}

// Shared canonical pool (max_concurrency=1) raced from both controllers.
func TestRuntimeReservationPoolCapacityRace(t *testing.T) {
	f := seedReservation(t, nil)
	f.setCap(t, 10)
	const racers = 4
	inputs := make([]domain.RuntimeAcquireInput, racers)
	for i := range inputs {
		task, request := f.checkedOutTask(t)
		inputs[i] = f.acquireInput(t, []string{"preferred", "preferred-b"}[i%2], task, request, fmt.Sprintf("w-%d", i), fmt.Sprintf("k-%d", i))
	}
	var wg sync.WaitGroup
	results := make([]error, racers)
	for i := range inputs {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, results[i] = f.acquire(inputs[i]) }()
	}
	wg.Wait()
	won := 0
	for _, err := range results {
		if err == nil {
			won++
			continue
		}
		requireReason(t, err, http.StatusConflict, "pool_exhausted")
	}
	require.Equal(t, 1, won)
}

func TestRuntimeReservationIdempotentReplayAndWriterUniqueness(t *testing.T) {
	f := seedReservation(t, roomyPools)
	task, request := f.checkedOutTask(t)
	in := f.acquireInput(t, "preferred", task, request, "worker-1", "attempt-1")
	first, created, err := f.acquire(in)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, domain.RuntimeReservationReserved, first.State)
	require.Len(t, first.Pools, 2, "every pool of the profile is server-derived")
	again, created, err := f.acquire(in)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, first.ReservationID, again.ReservationID)
	require.Equal(t, first.Fence, again.Fence)
	other := in
	other.WorkerRef = "worker-2"
	_, _, err = f.acquire(other)
	requireReason(t, err, http.StatusConflict, "idempotency_scope_mismatch")
	other.IdempotencyKey = "attempt-2"
	_, _, err = f.acquire(other)
	requireReason(t, err, http.StatusConflict, "task_writer_active")
	task2, request2 := f.checkedOutTask(t)
	_, _, err = f.acquire(f.acquireInput(t, "preferred", task2, request2, "worker-1", "attempt-3"))
	requireReason(t, err, http.StatusConflict, "worker_active")
	wrongDigest := f.acquireInput(t, "preferred", task2, request2, "worker-3", "attempt-4")
	wrongDigest.ExpectedPoolSetDigest = domain.RuntimePoolSetDigest([]string{"client-chosen"})
	_, _, err = f.acquire(wrongDigest)
	requireReason(t, err, http.StatusConflict, "stale_revision")
	stolen := f.acquireInput(t, "preferred", task2, request2, "worker-3", "attempt-5")
	stolen.CheckoutGeneration = 2
	_, _, err = f.acquire(stolen)
	requireReason(t, err, http.StatusConflict, "writer_lease_mismatch")

	consume := domain.RuntimeConsumeInput{Fence: first.Fence, CheckoutGeneration: 1, RunLeaseSeconds: 600}
	consumed, err := f.repo.ConsumeReservation(context.Background(), f.owner, f.receiver, "worker-b", first.ReservationID, f.worker, consume)
	require.NoError(t, err)
	require.NotNil(t, consumed.ConsumeReceipt)
	replayed, err := f.repo.ConsumeReservation(context.Background(), f.owner, f.receiver, "worker-b", first.ReservationID, f.worker, consume)
	require.NoError(t, err)
	require.Equal(t, consumed.ConsumeReceipt.ReceiptID, replayed.ConsumeReceipt.ReceiptID, "consume replay returns the same receipt")
	// Lost consume response: the original idempotency key reconciles it.
	reconciled, _, err := f.acquire(in)
	require.NoError(t, err)
	require.Equal(t, consumed.ConsumeReceipt.ReceiptID, reconciled.ConsumeReceipt.ReceiptID)
	consume.Fence++
	_, err = f.repo.ConsumeReservation(context.Background(), f.owner, f.receiver, "worker-b", first.ReservationID, f.worker, consume)
	requireReason(t, err, http.StatusConflict, "fence_mismatch")

	data, err := json.Marshal(reconciled)
	require.NoError(t, err)
	for _, secret := range []string{"fingerprint", "idempotency", "attempt-1", "hash", "cred:"} {
		require.NotContains(t, string(data), secret)
	}
	// Another identity, even inside the receiving workspace, cannot see it.
	_, err = f.repo.GetReservation(context.Background(), f.owner, f.receiver, "worker-b", first.ReservationID, RuntimeActor{AgentID: f.reporter.AgentID, AuthWorkspaceID: f.receiver})
	requireReason(t, err, http.StatusNotFound, "")
}

func TestRuntimeReservationStaleRevisionBetweenAcquireAndConsume(t *testing.T) {
	ctx := context.Background()
	f := seedReservation(t, roomyPools)
	f.setCap(t, 5) // stale reservations stay occupied until released
	consume := func(r *domain.RuntimeReservation) error {
		_, err := f.repo.ConsumeReservation(ctx, f.owner, f.receiver, "worker-b", r.ReservationID, f.worker, domain.RuntimeConsumeInput{Fence: r.Fence, CheckoutGeneration: 1, RunLeaseSeconds: 600})
		return err
	}
	task, request := f.checkedOutTask(t)
	res, _, err := f.acquire(f.acquireInput(t, "preferred", task, request, "w1", "k1"))
	require.NoError(t, err)
	f.input.IfRevision = 1 // same content, new desired revision
	_, err = f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	requireReason(t, consume(res), http.StatusConflict, "stale_revision")

	task, request = f.checkedOutTask(t)
	res, _, err = f.acquire(f.acquireInput(t, "preferred", task, request, "w2", "k2"))
	require.NoError(t, err)
	enabled := true
	view, err := f.repo.Binding(ctx, f.owner, f.receiver, "worker-b", f.worker)
	require.NoError(t, err)
	_, err = f.repo.Admit(ctx, f.owner, f.receiver, "worker-b", f.receiverActor, RuntimeAdmissionInput{IfRevision: view.Admission.Revision, Enabled: &enabled, PermittedProfiles: view.Admission.PermittedProfiles})
	require.NoError(t, err)
	requireReason(t, consume(res), http.StatusConflict, "stale_revision")

	task, request = f.checkedOutTask(t)
	res, _, err = f.acquire(f.acquireInput(t, "preferred", task, request, "w3", "k3"))
	require.NoError(t, err)
	_, err = f.db.Exec(`UPDATE tasks SET checkout_generation=2 WHERE id=$1`, task)
	require.NoError(t, err)
	requireReason(t, consume(res), http.StatusConflict, "writer_lease_mismatch")
	var state string
	require.NoError(t, f.db.Get(&state, `SELECT state FROM runtime_reservations WHERE id=$1`, res.ReservationID))
	require.Equal(t, domain.RuntimeReservationReserved, state, "a refused consume leaves the reservation unconsumed")
}

func TestRuntimeReservationTTLExpiresOnlyUnconsumed(t *testing.T) {
	ctx := context.Background()
	f := seedReservation(t, roomyPools)
	task, request := f.checkedOutTask(t)
	unconsumed, _, err := f.acquire(f.acquireInput(t, "preferred", task, request, "w1", "k1"))
	require.NoError(t, err)
	task2, request2 := f.checkedOutTask(t)
	running, _, err := f.acquire(f.acquireInput(t, "preferred", task2, request2, "w2", "k2"))
	require.NoError(t, err)
	_, err = f.repo.ConsumeReservation(ctx, f.owner, f.receiver, "worker-b", running.ReservationID, f.worker, domain.RuntimeConsumeInput{Fence: running.Fence, CheckoutGeneration: 1, RunLeaseSeconds: 600})
	require.NoError(t, err)
	_, err = f.db.Exec(`UPDATE runtime_reservations SET expires_at=now()-interval '1 second' WHERE id IN ($1,$2)`, unconsumed.ReservationID, running.ReservationID)
	require.NoError(t, err)

	got, err := f.repo.GetReservation(ctx, f.owner, f.receiver, "worker-b", unconsumed.ReservationID, f.worker)
	require.NoError(t, err)
	require.Equal(t, domain.RuntimeReservationExpired, got.State)
	got, err = f.repo.GetReservation(ctx, f.owner, f.receiver, "worker-b", running.ReservationID, f.worker)
	require.NoError(t, err)
	require.Equal(t, domain.RuntimeReservationReconcile, got.State, "consumed past its lease is reconcile, not free")
	_, err = f.repo.ConsumeReservation(ctx, f.owner, f.receiver, "worker-b", unconsumed.ReservationID, f.worker, domain.RuntimeConsumeInput{Fence: unconsumed.Fence, CheckoutGeneration: 1, RunLeaseSeconds: 600})
	requireReason(t, err, http.StatusGone, "reservation_expired")

	// The expired one freed its task/worker/cap slot; the consumed one did not.
	_, _, err = f.acquire(f.acquireInput(t, "preferred", task, request, "w1", "k3"))
	require.NoError(t, err)
	_, _, err = f.acquire(f.acquireInput(t, "preferred", task2, request2, "w4", "k4"))
	requireReason(t, err, http.StatusConflict, "identity_cap_reached")
	f.setCap(t, 5)
	_, _, err = f.acquire(f.acquireInput(t, "preferred", task2, request2, "w4", "k5"))
	requireReason(t, err, http.StatusConflict, "task_writer_active")
	require.Equal(t, 2, f.claimCount(t, running.ReservationID))
	require.Zero(t, f.claimCount(t, unconsumed.ReservationID))
}

func TestRuntimeReservationReleaseRequiresProofAndReplays(t *testing.T) {
	ctx := context.Background()
	f := seedReservation(t, roomyPools)
	task, request := f.checkedOutTask(t)
	res, _, err := f.acquire(f.acquireInput(t, "preferred", task, request, "w1", "k1"))
	require.NoError(t, err)
	_, err = f.repo.ConsumeReservation(ctx, f.owner, f.receiver, "worker-b", res.ReservationID, f.worker, domain.RuntimeConsumeInput{Fence: res.Fence, CheckoutGeneration: 1, RunLeaseSeconds: 600})
	require.NoError(t, err)
	release := func(in domain.RuntimeReleaseInput) (*domain.RuntimeReservation, error) {
		return f.repo.ReleaseReservation(ctx, f.owner, f.receiver, "worker-b", res.ReservationID, f.worker, in)
	}
	base := domain.RuntimeReleaseInput{Fence: res.Fence, CheckoutRequestID: &request, CheckoutGeneration: 1, Stopped: true, Proof: domain.RuntimeReleaseProof{Kind: domain.RuntimeProofNoChild}}
	for name, in := range map[string]domain.RuntimeReleaseInput{
		"not stopped":              {Fence: base.Fence, CheckoutRequestID: &request, CheckoutGeneration: 1, Stopped: false, Proof: base.Proof},
		"unknown proof":            {Fence: base.Fence, CheckoutRequestID: &request, CheckoutGeneration: 1, Stopped: true, Proof: domain.RuntimeReleaseProof{Kind: "timeout"}},
		"not_started on consumed":  {Fence: base.Fence, CheckoutRequestID: &request, CheckoutGeneration: 1, Stopped: true, Proof: domain.RuntimeReleaseProof{Kind: domain.RuntimeProofNotStarted}},
		"birth proof w/o evidence": {Fence: base.Fence, CheckoutRequestID: &request, CheckoutGeneration: 1, Stopped: true, Proof: domain.RuntimeReleaseProof{Kind: domain.RuntimeProofStoppedBirthProof}},
	} {
		_, err = release(in)
		requireReason(t, err, http.StatusConflict, "release_unproven")
		require.Equal(t, 2, f.claimCount(t, res.ReservationID), name+": unproven release keeps occupancy")
	}
	wrongWriter := base
	wrongWriter.CheckoutGeneration = 2
	_, err = release(wrongWriter)
	requireReason(t, err, http.StatusConflict, "writer_mismatch")
	require.Equal(t, 1, f.activeCount(t))

	done, err := release(base)
	require.NoError(t, err)
	require.Equal(t, domain.RuntimeReservationReleased, done.State)
	require.NotNil(t, done.ReleaseReceipt)
	require.Zero(t, f.claimCount(t, res.ReservationID))
	require.Zero(t, f.activeCount(t))

	// A new writer on the same task; a late replay of the old release returns
	// the old receipt and does not touch the new reservation.
	next, _, err := f.acquire(f.acquireInput(t, "preferred", task, request, "w1", "k2"))
	require.NoError(t, err)
	replay, err := release(base)
	require.NoError(t, err)
	require.Equal(t, done.ReleaseReceipt.ReleaseID, replay.ReleaseReceipt.ReleaseID)
	require.Equal(t, 2, f.claimCount(t, next.ReservationID))
	require.Equal(t, 1, f.activeCount(t))
}

func TestRuntimeReservationRevokedGrantDisabledAndDirectMode(t *testing.T) {
	ctx := context.Background()
	f := seedReservation(t, roomyPools)
	task, request := f.checkedOutTask(t)
	held, _, err := f.acquire(f.acquireInput(t, "preferred", task, request, "w1", "k1"))
	require.NoError(t, err)

	f.setCap(t, 0)
	task2, request2 := f.checkedOutTask(t)
	in := f.acquireInput(t, "preferred", task2, request2, "w2", "k2")
	_, _, err = f.acquire(in)
	requireReason(t, err, http.StatusLocked, "identity_cap_unset")
	f.setCap(t, 2)

	disabled := false
	f.input.IfRevision = 1
	f.input.Enabled = &disabled
	_, err = f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	_, _, err = f.acquire(f.acquireInput(t, "preferred", task2, request2, "w2", "k3"))
	requireReason(t, err, http.StatusLocked, "runtime_disabled")
	require.Equal(t, 1, f.activeCount(t), "disabling denies new admission but never drops an active one")

	_, err = f.db.Exec(`UPDATE agent_workspace_grants SET revoked_at=now() WHERE id=$1`, f.grant)
	require.NoError(t, err)
	_, _, err = f.acquire(in)
	requireReason(t, err, http.StatusForbidden, "")
	// Revocation must not strand capacity: the original writer can still release.
	_, err = f.repo.ReleaseReservation(ctx, f.owner, f.receiver, "worker-b", held.ReservationID, f.worker,
		domain.RuntimeReleaseInput{Fence: held.Fence, CheckoutRequestID: &request, CheckoutGeneration: 1, Stopped: true, Proof: domain.RuntimeReleaseProof{Kind: domain.RuntimeProofNotStarted}})
	require.NoError(t, err)
	require.Zero(t, f.activeCount(t))

	// Direct mode: a workspace without a runtime connection has no reservation
	// surface and its inventory still reports direct.
	direct := seedGrantWorkspace(t, f.db)
	_, _, err = f.repo.AcquireReservation(ctx, direct, f.receiver, "worker-b", f.worker, in)
	requireReason(t, err, http.StatusNotFound, "")
	var ownerID uuid.UUID
	require.NoError(t, f.db.Get(&ownerID, `SELECT owner_id FROM workspaces WHERE id=$1`, direct))
	snapshot, err := f.repo.Inventory(ctx, direct, RuntimeActor{UserID: ownerID})
	require.NoError(t, err)
	require.Equal(t, "direct", snapshot.Mode)
}
