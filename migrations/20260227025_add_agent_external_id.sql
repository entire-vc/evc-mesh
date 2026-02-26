-- +goose Up

-- Add optional external_agent_id for linking agents to external system identifiers.
ALTER TABLE agents
    ADD COLUMN IF NOT EXISTS external_agent_id TEXT;

-- +goose Down

ALTER TABLE agents
    DROP COLUMN IF EXISTS external_agent_id;
