-- +goose Up
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS dod_checks JSONB NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE tasks DROP COLUMN IF EXISTS dod_checks;
