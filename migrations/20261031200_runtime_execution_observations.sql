-- +goose Up
-- Execution observation telemetry (docs/api/agent-executions.md). Read-only
-- evidence beside the runtime catalog: no table here is consulted by launch,
-- admission, leases or routing. Additive; nothing existing is altered.

-- One row per controller boot. generation is server-issued and monotone per
-- (integration, controller); it fences telemetry only, never a launch lease.
CREATE TABLE runtime_execution_instances (
    integration_id UUID NOT NULL REFERENCES integration_configs(id) ON DELETE CASCADE,
    controller_ref TEXT NOT NULL,
    instance_id TEXT NOT NULL,
    generation BIGINT NOT NULL CHECK (generation > 0),
    reporter_grant_id UUID NOT NULL,
    grant_fingerprint TEXT NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (integration_id, controller_ref, instance_id),
    UNIQUE (integration_id, controller_ref, generation)
);

-- Accepted report envelopes: instance+sequence idempotency and replay detection.
CREATE TABLE runtime_execution_reports (
    integration_id UUID NOT NULL REFERENCES integration_configs(id) ON DELETE CASCADE,
    controller_ref TEXT NOT NULL,
    instance_id TEXT NOT NULL,
    sequence BIGINT NOT NULL CHECK (sequence > 0),
    body_hash TEXT NOT NULL,
    reported_at TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (integration_id, controller_ref, instance_id, sequence)
);

-- Latest known state of one attempt, scoped by project and agent. Whitelisted
-- columns only: no environment, prompt, argv, path, account or credential.
CREATE TABLE runtime_execution_attempts (
    integration_id UUID NOT NULL REFERENCES integration_configs(id) ON DELETE CASCADE,
    controller_ref TEXT NOT NULL,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    agent_id UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    attempt_id TEXT NOT NULL,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    binding_ref TEXT NOT NULL,
    task_id UUID,
    instance_id TEXT NOT NULL,
    generation BIGINT NOT NULL,
    sequence BIGINT NOT NULL,
    turn_id TEXT,
    state TEXT NOT NULL CHECK (state IN ('starting','running','waiting','stopped','completed','failed')),
    route_source TEXT NOT NULL,
    route_revision BIGINT NOT NULL CHECK (route_revision >= 0),
    route_digest TEXT,
    route_profile_ref TEXT,
    route_harness TEXT,
    route_model TEXT,
    route_effort TEXT,
    observed_harness TEXT,
    observed_model TEXT,
    observed_effort TEXT,
    observed_source TEXT NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    provenance JSONB,
    reporter_grant_id UUID NOT NULL,
    grant_fingerprint TEXT NOT NULL,
    heartbeat_at TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (integration_id, controller_ref, project_id, agent_id, attempt_id)
);

CREATE INDEX runtime_execution_attempts_read
    ON runtime_execution_attempts (workspace_id, project_id, agent_id);

-- +goose Down
DROP TABLE IF EXISTS runtime_execution_attempts;
DROP TABLE IF EXISTS runtime_execution_reports;
DROP TABLE IF EXISTS runtime_execution_instances;
