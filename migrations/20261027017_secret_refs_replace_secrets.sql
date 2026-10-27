-- +goose Up

-- Workspace secrets live in Keep only (W3.6b step 3, an internal ticket).
--
-- After step 2 every current row of `secrets` carries keep_ref and is served
-- from Keep; the builtin ciphertext is no longer read. This migration drops the
-- table with its ciphertext and keeps what Mesh still has to know itself, since
-- Keep cannot list a workspace's secrets: which names exist in which scope, the
-- Keep ref of each, the masked metadata the list shows, and the write history.
-- secret_refs has no column a value or a ciphertext could occupy.
--
-- Fail closed: a current row without a Keep reference would lose its value
-- here, so the migration refuses and names the count, the same figure the
-- pre-rollout `migrate-secrets-to-keep` dry-run prints. Rotated rows without a
-- ref are old builtin versions nothing resolves any more; they go with the
-- table (pg_dump -t secrets before the rollout is their backup).
--
-- Before the rollout, on the prod DB host:
--   pg_dump -Fc -t secrets mesh > secrets-pre-drop.dump
--
-- Rollback:
--   1. Put the previous mesh-api image back.
--   2. goose -dir migrations down-to 20261026001
--      (Down below restores secrets with every ref row, served from Keep.)
--   3. Only if the builtin ciphertext is needed:
--      psql mesh -c 'TRUNCATE secrets'
--      pg_restore -d mesh --data-only -t secrets secrets-pre-drop.dump
-- +goose StatementBegin
DO $$
DECLARE
    pending integer;
BEGIN
    SELECT count(*) INTO pending FROM secrets WHERE rotated_at IS NULL AND keep_ref IS NULL;
    IF pending > 0 THEN
        RAISE EXCEPTION '% current secret(s) without a Keep reference', pending
            USING HINT = 'Copy them into Keep with migrate-secrets-to-keep --apply before this migration.';
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE TABLE secret_refs (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id         UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id           UUID REFERENCES projects(id) ON DELETE CASCADE,
    agent_id             UUID REFERENCES agents(id) ON DELETE CASCADE,
    scope                TEXT NOT NULL CHECK (scope IN ('workspace', 'project', 'agent')),
    name                 TEXT NOT NULL CHECK (name ~ '^[A-Z][A-Z0-9_]*$'),
    keep_ref             TEXT NOT NULL,
    value_sha256_prefix  CHAR(8) NOT NULL,
    value_length         INTEGER NOT NULL CHECK (value_length > 0),
    value_char_class     TEXT NOT NULL,
    expires_at           TIMESTAMPTZ,
    created_by           UUID NOT NULL,
    created_by_type      actor_type NOT NULL DEFAULT 'user',
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at           TIMESTAMPTZ,

    CONSTRAINT chk_secret_refs_scope_ref CHECK (
        (scope = 'workspace' AND project_id IS NULL AND agent_id IS NULL) OR
        (scope = 'project'   AND project_id IS NOT NULL AND agent_id IS NULL) OR
        (scope = 'agent'     AND agent_id IS NOT NULL AND project_id IS NULL)
    ),
    -- Same alphabet as keepRefRe in internal/extensions/secretstore/keep_http.go.
    CONSTRAINT chk_secret_refs_keep_ref_shape CHECK (
        keep_ref ~ '^keep:[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'
        AND position('..' in keep_ref) = 0
    )
);

CREATE UNIQUE INDEX uq_secret_refs_ws_name
    ON secret_refs (workspace_id, name)
    WHERE scope = 'workspace' AND rotated_at IS NULL;

CREATE UNIQUE INDEX uq_secret_refs_proj_name
    ON secret_refs (workspace_id, project_id, name)
    WHERE scope = 'project' AND rotated_at IS NULL;

CREATE UNIQUE INDEX uq_secret_refs_agent_name
    ON secret_refs (workspace_id, agent_id, name)
    WHERE scope = 'agent' AND rotated_at IS NULL;

CREATE INDEX idx_secret_refs_workspace_scope ON secret_refs(workspace_id, scope);
CREATE INDEX idx_secret_refs_current ON secret_refs(workspace_id, scope) WHERE rotated_at IS NULL;

-- Ids are kept, so issuance journal entries (activity_log, secret_id) still
-- name the same rows.
INSERT INTO secret_refs (
    id, workspace_id, project_id, agent_id, scope, name, keep_ref,
    value_sha256_prefix, value_length, value_char_class, expires_at,
    created_by, created_by_type, created_at, rotated_at
)
SELECT id, workspace_id, project_id, agent_id, scope, name, keep_ref,
       value_sha256_prefix, value_length, value_char_class, expires_at,
       created_by, created_by_type, created_at, rotated_at
  FROM secrets
 WHERE keep_ref IS NOT NULL;

COMMENT ON TABLE secret_refs IS
    'Index of workspace secrets held by Keep: name, scope, Keep ref '
    'and masked metadata. Values live in Keep only; nothing here can hold one.';
COMMENT ON COLUMN secret_refs.rotated_at IS
    'Set when a newer row supersedes this one (replace = insert, not update). '
    'NULL means this row is the current entry for its (scope, name).';

DROP TABLE secrets;
DROP FUNCTION IF EXISTS secrets_require_encrypted_value();

-- +goose Down

-- Restores `secrets` in the shape of 20261026001 with every ref row, so the
-- previous binary serves them from Keep exactly as after step 2: it resolves a
-- row through keep_ref and never decrypts it. encrypted_value is a placeholder
-- that satisfies the enc:v<N>: trigger and decrypts to nothing; the real
-- builtin ciphertext comes back only from the pg_dump taken before the rollout.

CREATE TABLE secrets (
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
    keep_ref             TEXT,

    CONSTRAINT chk_secrets_scope_ref CHECK (
        (scope = 'workspace' AND project_id IS NULL AND agent_id IS NULL) OR
        (scope = 'project'   AND project_id IS NOT NULL AND agent_id IS NULL) OR
        (scope = 'agent'     AND agent_id IS NOT NULL AND project_id IS NULL)
    ),
    CONSTRAINT chk_secrets_keep_ref_shape CHECK (keep_ref IS NULL OR (
        keep_ref ~ '^keep:[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'
        AND position('..' in keep_ref) = 0
    ))
);

CREATE UNIQUE INDEX uq_secrets_ws_name
    ON secrets (workspace_id, name)
    WHERE scope = 'workspace' AND rotated_at IS NULL;

CREATE UNIQUE INDEX uq_secrets_proj_name
    ON secrets (workspace_id, project_id, name)
    WHERE scope = 'project' AND rotated_at IS NULL;

CREATE UNIQUE INDEX uq_secrets_agent_name
    ON secrets (workspace_id, agent_id, name)
    WHERE scope = 'agent' AND rotated_at IS NULL;

CREATE INDEX idx_secrets_workspace_scope ON secrets(workspace_id, scope);
CREATE INDEX idx_secrets_expires ON secrets(expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX idx_secrets_current ON secrets(workspace_id, scope) WHERE rotated_at IS NULL;

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

INSERT INTO secrets (
    id, workspace_id, project_id, agent_id, scope, name, encrypted_value,
    value_sha256_prefix, value_length, value_char_class, expires_at,
    created_by, created_by_type, created_at, rotated_at, keep_ref
)
SELECT id, workspace_id, project_id, agent_id, scope, name, 'enc:v0:keep-only',
       value_sha256_prefix, value_length, value_char_class, expires_at,
       created_by, created_by_type, created_at, rotated_at, keep_ref
  FROM secret_refs;

DROP TABLE secret_refs;
