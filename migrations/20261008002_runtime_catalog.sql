-- +goose Up
-- Runtime configuration extends existing integration connections and grants.
-- It creates no agent identity, workspace key or provider credential.
CREATE TABLE runtime_catalog_revisions (
    integration_id UUID NOT NULL REFERENCES integration_configs(id) ON DELETE CASCADE,
    revision BIGINT NOT NULL CHECK (revision > 0),
    digest TEXT NOT NULL CHECK (digest ~ '^[0-9a-f]{64}$'),
    config JSONB NOT NULL,
    enabled BOOLEAN NOT NULL,
    created_by UUID NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (integration_id, revision)
);

CREATE TABLE runtime_controller_reports (
    integration_id UUID NOT NULL REFERENCES integration_configs(id) ON DELETE CASCADE,
    controller_ref TEXT NOT NULL,
    reporter_grant_id UUID NOT NULL REFERENCES agent_workspace_grants(id),
    grant_fingerprint TEXT NOT NULL,
    report JSONB NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (integration_id, controller_ref)
);

CREATE TABLE runtime_binding_admissions (
    integration_id UUID NOT NULL REFERENCES integration_configs(id) ON DELETE CASCADE,
    binding_ref TEXT NOT NULL,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    agent_id UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    grant_id UUID NOT NULL REFERENCES agent_workspace_grants(id),
    grant_fingerprint TEXT NOT NULL,
    revision BIGINT NOT NULL CHECK (revision > 0),
    catalog_digest TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT false,
    permitted_profiles JSONB NOT NULL,
    updated_by UUID NOT NULL REFERENCES users(id),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (integration_id, binding_ref)
);
CREATE INDEX runtime_binding_admissions_receiver ON runtime_binding_admissions(workspace_id, agent_id);

-- +goose Down
-- No identity or existing integration is removed by rollback. The old API
-- continues to ignore the runtime provider; credentials were never persisted.
DROP TABLE runtime_binding_admissions;
DROP TABLE runtime_controller_reports;
DROP TABLE runtime_catalog_revisions;
