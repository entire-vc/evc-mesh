-- +goose Up

-- One-time data cleanup: human user UUIDs were recorded as assignee_type='agent'
-- due to a missing enum-gate at write time. Cross-reference the users table to
-- identify all human UUIDs and flip the column to 'user'.
--
-- Safe to run repeatedly (no-op when already correct).
-- No batching needed: the predicate is indexed (idx_tasks_assignee) and the
-- affected row count is expected to be small (<1 000 rows).
UPDATE tasks
SET    assignee_type = 'user'
WHERE  assignee_type = 'agent'
  AND  assignee_id IN (SELECT id FROM users);

-- Apply the same fix to recurring_schedules, which uses the same enum column.
UPDATE recurring_schedules
SET    assignee_type = 'user'
WHERE  assignee_type = 'agent'
  AND  assignee_id IN (SELECT id FROM users);

-- +goose Down

-- No safe rollback: pre-migration state is not known.
-- Intentional no-op — do not re-introduce bad data.
