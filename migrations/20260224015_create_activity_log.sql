-- +goose Up

-- Append-only audit log for all entity mutations.
CREATE TABLE activity_log (
    id              UUID PRIMARY KEY,
    workspace_id    UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    entity_type     VARCHAR(50) NOT NULL,
    entity_id       UUID NOT NULL,
    action          VARCHAR(50) NOT NULL,
    actor_id        UUID NOT NULL,
    actor_type      actor_type NOT NULL,
    changes         JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_activity_workspace ON activity_log(workspace_id, created_at DESC);
CREATE INDEX idx_activity_entity ON activity_log(entity_type, entity_id, created_at DESC);
CREATE INDEX idx_activity_actor ON activity_log(actor_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS activity_log;
