-- +goose Up

-- The legal channel for a human to hand an agent a secret (task #64e84eb1).
--
-- Two paths existed before this table, both bad: pasting a value into a
-- Telegram message or Mesh comment (read forever after by every session's
-- startup hook — the Casdoor client_secret leak from #d2f79c73 sat live for
-- two weeks that way), or Pavel walking up to the Mac Mini and hand-editing
-- ~/.config/agents/*.env (works, but stalls a lane for hours while he does
-- it). This table is write-only: a value goes in once through the API and
-- is never readable again through any surface — see S6 for the negative
-- test that checks every read path, not just this one.
--
-- Reuses pkg/encryption exactly as shipped for project_integrations.agent_key
-- (migration 20260820108) rather than a second key or scheme: same trust
-- boundary (mesh-api process memory), same MESH_INTEGRATION_ENCRYPTION_KEY,
-- same enc:v1: prefix, same DB-side "must be encrypted" trigger pattern so a
-- direct-SQL write can never bypass it the way the 2026-06 TR credentials
-- did. A general secret store is a second surface over that layer, not a
-- second layer.
--
-- value_sha256_prefix / value_length / value_char_class exist because the
-- masked list view (scripts/env-inventory.py's fields, reused verbatim) has
-- to answer "what does this look like" without ever decrypting -- and
-- ciphertext length/shape reveal nothing about the plaintext's. They are
-- computed once from the plaintext at write time and stored alongside the
-- ciphertext; there is no way to derive them later without defeating the
-- point of the table.
--
-- Scope + uniqueness mirrors the memories table's established pattern
-- (workspace_id/project_id/agent_id + scope enum, migrations 20260315041 and
-- 20260730088): one row identity per (scope tuple, name), latest wins.
--
-- Rotation is append, not update: "replace the value" inserts a NEW row and
-- stamps the OLD row's rotated_at, so a secret's write history is an audit
-- trail like activity_log, never an in-place overwrite. The partial unique
-- indexes below enforce "only one CURRENT row per name+scope" the same way
-- uq_mem_ws_key/uq_mem_proj_key/uq_mem_agent_key do for memories.

CREATE TABLE IF NOT EXISTS secrets (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id         UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id           UUID REFERENCES projects(id) ON DELETE CASCADE,
    agent_id             UUID REFERENCES agents(id) ON DELETE CASCADE,
    scope                TEXT NOT NULL CHECK (scope IN ('workspace', 'project', 'agent')),
    name                 TEXT NOT NULL CHECK (name ~ '^[A-Z][A-Z0-9_]*$'),
    encrypted_value      TEXT NOT NULL,
    value_sha256_prefix  CHAR(8) NOT NULL,
    value_length         INTEGER NOT NULL CHECK (value_length > 0),
    value_char_class     TEXT NOT NULL,
    expires_at           TIMESTAMPTZ,
    created_by           UUID NOT NULL,
    created_by_type      actor_type NOT NULL DEFAULT 'user',
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at           TIMESTAMPTZ,

    CONSTRAINT chk_secrets_scope_ref CHECK (
        (scope = 'workspace' AND project_id IS NULL AND agent_id IS NULL) OR
        (scope = 'project'   AND project_id IS NOT NULL AND agent_id IS NULL) OR
        (scope = 'agent'     AND agent_id IS NOT NULL AND project_id IS NULL)
    )
);

-- Partial unique indexes: exactly one CURRENT (rotated_at IS NULL) row per
-- name within its scope tuple. A rotated-out row keeps its name so history
-- stays queryable, but no longer competes for the identity.
CREATE UNIQUE INDEX IF NOT EXISTS uq_secrets_ws_name
    ON secrets (workspace_id, name)
    WHERE scope = 'workspace' AND rotated_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_secrets_proj_name
    ON secrets (workspace_id, project_id, name)
    WHERE scope = 'project' AND rotated_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_secrets_agent_name
    ON secrets (workspace_id, agent_id, name)
    WHERE scope = 'agent' AND rotated_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_secrets_workspace_scope ON secrets(workspace_id, scope);
CREATE INDEX IF NOT EXISTS idx_secrets_expires ON secrets(expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_secrets_current ON secrets(workspace_id, scope) WHERE rotated_at IS NULL;

COMMENT ON TABLE secrets IS
    'Write-only secret store -- the only legal channel for a human to hand an '
    'agent a credential (task #64e84eb1). encrypted_value is never returned by '
    'any read path; see S6''s negative test for the enumerated surfaces.';
COMMENT ON COLUMN secrets.encrypted_value IS
    'AES-256-GCM via pkg/encryption, enc:v1:<base64> -- same key and scheme as '
    'project_integrations.agent_key (migration 20260820108). Enforced by '
    'trg_secrets_require_encrypted_value below.';
COMMENT ON COLUMN secrets.rotated_at IS
    'Set when a newer row supersedes this one (replace = insert, not update). '
    'NULL means this row is the current value for its (scope, name).';

-- Same enforcement pattern as trg_project_integrations_require_encrypted_agent_key
-- (migration 20260820108): fires on every write path, not just the API's,
-- because a bulk INSERT straight to Postgres bypassing the service layer is
-- exactly how the 2026-06 project_integrations leak happened. No plaintext
-- escape hatch here at all -- unlike project_integrations, this table has no
-- "self-hosting with no key configured" mode to support, since it doesn't
-- exist before this feature does.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION secrets_require_encrypted_value()
RETURNS trigger AS $$
BEGIN
    IF NEW.encrypted_value !~ '^enc:v[0-9]+:' THEN
        RAISE EXCEPTION
            'secrets.encrypted_value must be encrypted (expected an enc:v<N>: prefix)'
            USING HINT = 'Write it through the Mesh API, which encrypts on the way in.',
                  ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER trg_secrets_require_encrypted_value
    BEFORE INSERT OR UPDATE ON secrets
    FOR EACH ROW
    EXECUTE FUNCTION secrets_require_encrypted_value();

-- +goose Down

DROP TRIGGER IF EXISTS trg_secrets_require_encrypted_value ON secrets;
DROP FUNCTION IF EXISTS secrets_require_encrypted_value();
DROP TABLE IF EXISTS secrets;
