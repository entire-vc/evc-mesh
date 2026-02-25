-- +goose Up
CREATE TABLE saved_views (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name        VARCHAR(255) NOT NULL,
    view_type   VARCHAR(20) NOT NULL DEFAULT 'board',
    filters     JSONB NOT NULL DEFAULT '{}',
    sort_by     VARCHAR(100),
    sort_order  VARCHAR(4) DEFAULT 'asc',
    columns     JSONB,
    is_shared   BOOLEAN NOT NULL DEFAULT false,
    created_by  UUID NOT NULL REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_saved_views_project ON saved_views(project_id);
CREATE INDEX idx_saved_views_creator ON saved_views(created_by);

-- +goose Down
DROP TABLE IF EXISTS saved_views;
