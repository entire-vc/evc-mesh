-- +goose Up

CREATE TABLE projects (
    id                      UUID PRIMARY KEY,
    workspace_id            UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name                    VARCHAR(255) NOT NULL,
    description             TEXT NOT NULL DEFAULT '',
    slug                    VARCHAR(100) NOT NULL,
    icon                    VARCHAR(50) NOT NULL DEFAULT '',
    settings                JSONB NOT NULL DEFAULT '{}',
    default_assignee_type   default_assignee_type NOT NULL DEFAULT 'none',
    is_archived             BOOLEAN NOT NULL DEFAULT FALSE,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at              TIMESTAMPTZ,

    CONSTRAINT uq_projects_workspace_slug UNIQUE (workspace_id, slug),
    CONSTRAINT chk_projects_slug_format
        CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,98}[a-z0-9]$')
);

CREATE INDEX idx_projects_workspace ON projects(workspace_id)
    WHERE deleted_at IS NULL AND is_archived = FALSE;

-- +goose Down
DROP TABLE IF EXISTS projects;
