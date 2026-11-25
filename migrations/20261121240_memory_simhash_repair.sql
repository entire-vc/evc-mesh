-- +goose Up

-- Memory simhash repair.
--
-- The pre-fix content_simhash (bare FNV-64a per trigram) clustered unrelated
-- long memories onto equal hashes, and the nightly review-triage superseded a
-- review_needed memory by any newer memory with an equal hash, whatever its
-- key or text. The algorithm is fixed in internal/simhash and the triage now
-- needs equal text as well; this migration repairs the data written under the
-- old algorithm. It runs before the API starts serving, while the stored hashes
-- are still the old ones; the API then recomputes them (memory_simhash_backfill
-- below, cmd/api simhash backfill).
--
-- 1. memory_simhash_v1_anchor keeps every row's pre-fix hash: the rollback
--    anchor of the recompute and the only record of which rows shared a hash.
-- 2. Repair. A superseded row is restored when its supersede rested only on a
--    hash collision: a different key than its superseding row, an equal old
--    hash, and different text (whitespace-collapsed), and the linker would
--    not have superseded it without the hash signal. Same-key supersedes and
--    equal-text duplicates stay. A row whose key is meanwhile taken by a live
--    row of its scope stays superseded (would violate uq_mem_*_key) and is not
--    logged. Restored rows become active with full freshness: they were
--    review_needed before the faulty supersede, and an access-based stale
--    decision now would measure the two months they were hidden by the defect.
--    The supersedes edge of the pair, when decay has not pruned it yet, is
--    removed. Every restored row is written to memory_simhash_repair_log with
--    its previous values.
-- 3. memory_simhash_backfill records the recompute run by the API and the
--    recount of the daily aggregates counted on the old hashes
--    (memory_base_daily dup columns, memory_stats_daily misses).
--
-- Rollback: revert the MR and run this file's Down. Down touches only rows
-- whose text is still the anchored one (md5 in memory_simhash_v1_anchor): it
-- puts the logged rows back to superseded with their previous status,
-- freshness, superseded_by, updated_at and edge, and restores the anchored
-- pre-fix hashes. Rows written or edited after this migration stay as they
-- are, with the hash their current text was written with.

