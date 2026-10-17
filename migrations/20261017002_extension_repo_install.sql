-- +goose Up
-- Install from a repository (BRAT mode): the previous manifest keeps rollback
-- possible, the pending_* columns hold an update waiting for consent to a
-- wider permission set.
ALTER TABLE installed_extensions
    ADD COLUMN previous_manifest   JSONB,
    ADD COLUMN previous_commit_sha VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN previous_ref        TEXT NOT NULL DEFAULT '',
    ADD COLUMN pending_version     VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN pending_ref         TEXT NOT NULL DEFAULT '',
    ADD COLUMN pending_commit_sha  VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN pending_manifest    JSONB,
    ADD COLUMN skipped_commit_sha  VARCHAR(64) NOT NULL DEFAULT '';

-- Access token of a private repository. Encrypted at rest, write-only: no API
-- returns it.
CREATE TABLE extension_repo_credentials (
    extension_id VARCHAR(64) PRIMARY KEY,
    token_enc    TEXT NOT NULL,
    updated_by   UUID,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS extension_repo_credentials;
ALTER TABLE installed_extensions
    DROP COLUMN IF EXISTS skipped_commit_sha,
    DROP COLUMN IF EXISTS pending_manifest,
    DROP COLUMN IF EXISTS pending_commit_sha,
    DROP COLUMN IF EXISTS pending_ref,
    DROP COLUMN IF EXISTS pending_version,
    DROP COLUMN IF EXISTS previous_ref,
    DROP COLUMN IF EXISTS previous_commit_sha,
    DROP COLUMN IF EXISTS previous_manifest;
