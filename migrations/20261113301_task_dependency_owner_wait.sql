-- +goose Up
-- A typed card wait records which wait owns the blocks edge it
-- declared, so the runner can remove exactly its own edge and never an edge a
-- person or another agent made.
--
-- owner_wait_id is NULL for every existing edge (no backfill): an edge with no
-- owner is never removed by an owner-checked delete. The reference is checked
-- immediately (a deferred one queues trigger events on every edge write and
-- breaks a later ALTER TABLE in the same transaction); the wait command sets
-- the owner after it has inserted the wait row. wait_agent_id keeps its meaning
-- (the cross-workspace membership authority) and is not touched here.
ALTER TABLE task_dependencies
    ADD COLUMN owner_wait_id UUID NULL
        REFERENCES task_waits(id) ON DELETE SET NULL;

-- ON DELETE SET NULL looks edges up by owner_wait_id.
CREATE INDEX idx_task_dependencies_owner_wait ON task_dependencies(owner_wait_id)
    WHERE owner_wait_id IS NOT NULL;

-- The receipt of a card wait: the edge it declared, its generation then, and
-- whether this wait owns it. No reference: the edge may be removed later and the
-- receipt still says what it was.
ALTER TABLE task_waits
    ADD COLUMN dependency_id UUID NULL,
    ADD COLUMN dependency_generation BIGINT NULL,
    ADD COLUMN dependency_owned BOOLEAN NULL;

-- +goose Down
ALTER TABLE task_waits
    DROP COLUMN dependency_owned,
    DROP COLUMN dependency_generation,
    DROP COLUMN dependency_id;
DROP INDEX IF EXISTS idx_task_dependencies_owner_wait;
ALTER TABLE task_dependencies DROP COLUMN owner_wait_id;
