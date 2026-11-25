package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// memorySimhashBackfillLock is the pg advisory lock key of the content_simhash
// recompute, so two API replicas never run it at once.
const memorySimhashBackfillLock int64 = 0x4d454d5f53494d48 // "MEM_SIMH"

// MemorySimhashBackfillResult reports one BackfillContentSimhash call.
type MemorySimhashBackfillResult struct {
	// AlreadyDone: memory_simhash_backfill holds a finished row for the
	// algorithm; nothing was scanned.
	AlreadyDone bool
	// Locked: another session holds the lock; nothing was scanned.
	Locked  bool
	Scanned int64
	Updated int64
	// Skipped: the content changed between the read and the update. The
	// writer of that content stored its own hash (Upsert computes it).
	Skipped int64
}

// BackfillContentSimhash recomputes memories.content_simhash with compute for
// every row whose stored value differs, in id order and batches of batchSize,
// and records the run in memory_simhash_backfill under algorithm.
//
//   - Once per algorithm: a finished row for algorithm makes the call a no-op.
//   - Resumable and idempotent: a killed run leaves an unfinished row and the
//     next call starts over; rows already holding the computed value are not
//     written again.
//   - Concurrent writes: a row is updated only if its content is still the one
//     the hash was computed from; updated_at and version are not touched.
//
// The pre-fix values are kept in memory_simhash_v1_anchor (migration
// 20261121240) as the rollback anchor.
func (r *MemoryRepo) BackfillContentSimhash(ctx context.Context, algorithm string, compute func(string) int64, batchSize int) (MemorySimhashBackfillResult, error) {
	var res MemorySimhashBackfillResult
	if batchSize <= 0 {
		batchSize = 500
	}
	conn, err := r.db.Connx(ctx)
	if err != nil {
		return res, fmt.Errorf("simhash backfill: conn: %w", err)
	}
	defer conn.Close()

	var got bool
	if err := conn.GetContext(ctx, &got, `SELECT pg_try_advisory_lock($1)`, memorySimhashBackfillLock); err != nil {
		return res, fmt.Errorf("simhash backfill: lock: %w", err)
	}
	if !got {
		res.Locked = true
		return res, nil
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, memorySimhashBackfillLock)
	}()

	var finished sql.NullTime
	serr := conn.GetContext(ctx, &finished, `SELECT finished_at FROM memory_simhash_backfill WHERE algorithm = $1`, algorithm)
	switch {
	case errors.Is(serr, sql.ErrNoRows):
	case serr != nil:
		return res, fmt.Errorf("simhash backfill: read state: %w", serr)
	case finished.Valid:
		res.AlreadyDone = true
		return res, nil
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO memory_simhash_backfill (algorithm) VALUES ($1)
		ON CONFLICT (algorithm) DO UPDATE
		   SET started_at = NOW(), rows_scanned = 0, rows_updated = 0, rows_skipped = 0`, algorithm); err != nil {
		return res, fmt.Errorf("simhash backfill: start: %w", err)
	}

	cursor := uuid.Nil
	for {
		var rows []simhashRow
		if err := conn.SelectContext(ctx, &rows, `
			SELECT id, content, content_simhash FROM memories
			WHERE id > $1 ORDER BY id LIMIT $2`, cursor, batchSize); err != nil {
			return res, fmt.Errorf("simhash backfill: read batch: %w", err)
		}
		if len(rows) == 0 {
			break
		}
		cursor = rows[len(rows)-1].ID
		res.Scanned += int64(len(rows))

		if err := r.backfillSimhashBatch(ctx, conn, staleSimhashes(rows, compute), &res); err != nil {
			return res, err
		}
		if _, err := conn.ExecContext(ctx, `
			UPDATE memory_simhash_backfill
			SET rows_scanned = $2, rows_updated = $3, rows_skipped = $4
			WHERE algorithm = $1`, algorithm, res.Scanned, res.Updated, res.Skipped); err != nil {
			return res, fmt.Errorf("simhash backfill: progress: %w", err)
		}
	}

	if _, err := conn.ExecContext(ctx, `
		UPDATE memory_simhash_backfill
		SET finished_at = NOW(), rows_scanned = $2, rows_updated = $3, rows_skipped = $4
		WHERE algorithm = $1`, algorithm, res.Scanned, res.Updated, res.Skipped); err != nil {
		return res, fmt.Errorf("simhash backfill: finish: %w", err)
	}
	return res, nil
}

type simhashRow struct {
	ID      uuid.UUID     `db:"id"`
	Content string        `db:"content"`
	Simhash sql.NullInt64 `db:"content_simhash"`
}

type simhashUpdate struct {
	id      uuid.UUID
	content string
	hash    int64
}

// staleSimhashes returns the rows whose stored hash differs from compute's.
func staleSimhashes(rows []simhashRow, compute func(string) int64) []simhashUpdate {
	var out []simhashUpdate
	for _, r := range rows {
		h := compute(r.Content)
		if r.Simhash.Valid && r.Simhash.Int64 == h {
			continue
		}
		out = append(out, simhashUpdate{id: r.ID, content: r.Content, hash: h})
	}
	return out
}

func (r *MemoryRepo) backfillSimhashBatch(ctx context.Context, conn *sqlx.Conn, ups []simhashUpdate, res *MemorySimhashBackfillResult) error {
	if len(ups) == 0 {
		return nil
	}
	tx, err := conn.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("simhash backfill: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, u := range ups {
		out, err := tx.ExecContext(ctx, `
			UPDATE memories SET content_simhash = $2
			WHERE id = $1 AND content = $3`, u.id, u.hash, u.content)
		if err != nil {
			return fmt.Errorf("simhash backfill: update %s: %w", u.id, err)
		}
		if n, _ := out.RowsAffected(); n == 1 {
			res.Updated++
		} else {
			res.Skipped++
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("simhash backfill: commit: %w", err)
	}
	return nil
}
