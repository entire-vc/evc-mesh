package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// MemoryDailyRollupRepo writes the daily memory aggregates of the widget
// "Память Mesh" (U4, migration 20261120400): memory_stats_daily from the read
// journal through memory_stats_rollup, and the end-of-day memory_base_daily
// snapshot. memory_daily_rollups marks the written days. Every write runs in
// one transaction under memoryDailyRollupLock, so two API replicas never
// interleave on a day.
type MemoryDailyRollupRepo struct {
	db *sqlx.DB
}

// NewMemoryDailyRollupRepo creates the repo.
func NewMemoryDailyRollupRepo(db *sqlx.DB) *MemoryDailyRollupRepo {
	return &MemoryDailyRollupRepo{db: db}
}

// memoryDailyRollupLock is the pg_advisory_xact_lock key of the daily writes.
const memoryDailyRollupLock int64 = 0x4d454d5f524f4c4c // "MEM_ROLL"

// memoryStatsColumns are the stored columns of memory_stats_daily, in the
// order memory_stats_rollup returns them (served_noise is generated).
const memoryStatsColumns = `day, workspace_id, project_id, agent_id, caller_kind, op, source,
	reads, errors, hits, useful, misses, misses_reason, empty_repeats,
	served_rows, served_superseded, response_chars, top_score_hist`

// UTCDay is the start of t's UTC day.
func UTCDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// WriteStatsDay replaces the memory_stats_daily rows of the UTC day of day with
// memory_stats_rollup over that day and marks the day written. The journal of
// a closed day does not change once its recall usage is final, so a repeated
// write leaves the same rows. Returns the number of rows written.
func (r *MemoryDailyRollupRepo) WriteStatsDay(ctx context.Context, day time.Time) (int64, error) {
	start := UTCDay(day)
	end := start.AddDate(0, 0, 1)
	var n int64
	err := r.inLockedTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM memory_stats_daily WHERE day = $1::date`, start); err != nil {
			return fmt.Errorf("clear stats day: %w", err)
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO memory_stats_daily (`+memoryStatsColumns+`)
			SELECT `+memoryStatsColumns+` FROM memory_stats_rollup($1, $2)`, start, end)
		if err != nil {
			return fmt.Errorf("roll up stats day: %w", err)
		}
		n, _ = res.RowsAffected()
		_, err = tx.ExecContext(ctx, `INSERT INTO memory_daily_rollups (day, stats_written_at) VALUES ($1::date, now())
			ON CONFLICT (day) DO UPDATE SET stats_written_at = now()`, start)
		return err
	})
	return n, err
}

// memoryBaseDupCTE is the dup CTE of the snapshot over its m CTE: exact
// content_simhash duplicates among non-archived rows of a workspace. Shared
// with RecountSimhashAggregates so the recount after a hash recompute is the same
// formula.
const memoryBaseDupCTE = `dup AS (
    SELECT m.id,
           count(*) OVER (PARTITION BY m.workspace_id, m.content_simhash) AS n,
           row_number() OVER (PARTITION BY m.workspace_id, m.content_simhash
                              ORDER BY m.created_at NULLS FIRST, m.id) AS rn
    FROM m
    WHERE NOT m.is_archived AND m.content_simhash IS NOT NULL
)`

