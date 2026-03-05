-- +goose Up
ALTER TABLE tasks
    ADD COLUMN recurring_schedule_id UUID REFERENCES recurring_schedules(id) ON DELETE SET NULL,
    ADD COLUMN recurring_instance_number INT;

-- Index for fast lookup of all instances in a series
CREATE INDEX idx_tasks_recurring ON tasks(recurring_schedule_id, recurring_instance_number)
    WHERE recurring_schedule_id IS NOT NULL AND deleted_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_tasks_recurring;
ALTER TABLE tasks
    DROP COLUMN IF EXISTS recurring_schedule_id,
    DROP COLUMN IF EXISTS recurring_instance_number;
