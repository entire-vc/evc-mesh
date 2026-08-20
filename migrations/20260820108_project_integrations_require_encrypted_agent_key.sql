-- +goose Up

-- Refuse to store a Team Relay credential in the clear, on every write path.
--
-- Mesh has encrypted project_integrations.agent_key since 2026-05 — in
-- ProjectIntegrationRepo, which is one of the ways a row gets written. It is
-- not the way the production rows got written. Ten of the eleven credentials
-- live on prod today share a single created_at to the microsecond
-- (2026-05-31 18:24:23.48641+00) and carry created_by = NULL: one bulk INSERT
-- issued straight to Postgres, which never passed through the repository and
-- therefore never passed through the encryption. The domain struct said
-- "encrypted in DB" the whole time, and for those rows it was decoration.
--
-- An application-level guard cannot fix that, because the application was
-- never in the path. This trigger is: it fires wherever the row is written
-- from — API, psql, an ops script, a future service.
--
-- Scope is deliberately narrow. It fires only when a credential is being SET
-- or CHANGED, so the pre-existing plaintext rows keep working, and an UPDATE
-- that touches only `enabled` or `settings` still succeeds. That is what lets
-- this ship before the backfill (cmd/encrypt-integration-keys) rather than
-- after it: enforce forward, migrate the tail separately.
--
-- The escape hatch exists for one caller. A deployment with no
-- MESH_INTEGRATION_ENCRYPTION_KEY configured has nothing to encrypt with, and
-- self-hosting has always documented that as a supported (warned-about) mode;
-- ProjectIntegrationRepo sets mesh.allow_plaintext_agent_key for exactly that
-- case, inside the writing transaction. Nobody typing SQL by hand sets it,
-- which is the entire point.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION project_integrations_require_encrypted_agent_key()
RETURNS trigger AS $$
BEGIN
    IF NEW.agent_key IS NULL OR NEW.agent_key = '' THEN
        RETURN NEW;
    END IF;

    -- Untouched credential on an UPDATE: not this trigger's business.
    IF TG_OP = 'UPDATE' AND NEW.agent_key IS NOT DISTINCT FROM OLD.agent_key THEN
        RETURN NEW;
    END IF;

    -- Version-agnostic on purpose: enc:v2: must not require a migration here.
    IF NEW.agent_key ~ '^enc:v[0-9]+:' THEN
        RETURN NEW;
    END IF;

    IF current_setting('mesh.allow_plaintext_agent_key', true) = 'on' THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION
        'project_integrations.agent_key must be encrypted (expected an enc:v<N>: prefix)'
        USING HINT = 'Write it through the Mesh API, which encrypts on the way in. '
                     'Rewriting existing plaintext rows is cmd/encrypt-integration-keys.',
              ERRCODE = 'check_violation';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_project_integrations_require_encrypted_agent_key ON project_integrations;

CREATE TRIGGER trg_project_integrations_require_encrypted_agent_key
    BEFORE INSERT OR UPDATE ON project_integrations
    FOR EACH ROW
    EXECUTE FUNCTION project_integrations_require_encrypted_agent_key();

COMMENT ON COLUMN project_integrations.agent_key IS
    'Integration credential, AES-256-GCM encrypted by pkg/encryption and stored as enc:v<N>:<base64>. '
    'Enforced on every write path by trg_project_integrations_require_encrypted_agent_key. '
    'Rows predating 2026-08 may still hold plaintext until cmd/encrypt-integration-keys has run.';

-- +goose Down

DROP TRIGGER IF EXISTS trg_project_integrations_require_encrypted_agent_key ON project_integrations;
DROP FUNCTION IF EXISTS project_integrations_require_encrypted_agent_key();
COMMENT ON COLUMN project_integrations.agent_key IS NULL;
