-- +goose Up
ALTER TABLE memories ADD COLUMN IF NOT EXISTS source_url TEXT;
CREATE INDEX IF NOT EXISTS idx_memories_workspace_created ON memories(workspace_id, created_at);
CREATE INDEX IF NOT EXISTS idx_memories_workspace_project_created ON memories(workspace_id, project_id, created_at) WHERE project_id IS NOT NULL;
-- Note: GIN index on tags already exists (idx_memories_tags)

-- +goose Down
DROP INDEX IF EXISTS idx_memories_workspace_project_created;
DROP INDEX IF EXISTS idx_memories_workspace_created;
ALTER TABLE memories DROP COLUMN IF EXISTS source_url;
