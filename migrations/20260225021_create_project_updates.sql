-- +goose Up
CREATE TABLE project_updates (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title       VARCHAR(255) NOT NULL,
    status      VARCHAR(20) NOT NULL DEFAULT 'on_track',
    summary     TEXT NOT NULL,
    highlights  JSONB NOT NULL DEFAULT '[]',
    blockers    JSONB NOT NULL DEFAULT '[]',
    next_steps  JSONB NOT NULL DEFAULT '[]',
    metrics     JSONB NOT NULL DEFAULT '{}',
    created_by  UUID NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT project_updates_status_check CHECK (status IN ('on_track','at_risk','off_track','completed'))
);

CREATE INDEX idx_project_updates_project ON project_updates(project_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS project_updates;
