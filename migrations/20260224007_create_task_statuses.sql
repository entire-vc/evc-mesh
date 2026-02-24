-- +goose Up

-- Customizable task statuses per project, each mapped to a semantic category.
CREATE TABLE task_statuses (
    id              UUID PRIMARY KEY,
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name            VARCHAR(100) NOT NULL,
    slug            VARCHAR(50) NOT NULL,
    color           VARCHAR(7) NOT NULL DEFAULT '#6B7280',
    position        INT NOT NULL DEFAULT 0,
    category        task_status_category NOT NULL,
    is_default      BOOLEAN NOT NULL DEFAULT FALSE,
    auto_transition JSONB NOT NULL DEFAULT '{}',

    CONSTRAINT uq_task_statuses_project_slug UNIQUE (project_id, slug),
    CONSTRAINT chk_task_statuses_color_format CHECK (color ~ '^#[0-9A-Fa-f]{6}$')
);

CREATE INDEX idx_task_statuses_project ON task_statuses(project_id, position);

-- Guarantee at most one default status per project
CREATE UNIQUE INDEX uq_task_statuses_default
    ON task_statuses(project_id) WHERE is_default = TRUE;

-- +goose Down
DROP TABLE IF EXISTS task_statuses;
