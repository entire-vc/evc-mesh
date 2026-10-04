package postgres

import (
	"context"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository"
)

// ClosedFollowUpRootRepo against a real PostgreSQL (#5194afd4). The service
// tests run against a hand-written mock of this repo; these are the tests that
// keep the mock honest — every branch asserted here is one the mock mirrors:
//
//	Claim          → INSERT ... ON CONFLICT DO NOTHING, RowsAffected decides
//	TryReopen      → the 24h-window CASE that resets to 1 after expiry, plus
//	                 the limit WHERE that refuses the limit+1-th increment
//	Get miss       → (nil, nil), never sql.ErrNoRows leaking upward
//	Delete         → the compensation for a claim whose card never appeared
//
// The table has no FKs by design (additive migration, standalone identity
// store), so no task fixtures are needed — bare UUIDs are enough.
//
// Untagged on purpose — the *_db_test.go convention (DATABASE_URL, skip when
// no Postgres is reachable): the coverage-gate job runs a migrated postgres
// service, so these tests execute there and the repo lines they cover count
// toward the diff-coverage gate; jobs and laptops without a database skip
// them instead of failing. The `-tags=integration` job runs them too.

// redactedDSN strips credentials before a DSN reaches test output: a skip
// message says WHERE it tried to connect, never the secret half of the DSN
// (codex-review P2 round 7, P1 round 8, MR !1078). Credentials ride in more
// than userinfo — the password= query parameter and keyword-style DSNs both
// carry them — so nothing is CARRIED OVER from the parsed DSN: the whitelist
// scheme, host and database name is rebuilt by hand. A DSN that does not
// parse as a URL with a host (keyword style among them) renders as a fixed
// string — a loss of diagnostics, never of a secret.
func redactedDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "[redacted dsn]"
	}
	return u.Scheme + "://" + u.Host + u.Path
}

// The scrubber must survive every DSN shape Postgres accepts (codex-review
// P1, MR !1078, round 8): credentials ride not only in userinfo but in the
// password= query parameter and in keyword-style DSNs. RED against a
// userinfo-only scrubber.
func TestRedactedDSN_NeverCarriesCredentials(t *testing.T) {
	for _, dsn := range []string{
		"postgres://mesh:hunter2@localhost:5432/mesh?sslmode=disable",
		"postgres://mesh@localhost:5432/mesh?password=hunter2&sslmode=disable",
		"postgres://localhost:5432/mesh?user=mesh&password=hunter2",
		"host=localhost password=hunter2 dbname=mesh",
	} {
		out := redactedDSN(dsn)
		assert.NotContains(t, out, "hunter2", "dsn %q leaked its credential as %q", dsn, out)
	}
	assert.Equal(t, "postgres://localhost:5432/mesh",
		redactedDSN("postgres://mesh:hunter2@localhost:5432/mesh?sslmode=disable"),
		"the safe parts — scheme, host, database — stay for diagnostics")
}

func closedFollowUpRootsTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres at %s, skipping: %v", redactedDSN(dsn), err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Skipf("Postgres at %s not accepting connections, skipping: %v", redactedDSN(dsn), err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newClosedFollowUpRootsTest(t *testing.T) (*ClosedFollowUpRootRepo, uuid.UUID, string) {
	t.Helper()
	repo := NewClosedFollowUpRootRepo(closedFollowUpRootsTestDB(t))
	sourceID := uuid.New()
	key := "txt:" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = repo.db.ExecContext(context.Background(),
			"DELETE FROM closed_followup_reopen_claims WHERE source_task_id = $1", sourceID)
		_, _ = repo.db.ExecContext(context.Background(),
			"DELETE FROM closed_followup_roots WHERE source_task_id = $1", sourceID)
	})
	return repo, sourceID, key
}

