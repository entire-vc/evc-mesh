package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ClosedFollowUpPendingRepo against a real PostgreSQL (#db1c6c7a). The
// service tests run against a hand-written mock of this repo; these are the
// tests that keep the mock honest — every branch asserted here is one the
// mock mirrors:
//
//	Enqueue      → INSERT ... ON CONFLICT (comment_id) DO UPDATE last_error,
//	               with attempts SURVIVING the re-enqueue (no budget reset)
//	ListDue      → attempts < max (or, past the max, escalation notice not
//	               landed yet), oldest first, limited
//	MarkAttempt  → atomic increment returning the new value; (0, nil) for a
//	               row a concurrent pass already deleted
//	MarkEscalated→ first-write-wins stamp retiring the row from the due set
//	MarkNoticed  → first-write-wins stamp that does NOT retire the row (a
//	               noticed finding is still an undelivered one)
//	ClaimForReconcile → one atomic lease UPDATE: of N concurrent claimants
//	               exactly one wins; expired leases are reclaimable
//	ReleaseClaim → the end-of-unit lease release (progress writes hold it)
//	Delete       → idempotent
//
// The table has no FKs by design (it must stay writable while the rest of
// the schema is failing — that is its whole reason to exist), so no task or
// comment fixtures are needed: bare UUIDs are enough.
//
// Untagged on purpose — the *_db_test.go convention (DATABASE_URL, skip when
// no Postgres is reachable); the coverage-gate job runs a migrated postgres
// service, so these execute there. Jobs and laptops without a database skip
// them instead of failing.

func newClosedFollowUpPendingTest(t *testing.T) (*ClosedFollowUpPendingRepo, uuid.UUID) {
	t.Helper()
	repo := NewClosedFollowUpPendingRepo(closedFollowUpRootsTestDB(t))
	sourceID := uuid.New()
	t.Cleanup(func() {
		_, _ = repo.db.ExecContext(context.Background(),
			"DELETE FROM closed_followup_pending WHERE source_task_id = $1", sourceID)
	})
	return repo, sourceID
}

func TestClosedFollowUpPendingRepo_EnqueueListMarkDelete(t *testing.T) {
	repo, sourceID := newClosedFollowUpPendingTest(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)

	row := func(commentID uuid.UUID, key string, attempts int, lastErr string, created time.Time) *domain.ClosedFollowUpPending {
		return &domain.ClosedFollowUpPending{
			CommentID: commentID, SourceTaskID: sourceID, FindingKey: key,
			Attempts: attempts, LastError: lastErr, CreatedAt: created,
		}
	}

	older := uuid.New()
	require.NoError(t, repo.Enqueue(ctx, row(older, "txt:older", 0, "first failure", base)))
	newer := uuid.New()
	require.NoError(t, repo.Enqueue(ctx, row(newer, "txt:newer", 0, "first failure", base.Add(time.Minute))))

	// ListDue: oldest first — the finding that has been waiting longest is
	// delivered first.
	due, err := repo.ListDue(ctx, 10, 50)
	require.NoError(t, err)
	require.True(t, len(due) >= 2, "both rows are due")
	require.Equal(t, older, due[0].CommentID, "the OLDEST pending finding is retried first")
	require.Equal(t, newer, due[len(due)-1].CommentID)
	found := due[len(due)-1]
	assert.Equal(t, sourceID, found.SourceTaskID)
	assert.Equal(t, "txt:newer", found.FindingKey)
	assert.Zero(t, found.Attempts, "a freshly enqueued row carries attempts 0 — the counter counts reconcile passes")

	// MarkAttempt: atomic, returns the NEW value, records the cause.
	n, err := repo.MarkAttempt(ctx, newer, "claim: connection refused")
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	// Re-enqueue of the SAME comment must not reset the budget: the finding
	// failed inline again, which costs the reconcile budget nothing. Only
	// last_error refreshes.
	require.NoError(t, repo.Enqueue(ctx, row(newer, "txt:newer", 0, "failed inline again", base.Add(2*time.Minute))))
	due, err = repo.ListDue(ctx, 10, 50)
	require.NoError(t, err)
	for _, p := range due {
		if p.CommentID == newer {
			assert.Equal(t, 1, p.Attempts, "ON CONFLICT must keep attempts — a re-enqueued finding does not start its budget over")
			assert.Equal(t, "failed inline again", p.LastError)
		}
	}

	// A row at the budget's edge with NO escalation stamp STAYS listed — for
	// the notice alone (codex-review P1, round 3): the attempt counter must
	// never be what takes an untold human out of the queue.
	for i := 0; i < 9; i++ {
		_, mErr := repo.MarkAttempt(ctx, newer, "still failing")
		require.NoError(t, mErr)
	}
	due, err = repo.ListDue(ctx, 10, 50)
	require.NoError(t, err)
	var listedAtBudget bool
	for _, p := range due {
		if p.CommentID == newer {
			listedAtBudget = true
			require.Nil(t, p.EscalatedAt, "the notice has not landed — the row must read un-escalated")
		}
	}
	assert.True(t, listedAtBudget, "a row at 10 attempts stays listed while its escalation notice has not landed")

	// MarkEscalated stamps the escalation — first write wins. But with the
	// park notice still un-landed (noticed_at NULL) the row STAYS listed:
	// retiring on the escalation alone is the strand round 5 closed.
	stamped := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, repo.MarkEscalated(ctx, newer, stamped))
	require.NoError(t, repo.MarkEscalated(ctx, newer, stamped.Add(time.Hour)))
	var gotStamped time.Time
	require.NoError(t, repo.db.GetContext(ctx, &gotStamped,
		`SELECT escalated_at FROM closed_followup_pending WHERE comment_id = $1`, newer))
	assert.True(t, gotStamped.Equal(stamped), "the first stamp wins — a duplicate stamp never moves the landing time")
	due, err = repo.ListDue(ctx, 10, 50)
	require.NoError(t, err)
	var escalatedButUnnoticed bool
	for _, p := range due {
		if p.CommentID == newer {
			escalatedButUnnoticed = true
		}
	}
	assert.True(t, escalatedButUnnoticed,
		"an escalated row whose park notice never landed stays in the due set — BOTH stamps must land before it retires")

	// Only the second stamp retires it: a row is done when the escalation
	// AND the park notice have both been told.
	require.NoError(t, repo.MarkNoticed(ctx, newer, stamped))
	due, err = repo.ListDue(ctx, 10, 50)
	require.NoError(t, err)
	for _, p := range due {
		assert.NotEqual(t, newer, p.CommentID, "a row with BOTH stamps set leaves the due set — it is done")
	}

	// MarkNoticed: the park's own visibility stamp, same first-write-wins
	// discipline — a retried pass arriving late never moves the landing time
	// (codex-review P1, round 4). It does NOT retire the row: a noticed
	// finding is still an undelivered one, and stays in the due set.
	noticed := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, repo.MarkNoticed(ctx, older, noticed))
	require.NoError(t, repo.MarkNoticed(ctx, older, noticed.Add(time.Hour)))
	var gotNoticed time.Time
	require.NoError(t, repo.db.GetContext(ctx, &gotNoticed,
		`SELECT noticed_at FROM closed_followup_pending WHERE comment_id = $1`, older))
	assert.True(t, gotNoticed.Equal(noticed), "the first notice stamp wins — a duplicate never moves the landing time")
	due, err = repo.ListDue(ctx, 10, 50)
	require.NoError(t, err)
	var noticedStillDue bool
	for _, p := range due {
		if p.CommentID == older {
			noticedStillDue = true
			require.NotNil(t, p.NoticedAt, "the stamp round-trips through ListDue")
		}
	}
	assert.True(t, noticedStillDue, "a noticed row is still due — only DELIVERY (or its terminal outcome) retires it")

	// limit bounds one pass's work.
	due, err = repo.ListDue(ctx, 10, 1)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, older, due[0].CommentID, "limit keeps the oldest row")

	// MarkAttempt on a row a concurrent pass already deleted: (0, nil), never
	// an error the reconcile loop would count as a failed attempt.
	n, err = repo.MarkAttempt(ctx, uuid.New(), "gone")
	require.NoError(t, err)
	assert.Zero(t, n)

	// Delete is idempotent — the crash window between delivery and delete is
	// survived by a second pass re-delivering idempotently and deleting again.
	require.NoError(t, repo.Delete(ctx, older))
	require.NoError(t, repo.Delete(ctx, older))
	due, err = repo.ListDue(ctx, 10, 50)
	require.NoError(t, err)
	for _, p := range due {
		assert.NotEqual(t, older, p.CommentID)
	}
}

