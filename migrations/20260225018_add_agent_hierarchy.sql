-- +goose Up
ALTER TABLE agents ADD COLUMN parent_agent_id UUID REFERENCES agents(id) ON DELETE SET NULL;
CREATE INDEX idx_agents_parent ON agents(parent_agent_id) WHERE parent_agent_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_agents_parent;
ALTER TABLE agents DROP COLUMN IF EXISTS parent_agent_id;
