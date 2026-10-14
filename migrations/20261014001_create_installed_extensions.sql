-- +goose Up
CREATE TABLE installed_extensions (
    extension_id     VARCHAR(64) PRIMARY KEY,
    version          VARCHAR(64) NOT NULL,
    manifest         JSONB NOT NULL,
    source           VARCHAR(16) NOT NULL CHECK (source IN ('builtin', 'catalog', 'repo')),
    repo_url         TEXT NOT NULL DEFAULT '',
    repo_ref         TEXT NOT NULL DEFAULT '',
    commit_sha       VARCHAR(64) NOT NULL DEFAULT '',
    artifact_sha256  VARCHAR(64) NOT NULL DEFAULT '',
    trust_level      VARCHAR(16) NOT NULL DEFAULT 'builtin',
    installed_by     UUID,
    previous_version VARCHAR(64) NOT NULL DEFAULT '',
    blocked          BOOLEAN NOT NULL DEFAULT false,
    blocked_reason   TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS installed_extensions;
