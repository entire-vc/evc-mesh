package postgres

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Controller freshness/pause, run-lease renewal and consume/release recovery
// (R3b). Real PostgreSQL; skipped without a reachable database.

func (f reservationFixture) capacityFor(t *testing.T) domain.RuntimeAgentCapacity {
	t.Helper()
	agent := f.worker.AgentID
	out, err := f.repo.Capacity(context.Background(), f.receiver, f.receiverActor, &agent)
	require.NoError(t, err)
	require.Len(t, out.Agents, 1)
	return out.Agents[0]
}

func (f reservationFixture) consumeNow(t *testing.T, res *domain.RuntimeReservation) {
	t.Helper()
	_, err := f.repo.ConsumeReservation(context.Background(), f.owner, f.receiver, "worker-b", res.ReservationID, f.worker,
		domain.RuntimeConsumeInput{Fence: res.Fence, CheckoutGeneration: 1, RunLeaseSeconds: 600})
	require.NoError(t, err)
}

func (f reservationFixture) renew(res *domain.RuntimeReservation, request uuid.UUID, generation int64, seconds int) (*domain.RuntimeReservation, error) {
	return f.repo.RenewReservation(context.Background(), f.owner, f.receiver, "worker-b", res.ReservationID, f.worker,
		domain.RuntimeRenewInput{Fence: res.Fence, CheckoutRequestID: &request, CheckoutGeneration: generation, RunLeaseSeconds: seconds})
}

// A paused, stale or silent controller cannot admit new work and is never
// reported as ready; reservations already held stay occupied and replayable.
func TestRuntimeAdmissionRefusesPausedStaleUnreportedController(t *testing.T) {
	f := seedReservation(t, roomyPools)
	task, request := f.checkedOutTask(t)
	held, _, err := f.acquire(f.acquireInput(t, "preferred", task, request, "w-held", "k-held"))
	require.NoError(t, err)

	view := f.capacityFor(t)
	require.Equal(t, domain.RuntimeCapacityAvailable, view.Reason)
	require.ElementsMatch(t, []domain.RuntimeAgentController{{ControllerRef: "runner-a", State: "current"}, {ControllerRef: "runner-b", State: "current"}}, view.Controllers)

	// The next task's live checkout is already a writer, so leave room for it.
	f.setCap(t, 3)
	next, nextRequest := f.checkedOutTask(t)
	in := f.acquireInput(t, "preferred", next, nextRequest, "w-next", "k-next")

	f.reportControllers(t, true)
	_, _, err = f.acquire(in)
	requireReason(t, err, http.StatusLocked, "controller_paused")
	view = f.capacityFor(t)
	require.Equal(t, domain.RuntimeCapacityControllerDown, view.Reason)
	require.Zero(t, view.Ready)
	require.Equal(t, 1, view.Reserved, "a held reservation stays occupied while its controller is paused")
	require.Equal(t, 2, view.Occupied)
	replay, created, err := f.acquire(f.acquireInput(t, "preferred", task, request, "w-held", "k-held"))
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, held.ReservationID, replay.ReservationID, "idempotent replay of an existing reservation still answers")

	f.reportControllers(t, false)
	_, err = f.db.Exec(`UPDATE runtime_controller_reports SET received_at=now()-interval '1 day'`)
	require.NoError(t, err)
	_, _, err = f.acquire(in)
	requireReason(t, err, http.StatusLocked, "controller_unavailable")
	require.Equal(t, domain.RuntimeCapacityControllerDown, f.capacityFor(t).Reason)

	_, err = f.db.Exec(`DELETE FROM runtime_controller_reports`)
	require.NoError(t, err)
	_, _, err = f.acquire(in)
	requireReason(t, err, http.StatusLocked, "controller_unavailable")
	states := f.capacityFor(t).Controllers
	require.Len(t, states, 2)
	for _, s := range states {
		require.Equal(t, domain.RuntimeControllerUnreported, s.State)
	}

	f.reportControllers(t, false)
	_, created, err = f.acquire(in)
	require.NoError(t, err)
	require.True(t, created)
	back := f.capacityFor(t)
	require.Equal(t, domain.RuntimeCapacityAvailable, back.Reason, "a current controller reopens admission")
	require.Equal(t, 2, back.Reserved)
}

