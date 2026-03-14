-- +goose Up

-- Extend agents table with heartbeat detail fields
ALTER TABLE agents ADD COLUMN IF NOT EXISTS heartbeat_status VARCHAR(20);
ALTER TABLE agents ADD COLUMN IF NOT EXISTS heartbeat_message TEXT;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS heartbeat_metadata JSONB;

-- Index for staleness queries
CREATE INDEX IF NOT EXISTS idx_agents_last_heartbeat
    ON agents(last_heartbeat) WHERE last_heartbeat IS NOT NULL;

-- Agent activity log for monitoring timeline
CREATE TABLE agent_activity_log (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id        UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    workspace_id    UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    event_type      VARCHAR(50) NOT NULL,
    task_id         UUID REFERENCES tasks(id) ON DELETE SET NULL,
    message         TEXT,
    metadata        JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_agent_activity_agent_time
    ON agent_activity_log(agent_id, created_at DESC);

CREATE INDEX idx_agent_activity_workspace
    ON agent_activity_log(workspace_id, created_at DESC);

-- RLS for agent_activity_log (defense-in-depth)
ALTER TABLE agent_activity_log ENABLE ROW LEVEL SECURITY;
CREATE POLICY rls_agent_activity_log ON agent_activity_log
    USING (workspace_id = current_setting('app.current_workspace_id', true)::uuid)
    WITH CHECK (workspace_id = current_setting('app.current_workspace_id', true)::uuid);

-- +goose Down
DROP POLICY IF EXISTS rls_agent_activity_log ON agent_activity_log;
ALTER TABLE agent_activity_log DISABLE ROW LEVEL SECURITY;
DROP TABLE IF EXISTS agent_activity_log;
DROP INDEX IF EXISTS idx_agents_last_heartbeat;
ALTER TABLE agents DROP COLUMN IF EXISTS heartbeat_metadata;
ALTER TABLE agents DROP COLUMN IF EXISTS heartbeat_message;
ALTER TABLE agents DROP COLUMN IF EXISTS heartbeat_status;
