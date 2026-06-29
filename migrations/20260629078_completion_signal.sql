-- +goose Up
ALTER TABLE tasks ADD COLUMN completion_signal bool NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE tasks DROP COLUMN completion_signal;