// CompensateReopen returns a slot TryReopen took for a reopen that never
// delivered — deleting the claim's OWN ledger row and recomputing the count
// and the window anchor from the survivors. Deleting OUR row is the exact
// protection that keeps the compensation from taking back somebody else's
// slot (codex-review P2, MR !1078).
func TestClosedFollowUpRootRepo_CompensateReopen(t *testing.T) {
	repo, sourceID, key := newClosedFollowUpRootsTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	_, err := repo.Claim(ctx, sourceID, key, uuid.New(), now)
	require.NoError(t, err)

	ok, claim, err := repo.TryReopen(ctx, sourceID, key, now)
	require.NoError(t, err)
	require.True(t, ok)
	assert.NotEqual(t, uuid.Nil, claim.ID, "a taken slot comes with its own ledger row's id")

	// A claim id that has no ledger row must be a clean no-op.
	stale := claim
	stale.ID = uuid.New()
	require.NoError(t, repo.CompensateReopen(ctx, sourceID, key, stale))
	row, err := repo.Get(ctx, sourceID, key)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, 1, row.ReopenCount, "a compensation for another claim must not touch the count")

	// Our claim: the slot comes back whole — the row is exactly as it was
	// before the reopen, anchor included.
	require.NoError(t, repo.CompensateReopen(ctx, sourceID, key, claim))
	row, err = repo.Get(ctx, sourceID, key)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Zero(t, row.ReopenCount)
	assert.Nil(t, row.LastReopenedAt, "the anchor returns to its pre-claim value, not the failed claim's instant")

	ok, _, err = repo.TryReopen(ctx, sourceID, key, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, ok, "the compensated slot must be usable again")
}

// A compensation must give back the whole slot — the window anchor with the
// count (codex-review P2, MR !1078, round 5). Restoring the count alone
// leaves the anchor at the failed attempt's instant: a failed attempt near
// the window's end stretches the window over reopens that never delivered
// and storm-limits a healthy repeat a day later. RED against a compensation
// that only decrements.
func TestClosedFollowUpRootRepo_CompensationRestoresTheWindowAnchor(t *testing.T) {
	repo, sourceID, key := newClosedFollowUpRootsTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err := repo.Claim(ctx, sourceID, key, uuid.New(), now)
	require.NoError(t, err)

	// Two delivered reopens: count 2, anchor now+1h.
	ok, _, err := repo.TryReopen(ctx, sourceID, key, now)
	require.NoError(t, err)
	require.True(t, ok)
	ok, _, err = repo.TryReopen(ctx, sourceID, key, now.Add(time.Hour))
	require.NoError(t, err)
	require.True(t, ok)

	// A claimed-but-failed attempt near the window's end (now+23h).
	ok, failedClaim, err := repo.TryReopen(ctx, sourceID, key, now.Add(23*time.Hour))
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, repo.CompensateReopen(ctx, sourceID, key, failedClaim))

	row, err := repo.Get(ctx, sourceID, key)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, 2, row.ReopenCount, "the failed attempt's slot is returned")
	require.NotNil(t, row.LastReopenedAt)
	assert.True(t, row.LastReopenedAt.Equal(now.Add(time.Hour)),
		"the anchor must return to the last DELIVERED reopen, not stay at the failed attempt")

	// A day after the last delivered reopen: a FRESH window, not slot 3 of the
	// stretched one.
	ok, _, err = repo.TryReopen(ctx, sourceID, key, now.Add(26*time.Hour))
	require.NoError(t, err)
	require.True(t, ok)
	row, err = repo.Get(ctx, sourceID, key)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, 1, row.ReopenCount,
		"a window stretched by a failed attempt must not exhaust the budget prematurely")
}

func TestClosedFollowUpRootRepo_ClaimGetDelete(t *testing.T) {
	repo, sourceID, key := newClosedFollowUpRootsTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	claimed, err := repo.Claim(ctx, sourceID, key, uuid.New(), now)
	require.NoError(t, err)
	assert.True(t, claimed, "the first claim of a fresh finding wins")

	row, err := repo.Get(ctx, sourceID, key)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, sourceID, row.SourceTaskID)
	assert.Equal(t, key, row.FindingKey)
	assert.Zero(t, row.ReopenCount, "a fresh claim carries the default 0, not a phantom reopen")
	assert.Nil(t, row.LastReopenedAt)

	// A different finding_key on the same source is a different identity — its
	// Get must miss cleanly (nil, nil), because that is the exact signal the
	// service treats as "no root yet".
	other, err := repo.Get(ctx, sourceID, "txt:never-claimed")
	require.NoError(t, err)
	assert.Nil(t, other, "a missing row must come back as (nil,nil), never as sql.ErrNoRows")

	// The second claim of the SAME finding loses, and the winner's root stays.
	winner := row.RootTaskID
	claimedAgain, err := repo.Claim(ctx, sourceID, key, uuid.New(), now)
	require.NoError(t, err)
	assert.False(t, claimedAgain, "a repeat claim must lose to the row that already exists")
	row2, err := repo.Get(ctx, sourceID, key)
	require.NoError(t, err)
	require.NotNil(t, row2)
	assert.Equal(t, winner, row2.RootTaskID, "the loser must be able to read the winner's root")

	// Delete — the create-failure compensation. After it the finding is
	// unclaimed again and can be claimed cleanly.
	require.NoError(t, repo.Delete(ctx, sourceID, key))
	gone, err := repo.Get(ctx, sourceID, key)
	require.NoError(t, err)
	assert.Nil(t, gone)
	require.NoError(t, repo.Delete(ctx, sourceID, key), "deleting a row that is already gone is success, not error")
	reclaimed, err := repo.Claim(ctx, sourceID, key, uuid.New(), now)
	require.NoError(t, err)
	assert.True(t, reclaimed, "after compensation the finding claims cleanly")
}

