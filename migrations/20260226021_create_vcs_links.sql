-- +goose Up
CREATE TABLE vcs_links (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id     UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    provider    VARCHAR(20) NOT NULL DEFAULT 'github',
    link_type   VARCHAR(20) NOT NULL,
    external_id VARCHAR(255) NOT NULL,
    url         TEXT NOT NULL,
    title       VARCHAR(500),
    status      VARCHAR(20),
    metadata    JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_vcs_links_task ON vcs_links(task_id);
CREATE UNIQUE INDEX idx_vcs_links_unique ON vcs_links(task_id, provider, link_type, external_id);

-- +goose Down
DROP TABLE IF EXISTS vcs_links;
