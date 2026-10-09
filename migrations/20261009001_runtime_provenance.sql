-- +goose Up
-- Exact-artifact authorship attested by a catalog controller. Preview reads it
-- to decide reviewer independence; a client request can never supply it.
CREATE TABLE runtime_artifact_provenance (
    integration_id UUID NOT NULL REFERENCES integration_configs(id) ON DELETE CASCADE,
    artifact_id UUID NOT NULL,
    artifact_revision TEXT NOT NULL CHECK (artifact_revision <> ''),
    controller_ref TEXT NOT NULL,
    reporter_grant_id UUID NOT NULL REFERENCES agent_workspace_grants(id),
    grant_fingerprint TEXT NOT NULL,
    complete BOOLEAN NOT NULL,
    authors JSONB NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (integration_id, artifact_id, artifact_revision)
);

-- +goose Down
DROP TABLE runtime_artifact_provenance;
