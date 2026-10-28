-- +goose Up

-- A catalog revision saved by an agent key under its operator's runtime_manager
-- grant keeps the operator in created_by (the user the change is made for) and
-- the agent here; NULL means a person saved it.
ALTER TABLE runtime_catalog_revisions ADD COLUMN author_agent_id UUID REFERENCES agents(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE runtime_catalog_revisions DROP COLUMN IF EXISTS author_agent_id;
