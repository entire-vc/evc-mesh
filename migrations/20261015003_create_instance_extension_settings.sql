-- +goose Up
CREATE TABLE instance_extension_settings (
    extension_id VARCHAR(64) PRIMARY KEY,
    settings     JSONB NOT NULL DEFAULT '{}'::jsonb,
    enabled      BOOLEAN NOT NULL DEFAULT false,
    source       VARCHAR(16) NOT NULL CHECK (source IN ('env', 'admin')),
    updated_by   UUID,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS instance_extension_settings;
