-- +goose Up

-- Lead-accept context (contract CH→Mesh lead-accept-context/v1,
-- docs/lead-accept-context.md).
--
-- Two kinds of service principal, separate from gate intake on purpose: a
-- gate-intake token (mgi_) and its principals are not touched, and a
-- lead-accept token never reaches the gate-intake route.
--   reader    (mlr_): only GET /api/v1/lead-accept-context, bound immutably to
--             one exact request tuple (binding + binding_sha256) and to the
--             workspace and evidence project it may read.
--   publisher (mlp_): only POST /api/v1/lead-accept-registry/attestations.
-- Tokens are issued, rotated and revoked only host-locally
-- (`mesh-api lead-accept …`); only their SHA-256 is stored.
CREATE TABLE IF NOT EXISTS lead_accept_principals (
    id                    UUID PRIMARY KEY,
    name                  TEXT NOT NULL UNIQUE,
    kind                  TEXT NOT NULL CHECK (kind IN ('reader', 'publisher')),
    workspace_id          UUID REFERENCES workspaces(id),
    evidence_project_id   UUID REFERENCES projects(id),
    binding               JSONB,
    binding_sha256        TEXT CHECK (binding_sha256 ~ '^[0-9a-f]{64}$'),
    token_hash            TEXT UNIQUE,
    token_prefix          TEXT,
    previous_token_hash   TEXT UNIQUE,
    previous_valid_until  TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL,
    issued_at             TIMESTAMPTZ,
    revoked_at            TIMESTAMPTZ,
    revoke_reason         TEXT,
    CONSTRAINT lead_accept_principals_name_ck CHECK (name ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
    CONSTRAINT lead_accept_principals_live_token_ck CHECK (revoked_at IS NOT NULL OR token_hash IS NOT NULL),
    -- A reader always carries its whole binding; a publisher never has one.
    CONSTRAINT lead_accept_principals_binding_ck CHECK (
        (kind = 'reader' AND binding IS NOT NULL AND binding_sha256 IS NOT NULL
             AND workspace_id IS NOT NULL AND evidence_project_id IS NOT NULL)
        OR (kind = 'publisher' AND binding IS NULL AND binding_sha256 IS NULL
             AND workspace_id IS NULL AND evidence_project_id IS NULL))
);

-- Operator actions on principals (issue, rearm, rotate, revoke, show). Never holds a token.
CREATE TABLE IF NOT EXISTS lead_accept_audit (
    id           BIGSERIAL PRIMARY KEY,
    principal_id UUID NOT NULL REFERENCES lead_accept_principals(id),
    operator_id  UUID NOT NULL,
    action       TEXT NOT NULL CHECK (action IN ('issue', 'rearm', 'rotate', 'revoke', 'show')),
    reason       TEXT,
    token_prefix TEXT,
    details      JSONB NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS lead_accept_audit_principal_idx ON lead_accept_audit (principal_id, id);

-- Every accepted registry observation of a publisher is a new row, never
-- updated. The current one is the row with the greatest observed_at. Rows are
-- kept at least 7 days (pruned after 30, never the current one).
CREATE TABLE IF NOT EXISTS lead_accept_observations (
    observation_id UUID PRIMARY KEY,
    principal_id   UUID NOT NULL REFERENCES lead_accept_principals(id),
    observed_at    TIMESTAMPTZ NOT NULL,
    received_at    TIMESTAMPTZ NOT NULL,
    ok             BOOLEAN NOT NULL,
    in_sync        BOOLEAN NOT NULL,
    error_step     TEXT,
    body           JSONB NOT NULL,
    CONSTRAINT lead_accept_observations_ok_ck CHECK (ok OR (NOT in_sync AND error_step IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS lead_accept_observations_observed_idx ON lead_accept_observations (observed_at DESC);
CREATE INDEX IF NOT EXISTS lead_accept_observations_received_idx ON lead_accept_observations (received_at);

-- +goose Down
DROP TABLE IF EXISTS lead_accept_observations;
DROP TABLE IF EXISTS lead_accept_audit;
DROP TABLE IF EXISTS lead_accept_principals;
