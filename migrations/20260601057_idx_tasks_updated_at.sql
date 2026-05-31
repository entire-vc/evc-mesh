-- +goose Up
-- Covering index for the default task-list sort (updated_at DESC, id ASC).
-- Prevents non-deterministic page boundaries when many tasks share the same
-- updated_at value (e.g. after a bulk migration).  CONCURRENTLY avoids a
-- full-table lock on prod.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_tasks_updated_at
    ON tasks (project_id, updated_at DESC, id ASC)
    WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_tasks_updated_at;