CREATE TABLE memory_simhash_v1_anchor (
    memory_id       UUID PRIMARY KEY,
    content_simhash BIGINT,
    content_md5     TEXT        NOT NULL, -- the text the hash belongs to
    captured_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO memory_simhash_v1_anchor (memory_id, content_simhash, content_md5)
SELECT id, content_simhash, md5(content) FROM memories;

CREATE TABLE memory_simhash_repair_log (
    memory_id         UUID PRIMARY KEY,
    workspace_id      UUID        NOT NULL,
    superseded_by     UUID        NOT NULL,
    status_before     TEXT        NOT NULL,
    freshness_before  REAL        NOT NULL,
    updated_at_before TIMESTAMPTZ NOT NULL,
    edge_weight       REAL        NULL, -- the removed supersedes edge; NULL = none left
    reason            TEXT        NOT NULL,
    repaired_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO memory_simhash_repair_log
    (memory_id, workspace_id, superseded_by, status_before, freshness_before,
     updated_at_before, edge_weight, reason)
SELECT DISTINCT ON (o.workspace_id, o.scope, o.project_id, o.agent_id, o.key)
       o.id, o.workspace_id, o.superseded_by, o.status, o.freshness_score, o.updated_at,
       (SELECT e.weight FROM memory_edges e
         WHERE e.memory_from_id = n.id AND e.memory_to_id = o.id
           AND e.relationship_type = 'supersedes'),
       'simhash collision: different key, equal pre-fix hash, different text'
FROM memories o
JOIN memories n ON n.id = o.superseded_by AND n.workspace_id = o.workspace_id
WHERE o.status = 'superseded'
  AND NOT o.archived
  AND o.key <> n.key
  AND o.content_simhash = n.content_simhash
  AND btrim(regexp_replace(o.content, '\s+', ' ', 'g'))
   <> btrim(regexp_replace(n.content, '\s+', ' ', 'g'))
  -- The linker (reconciler computeConfidence) reaches its 0.70 threshold
  -- without the simhash signal when the keys share their first two segments,
  -- the tags overlap by half or more and the newer row is over an hour newer;
  -- importance >= 0.8 keeps it from superseding at all. Such a supersede did
  -- not need the collision and stays (the linker's own supersedes: separate).
  AND NOT (
        o.importance_score < 0.8
    AND array_length(string_to_array(replace(o.key, '/', ':'), ':'), 1) >= 2
    AND array_length(string_to_array(replace(n.key, '/', ':'), ':'), 1) >= 2
    AND (string_to_array(replace(o.key, '/', ':'), ':'))[1:2]
      = (string_to_array(replace(n.key, '/', ':'), ':'))[1:2]
    AND COALESCE(cardinality(o.tags), 0) > 0
    AND COALESCE(cardinality(n.tags), 0) > 0
    AND (SELECT count(*) FROM unnest(n.tags) t WHERE t = ANY (o.tags))::float8
        / LEAST(cardinality(o.tags), cardinality(n.tags)) >= 0.5
    AND n.created_at > o.created_at + INTERVAL '1 hour')
  AND NOT EXISTS (
        SELECT 1 FROM memories l
        WHERE l.workspace_id = o.workspace_id
          AND l.scope = o.scope
          AND l.project_id IS NOT DISTINCT FROM o.project_id
          AND l.agent_id IS NOT DISTINCT FROM o.agent_id
          AND l.key = o.key
          AND l.status <> 'superseded'
          AND NOT l.archived
          AND l.id <> o.id)
ORDER BY o.workspace_id, o.scope, o.project_id, o.agent_id, o.key, o.created_at DESC, o.id;

DELETE FROM memory_edges e
USING memory_simhash_repair_log r
WHERE e.memory_from_id = r.superseded_by
  AND e.memory_to_id = r.memory_id
  AND e.relationship_type = 'supersedes';

UPDATE memories m
SET status          = 'active',
    freshness_score = 1.0,
    superseded_by   = NULL,
    updated_at      = NOW()
FROM memory_simhash_repair_log r
WHERE m.id = r.memory_id
  AND m.status = 'superseded'
  AND m.superseded_by = r.superseded_by;

CREATE TABLE memory_simhash_backfill (
    algorithm     TEXT PRIMARY KEY,
    started_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at   TIMESTAMPTZ NULL,
    rows_scanned  BIGINT      NOT NULL DEFAULT 0,
    rows_updated  BIGINT      NOT NULL DEFAULT 0,
    rows_skipped  BIGINT      NOT NULL DEFAULT 0,
    -- daily aggregates brought onto the new hashes (RecountSimhashAggregates)
    aggregates_at TIMESTAMPTZ NULL
);

-- +goose Down

UPDATE memories m
SET status          = r.status_before,
    freshness_score = r.freshness_before,
    superseded_by   = r.superseded_by,
    updated_at      = r.updated_at_before
FROM memory_simhash_repair_log r
WHERE m.id = r.memory_id
  AND m.status = 'active'
  AND m.superseded_by IS NULL
  AND EXISTS (SELECT 1 FROM memories n WHERE n.id = r.superseded_by)
  -- a row edited after the repair is live content: leave it as it is
  AND EXISTS (SELECT 1 FROM memory_simhash_v1_anchor a
              WHERE a.memory_id = m.id AND a.content_md5 = md5(m.content));

INSERT INTO memory_edges (memory_from_id, memory_to_id, relationship_type, weight, workspace_id)
SELECT r.superseded_by, r.memory_id, 'supersedes', r.edge_weight, r.workspace_id
FROM memory_simhash_repair_log r
WHERE r.edge_weight IS NOT NULL
  -- only onto the rows the statement above put back under their superseder
  AND EXISTS (SELECT 1 FROM memories o
              JOIN memory_simhash_v1_anchor a ON a.memory_id = o.id
              WHERE o.id = r.memory_id AND o.superseded_by = r.superseded_by
                AND a.content_md5 = md5(o.content))
ON CONFLICT (memory_from_id, memory_to_id, relationship_type) DO NOTHING;

UPDATE memories m
SET content_simhash = a.content_simhash
FROM memory_simhash_v1_anchor a
WHERE a.memory_id = m.id
  AND md5(m.content) = a.content_md5;

DROP TABLE IF EXISTS memory_simhash_backfill;
DROP TABLE IF EXISTS memory_simhash_repair_log;
DROP TABLE IF EXISTS memory_simhash_v1_anchor;
