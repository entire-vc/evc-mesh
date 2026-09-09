-- +goose Up

-- U1 of the agent-multi-workspace feature (Mesh Doc specs/agent-multi-workspace,
-- Schema C). Table + backfill only — nothing reads this table yet.
-- `agents.workspace_id` stays the source of truth for the agent's home
-- workspace and for authentication until U2 switches the read path; this table
-- adds ADDITIONAL grants an agent can hold in other workspaces, on top of that
-- home relationship, without touching it.
CREATE TABLE agent_workspace_grants (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id       UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    workspace_id   UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    role           workspace_role NOT NULL DEFAULT 'member',
    api_key_prefix VARCHAR(32) NOT NULL,
    api_key_hash   TEXT NOT NULL,
    invited_by     UUID REFERENCES users(id),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at     TIMESTAMPTZ NULL,
    CONSTRAINT uq_agent_ws_grant UNIQUE (agent_id, workspace_id)
);

CREATE INDEX idx_grants_lookup ON agent_workspace_grants(workspace_id, api_key_prefix) WHERE revoked_at IS NULL;
CREATE INDEX idx_grants_agent  ON agent_workspace_grants(agent_id);

-- Same convention as every table added since 20260301030_enable_rls_policies.sql:
-- the backend connects as the table owner and bypasses this without FORCE, so it
-- changes nothing for the app today (nothing reads this table in U1 anyway) — it
-- only stops a future direct connection from reading a grant across workspaces.
ALTER TABLE agent_workspace_grants ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_agent_workspace_grants ON agent_workspace_grants
    USING (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    )
    WITH CHECK (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    );

-- Backfill: exactly one grant per LIVE agent, to its home workspace, key
-- carried over as-is (no reissue). Soft-deleted agents (deleted_at IS NOT
-- NULL) are deliberately excluded from the count this backfill produces.
-- `role` is left to its DEFAULT ('member') — an agent does not own a
-- workspace the way workspaces.owner_id (a user) does, and nothing reads
-- this column yet to give a different value meaning.
INSERT INTO agent_workspace_grants (agent_id, workspace_id, api_key_prefix, api_key_hash, created_at)
SELECT id, workspace_id, api_key_prefix, api_key_hash, created_at
FROM agents
WHERE deleted_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS agent_workspace_grants;
