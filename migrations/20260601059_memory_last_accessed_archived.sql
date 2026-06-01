-- +goose Up
ALTER TABLE memories ADD COLUMN IF NOT EXISTS last_accessed_at TIMESTAMP DEFAULT NULL;
ALTER TABLE memories ADD COLUMN IF NOT EXISTS archived BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX IF NOT EXISTS idx_memories_archived ON memories(workspace_id, archived);
CREATE INDEX IF NOT EXISTS idx_memories_decay ON memories(workspace_id, last_accessed_at) WHERE archived = false;

-- +goose Down
DROP INDEX IF EXISTS idx_memories_decay;
DROP INDEX IF EXISTS idx_memories_archived;
ALTER TABLE memories DROP COLUMN IF EXISTS archived;
ALTER TABLE memories DROP COLUMN IF EXISTS last_accessed_at;
