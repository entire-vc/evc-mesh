package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// memoryLinkerRepairLock is the pg advisory (transaction) lock key of the
// linker supersede repair, so two API replicas never run it at once.
const memoryLinkerRepairLock int64 = 0x4d454d5f4c4e4b52 // "MEM_LNKR"

// LinkerSimhashRepairResult reports one RepairLinkerSimhashSupersedes call.
type LinkerSimhashRepairResult struct {
	// AlreadyDone: memory_linker_simhash_repair_run holds the algorithm.
	AlreadyDone bool
	// NotReady: the simhash recompute for the algorithm has not finished, so
	// the stored hashes may still be the old ones; nothing was examined.
	NotReady bool
	Restored int64
	// Repointed: rows that could not come back (their key is current
	// elsewhere) now point at the current row of their key instead.
	Repointed int64
}

// linkerWouldSupersedeSQL is the linker's decision (reconciler decide and
// computeConfidence) over o (older, superseded) and n (newer) on the stored
// hashes: importance below 0.8 and at least three of the four signals — keys
// share their first two segments (+0.3), tags overlap by half or more (+0.2),
// simhash within $1 bits (+0.3), newer by over an hour (+0.2). Every
// combination of three or four signals sums to 0.70 or more, every
// combination of two to 0.60 at most (TestComputeConfidence_ThreeOfFourSignals
// pins it), so the count is the threshold.
const linkerWouldSupersedeSQL = `(
    o.importance_score < 0.8
AND (  (array_length(string_to_array(replace(o.key, '/', ':'), ':'), 1) >= 2
        AND array_length(string_to_array(replace(n.key, '/', ':'), ':'), 1) >= 2
        AND (string_to_array(replace(o.key, '/', ':'), ':'))[1:2]
          = (string_to_array(replace(n.key, '/', ':'), ':'))[1:2])::int
     + (COALESCE(cardinality(o.tags), 0) > 0
        AND COALESCE(cardinality(n.tags), 0) > 0
        AND (SELECT count(*) FROM unnest(n.tags) t WHERE t = ANY (o.tags))::float8
            / LEAST(cardinality(o.tags), cardinality(n.tags)) >= 0.5)::int
     + COALESCE(bit_count((o.content_simhash # n.content_simhash)::bit(64)) <= $1, false)::int
     + (n.created_at > o.created_at + INTERVAL '1 hour')::int
    ) >= 3)`