// The concurrency contract: N racers, one key, exactly one winner whose
// root_task_id survives. This is the SQL that makes "two concurrent remarks →
// one card" a database guarantee rather than a service-layer hope.
func TestClosedFollowUpRootRepo_ConcurrentClaimExactlyOneWinner(t *testing.T) {
	repo, sourceID, key := newClosedFollowUpRootsTest(t)
	ctx := context.Background()
	const racers = 16

	roots := make([]uuid.UUID, racers)
	var wg sync.WaitGroup
	wins := make(chan uuid.UUID, racers)
	for i := 0; i < racers; i++ {
		roots[i] = uuid.New()
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			claimed, err := repo.Claim(ctx, sourceID, key, roots[i], time.Now().UTC())
			if err != nil {
				t.Errorf("claim racer %d: %v", i, err)
				return
			}
			if claimed {
				wins <- roots[i]
			}
		}(i)
	}
	wg.Wait()
	close(wins)

	var winners []uuid.UUID
	for w := range wins {
		winners = append(winners, w)
	}
	require.Len(t, winners, 1, "exactly one racer wins the claim, got %d", len(winners))

	row, err := repo.Get(ctx, sourceID, key)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, winners[0], row.RootTaskID,
		"the surviving row must be the winner's — the losers deliver into it")
}

// The window CASE + limit WHERE in TryReopen: counts up inside 24h, refuses
// the limit+1-th increment, resets to 1 once the last reopen has SQL-aged
// past the window. The reset half can only be tested against real SQL — the
// mock mirrors this CASE+WHERE, and this test is what keeps that mirror from
// drifting.
func TestClosedFollowUpRootRepo_TryReopenWindowAndLimitSemantics(t *testing.T) {
	repo, sourceID, key := newClosedFollowUpRootsTest(t)
	ctx := context.Background()
	root := uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err := repo.Claim(ctx, sourceID, key, root, now)
	require.NoError(t, err)

	// Fresh row, last_reopened_at NULL: first TryReopen anchors the window at 1.
	ok, _, err := repo.TryReopen(ctx, sourceID, key, now)
	require.NoError(t, err)
	assert.True(t, ok, "the first reopen of a fresh window takes its slot")
	row, err := repo.Get(ctx, sourceID, key)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, 1, row.ReopenCount)
	require.NotNil(t, row.LastReopenedAt)

	// Inside the window: increments up to the limit.
	ok, _, err = repo.TryReopen(ctx, sourceID, key, now.Add(time.Hour))
	require.NoError(t, err)
	assert.True(t, ok)
	ok, _, err = repo.TryReopen(ctx, sourceID, key, now.Add(2*time.Hour))
	require.NoError(t, err)
	assert.True(t, ok)
	row, err = repo.Get(ctx, sourceID, key)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, repository.ClosedFollowUpReopenLimit, row.ReopenCount, "reopens inside 24h accumulate up to the limit")

	// The limit+1-th increment inside the window is REFUSED, in the same
	// statement — the count never exceeds the repeats actually delivered.
	ok, _, err = repo.TryReopen(ctx, sourceID, key, now.Add(3*time.Hour))
	require.NoError(t, err)
	assert.False(t, ok, "the limit+1-th reopen inside the window must not take a slot")
	row, err = repo.Get(ctx, sourceID, key)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, repository.ClosedFollowUpReopenLimit, row.ReopenCount, "a refused reopen does not count")

	// Age the anchor 25h back — the next reopen starts a fresh window at 1,
	// not a lifetime total. The aged timestamp is computed in Go and
	// bound as a parameter: `$1 - interval` in SQL types the parameter as
	// interval, the same inference trap TryReopen itself had to avoid.
	aged := now.Add(-25 * time.Hour)
	_, err = repo.db.ExecContext(ctx,
		"UPDATE closed_followup_roots SET last_reopened_at = $1 WHERE source_task_id = $2 AND finding_key = $3",
		aged, sourceID, key)
	require.NoError(t, err)
	ok, _, err = repo.TryReopen(ctx, sourceID, key, now)
	require.NoError(t, err)
	assert.True(t, ok, "after the window expires the finding can wake its root again")
	row, err = repo.Get(ctx, sourceID, key)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, 1, row.ReopenCount, "an expired window resets to 1 — the storm limit is not a lifetime lock")
	require.NotNil(t, row.LastReopenedAt)
	assert.True(t, row.LastReopenedAt.Equal(now),
		"the anchor moves to this reopen, re-arming the window")
}

