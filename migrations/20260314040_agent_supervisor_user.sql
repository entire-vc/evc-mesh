-- +goose Up

-- Allow agents to have a human supervisor (workspace member) in addition to parent agent.
ALTER TABLE agents ADD COLUMN IF NOT EXISTS supervisor_user_id UUID REFERENCES users(id);

-- +goose Down
ALTER TABLE agents DROP COLUMN IF EXISTS supervisor_user_id;
