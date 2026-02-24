-- +goose Up

-- Links users to workspaces with roles.
CREATE TABLE workspace_members (
    id              UUID PRIMARY KEY,
    workspace_id    UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role            workspace_role NOT NULL DEFAULT 'member',
    invited_by      UUID REFERENCES users(id),
    joined_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_workspace_members_user UNIQUE (workspace_id, user_id)
);

CREATE INDEX idx_ws_members_user ON workspace_members(user_id);

-- +goose Down
DROP TABLE IF EXISTS workspace_members;
