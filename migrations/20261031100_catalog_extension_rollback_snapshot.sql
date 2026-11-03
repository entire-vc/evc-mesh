-- +goose Up
-- Catalog installs: a full snapshot of the release an update replaced, so
-- rollback restores version, manifest, commit, digest, source and trust as one
-- unit. previous_version alone (rows written before this migration) is not a
-- snapshot and does not make a catalog row rollback-able.
ALTER TABLE installed_extensions
    ADD COLUMN previous_snapshot JSONB;

-- +goose Down
ALTER TABLE installed_extensions
    DROP COLUMN IF EXISTS previous_snapshot;
