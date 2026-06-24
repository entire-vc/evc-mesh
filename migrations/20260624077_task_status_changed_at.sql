-- +goose Up
ALTER TABLE tasks ADD COLUMN status_changed_at TIMESTAMPTZ;
UPDATE tasks SET status_changed_at = updated_at WHERE status_changed_at IS NULL;

-- +goose Down
ALTER TABLE tasks DROP COLUMN status_changed_at;
