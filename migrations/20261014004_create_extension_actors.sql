-- +goose NO TRANSACTION
-- +goose Up
-- Service principal of an extension connection (spec 03 §1, §4, §9).
--
-- 'extension' is a third actor type next to user/agent so that history and
-- activity can be signed by the extension ("GitLab did X") instead of being
-- attributed to a person or an agent. ADD VALUE cannot run inside a transaction
-- block, hence NO TRANSACTION for this file.
ALTER TYPE actor_type ADD VALUE IF NOT EXISTS 'extension';

-- One actor per connection (integration_configs row). The token is random, stored
-- only as a SHA-256 digest (a 256-bit random value has no dictionary to stretch),
-- scoped to workspace_id and to the capabilities consented to at connect time.
-- Rotation keeps the previous digest for a short window (prev_token_*) so the
-- extension can switch without a gap; revoked_at kills both digests at once.
CREATE TABLE IF NOT EXISTS extension_actors (
    connection_id         UUID PRIMARY KEY REFERENCES integration_configs(id) ON DELETE CASCADE,
    workspace_id          UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    actor_id              UUID NOT NULL UNIQUE,
    extension_id          VARCHAR(64) NOT NULL,
    display_name          VARCHAR(128) NOT NULL DEFAULT '',
    token_hash            CHAR(64) NOT NULL,
    prev_token_hash       CHAR(64),
    prev_token_expires_at TIMESTAMPTZ,
    capabilities          JSONB NOT NULL DEFAULT '{}'::jsonb,
    violation_count       BIGINT NOT NULL DEFAULT 0,
    last_violation_at     TIMESTAMPTZ,
    last_violation        TEXT NOT NULL DEFAULT '',
    issued_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at            TIMESTAMPTZ,
    revoked_at            TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_extension_actors_token_hash
    ON extension_actors (token_hash);
CREATE UNIQUE INDEX IF NOT EXISTS uq_extension_actors_prev_token_hash
    ON extension_actors (prev_token_hash) WHERE prev_token_hash IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_extension_actors_workspace
    ON extension_actors (workspace_id);

-- Same convention as every table added since 20260301030_enable_rls_policies.sql.
-- Token lookup happens before a workspace is known (the token names it), through
-- the owner connection, exactly like agent key authentication.
ALTER TABLE extension_actors ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS rls_extension_actors ON extension_actors;
CREATE POLICY rls_extension_actors ON extension_actors
    USING (workspace_id = current_setting('app.current_workspace_id', true)::uuid)
    WITH CHECK (workspace_id = current_setting('app.current_workspace_id', true)::uuid);

-- +goose Down
-- The 'extension' enum value stays: Postgres cannot drop an enum value, and rows
-- written with it (activity, comments) would otherwise block the downgrade.
DROP TABLE IF EXISTS extension_actors;
