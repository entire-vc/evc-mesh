-- +goose Up

-- Files and artifacts attached to tasks. Stored in S3/MinIO.
CREATE TABLE artifacts (
    id                  UUID PRIMARY KEY,
    task_id             UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    name                VARCHAR(500) NOT NULL,
    artifact_type       artifact_type NOT NULL DEFAULT 'file',
    mime_type           VARCHAR(100) NOT NULL DEFAULT 'application/octet-stream',
    storage_key         VARCHAR(1000) NOT NULL,
    size_bytes          BIGINT NOT NULL DEFAULT 0,
    checksum_sha256     VARCHAR(64) NOT NULL DEFAULT '',
    metadata            JSONB NOT NULL DEFAULT '{}',
    uploaded_by         UUID NOT NULL,
    uploaded_by_type    uploader_type NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_artifacts_task ON artifacts(task_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS artifacts;
