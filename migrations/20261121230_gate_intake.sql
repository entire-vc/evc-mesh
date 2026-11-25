-- +goose Up

-- Gate intake (contract CH→Mesh gate-create/v1, docs/gate-intake.md).
--
-- A gate-intake principal is a service credential that can do exactly one
-- thing: open a gate card through POST /api/v1/gate-intake. Where the card
-- goes (workspace, project, assignee, labels, priority) is bound here, on the
-- server; the caller cannot choose it. Tokens are issued, rotated and revoked
-- only host-locally (`mesh-api gate-intake …`); only their SHA-256 is stored.
--
-- The id of a principal never changes: rotate, revoke and a later issue of the
-- same name keep it, so the receipt namespace (principal_id, approval_id) is
-- stable across key changes.
CREATE TABLE IF NOT EXISTS gate_intake_principals (
    id                   UUID PRIMARY KEY,
    name                 TEXT NOT NULL UNIQUE,
    workspace_id         UUID NOT NULL REFERENCES workspaces(id),
    project_id           UUID NOT NULL REFERENCES projects(id),
    assignee_agent_id    UUID NOT NULL REFERENCES agents(id),
    labels               TEXT[] NOT NULL DEFAULT '{}',
    priority             task_priority NOT NULL DEFAULT 'high',
    schema               TEXT NOT NULL,
    token_hash           TEXT UNIQUE,
    token_prefix         TEXT,
    previous_token_hash  TEXT UNIQUE,
    previous_valid_until TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    issued_at            TIMESTAMPTZ,
    revoked_at           TIMESTAMPTZ,
    revoke_reason        TEXT,
    CONSTRAINT gate_intake_principals_name_ck CHECK (name ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
    CONSTRAINT gate_intake_principals_live_token_ck CHECK (revoked_at IS NOT NULL OR token_hash IS NOT NULL)
);

-- One receipt per (principal, approval): the idempotency key of the intake.
-- Written in the same transaction as the card. task_id has no foreign key on
-- purpose: the receipt outlives the card (a deleted card is still the card that
-- approval opened) and is never cascaded away. No TTL.
CREATE TABLE IF NOT EXISTS gate_intake_receipts (
    principal_id   UUID NOT NULL REFERENCES gate_intake_principals(id),
    approval_id    UUID NOT NULL,
    request_sha256 TEXT NOT NULL CHECK (request_sha256 ~ '^[0-9a-f]{64}$'),
    task_id        UUID NOT NULL,
    response       JSONB NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (principal_id, approval_id)
);

-- Operator actions on principals (issue, rotate, revoke). Never holds a token.
CREATE TABLE IF NOT EXISTS gate_intake_audit (
    id           BIGSERIAL PRIMARY KEY,
    principal_id UUID NOT NULL REFERENCES gate_intake_principals(id),
    operator_id  UUID NOT NULL,
    action       TEXT NOT NULL CHECK (action IN ('issue', 'rearm', 'rotate', 'revoke')),
    reason       TEXT,
    token_prefix TEXT,
    details      JSONB NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS gate_intake_audit_principal_idx ON gate_intake_audit (principal_id, created_at);

-- +goose Down
DROP TABLE IF EXISTS gate_intake_audit;
DROP TABLE IF EXISTS gate_intake_receipts;
DROP TABLE IF EXISTS gate_intake_principals;
