-- +goose Up

-- Links users to projects within a workspace. Optional: if absent, workspace role applies.
CREATE TABLE project_members (
    id              UUID PRIMARY KEY,
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role            project_role NOT NULL DEFAULT 'member',
    added_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_project_members_user UNIQUE (project_id, user_id)
);

CREATE INDEX idx_proj_members_user ON project_members(user_id);

-- +goose Down
DROP TABLE IF EXISTS project_members;
