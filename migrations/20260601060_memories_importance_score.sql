-- +goose Up
ALTER TABLE memories
    ADD COLUMN IF NOT EXISTS importance_score REAL NOT NULL DEFAULT 0.5
        CHECK (importance_score >= 0 AND importance_score <= 1);

CREATE INDEX IF NOT EXISTS idx_memories_importance
    ON memories(workspace_id, importance_score DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_memories_importance;
ALTER TABLE memories DROP COLUMN IF EXISTS importance_score;
