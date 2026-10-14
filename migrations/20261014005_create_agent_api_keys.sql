-- +goose Up
-- Additional API keys of an agent, so a key can be changed in two phases
-- (issue the new key, move consumers, revoke the old one) with both keys valid
-- in between. The agent's original key stays where it is (agents.api_key_*,
-- mirrored on its home agent_workspace_grants row); this table only holds keys
-- issued through POST /api/v1/agents/{id}/keys.
--
-- Only the keyed digest is stored (HMAC-SHA256, same function as
-- agents.api_key_sha256): the key is 192+ bits from crypto/rand.
CREATE TABLE agent_api_keys (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id       UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    workspace_id   UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    key_prefix     VARCHAR(32) NOT NULL,
    key_sha256     TEXT NOT NULL,
    label          VARCHAR(128) NOT NULL DEFAULT '',
    issued_by_type actor_type NOT NULL,
    issued_by_id   UUID NOT NULL,
    expires_at     TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at     TIMESTAMPTZ
);

CREATE UNIQUE INDEX uq_agent_api_keys_sha256 ON agent_api_keys (key_sha256);
CREATE UNIQUE INDEX uq_agent_api_keys_prefix ON agent_api_keys (workspace_id, key_prefix);
CREATE INDEX idx_agent_api_keys_agent ON agent_api_keys (agent_id) WHERE revoked_at IS NULL;

ALTER TABLE agent_api_keys ENABLE ROW LEVEL SECURITY;
CREATE POLICY rls_agent_api_keys ON agent_api_keys
    USING (workspace_id = current_setting('app.current_workspace_id', true)::uuid)
    WITH CHECK (workspace_id = current_setting('app.current_workspace_id', true)::uuid);

-- +goose Down
DROP TABLE IF EXISTS agent_api_keys;
