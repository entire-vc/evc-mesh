-- +goose Up

-- Linker supersedes decided by the pre-fix simhash.
--
-- The linker (reconciler computeConfidence) supersedes the older memory of an
-- embedding-similar pair when at least three of four signals hold: the keys
-- share their first two segments, the tags overlap by half or more, the
-- content simhashes are within 10 bits, the newer row is over an hour newer.
-- The pre-fix simhash put unrelated texts within 10 bits, so the hash signal
-- was granted to pairs it does not describe and decided their supersede.
--
-- The repair itself runs in the API after the simhash recompute has finished
-- (MemoryRepo.RepairLinkerSimhashSupersedes, called by the backfill job): only
-- then do the stored hashes carry the new algorithm, which SQL cannot compute.
-- This migration creates its journal and run marker; Down reverts it.
--
-- Scope: supersedes between different keys and different texts whose pre-fix
-- hashes (memory_simhash_v1_anchor, migration 20261121240) were 1 to 10 bits
-- apart, so the linker granted the hash signal. Pre-fix hashes over 10 bits
-- apart: superseded without the hash signal, legitimate, kept. Equal pre-fix
-- hashes: the review triage's collision, repaired by 20261121240.
--
-- memory_linker_simhash_repair_log holds one row per examined supersede:
--   action 'restored': the row is put back where the linker would send it now,
--     review_needed (freshness 0.5), superseded_by cleared, the pair's
--     supersedes edge removed; the previous values are kept here.
--   action 'repointed': the linker would not supersede it now, but the row
--     cannot come back: its key is held by a current row of its scope (or by
--     a newer row of the key restored in the same run). It is an older version
--     of that key, so it stays superseded, now by that current row
--     (repointed_to); the pair's supersedes edge is removed.
--   action 'kept': as 'repointed', but no current row was found; unchanged.
-- memory_linker_simhash_repair_run records the finished run per algorithm, so
-- the repair runs once.
--
-- Rollback: revert the MR and run this file's Down. Down puts each changed row
-- back under the linker's superseder with its previous status, freshness,
-- updated_at and edge, as long as it was not edited since the repair (content
-- md5 in the log) and not superseded by anything else since: a restored row
-- the nightly review triage moved to active or stale is reverted, one it
-- superseded again is left; a repointed row is reverted while it still points
-- at repointed_to.

CREATE TABLE memory_linker_simhash_repair_log (
    memory_id         UUID PRIMARY KEY,
    workspace_id      UUID        NOT NULL,
    superseded_by     UUID        NOT NULL, -- the superseder the linker set
    repointed_to      UUID        NULL, -- action 'repointed': the current row of the key
    action            TEXT        NOT NULL CHECK (action IN ('restored', 'repointed', 'kept')),
    reason            TEXT        NOT NULL,
    status_before     TEXT        NOT NULL,
    freshness_before  REAL        NOT NULL,
    updated_at_before TIMESTAMPTZ NOT NULL,
    edge_weight       REAL        NULL, -- the removed supersedes edge; NULL = none left
    content_md5       TEXT        NOT NULL, -- the text at the repair
    hash_distance     INT         NULL, -- new-algorithm Hamming distance of the pair
    old_hash_distance INT         NULL, -- pre-fix distance (memory_simhash_v1_anchor)
    repaired_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE memory_linker_simhash_repair_run (
    algorithm   TEXT PRIMARY KEY,
    finished_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    restored    INT         NOT NULL,
    repointed   INT         NOT NULL
);

-- +goose Down

WITH reverted AS (
UPDATE memories m
SET status          = r.status_before,
    freshness_score = r.freshness_before,
    superseded_by   = r.superseded_by,
    updated_at      = r.updated_at_before
FROM memory_linker_simhash_repair_log r
WHERE m.id = r.memory_id
  AND NOT m.archived
  AND md5(m.content) = r.content_md5
  AND EXISTS (SELECT 1 FROM memories n WHERE n.id = r.superseded_by)
  AND (   (r.action = 'restored'
           AND m.superseded_by IS NULL
           AND m.status IN ('review_needed', 'active', 'stale'))
       OR (r.action = 'repointed'
           AND m.status = 'superseded'
           AND m.superseded_by = r.repointed_to))
RETURNING m.id, m.superseded_by
)

INSERT INTO memory_edges (memory_from_id, memory_to_id, relationship_type, weight, workspace_id)
SELECT r.superseded_by, r.memory_id, 'supersedes', r.edge_weight, r.workspace_id
FROM memory_linker_simhash_repair_log r
JOIN reverted v ON v.id = r.memory_id AND v.superseded_by = r.superseded_by
WHERE r.action IN ('restored', 'repointed')
  AND r.edge_weight IS NOT NULL
ON CONFLICT (memory_from_id, memory_to_id, relationship_type) DO NOTHING;

DROP TABLE IF EXISTS memory_linker_simhash_repair_run;
DROP TABLE IF EXISTS memory_linker_simhash_repair_log;
