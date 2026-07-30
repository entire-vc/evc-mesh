-- +goose Up
-- Task #2c0154db, finding F1 point 3. This legacy constraint (workspace_id,
-- project_id, agent_id, key, scope) is NOT partial -- it counts rows of every
-- status. Once GetByKey (migration-adjacent code fix, task F1) resolves
-- ambiguity with a deterministic ORDER BY instead of relying on a predicate-
-- less scan, this constraint starts blocking honest INSERTs: a retired row
-- still holding the full tuple (ws, project, agent, key, scope) prevents
-- inserting a new active row with that same tuple. Measured on prod: 999 keys
-- in exactly this position (retired rows with non-null project_id+agent_id).
--
-- It is not needed for correctness any more -- it is fully superseded by the
-- three partial unique indexes from #4edf3fb5 (uq_mem_ws_key / uq_mem_proj_key
-- / uq_mem_agent_key), each scoped to `status='active'` and to the identity
-- dimensions that actually apply per declared scope. Application code never
-- references this constraint by name: Upsert (memory_repo.go) conflicts on
-- `ON CONFLICT (id)`, not on this composite -- see the comment there on why
-- (NULLs are distinct in a UNIQUE constraint, so this one silently never
-- matched a NULL project_id/agent_id row anyway).
--
-- Separate release from the F1 code fix (CLAUDE-workflow.md §1b) -- the code
-- fix must be live first, since the 999-key blocking risk this migration
-- removes only exists once GetByKey's ORDER BY fix is deployed.
ALTER TABLE memories DROP CONSTRAINT IF EXISTS uq_memory_key_scope;

-- +goose Down
-- Best-effort rollback: recreates the exact original constraint definition
-- from migrations/20260315041_create_memories.sql. If live data now contains
-- rows that would violate it (e.g. two retired rows sharing a tuple that
-- happened to coexist only because this constraint was gone), this will fail
-- -- which is the correct, fail-closed behavior for a rollback of a
-- constraint-removal migration.
ALTER TABLE memories ADD CONSTRAINT uq_memory_key_scope
    UNIQUE (workspace_id, project_id, agent_id, key, scope);