func TestRuntimeReservationRenewLease(t *testing.T) {
	ctx := context.Background()
	f := seedReservation(t, roomyPools)
	task, request := f.checkedOutTask(t)
	res, _, err := f.acquire(f.acquireInput(t, "preferred", task, request, "w1", "k1"))
	require.NoError(t, err)

	_, err = f.renew(res, request, 1, 600)
	requireReason(t, err, http.StatusConflict, "not_consumed")

	f.consumeNow(t, res)
	got, err := f.repo.GetReservation(ctx, f.owner, f.receiver, "worker-b", res.ReservationID, f.worker)
	require.NoError(t, err)
	base := got.ConsumeReceipt.RunLeaseExpiresAt

	longer, err := f.renew(res, request, 1, 7200)
	require.NoError(t, err)
	require.Equal(t, domain.RuntimeReservationConsumed, longer.State)
	require.True(t, longer.ConsumeReceipt.RunLeaseExpiresAt.After(base.Add(60*60*1e9)), "renew extends the run lease")
	shorter, err := f.renew(res, request, 1, 60)
	require.NoError(t, err)
	require.Equal(t, longer.ConsumeReceipt.RunLeaseExpiresAt, shorter.ConsumeReceipt.RunLeaseExpiresAt, "renew never shortens a lease")
	require.Equal(t, longer.ConsumeReceipt.ReceiptID, shorter.ConsumeReceipt.ReceiptID)

	_, err = f.renew(res, request, 2, 600)
	requireReason(t, err, http.StatusConflict, "writer_mismatch")
	_, err = f.renew(res, uuid.New(), 1, 600)
	requireReason(t, err, http.StatusConflict, "writer_mismatch")
	_, err = f.repo.RenewReservation(ctx, f.owner, f.receiver, "worker-b", res.ReservationID, f.worker, domain.RuntimeRenewInput{Fence: res.Fence + 1, CheckoutRequestID: &request, CheckoutGeneration: 1, RunLeaseSeconds: 600})
	requireReason(t, err, http.StatusConflict, "writer_mismatch")
	_, err = f.repo.RenewReservation(ctx, f.owner, f.receiver, "worker-b", res.ReservationID, f.reporter, domain.RuntimeRenewInput{Fence: res.Fence, CheckoutRequestID: &request, CheckoutGeneration: 1, RunLeaseSeconds: 600})
	require.Error(t, err, "another agent cannot renew")
	_, err = f.repo.RenewReservation(ctx, f.owner, f.receiver, "worker-b", res.ReservationID, f.worker, domain.RuntimeRenewInput{Fence: res.Fence, CheckoutRequestID: &request, CheckoutGeneration: 1, RunLeaseSeconds: 5})
	require.Error(t, err, "lease below the minimum is rejected")
}

