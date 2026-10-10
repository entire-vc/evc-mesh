-- +goose Up
-- Operational (runtime) state of a card, orthogonal to its Mesh status: what is
-- physically happening with the work right now (working / waiting / queued /
-- stopped). Additive and nullable: NULL means "nothing reported", and readers
-- then derive `working` from a live checkout. Nothing existing reads this column.
ALTER TABLE tasks ADD COLUMN runtime_state JSONB;
ALTER TABLE tasks ADD CONSTRAINT tasks_runtime_state_kind_chk
    CHECK (runtime_state IS NULL OR runtime_state->>'kind' IN ('working', 'waiting', 'queued', 'stopped'));
CREATE INDEX idx_tasks_runtime_kind ON tasks ((runtime_state->>'kind')) WHERE runtime_state IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_tasks_runtime_kind;
ALTER TABLE tasks DROP CONSTRAINT IF EXISTS tasks_runtime_state_kind_chk;
ALTER TABLE tasks DROP COLUMN IF EXISTS runtime_state;
