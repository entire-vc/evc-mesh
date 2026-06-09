-- +goose NO TRANSACTION

-- +goose Up
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS task_id UUID NULL REFERENCES tasks(id) ON DELETE SET NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS agent_sessions_task_id_idx
    ON agent_sessions(task_id) WHERE task_id IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS agent_sessions_task_id_idx;

ALTER TABLE agent_sessions DROP COLUMN IF EXISTS task_id;
