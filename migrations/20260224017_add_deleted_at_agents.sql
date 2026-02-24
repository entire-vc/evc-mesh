-- +goose Up
ALTER TABLE agents ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE agents DROP COLUMN IF EXISTS deleted_at;
