-- +goose Up
CREATE TABLE initiatives (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name          VARCHAR(255) NOT NULL,
    description   TEXT NOT NULL DEFAULT '',
    status        VARCHAR(20) NOT NULL DEFAULT 'active',
    target_date   DATE,
    created_by    UUID NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT initiatives_status_check CHECK (status IN ('active','completed','archived'))
);

CREATE INDEX idx_initiatives_workspace ON initiatives(workspace_id);

CREATE TABLE initiative_projects (
    initiative_id UUID NOT NULL REFERENCES initiatives(id) ON DELETE CASCADE,
    project_id    UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    PRIMARY KEY (initiative_id, project_id)
);

-- +goose Down
DROP TABLE IF EXISTS initiative_projects;
DROP TABLE IF EXISTS initiatives;
