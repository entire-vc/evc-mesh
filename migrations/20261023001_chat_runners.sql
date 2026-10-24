-- +goose Up

-- Chat with Agent: registry of runners with their own credential (follow-up of
-- the runner orders unit). Until now runner_id on an order was only a delivery
-- address and the agent key was the sole credential, so any holder of the key
-- could poll and ack an order addressed to another runner of the same agent.
-- A runner now registers under its agent, gets a bearer shown once, and the
-- server takes runner_id from that bearer instead of from the request.
--
-- Additive only: one new table, nothing existing is altered. Orders addressed
-- to a runner id that was never registered stay undeliverable to a token
-- holder (nobody can authenticate as that id) and expire as before.
-- Only the sha256 of the bearer is stored.
CREATE TABLE ext_agent_chat_runners (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    agent_id     UUID NOT NULL,
    name         TEXT NOT NULL DEFAULT '',
    token_hash   TEXT NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    CONSTRAINT fk_ext_agent_chat_runners_agent FOREIGN KEY (agent_id, workspace_id)
        REFERENCES agents (id, workspace_id) ON DELETE CASCADE
);

CREATE INDEX ext_agent_chat_runners_agent ON ext_agent_chat_runners (agent_id);

ALTER TABLE ext_agent_chat_runners ENABLE ROW LEVEL SECURITY;
CREATE POLICY rls_ext_agent_chat_runners ON ext_agent_chat_runners
    USING (workspace_id = current_setting('app.current_workspace_id', true)::uuid)
    WITH CHECK (workspace_id = current_setting('app.current_workspace_id', true)::uuid);

-- +goose Down
DROP TABLE IF EXISTS ext_agent_chat_runners;
