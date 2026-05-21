-- +goose Up
CREATE TABLE agent_events (
    event_id     UUID        PRIMARY KEY,
    agent_id     UUID        NOT NULL,
    workspace_id UUID        NOT NULL,
    event_type   TEXT        NOT NULL,
    payload      JSONB       NOT NULL,
    emitted_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ NOT NULL
);

-- Supports replay queries: WHERE agent_id = $1 AND event_id > $2
CREATE INDEX agent_events_agent_eid_idx ON agent_events (agent_id, event_id);

-- Supports sweeper: WHERE expires_at < NOW()
CREATE INDEX agent_events_expires_idx ON agent_events (expires_at);

-- +goose Down
DROP TABLE IF EXISTS agent_events;