// RepairLinkerSimhashSupersedes returns to review_needed the memories the
// linker superseded on the pre-fix simhash and would not supersede now
// It runs once per algorithm, after BackfillContentSimhash has
// finished for it, in one transaction:
//
//   - Candidates: status superseded, not archived (archiving is an explicit
//     act), superseded by a row of the same workspace under a different key,
//     with different text (whitespace-collapsed; the review triage supersedes
//     equal texts on purpose), where the pre-fix hashes of the pair
//     (memory_simhash_v1_anchor) were 1 to maxBits apart, so the linker
//     granted its hash signal, and linkerWouldSupersedeSQL is false on the
//     new hashes. A pair whose pre-fix hashes were over maxBits apart was
//     superseded without the hash signal and stays. A pair with equal pre-fix
//     hashes is the review triage's hash collision, repaired by migration
//     20261121240, which leaves only rows whose key is taken; a
//     row without an anchor was written after the fix. Both current texts
//     must still match their anchors. Same-key supersedes
//     (versioning, collapse-memories) are not candidates.
//   - Restored rows: review_needed, freshness 0.5, superseded_by NULL, the
//     pair's supersedes edge removed.
//   - A candidate whose key is held by a current row of its scope, or by a
//     newer candidate of the same scope and key, cannot come back
//     (uq_mem_*_key): it is an older version of that key. It stays superseded,
//     now by the current row of its key, and the pair's edge is removed.
//   - Every changed row is logged with its previous values and the reason.
//
// maxBits is the linker's simhash threshold (reconciler.LinkerSimhashMaxBits).
// Down of migration 20261121400 reverts the repair.
func (r *MemoryRepo) RepairLinkerSimhashSupersedes(ctx context.Context, algorithm string, maxBits int) (LinkerSimhashRepairResult, error) {
	var res LinkerSimhashRepairResult
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("linker simhash repair: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, memoryLinkerRepairLock); err != nil {
		return res, fmt.Errorf("linker simhash repair: lock: %w", err)
	}
	var done int
	if err = tx.GetContext(ctx, &done, `SELECT count(*) FROM memory_linker_simhash_repair_run WHERE algorithm = $1`, algorithm); err != nil {
		return res, fmt.Errorf("linker simhash repair: read run: %w", err)
	}
	if done > 0 {
		res.AlreadyDone = true
		return res, nil
	}
	var finished sql.NullTime
	switch err = tx.GetContext(ctx, &finished, `SELECT finished_at FROM memory_simhash_backfill WHERE algorithm = $1`, algorithm); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return res, fmt.Errorf("linker simhash repair: read backfill: %w", err)
	}
	if !finished.Valid {
		res.NotReady = true
		return res, nil
	}

	// This one-off repair needs a stable view of both texts, current key
	// holders and edges until its journal and changes commit. The advisory
	// lock only serializes repair workers; ordinary writers do not take it.
	if _, err = tx.ExecContext(ctx, `LOCK TABLE memories, memory_edges IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return res, fmt.Errorf("linker simhash repair: lock rows and edges: %w", err)
	}

	// Rank candidates of one scope identity (the uq_mem_*_key columns):
	// the newest may come back, the rest stay with it as the reason.
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO memory_linker_simhash_repair_log
		    (memory_id, workspace_id, superseded_by, action, reason, status_before,
		     freshness_before, updated_at_before, edge_weight, content_md5, hash_distance,
		     old_hash_distance)
		SELECT c.id, c.workspace_id, c.superseded_by,
		       CASE WHEN c.taken OR c.rank > 1 THEN 'repointed' ELSE 'restored' END,
		       CASE WHEN c.taken THEN 'linker would not supersede on the new simhash; key held by a current row of its scope: superseded by that row'
		            WHEN c.rank > 1 THEN 'linker would not supersede on the new simhash; a newer row of its key comes back: superseded by that row'
		            ELSE 'linker would not supersede on the new simhash: back to review_needed' END,
		       c.status, c.freshness_score, c.updated_at, c.edge_weight, md5(c.content), c.dist,
		       c.old_dist
		FROM (
		    SELECT o.id, o.workspace_id, o.superseded_by, o.status, o.freshness_score,
		           o.updated_at, o.content,
		           bit_count((o.content_simhash # n.content_simhash)::bit(64)) AS dist,
		           bit_count((ao.content_simhash # an.content_simhash)::bit(64)) AS old_dist,
		           (SELECT e.weight FROM memory_edges e
		             WHERE e.memory_from_id = n.id AND e.memory_to_id = o.id
		               AND e.relationship_type = 'supersedes') AS edge_weight,
		           EXISTS (SELECT 1 FROM memories l
		                   WHERE l.workspace_id = o.workspace_id
		                     AND l.scope = o.scope
		                     AND l.key = o.key
		                     AND (o.scope <> 'project' OR l.project_id IS NOT DISTINCT FROM o.project_id)
		                     AND (o.scope <> 'agent' OR l.agent_id IS NOT DISTINCT FROM o.agent_id)
		                     AND l.status <> 'superseded'
		                     AND NOT l.archived
		                     AND l.id <> o.id) AS taken,
		           row_number() OVER (
		               PARTITION BY o.workspace_id, o.scope, o.key,
		                            CASE o.scope WHEN 'project' THEN o.project_id END,
		                            CASE o.scope WHEN 'agent' THEN o.agent_id END
		               ORDER BY o.created_at DESC, o.id) AS rank
		    FROM memories o
		    JOIN memories n ON n.id = o.superseded_by AND n.workspace_id = o.workspace_id
		    JOIN memory_simhash_v1_anchor ao ON ao.memory_id = o.id
		    JOIN memory_simhash_v1_anchor an ON an.memory_id = n.id
		    WHERE o.status = 'superseded'
		      AND NOT o.archived
		      AND ao.content_md5 = md5(o.content)
		      AND an.content_md5 = md5(n.content)
		      AND o.key <> n.key
		      AND btrim(regexp_replace(o.content, '\s+', ' ', 'g'))
		       <> btrim(regexp_replace(n.content, '\s+', ' ', 'g'))
		      AND bit_count((ao.content_simhash # an.content_simhash)::bit(64)) BETWEEN 1 AND $1
		      AND NOT `+linkerWouldSupersedeSQL+`
		) c`, maxBits); err != nil {
		return res, fmt.Errorf("linker simhash repair: log: %w", err)
	}

	upd, err := tx.ExecContext(ctx, `
		UPDATE memories m
		SET status          = 'review_needed',
		    freshness_score = 0.5,
		    superseded_by   = NULL,
		    updated_at      = NOW()
		FROM memory_linker_simhash_repair_log r
		WHERE m.id = r.memory_id
		  AND r.action = 'restored'
		  AND m.status = 'superseded'
		  AND m.superseded_by = r.superseded_by`)
	if err != nil {
		return res, fmt.Errorf("linker simhash repair: restore: %w", err)
	}
	if res.Restored, err = upd.RowsAffected(); err != nil {
		return res, fmt.Errorf("linker simhash repair: restore count: %w", err)
	}

	// The current row of the key, after the restore above: the live holder, or
	// the newer candidate that just came back.
	if _, err = tx.ExecContext(ctx, `
		UPDATE memory_linker_simhash_repair_log r
		SET repointed_to = (
		    SELECT l.id FROM memories o, memories l
		    WHERE o.id = r.memory_id
		      AND l.workspace_id = o.workspace_id
		      AND l.scope = o.scope
		      AND l.key = o.key
		      AND (o.scope <> 'project' OR l.project_id IS NOT DISTINCT FROM o.project_id)
		      AND (o.scope <> 'agent' OR l.agent_id IS NOT DISTINCT FROM o.agent_id)
		      AND l.status <> 'superseded'
		      AND NOT l.archived
		      AND l.id <> o.id
		    ORDER BY l.created_at DESC, l.id
		    LIMIT 1)
		WHERE r.action = 'repointed'`); err != nil {
		return res, fmt.Errorf("linker simhash repair: find current row: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE memory_linker_simhash_repair_log
		SET action = 'kept', reason = reason || ' (no current row found: left as it was)'
		WHERE action = 'repointed' AND repointed_to IS NULL`); err != nil {
		return res, fmt.Errorf("linker simhash repair: unmatched: %w", err)
	}
	rp, err := tx.ExecContext(ctx, `
		UPDATE memories m
		SET superseded_by = r.repointed_to,
		    updated_at    = NOW()
		FROM memory_linker_simhash_repair_log r
		WHERE m.id = r.memory_id
		  AND r.action = 'repointed'
		  AND m.status = 'superseded'
		  AND m.superseded_by = r.superseded_by`)
	if err != nil {
		return res, fmt.Errorf("linker simhash repair: repoint: %w", err)
	}
	if res.Repointed, err = rp.RowsAffected(); err != nil {
		return res, fmt.Errorf("linker simhash repair: repoint count: %w", err)
	}

	// The linker's edge of every changed pair: the pair is no supersede now.
	if _, err = tx.ExecContext(ctx, `
		DELETE FROM memory_edges e
		USING memory_linker_simhash_repair_log r
		WHERE r.action IN ('restored', 'repointed')
		  AND e.memory_from_id = r.superseded_by
		  AND e.memory_to_id = r.memory_id
		  AND e.relationship_type = 'supersedes'`); err != nil {
		return res, fmt.Errorf("linker simhash repair: edges: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO memory_linker_simhash_repair_run (algorithm, restored, repointed) VALUES ($1, $2, $3)`,
		algorithm, res.Restored, res.Repointed); err != nil {
		return res, fmt.Errorf("linker simhash repair: finish: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return res, fmt.Errorf("linker simhash repair: commit: %w", err)
	}
	return res, nil
}