// The increment must live in the UPDATE itself: two passes marking attempts
// concurrently can only ever count MORE attempts, never lose one. A Go-side
// read-modify-write here would let the budget undercount and a wedged finding
// retry forever.
func TestClosedFollowUpPendingRepo_MarkAttemptIsAtomicUnderConcurrency(t *testing.T) {
	repo, sourceID := newClosedFollowUpPendingTest(t)
	ctx := context.Background()
	commentID := uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, repo.Enqueue(ctx, &domain.ClosedFollowUpPending{
		CommentID: commentID, SourceTaskID: sourceID, FindingKey: "txt:race",
		Attempts: 0, LastError: "seed", CreatedAt: now,
	}))

	const perPass, passes = 5, 2
	var wg sync.WaitGroup
	for p := 0; p < passes; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perPass; i++ {
				if _, err := repo.MarkAttempt(ctx, commentID, "concurrent failure"); err != nil {
					t.Errorf("mark attempt: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	due, err := repo.ListDue(ctx, 100, 1)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, perPass*passes, due[0].Attempts,
		"every concurrent MarkAttempt must be counted exactly once — the budget is the escalation trigger")
}

// The lease column's whole contract (#a2368528, from the codex-review R7
// finding on !1083): ClaimForReconcile is ONE atomic UPDATE, so of N
// concurrent claimants exactly one wins; an unexpired lease refuses a second
// claimant; an EXPIRED lease is claimable again (a pass that died mid-row
// returns its rows by TTL, never by hand); and every progress write releases
// the lease — a failing row keeps its per-pass retry cadence instead of
// waiting out the TTL. With the service-level overlap test in
// comment_closed_task_followup_pending_test.go this is the card's "two
// parallel reconcile passes over one row → one delivery" acceptance,
// decomposed: atomicity of the claim here, the skip in the loop there.
func TestClosedFollowUpPendingRepo_ClaimLeaseIsAtomic(t *testing.T) {
	repo, sourceID := newClosedFollowUpPendingTest(t)
	ctx := context.Background()
	leaseFor := 10 * time.Minute // ClosedFollowUpClaimTTL in production

	seed := func(key string) uuid.UUID {
		id := uuid.New()
		require.NoError(t, repo.Enqueue(ctx, &domain.ClosedFollowUpPending{
			CommentID: id, SourceTaskID: sourceID, FindingKey: key,
			Attempts: 0, LastError: "seed", CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		}))
		return id
	}
	claimedUntil := func(id uuid.UUID) *time.Time {
		var v *time.Time
		require.NoError(t, repo.db.GetContext(ctx, &v,
			`SELECT claimed_until FROM closed_followup_pending WHERE comment_id = $1`, id))
		return v
	}

	// Row A — the lifecycle. Unleased: the first claim takes it, the second
	// is refused while the lease is live.
	a := seed("txt:lease-a")
	claimed, err := repo.ClaimForReconcile(ctx, a, time.Now().UTC().Add(leaseFor))
	require.NoError(t, err)
	assert.True(t, claimed)
	assert.NotNil(t, claimedUntil(a), "a taken lease is visible in the row")
	claimed, err = repo.ClaimForReconcile(ctx, a, time.Now().UTC().Add(leaseFor))
	require.NoError(t, err)
	assert.False(t, claimed, "a live lease refuses a second claimant — the atomic UPDATE is the lock")

	// Progress writes HOLD the lease to the end of the row unit: a stamp is
	// work INSIDE the unit, and a mid-row release is the overlap window
	// reopening — MarkNoticed lands BEFORE the delivery attempt on the
	// normal path, so its release would let a second pass claim the row
	// while the first is still delivering (codex-review round 2 on !1085,
	// the P1 this table's lease exists to close). Only ReleaseClaim, the
	// row's delete, or TTL expiry give a leased row back.
	_, err = repo.MarkAttempt(ctx, a, "failed again")
	require.NoError(t, err)
	assert.NotNil(t, claimedUntil(a), "MarkAttempt holds the lease — the failed pass owns its row to the unit's end")
	claimed, err = repo.ClaimForReconcile(ctx, a, time.Now().UTC().Add(leaseFor))
	require.NoError(t, err)
	assert.False(t, claimed, "a progress write is not a release — the row stays unclaimable mid-unit")
	require.NoError(t, repo.MarkNoticed(ctx, a, time.Now().UTC()))
	assert.NotNil(t, claimedUntil(a), "MarkNoticed holds the lease — the notice stamp precedes delivery")
	require.NoError(t, repo.MarkEscalated(ctx, a, time.Now().UTC()))
	assert.NotNil(t, claimedUntil(a), "MarkEscalated holds the lease to the unit's end")

	// An EXPIRED lease is a dead lease: a pass that crashed mid-row loses
	// its rows by TTL, so the row comes back on its own.
	_, err = repo.db.ExecContext(ctx,
		`UPDATE closed_followup_pending SET claimed_until = now() - interval '1 minute' WHERE comment_id = $1`, a)
	require.NoError(t, err)
	claimed, err = repo.ClaimForReconcile(ctx, a, time.Now().UTC().Add(leaseFor))
	require.NoError(t, err)
	assert.True(t, claimed, "an expired lease is reclaimable — the TTL is the crash recovery, not a lock")

	// ReleaseClaim: the keep-path write for a row that survives a pass with
	// no progress stamp (delivered, park notice still unlanded).
	require.NoError(t, repo.ReleaseClaim(ctx, a))
	assert.Nil(t, claimedUntil(a))
	claimed, err = repo.ClaimForReconcile(ctx, a, time.Now().UTC().Add(leaseFor))
	require.NoError(t, err)
	assert.True(t, claimed, "a released row is claimable again immediately")

	// Row B — the race itself: N concurrent claimants on one unleased row,
	// exactly one winner. This is the SQL-level half of "two parallel
	// reconcile passes over one row deliver once": if two claimants could
	// both win, the loop's lease check would be advisory, not a lock.
	b := seed("txt:lease-b")
	const claimants = 8
	winners := make(chan bool, claimants)
	var wg sync.WaitGroup
	for i := 0; i < claimants; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w, werr := repo.ClaimForReconcile(ctx, b, time.Now().UTC().Add(leaseFor))
			if werr != nil {
				t.Errorf("claim: %v", werr)
				return
			}
			winners <- w
		}()
	}
	wg.Wait()
	close(winners)
	var won int
	for w := range winners {
		if w {
			won++
		}
	}
	assert.Equal(t, 1, won, "exactly one of the concurrent claimants takes the row — RowsAffected is the arbiter")
	assert.NotNil(t, claimedUntil(b), "the winner's lease is persisted")
}
