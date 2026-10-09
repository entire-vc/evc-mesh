-- +goose Up
-- Global execution admission for the agent_runtime integration. Additive: no
-- existing table or column changes, and nothing reads these tables outside
-- the runtime reservation routes.
CREATE SEQUENCE runtime_reservation_fence_seq;

CREATE TABLE runtime_reservations (
    id UUID PRIMARY KEY,
    fence BIGINT NOT NULL UNIQUE,
    integration_id UUID NOT NULL REFERENCES integration_configs(id) ON DELETE CASCADE,
    binding_ref TEXT NOT NULL,
    agent_id UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    grant_id UUID NOT NULL REFERENCES agent_workspace_grants(id),
    grant_fingerprint TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    scope_digest TEXT NOT NULL,
    task_id UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    checkout_generation BIGINT NOT NULL CHECK (checkout_generation > 0),
    checkout_request_id UUID,
    controller_ref TEXT NOT NULL,
    worker_ref TEXT NOT NULL,
    profile_ref TEXT NOT NULL,
    catalog_revision BIGINT NOT NULL,
    catalog_digest TEXT NOT NULL,
    admission_revision BIGINT NOT NULL,
    profile_revision TEXT NOT NULL,
    pools JSONB NOT NULL,
    pool_set_digest TEXT NOT NULL,
    -- reserved -> consumed -> released; reserved -> expired | released.
    -- "reconcile" is reported for consumed rows past expires_at and is
    -- never stored: it stays occupied exactly like consumed.
    state TEXT NOT NULL CHECK (state IN ('reserved','consumed','released','expired')),
    expires_at TIMESTAMPTZ NOT NULL,
    receipt_id UUID,
    consumed_at TIMESTAMPTZ,
    release_id UUID,
    released_at TIMESTAMPTZ,
    release_proof_kind TEXT,
    release_evidence_ref TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (agent_id, idempotency_key)
);
-- One active writer per task and per controller worker, whatever workspace,
-- binding or controller connection the reservation came through.
CREATE UNIQUE INDEX runtime_reservations_active_task ON runtime_reservations(task_id)
    WHERE state IN ('reserved','consumed');
CREATE UNIQUE INDEX runtime_reservations_active_worker ON runtime_reservations(integration_id, controller_ref, worker_ref)
    WHERE state IN ('reserved','consumed');
CREATE INDEX runtime_reservations_active_agent ON runtime_reservations(agent_id)
    WHERE state IN ('reserved','consumed');
CREATE INDEX runtime_reservations_unconsumed_expiry ON runtime_reservations(expires_at)
    WHERE state = 'reserved';

-- A row exists only while its reservation occupies the pool. The slot number
-- is a database backstop for capacity: two claims can never share one slot.
CREATE TABLE runtime_reservation_pool_claims (
    reservation_id UUID NOT NULL REFERENCES runtime_reservations(id) ON DELETE CASCADE,
    pool_id TEXT NOT NULL,
    slot INT NOT NULL CHECK (slot > 0),
    PRIMARY KEY (reservation_id, pool_id),
    UNIQUE (pool_id, slot)
);

-- +goose Down
DROP TABLE runtime_reservation_pool_claims;
DROP TABLE runtime_reservations;
DROP SEQUENCE runtime_reservation_fence_seq;
