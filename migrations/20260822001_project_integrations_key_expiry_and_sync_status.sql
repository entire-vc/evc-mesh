-- +goose Up
-- R5-A: key expiry (with provenance) + last-sync-check state for project_integrations.
--
-- The mount-point "single switch" (settings.docs_mount_path, added by R3 / PR #697)
-- needs NO schema change here — it already lives in the settings JSONB and is
-- orthogonal to subfolder/include_project_slug (those power Team Relay
-- *publishing*, a separate feature; see task #002d6ad6 comment for the trace).
--
-- key_expiry_source is NULL until an expiry is known at all; 'manual' means a
-- human typed the date into our settings screen (no guarantee it is accurate);
-- 'source' is reserved for a future value fetched from Team Relay's own
-- key-introspection endpoint (not built yet — task bc11d499 in evc-team-relay).
-- The UI MUST show provenance whenever key_expires_at is set, per the spec
-- decision recorded on the parent task (#218d5847): an unverifiable manual
-- date is only safe to display labeled as such.
ALTER TABLE project_integrations
    ADD COLUMN key_expires_at TIMESTAMPTZ NULL,
    ADD COLUMN key_expiry_source TEXT NULL
        CHECK (key_expiry_source IN ('manual', 'source')),
    ADD COLUMN last_sync_checked_at TIMESTAMPTZ NULL,
    ADD COLUMN last_sync_status TEXT NULL
        CHECK (last_sync_status IN ('ok', 'error', 'key_expired')),
    ADD COLUMN last_sync_error TEXT NULL,
    ADD CONSTRAINT project_integrations_key_expiry_source_requires_value
        CHECK (key_expiry_source IS NULL OR key_expires_at IS NOT NULL),
    ADD CONSTRAINT project_integrations_last_sync_status_requires_timestamp
        CHECK (last_sync_status IS NULL OR last_sync_checked_at IS NOT NULL);

-- +goose Down
ALTER TABLE project_integrations
    DROP CONSTRAINT IF EXISTS project_integrations_last_sync_status_requires_timestamp,
    DROP CONSTRAINT IF EXISTS project_integrations_key_expiry_source_requires_value,
    DROP COLUMN IF EXISTS last_sync_error,
    DROP COLUMN IF EXISTS last_sync_status,
    DROP COLUMN IF EXISTS last_sync_checked_at,
    DROP COLUMN IF EXISTS key_expiry_source,
    DROP COLUMN IF EXISTS key_expires_at;
