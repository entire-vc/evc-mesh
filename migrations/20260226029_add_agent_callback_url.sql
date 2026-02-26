-- +goose Up
ALTER TABLE agents ADD COLUMN callback_url TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE agents DROP COLUMN IF EXISTS callback_url;
