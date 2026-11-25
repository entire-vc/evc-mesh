-- +goose Up
-- Hourly history of provider quota windows per catalog pool, written in the
-- same transaction as runtime_quota_observations (which keeps only the latest
-- observation). quota_window is the normalized window (5h, week, month,
-- budget, other); window_raw keeps what the controller sent. A used value is
-- NULL when the source gave no measurement: "no data" is never stored as 0.
-- Rows older than 400 days are deleted by the writer.
CREATE TABLE runtime_quota_hourly (
    integration_id UUID NOT NULL REFERENCES integration_configs(id) ON DELETE CASCADE,
    pool_ref TEXT NOT NULL,
    quota_window TEXT NOT NULL CHECK (quota_window IN ('5h','week','month','budget','other')),
    hour TIMESTAMPTZ NOT NULL CHECK (hour = date_trunc('hour', hour AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'),
    window_raw TEXT NOT NULL,
    used_last DOUBLE PRECISION CHECK (used_last IS NULL OR (used_last >= 0 AND used_last <= 1)),
    used_max DOUBLE PRECISION CHECK (used_max IS NULL OR (used_max >= 0 AND used_max <= 1)),
    reset_at_last TIMESTAMPTZ,
    status_last TEXT NOT NULL,
    observations INTEGER NOT NULL DEFAULT 1 CHECK (observations > 0),
    controller_ref_last TEXT NOT NULL,
    observed_at_last TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (integration_id, pool_ref, quota_window, hour)
);

CREATE INDEX runtime_quota_hourly_integration_hour_idx ON runtime_quota_hourly (integration_id, hour);

-- +goose Down
DROP TABLE runtime_quota_hourly;
