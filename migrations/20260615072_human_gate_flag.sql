-- +goose Up
ALTER TABLE tasks ADD COLUMN human_gate BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE tasks DROP COLUMN human_gate;
