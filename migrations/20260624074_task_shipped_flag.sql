-- +goose Up
ALTER TABLE tasks ADD COLUMN is_shipped BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX idx_tasks_is_shipped ON tasks(is_shipped) WHERE is_shipped = TRUE;

-- +goose Down
DROP INDEX IF EXISTS idx_tasks_is_shipped;
ALTER TABLE tasks DROP COLUMN IF EXISTS is_shipped;
