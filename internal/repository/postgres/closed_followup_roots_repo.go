package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// ClosedFollowUpRootRepo implements repository.ClosedFollowUpRootRepository
// with PostgreSQL (#5194afd4).
type ClosedFollowUpRootRepo struct {
	db *sqlx.DB
}

// NewClosedFollowUpRootRepo creates a new ClosedFollowUpRootRepo.
func NewClosedFollowUpRootRepo(db *sqlx.DB) *ClosedFollowUpRootRepo {
	return &ClosedFollowUpRootRepo{db: db}
}

const closedFollowUpRootSelectCols = `source_task_id, finding_key, root_task_id, created_at, reopen_count, last_reopened_at`

// Claim atomically records rootTaskID as THE root for the finding. ON
// CONFLICT DO NOTHING against the PK is the atomicity the contract demands:
// of N racing claims exactly one inserts, and RowsAffected tells this caller
// whether it was the one. No SELECT-then-INSERT anywhere — that is the
// check-then-act race this table exists to make impossible.
func (r *ClosedFollowUpRootRepo) Claim(ctx context.Context, sourceTaskID uuid.UUID, findingKey string, rootTaskID uuid.UUID, now time.Time) (bool, error) {
	const q = `
		INSERT INTO closed_followup_roots (source_task_id, finding_key, root_task_id, created_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (source_task_id, finding_key) DO NOTHING
	`
	res, err := r.db.ExecContext(ctx, q, sourceTaskID, findingKey, rootTaskID, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// Get returns the root row for the finding, or (nil, nil) when there is none.
func (r *ClosedFollowUpRootRepo) Get(ctx context.Context, sourceTaskID uuid.UUID, findingKey string) (*domain.ClosedFollowUpRoot, error) {
	const q = `SELECT ` + closedFollowUpRootSelectCols + ` FROM closed_followup_roots WHERE source_task_id = $1 AND finding_key = $2`
	var root domain.ClosedFollowUpRoot
	if err := r.db.GetContext(ctx, &root, q, sourceTaskID, findingKey); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &root, nil
}

// TryReopen atomically consumes one of the ClosedFollowUpReopenLimit reopen
// slots inside the window anchored at last_reopened_at. The CASE is the
// window's memory — a previous reopen older than the window starts a fresh
// window at 1 instead of growing a lifetime total — and the WHERE clause is
// the semaphore: it recomputes the same next count and refuses the
// limit+1-th increment, so N racing repeats of one finding can take at most
// limit slots between them and the stored count can never exceed the repeats
// actually delivered. A read-check-record split in the service let every
// racer pass the check and inflate the counter (codex-review P1, MR !1078).
//
// The prev CTE locks the row FOR UPDATE inside the same statement: the count
// the WHERE checks and the count the UPDATE writes are one value, not two
// reads with a race window in between.
//
// Taking a slot also INSERTs the claim's row into the reopen-claim ledger
// (the ins CTE, gated on upd — a refused reopen leaves no ledger row). The
// ledger is what makes every claim independently compensable: a single pin
// column on the root row held only the latest claim's token, so a claim
// displaced by a later one could never give its slot back (codex-review P2,
// MR !1078, round 10). The claim_id is a fresh uuid per claim — never a
// timestamp: two claims inside one timestamptz microsecond are still two
// claims (round 9).
//
// The window threshold is computed in Go and passed as its own parameter, not
// as `$3 - interval '24 hours'` in SQL: with an untyped parameter Postgres
// resolves `$3 - interval` by typing the parameter AS interval, and the
// statement dies with "operator does not exist: timestamp with time zone <
// interval" — found by the integration test, invisible to the service mock.
func (r *ClosedFollowUpRootRepo) TryReopen(ctx context.Context, sourceTaskID uuid.UUID, findingKey string, now time.Time) (bool, repository.ReopenClaim, error) {
	windowStart := now.Add(-repository.ClosedFollowUpReopenWindow)
	claimID := uuid.New()
	const q = `
		WITH prev AS (
			SELECT reopen_count, last_reopened_at
			FROM closed_followup_roots
			WHERE source_task_id = $1 AND finding_key = $2
			FOR UPDATE
		), upd AS (
			UPDATE closed_followup_roots r
			SET reopen_count = CASE
					WHEN p.last_reopened_at IS NULL OR p.last_reopened_at < $4
					    THEN 1
					ELSE r.reopen_count + 1
				    END,
				    last_reopened_at = $3
			FROM prev p
			WHERE (CASE
				          WHEN p.last_reopened_at IS NULL OR p.last_reopened_at < $4
				              THEN 1
				          ELSE p.reopen_count + 1
				      END) <= $6
			RETURNING r.source_task_id
		), ins AS (
			INSERT INTO closed_followup_reopen_claims (claim_id, source_task_id, finding_key, claimed_at)
			-- Bare parameters in a SELECT list have no context to infer types
			-- from — the casts are load-bearing (42P18 otherwise).
			SELECT $5::uuid, $1::uuid, $2::text, $3::timestamptz
			WHERE EXISTS (SELECT 1 FROM upd)
		)
		SELECT 1 FROM upd
	`
	var taken int
	err := r.db.QueryRowxContext(ctx, q, sourceTaskID, findingKey, now, windowStart, claimID, repository.ClosedFollowUpReopenLimit).Scan(&taken)
	if errors.Is(err, sql.ErrNoRows) {
		return false, repository.ReopenClaim{}, nil
	}
	if err != nil {
		return false, repository.ReopenClaim{}, err
	}
	return true, repository.ReopenClaim{ID: claimID}, nil
}

// CompensateReopen gives back a reopen slot consumed by a reopen that never
// delivered (codex-review P2, MR !1078): TryReopen takes the slot atomically
// BEFORE the steps that can fail, and slots burned by failures let the window
// exhaust on attempts rather than reopens — three failed reopens would
// storm-limit the fourth, healthy one. The budget counts DELIVERED reopens,
// never attempts.
//
// The claim's ledger row is deleted first, then reopen_count and
// last_reopened_at are recomputed from the claims that SURVIVE inside the
// window — count and anchor both (round 5: leaving the anchor at the failed
// attempt's instant stretches the window over reopens that never delivered).
// Recomputing from the ledger, instead of restoring a remembered pre-claim
// state, is what keeps every claim independently compensable (round 10: a
// single pin column meant a displaced claim's compensation was a no-op and
// its slot leaked into the window) AND what keeps the round-9 guarantee —
// deleting OUR row can never free a slot that was not ours, not even for a
// claim inside the same timestamptz microsecond.
//
// Snapshot trap, deliberately (round 11): a single statement — even with the
// root locked by a FOR UPDATE CTE — takes its snapshot BEFORE the lock wait,
// so the recompute cannot see a claim a racing TryReopen committed while we
// waited, and the UPDATE would overwrite reopen_count/last_reopened_at
// without it. So this is a transaction of separate statements: lock the root
// first, then delete our claim and recompute in statements whose snapshot is
// taken after the lock was won (READ COMMITTED takes a fresh snapshot per
// statement). A claim id that does not exist, or one belonging to a different
// (source, finding) than the caller names (double compensation, or the root's
// Delete already took it) deletes nothing and is a clean no-op.
//
// The window threshold is computed in Go and bound as a parameter for the
// same interval-inference reason as in TryReopen.
func (r *ClosedFollowUpRootRepo) CompensateReopen(ctx context.Context, sourceTaskID uuid.UUID, findingKey string, claim repository.ReopenClaim) (err error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	const lock = `SELECT 1 FROM closed_followup_roots WHERE source_task_id = $1 AND finding_key = $2 FOR UPDATE`
	var one int
	if err = tx.QueryRowxContext(ctx, lock, sourceTaskID, findingKey).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = nil // the root is gone (Delete took it): nothing to give back
			return tx.Commit()
		}
		return err
	}

	const del = `DELETE FROM closed_followup_reopen_claims WHERE claim_id = $3 AND source_task_id = $1 AND finding_key = $2`
	res, err := tx.ExecContext(ctx, del, sourceTaskID, findingKey, claim.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return tx.Commit() // already compensated / never existed
	}

	windowStart := time.Now().UTC().Add(-repository.ClosedFollowUpReopenWindow)
	const recompute = `
		UPDATE closed_followup_roots r
		SET reopen_count = (
				SELECT count(*) FROM closed_followup_reopen_claims c
				WHERE c.source_task_id = r.source_task_id AND c.finding_key = r.finding_key
				  AND c.claimed_at >= $3),
			last_reopened_at = (
				SELECT max(c.claimed_at) FROM closed_followup_reopen_claims c
				WHERE c.source_task_id = r.source_task_id AND c.finding_key = r.finding_key
				  AND c.claimed_at >= $3)
		WHERE r.source_task_id = $1 AND r.finding_key = $2
	`
	if _, err = tx.ExecContext(ctx, recompute, sourceTaskID, findingKey, windowStart); err != nil {
		return err
	}
	return tx.Commit()
}

// Delete removes the claim — the compensation for a claim whose card creation
// failed, so the row cannot point at a card that was never created. The
// root's ledger rows die with it: they are the root's reopen history, and a
// later re-claim of the same finding starts with a fresh window.
func (r *ClosedFollowUpRootRepo) Delete(ctx context.Context, sourceTaskID uuid.UUID, findingKey string) error {
	// Two statements, not one multi-statement Exec: parameters force the
	// extended protocol, which rejects multiple commands in one prepared
	// statement.
	const claims = `DELETE FROM closed_followup_reopen_claims WHERE source_task_id = $1 AND finding_key = $2`
	if _, err := r.db.ExecContext(ctx, claims, sourceTaskID, findingKey); err != nil {
		return err
	}
	const root = `DELETE FROM closed_followup_roots WHERE source_task_id = $1 AND finding_key = $2`
	_, err := r.db.ExecContext(ctx, root, sourceTaskID, findingKey)
	return err
}