// Two claims inside one timestamptz microsecond are still two DISTINCT
// claims (codex-review P1, MR !1078, round 9). A timestamp pin matched both,
// so the earlier claim's failed-reopen compensation restored its pre-claim
// state OVER the later claim's delivered slot — an undercount that lets the
// window hand out more reopens than the limit. The ledger keeps the erasure
// structurally impossible — compensation deletes only its OWN row — while the
// round-10 recompute additionally returns the displaced claim's slot: the
// surviving count is the DELIVERED reopens (B's one slot), never an
// undercount, never a phantom.
func TestClosedFollowUpRootRepo_SameInstantCompensationNeverErasesTheLaterSlot(t *testing.T) {
	repo, sourceID, key := newClosedFollowUpRootsTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err := repo.Claim(ctx, sourceID, key, uuid.New(), now)
	require.NoError(t, err)

	// Both claims pass the SAME instant — one timestamptz microsecond, the
	// collision the timestamp pin could not tell apart.
	okA, claimA, err := repo.TryReopen(ctx, sourceID, key, now)
	require.NoError(t, err)
	require.True(t, okA)
	okB, _, err := repo.TryReopen(ctx, sourceID, key, now)
	require.NoError(t, err)
	require.True(t, okB, "both claims take slots: count 2 inside the window")

	// B delivered; A's reopen failed afterwards. Giving A's slot back must
	// not reach under B's delivered slot — and with the ledger it ALSO comes
	// back instead of leaking into the window.
	require.NoError(t, repo.CompensateReopen(ctx, sourceID, key, claimA))

	row, err := repo.Get(ctx, sourceID, key)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, 1, row.ReopenCount,
		"B's delivered slot survives A's compensation, and A's failed slot is returned: the count is the DELIVERED reopens — never an undercount, never a phantom")
	require.NotNil(t, row.LastReopenedAt)
	assert.True(t, row.LastReopenedAt.Equal(now), "B's anchor survives A's compensation")
}

// A claim whose pin a LATER claim displaced must still return its slot when
// its reopen fails (codex-review P2, MR !1078, round 10). The single-column
// pin made that compensation a no-op — the failed attempt's slot stayed in
// reopen_count, and overlapping failures ate the storm budget on attempts
// rather than reopens: the exact defect compensation exists to prevent (round
// 4), reborn in the concurrent corner. Every claim is independently
// compensable: deleting OUR claim and recomputing from the survivors can
// never touch another claim's slot, yet always returns ours. RED against the
// single reopen_claim_id column.
func TestClosedFollowUpRootRepo_DisplacedClaimCompensationStillReturnsTheSlot(t *testing.T) {
	repo, sourceID, key := newClosedFollowUpRootsTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err := repo.Claim(ctx, sourceID, key, uuid.New(), now)
	require.NoError(t, err)

	// A claims, then B claims a second later — B's claim displaces A's pin.
	okA, claimA, err := repo.TryReopen(ctx, sourceID, key, now)
	require.NoError(t, err)
	require.True(t, okA)
	okB, claimB, err := repo.TryReopen(ctx, sourceID, key, now.Add(time.Second))
	require.NoError(t, err)
	require.True(t, okB, "both claims take slots: count 2 inside the window")

	// B delivers; A's move fails afterwards. A's slot must come back WITHOUT
	// touching B's.
	require.NoError(t, repo.CompensateReopen(ctx, sourceID, key, claimA))
	row, err := repo.Get(ctx, sourceID, key)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, 1, row.ReopenCount,
		"the displaced claim's slot is returned: the budget counts delivered reopens, not attempts")
	require.NotNil(t, row.LastReopenedAt)
	assert.True(t, row.LastReopenedAt.Equal(now.Add(time.Second)),
		"the anchor stays at B's delivered claim")

	// B's own compensation is still clean, and the returned slots are usable.
	require.NoError(t, repo.CompensateReopen(ctx, sourceID, key, claimB))
	row, err = repo.Get(ctx, sourceID, key)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Zero(t, row.ReopenCount)
	assert.Nil(t, row.LastReopenedAt)
	ok, _, err := repo.TryReopen(ctx, sourceID, key, now.Add(2*time.Second))
	require.NoError(t, err)
	assert.True(t, ok, "both returned slots must be reusable")
}

