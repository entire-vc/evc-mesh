-- +goose Up

-- Tombstone for a stopped chat session (E4.4 an internal ticket). A stop is final for the
-- chat_session_id, but a session that never held a token has no row in
-- chat_session_tokens to carry that fact, so a Stop that arrives before the first
-- issue would leave the session open for a live token. RevokeSession writes the
-- tombstone and Issue reads it, both under the per-session advisory lock.
--
-- Additive only. Deliberately not purged with expired tokens: a late issue after
-- the token rows are gone must still be refused.
CREATE TABLE chat_session_stops (
    chat_session_id UUID PRIMARY KEY,
    stopped_at      TIMESTAMPTZ NOT NULL,
    reason          TEXT NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS chat_session_stops;
