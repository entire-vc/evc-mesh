-- +goose Up

-- Chat with Agent extension: orders for the runner (agent-chat-openapi §4 v6,
-- runner protocol §8): stop a live session, delete a transcript on the runner.
-- Additive only: one new table, nothing existing is altered.
--
-- thread_id has no FK on purpose: a command outlives the physical purge of its
-- thread (delete_transcript is issued exactly when the thread goes away).
-- Tenancy: workspace_id carries the RLS policy, same shape as the other
-- ext_agent_chat_* tables (app.current_workspace_id).

CREATE TABLE ext_agent_chat_commands (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    agent_id     UUID NOT NULL,
    runner_id    UUID,
    kind         TEXT NOT NULL CHECK (kind IN ('stop_session', 'delete_transcript')),
    thread_id    UUID NOT NULL,
    session_id   TEXT,
    harness      TEXT,
    state        TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'done', 'failed', 'dead')),
    attempts     INTEGER NOT NULL DEFAULT 0,
    detail       TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    CONSTRAINT fk_ext_agent_chat_commands_agent FOREIGN KEY (agent_id, workspace_id)
        REFERENCES agents (id, workspace_id)
);

-- The runner polls its pending orders by agent.
CREATE INDEX ext_agent_chat_commands_pending
    ON ext_agent_chat_commands (agent_id) WHERE state = 'pending';

ALTER TABLE ext_agent_chat_commands ENABLE ROW LEVEL SECURITY;
CREATE POLICY rls_ext_agent_chat_commands ON ext_agent_chat_commands
    USING (workspace_id = current_setting('app.current_workspace_id', true)::uuid)
    WITH CHECK (workspace_id = current_setting('app.current_workspace_id', true)::uuid);

-- +goose Down
DROP TABLE IF EXISTS ext_agent_chat_commands;
