-- +goose NO TRANSACTION
-- Task #2c0154db, finding F2 / F2b. The three scope-aware partial unique
-- indexes from #4edf3fb5 (uq_mem_ws_key/uq_mem_proj_key/uq_mem_agent_key,
-- migration 086) enforce uniqueness over `status = 'active'`, but GetByKey
-- and every read path (recall/FTS/vector, task #2c0154db/F1) treat
-- `status <> 'superseded' AND archived = false` as current -- a strictly
-- wider set. The gap is exactly the 1454 `review_needed` rows: current to
-- every reader, but outside the uniqueness guarantee, so a colliding
-- review_needed row and an active row for the same key could both exist
-- and both surface in recall as if either were "the" current fact.
--
-- Measured on prod 2026-07-30: 12 keys returned >=2 readable versions
-- through this gap (same-key-any-scope-2plus-READABLE = 12).
--
-- CONCURRENTLY requires NO TRANSACTION and cannot ALTER a partial index's
-- WHERE clause in place -- drop and recreate under the wider predicate.
-- Use CREATE/DROP INDEX CONCURRENTLY IF [NOT] EXISTS, never
-- ADD CONSTRAINT IF NOT EXISTS (42601 syntax error in PG for constraints).
--
-- HARD ORDERING GATE, not just a migration-file dependency: this MUST run
-- AFTER task #2c0154db/F2a (cmd/collapse-memories, PR #461) has actually
-- been executed against prod, collapsing every live collision under the
-- WIDER predicate. Applying this first would fail outright (or worse,
-- silently favor whichever colliding row CONCURRENTLY happens to index
-- first) on the very 12 keys this migration exists to protect. Do not
-- run/merge this until F2a's dry-run shows same-key-any-scope-2plus-
-- READABLE = 0.
--
-- +goose Up
DROP INDEX CONCURRENTLY IF EXISTS uq_mem_agent_key;
DROP INDEX CONCURRENTLY IF EXISTS uq_mem_proj_key;
DROP INDEX CONCURRENTLY IF EXISTS uq_mem_ws_key;

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_mem_ws_key
    ON memories (workspace_id, key)
    WHERE scope = 'workspace' AND status <> 'superseded' AND archived = false;

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_mem_proj_key
    ON memories (workspace_id, project_id, key)
    WHERE scope = 'project' AND status <> 'superseded' AND archived = false;

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_mem_agent_key
    ON memories (workspace_id, agent_id, key)
    WHERE scope = 'agent' AND status <> 'superseded' AND archived = false;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS uq_mem_agent_key;
DROP INDEX CONCURRENTLY IF EXISTS uq_mem_proj_key;
DROP INDEX CONCURRENTLY IF EXISTS uq_mem_ws_key;

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_mem_ws_key
    ON memories (workspace_id, key)
    WHERE scope = 'workspace' AND status = 'active';

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_mem_proj_key
    ON memories (workspace_id, project_id, key)
    WHERE scope = 'project' AND status = 'active';

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_mem_agent_key
    ON memories (workspace_id, agent_id, key)
    WHERE scope = 'agent' AND status = 'active';