// A lapsed run lease is unknown and stays occupied; only the exact writer that
// still holds its checkout can revive it, and a writer that lost the checkout
// cannot.
func TestRuntimeReservationLapsedLeaseStaysOccupiedUntilRenewedOrProven(t *testing.T) {
	ctx := context.Background()
	f := seedReservation(t, roomyPools)
	task, request := f.checkedOutTask(t)
	res, _, err := f.acquire(f.acquireInput(t, "preferred", task, request, "w1", "k1"))
	require.NoError(t, err)
	f.consumeNow(t, res)

	_, err = f.db.Exec(`UPDATE runtime_reservations SET expires_at=now()-interval '1 minute' WHERE id=$1`, res.ReservationID)
	require.NoError(t, err)
	got, err := f.repo.GetReservation(ctx, f.owner, f.receiver, "worker-b", res.ReservationID, f.worker)
	require.NoError(t, err)
	require.Equal(t, domain.RuntimeReservationReconcile, got.State)
	view := f.capacityFor(t)
	require.Equal(t, 1, view.Reconcile)
	require.Equal(t, 1, view.Occupied, "unknown after consume is occupied")
	require.Equal(t, 2, f.claimCount(t, res.ReservationID))

	// Without proof the writer cannot free it, and a lost consume replays the same receipt.
	_, err = f.repo.ReleaseReservation(ctx, f.owner, f.receiver, "worker-b", res.ReservationID, f.worker, domain.RuntimeReleaseInput{Fence: res.Fence, CheckoutRequestID: &request, CheckoutGeneration: 1, Stopped: false, Proof: domain.RuntimeReleaseProof{Kind: domain.RuntimeProofNoChild}})
	requireReason(t, err, http.StatusConflict, "release_unproven")
	again, err := f.repo.ConsumeReservation(ctx, f.owner, f.receiver, "worker-b", res.ReservationID, f.worker, domain.RuntimeConsumeInput{Fence: res.Fence, CheckoutGeneration: 1, RunLeaseSeconds: 600})
	require.NoError(t, err)
	require.Equal(t, got.ConsumeReceipt.ReceiptID, again.ConsumeReceipt.ReceiptID)
	require.Equal(t, 2, f.claimCount(t, res.ReservationID))

	// The writer lost its checkout: renewal is refused and occupancy stays.
	_, err = f.db.Exec(`UPDATE tasks SET checked_out_by=NULL,checkout_expires=NULL WHERE id=$1`, task)
	require.NoError(t, err)
	_, err = f.renew(res, request, 1, 600)
	requireReason(t, err, http.StatusConflict, "writer_lease_mismatch")
	require.Equal(t, 2, f.claimCount(t, res.ReservationID))
	_, err = f.db.Exec(`UPDATE tasks SET checked_out_by=$2,checkout_expires=now()+interval '1 hour' WHERE id=$1`, task, f.worker.AgentID)
	require.NoError(t, err)

	revived, err := f.renew(res, request, 1, 600)
	require.NoError(t, err)
	require.Equal(t, domain.RuntimeReservationConsumed, revived.State)
	require.Zero(t, f.capacityFor(t).Reconcile)

	// A revoked grant cannot renew; the reservation stays occupied and is
	// released only by a proven stop.
	_, err = f.db.Exec(`UPDATE agent_workspace_grants SET revoked_at=now() WHERE id=$1`, f.grant)
	require.NoError(t, err)
	_, err = f.renew(res, request, 1, 600)
	require.Error(t, err)
	require.Equal(t, 2, f.claimCount(t, res.ReservationID))
	done, err := f.repo.ReleaseReservation(ctx, f.owner, f.receiver, "worker-b", res.ReservationID, f.worker, domain.RuntimeReleaseInput{Fence: res.Fence, CheckoutRequestID: &request, CheckoutGeneration: 1, Stopped: true, Proof: domain.RuntimeReleaseProof{Kind: domain.RuntimeProofNoChild}})
	require.NoError(t, err)
	require.Equal(t, domain.RuntimeReservationReleased, done.State)
	_, err = f.renew(res, request, 1, 600)
	requireReason(t, err, http.StatusForbidden, "") // renewal needs a live grant, which is gone; release did not
}