// The storm semaphore under concurrency (codex-review P1, MR !1078): N racers
// call TryReopen on one fresh row, and exactly ClosedFollowUpReopenLimit slots
// exist. A read-check-record split let all N pass the check and inflate the
// counter past the number of delivered repeats — this is the ratchet that
// makes that regression structurally impossible in SQL.
func TestClosedFollowUpRootRepo_ConcurrentTryReopenAtMostLimit(t *testing.T) {
	repo, sourceID, key := newClosedFollowUpRootsTest(t)
	ctx := context.Background()
	_, err := repo.Claim(ctx, sourceID, key, uuid.New(), time.Now().UTC())
	require.NoError(t, err)

	const racers = 16
	var wg sync.WaitGroup
	slots := make(chan struct{}, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _, reopenErr := repo.TryReopen(ctx, sourceID, key, time.Now().UTC())
			if reopenErr != nil {
				t.Errorf("try-reopen racer: %v", reopenErr)
				return
			}
			if ok {
				slots <- struct{}{}
			}
		}()
	}
	wg.Wait()
	close(slots)

	taken := 0
	for range slots {
		taken++
	}
	require.Equal(t, repository.ClosedFollowUpReopenLimit, taken,
		"exactly the limit worth of slots exists no matter how many racers contend")

	row, err := repo.Get(ctx, sourceID, key)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, repository.ClosedFollowUpReopenLimit, row.ReopenCount,
		"the stored count equals the slots taken — never above the delivered repeats")
}

// Round 11 (codex-review P1, MR !1078): a compensation that waits for the
// root lock must recompute from the ledger AS IT IS AFTER the lock was won.
// A single statement took its snapshot before the wait, so a claim committed
// by a racing TryReopen in the meantime was invisible and the UPDATE
// overwrote reopen_count/last_reopened_at without it. Deterministic: the
// test itself holds the root lock while compensation queues behind it.
func TestClosedFollowUpRootRepo_CompensationSeesClaimCommittedWhileWaitingForTheLock(t *testing.T) {
	repo, sourceID, key := newClosedFollowUpRootsTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err := repo.Claim(ctx, sourceID, key, uuid.New(), now)
	require.NoError(t, err)
	okA, claimA, err := repo.TryReopen(ctx, sourceID, key, now)
	require.NoError(t, err)
	require.True(t, okA)

	// A racing TryReopen holds the root lock...
	tx, err := repo.db.BeginTxx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx,
		`SELECT 1 FROM closed_followup_roots WHERE source_task_id = $1 AND finding_key = $2 FOR UPDATE`, sourceID, key)
	require.NoError(t, err)

	// ...A's compensation queues behind it...
	done := make(chan error, 1)
	go func() { done <- repo.CompensateReopen(ctx, sourceID, key, claimA) }()
	time.Sleep(500 * time.Millisecond)
	select {
	case early := <-done:
		t.Fatalf("compensation must wait for the root lock, returned early: %v", early)
	default:
	}

	// ...and B's claim commits while it waits.
	claimedB := time.Now().UTC().Truncate(time.Microsecond)
	_, err = tx.ExecContext(ctx,
		`INSERT INTO closed_followup_reopen_claims (claim_id, source_task_id, finding_key, claimed_at) VALUES ($1, $2, $3, $4)`,
		uuid.New(), sourceID, key, claimedB)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx,
		`UPDATE closed_followup_roots SET reopen_count = 2, last_reopened_at = $3 WHERE source_task_id = $1 AND finding_key = $2`,
		sourceID, key, claimedB)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	require.NoError(t, <-done)

	row, err := repo.Get(ctx, sourceID, key)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, 1, row.ReopenCount, "B's concurrently committed claim must survive A's compensation")
	require.NotNil(t, row.LastReopenedAt)
	assert.True(t, row.LastReopenedAt.Equal(claimedB), "the anchor is B's claim, not erased")
}
