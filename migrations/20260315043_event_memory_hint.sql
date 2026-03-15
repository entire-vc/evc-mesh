-- +goose Up
ALTER TABLE event_bus_messages ADD COLUMN IF NOT EXISTS memory_hint JSONB;

-- +goose Down
ALTER TABLE event_bus_messages DROP COLUMN IF EXISTS memory_hint;
