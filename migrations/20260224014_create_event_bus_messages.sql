-- +goose Up

-- Event bus messages duplicated from NATS JetStream for filtered queries.
CREATE TABLE event_bus_messages (
    id              UUID PRIMARY KEY,
    workspace_id    UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_id         UUID REFERENCES tasks(id) ON DELETE SET NULL,
    agent_id        UUID REFERENCES agents(id) ON DELETE SET NULL,
    event_type      event_type NOT NULL,
    subject         VARCHAR(500) NOT NULL,
    payload         JSONB NOT NULL DEFAULT '{}',
    tags            TEXT[] NOT NULL DEFAULT '{}',
    ttl             INTERVAL NOT NULL DEFAULT INTERVAL '30 days',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at      TIMESTAMPTZ NOT NULL
);

-- Primary query: project events by type and date
CREATE INDEX idx_events_project_type ON event_bus_messages(project_id, event_type, created_at DESC);

-- Task events
CREATE INDEX idx_events_task ON event_bus_messages(task_id, created_at DESC)
    WHERE task_id IS NOT NULL;

-- Agent events
CREATE INDEX idx_events_agent ON event_bus_messages(agent_id, created_at DESC)
    WHERE agent_id IS NOT NULL;

-- Tag search
CREATE INDEX idx_events_tags ON event_bus_messages USING GIN(tags);

-- Cleanup worker: find expired messages
CREATE INDEX idx_events_expires ON event_bus_messages(expires_at)
    WHERE expires_at IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS event_bus_messages;