// memoryBaseSnapshotSQL builds the memory_base_daily rows of day $1 from the
// current memories (created before the end of the day) and the read journal.
// $2 is when the journal starts (NULL: no journal); a window (day end - N d,
// day end] that begins before it yields NULL deadN/eligibleN with the reason
// no_journal_before. See the migration for each column.
const memoryBaseSnapshotSQL = `
WITH b AS (
    SELECT ($1::date + 1)::timestamp AT TIME ZONE 'UTC' AS day_end
),
cov AS (
    SELECT b.day_end,
           ($2::timestamptz IS NOT NULL AND $2::timestamptz <= b.day_end - interval '30 days') AS has30,
           ($2::timestamptz IS NOT NULL AND $2::timestamptz <= b.day_end - interval '90 days') AS has90
    FROM b
),
m AS (
    SELECT m.id, m.workspace_id, m.project_id, m.created_at, m.key, m.tags, m.content_simhash, m.status,
           (m.archived OR m.status = 'archived') AS is_archived
    FROM memories m, b
    WHERE COALESCE(m.created_at, '-infinity') < b.day_end
),
served AS (
    SELECT DISTINCT (r->>'id')::uuid AS id,
           e.ts > cov.day_end - interval '30 days' AS in30
    FROM cov
    JOIN memory_read_events e
      ON e.ts > cov.day_end - interval '90 days' AND e.ts <= cov.day_end
    CROSS JOIN LATERAL jsonb_array_elements(e.results) AS r
    WHERE e.caller_kind = 'agent_tool'
      AND (r->>'id') ~ '^[0-9a-fA-F-]{36}$'
),
served30 AS (SELECT DISTINCT id FROM served WHERE in30),
served90 AS (SELECT DISTINCT id FROM served),
` + memoryBaseDupCTE + `,
x AS (
    SELECT m.workspace_id, m.project_id,
           m.is_archived,
           NOT m.is_archived AND m.status <> 'superseded' AS live,
           m.status,
           NOT m.is_archived AND NOT EXISTS (SELECT 1 FROM unnest(m.tags) t WHERE t LIKE 'kind:%') AS no_kind,
           NOT m.is_archived AND ('kind:session-checkpoint' = ANY(COALESCE(m.tags, '{}'))
                                  OR m.key ~ '^(handoff|checkpoint|status|progress)-') AS statuslog,
           COALESCE(d.n >= 2, false) AS dup_row,
           COALESCE(d.n >= 2 AND d.rn = 1, false) AS dup_first,
           COALESCE(m.created_at, '-infinity') < cov.day_end - interval '30 days' AS old30,
           COALESCE(m.created_at, '-infinity') < cov.day_end - interval '90 days' AS old90,
           s30.id IS NOT NULL AS seen30,
           s90.id IS NOT NULL AS seen90
    FROM m
    CROSS JOIN cov
    LEFT JOIN dup d ON d.id = m.id
    LEFT JOIN served30 s30 ON s30.id = m.id
    LEFT JOIN served90 s90 ON s90.id = m.id
)
SELECT $1::date AS day, x.workspace_id, x.project_id,
       count(*) AS total,
       count(*) FILTER (WHERE NOT x.is_archived) AS active,
       count(*) FILTER (WHERE x.is_archived) AS archived,
       count(*) FILTER (WHERE NOT x.is_archived AND x.status = 'superseded') AS superseded_active,
       count(*) FILTER (WHERE NOT x.is_archived AND x.status = 'conflicted') AS conflicted,
       count(*) FILTER (WHERE x.no_kind) AS no_kind,
       count(*) FILTER (WHERE x.statuslog) AS statuslog_like,
       count(*) FILTER (WHERE x.dup_first) AS dup_groups,
       count(*) FILTER (WHERE x.dup_row) AS dup_rows,
       CASE WHEN bool_and(cov.has30) THEN count(*) FILTER (WHERE x.live AND x.old30 AND NOT x.seen30) END AS dead30,
       CASE WHEN bool_and(cov.has90) THEN count(*) FILTER (WHERE x.live AND x.old90 AND NOT x.seen90) END AS dead90,
       CASE WHEN bool_and(cov.has30) THEN count(*) FILTER (WHERE x.live AND x.old30) END AS eligible30,
       CASE WHEN bool_and(cov.has90) THEN count(*) FILTER (WHERE x.live AND x.old90) END AS eligible90,
       CASE WHEN bool_and(cov.has30) THEN NULL ELSE 'no_journal_before' END AS dead30_reason,
       CASE WHEN bool_and(cov.has90) THEN NULL ELSE 'no_journal_before' END AS dead90_reason
FROM x CROSS JOIN cov
GROUP BY x.workspace_id, x.project_id`

