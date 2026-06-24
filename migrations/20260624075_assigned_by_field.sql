-- +goose Up
-- assigned_by tracks whether the current assignment was set by a human, a rule, or the system.
-- Used to enforce pin-assignee invariant: rule/system sources cannot override a human assignment.
ALTER TABLE tasks ADD COLUMN assigned_by VARCHAR(16) NOT NULL DEFAULT 'system';

-- +goose Down
ALTER TABLE tasks DROP COLUMN IF EXISTS assigned_by;
