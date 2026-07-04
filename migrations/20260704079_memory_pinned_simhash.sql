-- +goose Up
-- Add content_simhash for 64-bit near-duplicate detection at write time.
-- P3: supports kind:pinned/kind:preference and simhash dedup (WI-3).
-- P1 freshness lifecycle (migration 080+) reuses this column; uses IF NOT EXISTS as safety.
ALTER TABLE memories ADD COLUMN IF NOT EXISTS content_simhash BIGINT DEFAULT NULL;

CREATE INDEX IF NOT EXISTS idx_memories_simhash
    ON memories(workspace_id, content_simhash) WHERE content_simhash IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_memories_simhash;
ALTER TABLE memories DROP COLUMN IF EXISTS content_simhash;