// WriteBaseDay takes the memory_base_daily snapshot of the UTC day of day once:
// a day already marked is left as it is (the snapshot reads current memories
// and cannot be recomputed later). journalSince is when the read journal
// starts, nil when there is none. Returns rows written and whether the day was
// written by this call.
func (r *MemoryDailyRollupRepo) WriteBaseDay(ctx context.Context, day time.Time, journalSince *time.Time) (n int64, wrote bool, err error) {
	start := UTCDay(day)
	err = r.inLockedTx(ctx, func(tx *sqlx.Tx) error {
		var done bool
		if gerr := tx.GetContext(ctx, &done, `SELECT EXISTS (SELECT 1 FROM memory_daily_rollups
			WHERE day = $1::date AND base_written_at IS NOT NULL)`, start); gerr != nil {
			return fmt.Errorf("base day marker: %w", gerr)
		}
		if done {
			return nil
		}
		var since any
		if journalSince != nil {
			since = journalSince.UTC()
		}
		res, ierr := tx.ExecContext(ctx, `INSERT INTO memory_base_daily (day, workspace_id, project_id,
			total, active, archived, superseded_active, conflicted, no_kind, statuslog_like, dup_groups, dup_rows,
			dead30, dead90, eligible30, eligible90, dead30_reason, dead90_reason) `+memoryBaseSnapshotSQL+`
			ON CONFLICT DO NOTHING`, start, since)
		if ierr != nil {
			return fmt.Errorf("snapshot base day: %w", ierr)
		}
		n, _ = res.RowsAffected()
		wrote = true
		_, merr := tx.ExecContext(ctx, `INSERT INTO memory_daily_rollups (day, base_written_at) VALUES ($1::date, now())
			ON CONFLICT (day) DO UPDATE SET base_written_at = now()`, start)
		return merr
	})
	return n, wrote, err
}

// RecountSimhashAggregates brings the stored daily aggregates derived from
// content_simhash onto the new hashes once, after the recompute of algorithm
// has finished; the pre-fix hashes merged unrelated rows.
//
//   - memory_base_daily dup_groups and dup_rows of every stored day are
//     recounted with the snapshot's own dup formula over the rows created
//     before the day's end; archive state is the current one (a past day's own
//     state is not kept). The other columns stay as snapshotted.
//   - memory_stats_daily misses (U7 "rewritten": a new memory within 3 bits of
//     a missed one) of the written days from statsFrom whose raw journal is
//     complete get their written marker cleared, so the nightly job writes them
//     again from the journal, as on the U7 rollout. Days with misses not
//     collected, before statsFrom or with a purged journal are left as written.
//
// Marks memory_simhash_backfill.aggregates_at, so it runs once; before the
// recompute has finished it does nothing. Returns the days recounted and the
// stats days queued for a rewrite.
func (r *MemoryDailyRollupRepo) RecountSimhashAggregates(ctx context.Context, algorithm string, statsFrom time.Time) (baseDays int, statsDays int64, err error) {
	err = r.inLockedTx(ctx, func(tx *sqlx.Tx) error {
		var ready bool
		if gerr := tx.GetContext(ctx, &ready, `SELECT EXISTS (SELECT 1 FROM memory_simhash_backfill
			WHERE algorithm = $1 AND finished_at IS NOT NULL AND aggregates_at IS NULL)`, algorithm); gerr != nil {
			return fmt.Errorf("simhash backfill state: %w", gerr)
		}
		if !ready {
			return nil
		}
		var list []time.Time
		if serr := tx.SelectContext(ctx, &list, `SELECT DISTINCT day FROM memory_base_daily ORDER BY day`); serr != nil {
			return fmt.Errorf("base days: %w", serr)
		}
		for _, day := range list {
			if _, uerr := tx.ExecContext(ctx, `
WITH b AS (
    SELECT ($1::date + 1)::timestamp AT TIME ZONE 'UTC' AS day_end
),
m AS (
    SELECT m.id, m.workspace_id, m.project_id, m.created_at, m.content_simhash,
           (m.archived OR m.status = 'archived') AS is_archived
    FROM memories m, b
    WHERE COALESCE(m.created_at, '-infinity') < b.day_end
),
`+memoryBaseDupCTE+`,
agg AS (
    SELECT m.workspace_id, m.project_id,
           count(*) FILTER (WHERE d.n >= 2 AND d.rn = 1) AS groups,
           count(*) FILTER (WHERE d.n >= 2) AS rows
    FROM m JOIN dup d ON d.id = m.id
    GROUP BY m.workspace_id, m.project_id
)
UPDATE memory_base_daily t
SET dup_groups = COALESCE(a.groups, 0),
    dup_rows   = COALESCE(a.rows, 0)
FROM memory_base_daily k
LEFT JOIN agg a ON a.workspace_id = k.workspace_id AND a.project_id IS NOT DISTINCT FROM k.project_id
WHERE t.day = $1::date AND k.day = t.day
  AND k.workspace_id = t.workspace_id AND k.project_id IS NOT DISTINCT FROM t.project_id`, day); uerr != nil {
				return fmt.Errorf("recount dup of %s: %w", day.Format("2006-01-02"), uerr)
			}
			baseDays++
		}
		res, qerr := tx.ExecContext(ctx, `
UPDATE memory_daily_rollups d
SET stats_written_at = NULL
WHERE d.stats_written_at IS NOT NULL
  AND d.day >= $1::date
  AND EXISTS (SELECT 1 FROM memory_stats_daily s WHERE s.day = d.day AND s.misses_reason IS NULL)
  AND d.day::timestamp AT TIME ZONE 'UTC' >= (SELECT min(e.ts) FROM memory_read_events e)`, UTCDay(statsFrom))
		if qerr != nil {
			return fmt.Errorf("queue stats days: %w", qerr)
		}
		statsDays, _ = res.RowsAffected()
		_, merr := tx.ExecContext(ctx, `UPDATE memory_simhash_backfill SET aggregates_at = now() WHERE algorithm = $1`, algorithm)
		return merr
	})
	return baseDays, statsDays, err
}

