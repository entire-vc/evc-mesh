-- +goose Up

-- Chat-with-Agent session tokens (E4.4 an internal ticket, contract Mesh Doc
-- agent-chat-session-token v2). A cst_ bearer lets an agent act on behalf of
-- ONE user in ONE project of ONE chat; every request re-checks the user's live
-- role and the agent's live workspace connection, so a row here is necessary
-- but never sufficient.
--
-- Additive only: nothing existing reads or writes this table. Only the sha256
-- of the token is stored; the bearer itself exists in the issuing response and
-- the runner's token file.
CREATE TABLE chat_session_tokens (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash      TEXT NOT NULL UNIQUE,
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    agent_id        UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    workspace_id    UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    chat_thread_id  UUID NOT NULL,
    chat_session_id UUID NOT NULL,
    runner_id       TEXT NOT NULL,
    -- Generation of the execution that holds the session. A token with an older
    -- epoch than the session's newest one is never issued or renewed.
    execution_epoch BIGINT NOT NULL,
    issued_at       TIMESTAMPTZ NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    revoked_at      TIMESTAMPTZ NULL,
    -- 'superseded' = replaced by a renewal (the session lives on); anything else
    -- ('stop', 'manual', ...) ends the session: it cannot be renewed.
    revoked_reason  TEXT NULL
);

CREATE INDEX idx_chat_session_tokens_session ON chat_session_tokens(chat_session_id, issued_at DESC);
CREATE INDEX idx_chat_session_tokens_user ON chat_session_tokens(user_id);
CREATE INDEX idx_chat_session_tokens_expires ON chat_session_tokens(expires_at);

-- +goose Down
DROP TABLE IF EXISTS chat_session_tokens;
