-- +goose NO TRANSACTION
-- Release 2 (contract) of the memory scope-identity fix (task #4edf3fb5).
-- Applied manually on prod 2026-07-30 after the collapse script (cmd/collapse-memories,
-- PR #448) confirmed zero duplicate groups in all three scopes. This migration exists
-- so self-hosted instances and future prod rebuilds pick up the same constraint —
-- it is idempotent (IF NOT EXISTS) and safe to run against a DB that already has
-- these indexes from the manual prod application.
--
-- CONCURRENTLY requires NO TRANSACTION (cannot run inside a transaction block).
-- Use CREATE UNIQUE INDEX IF NOT EXISTS, never ADD CONSTRAINT IF NOT EXISTS
-- (42601 syntax error in PG for constraints — see solution-pg-add-constraint-idempotency-duplicate-object-gap).
--
-- +goose Up
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_mem_ws_key
    ON memories (workspace_id, key)
    WHERE scope = 'workspace' AND status = 'active';

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_mem_proj_key
    ON memories (workspace_id, project_id, key)
    WHERE scope = 'project' AND status = 'active';

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_mem_agent_key
    ON memories (workspace_id, agent_id, key)
    WHERE scope = 'agent' AND status = 'active';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS uq_mem_agent_key;
DROP INDEX CONCURRENTLY IF EXISTS uq_mem_proj_key;
DROP INDEX CONCURRENTLY IF EXISTS uq_mem_ws_key;
