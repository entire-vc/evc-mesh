-- +goose Up

-- Chat turn events (runner protocol §6): the model that answered a turn and the
-- link from a server-written session report back to the chat thread.
-- Additive only: two nullable columns and one partial index.
ALTER TABLE ext_agent_chat_turns ADD COLUMN model TEXT;
ALTER TABLE agent_sessions ADD COLUMN chat_thread_id UUID;
CREATE INDEX agent_sessions_chat_thread
    ON agent_sessions (chat_thread_id) WHERE chat_thread_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS agent_sessions_chat_thread;
ALTER TABLE agent_sessions DROP COLUMN IF EXISTS chat_thread_id;
ALTER TABLE ext_agent_chat_turns DROP COLUMN IF EXISTS model;