// Concurrent retries of the same exact release (a crashed releaser and its
// restart) commit one release; a successor writer on the same task is never
// touched, and a retry with another writer's identity is refused.
func TestRuntimeReservationReleaseIsExactlyOnceUnderRetry(t *testing.T) {
	ctx := context.Background()
	f := seedReservation(t, roomyPools)
	task, request := f.checkedOutTask(t)
	res, _, err := f.acquire(f.acquireInput(t, "preferred", task, request, "w1", "k1"))
	require.NoError(t, err)
	f.consumeNow(t, res)
	in := domain.RuntimeReleaseInput{Fence: res.Fence, CheckoutRequestID: &request, CheckoutGeneration: 1, Stopped: true, Proof: domain.RuntimeReleaseProof{Kind: domain.RuntimeProofNoChild}}

	const retries = 6
	ids := make([]uuid.UUID, retries)
	errs := make([]error, retries)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, releaseErr := f.repo.ReleaseReservation(ctx, f.owner, f.receiver, "worker-b", res.ReservationID, f.worker, in)
			errs[i] = releaseErr
			if releaseErr == nil {
				ids[i] = got.ReleaseReceipt.ReleaseID
			}
		}()
	}
	wg.Wait()
	for i := range ids {
		require.NoError(t, errs[i])
		require.Equal(t, ids[0], ids[i], "every retry confirms the same release")
	}
	var releases int
	require.NoError(t, f.db.Get(&releases, `SELECT count(*) FROM runtime_reservations WHERE id=$1 AND release_id IS NOT NULL`, res.ReservationID))
	require.Equal(t, 1, releases)

	newRequest := uuid.New()
	_, err = f.db.Exec(`UPDATE tasks SET checkout_generation=2,checkout_request_id=$2 WHERE id=$1`, task, newRequest)
	require.NoError(t, err)
	next := f.acquireInput(t, "preferred", task, newRequest, "w1", "k2")
	next.CheckoutGeneration = 2
	successor, _, err := f.acquire(next)
	require.NoError(t, err)
	stale := in
	stale.CheckoutGeneration = 2
	_, err = f.repo.ReleaseReservation(ctx, f.owner, f.receiver, "worker-b", res.ReservationID, f.worker, stale)
	requireReason(t, err, http.StatusConflict, "writer_mismatch")
	replay, err := f.repo.ReleaseReservation(ctx, f.owner, f.receiver, "worker-b", res.ReservationID, f.worker, in)
	require.NoError(t, err)
	require.Equal(t, ids[0], replay.ReleaseReceipt.ReleaseID)
	require.Equal(t, 2, f.claimCount(t, successor.ReservationID), "the successor writer keeps its slots")
}

// An unreadable report, a report whose reporter grant is gone and a controller
// the catalog no longer knows are all unsafe: stale or unreported, never current.
func TestRuntimeControllerLivenessUnreadableOrOrphanedReport(t *testing.T) {
	f := seedReservation(t, roomyPools)
	ctx := context.Background()
	connection, err := runtimeConnection(ctx, f.db, f.owner, false)
	require.NoError(t, err)
	catalog, err := domain.ParseRuntimeCatalog(connection.Config)
	require.NoError(t, err)
	snapshot, err := f.repo.Inventory(ctx, f.owner, f.ownerActor)
	require.NoError(t, err)
	state := func(ref string) string {
		got, liveErr := runtimeControllerLiveness(ctx, f.db, connection, catalog, ref, snapshot.Revision, snapshot.Digest)
		require.NoError(t, liveErr)
		return got
	}
	require.Equal(t, domain.RuntimeControllerCurrent, state("runner-a"))
	require.Equal(t, domain.RuntimeControllerUnreported, state("no-such-controller"))

	_, err = f.db.Exec(`UPDATE runtime_controller_reports SET report='[]'::jsonb WHERE integration_id=$1 AND controller_ref='runner-a'`, connection.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RuntimeControllerStale, state("runner-a"), "unreadable report")

	_, err = f.db.Exec(`DELETE FROM runtime_controller_reports WHERE integration_id=$1`, connection.ID)
	require.NoError(t, err)
	f.reportControllers(t, false)
	_, err = f.db.Exec(`UPDATE agent_workspace_grants SET revoked_at=now() WHERE id=$1`, f.reporterGrantID(t, connection.ID))
	require.NoError(t, err)
	require.Equal(t, domain.RuntimeControllerStale, state("runner-a"), "reporter grant revoked")
}

func (f reservationFixture) reporterGrantID(t *testing.T, integration uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, f.db.Get(&id, `SELECT reporter_grant_id FROM runtime_controller_reports WHERE integration_id=$1 AND controller_ref='runner-a'`, integration))
	return id
}
