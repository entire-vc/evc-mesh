-- +goose Up
-- Latest detailed quota observation per controller, catalog pool and provider
-- window. Additive: the existing runtime_controller_reports table and the
-- report route are untouched. NULL measurements mean "no data", never 0.
CREATE TABLE runtime_quota_observations (
    integration_id UUID NOT NULL REFERENCES integration_configs(id) ON DELETE CASCADE,
    controller_ref TEXT NOT NULL,
    pool_ref TEXT NOT NULL,
    window_ref TEXT NOT NULL,
    provider TEXT NOT NULL,
    unit TEXT NOT NULL,
    used_fraction DOUBLE PRECISION CHECK (used_fraction IS NULL OR (used_fraction >= 0 AND used_fraction <= 1)),
    remaining DOUBLE PRECISION CHECK (remaining IS NULL OR remaining >= 0),
    reset_at TIMESTAMPTZ,
    observed_at TIMESTAMPTZ NOT NULL,
    source TEXT NOT NULL,
    confidence TEXT NOT NULL,
    max_age_seconds INTEGER NOT NULL CHECK (max_age_seconds > 0),
    status TEXT NOT NULL,
    error_kind TEXT,
    reporter_grant_id UUID NOT NULL,
    grant_fingerprint TEXT NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (integration_id, controller_ref, pool_ref, window_ref)
);

-- +goose Down
DROP TABLE runtime_quota_observations;