// JournalSince is when the read journal starts: the earliest aggregated day or
// raw read, whichever is older (the raw journal alone moves with its 90-day
// purge). Nil when nothing was ever journaled. Same rule as the U5 reader.
func (r *MemoryDailyRollupRepo) JournalSince(ctx context.Context) (*time.Time, error) {
	var since sql.NullTime
	if err := r.db.GetContext(ctx, &since, `SELECT LEAST(
		(SELECT min(day)::timestamp AT TIME ZONE 'UTC' FROM memory_stats_daily),
		(SELECT min(ts) FROM memory_read_events))`); err != nil {
		return nil, fmt.Errorf("memory journal start: %w", err)
	}
	if !since.Valid {
		return nil, nil
	}
	t := since.Time.UTC()
	return &t, nil
}

// StatsWrittenDays returns the days in [from, to) whose stats are written.
func (r *MemoryDailyRollupRepo) StatsWrittenDays(ctx context.Context, from, to time.Time) (map[time.Time]bool, error) {
	var days []time.Time
	if err := r.db.SelectContext(ctx, &days, `SELECT day::timestamp AT TIME ZONE 'UTC' FROM memory_daily_rollups
		WHERE day >= $1::date AND day < $2::date AND stats_written_at IS NOT NULL`, UTCDay(from), UTCDay(to)); err != nil {
		return nil, fmt.Errorf("written stats days: %w", err)
	}
	out := make(map[time.Time]bool, len(days))
	for _, d := range days {
		out[UTCDay(d)] = true
	}
	return out, nil
}

// PendingUsageReads counts the recall reads whose usage attribution (U3) is
// not final yet among those of the UTC day of day and of the
// domain.MemoryMissWindow after it: until they are, the day's useful count can
// still grow, and so can its misses (a later recall useful by a memory the
// day's read did not return, U7).
func (r *MemoryDailyRollupRepo) PendingUsageReads(ctx context.Context, day time.Time) (int64, error) {
	start := UTCDay(day)
	var n int64
	err := r.db.GetContext(ctx, &n, `SELECT count(*) FROM memory_read_events
		WHERE usage_final_at IS NULL AND op IN ('recall', 'recall-final', 'recall_graph')
		  AND ts >= $1 AND ts < $2`, start, start.AddDate(0, 0, 1).Add(domain.MemoryMissWindow))
	if err != nil {
		return 0, fmt.Errorf("pending usage reads: %w", err)
	}
	return n, nil
}

func (r *MemoryDailyRollupRepo) inLockedTx(ctx context.Context, fn func(tx *sqlx.Tx) error) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, memoryDailyRollupLock); err != nil {
		return fmt.Errorf("daily rollup lock: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
