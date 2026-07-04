-- +goose Up
-- P1-A: Memory health status lifecycle columns.
-- content_simhash is added with IF NOT EXISTS since migration 079 (P3) may have already created it.
-- All other columns are new in this migration.

ALTER TABLE memories
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'stale', 'superseded', 'archived', 'conflicted', 'review_needed'));

ALTER TABLE memories
    ADD COLUMN IF NOT EXISTS freshness_score REAL NOT NULL DEFAULT 1.0
        CHECK (freshness_score >= 0.0 AND freshness_score <= 1.0);

ALTER TABLE memories
    ADD COLUMN IF NOT EXISTS superseded_by UUID
        REFERENCES memories(id) ON DELETE SET NULL;

ALTER TABLE memories
    ADD COLUMN IF NOT EXISTS valid_from TIMESTAMPTZ DEFAULT NULL;

ALTER TABLE memories
    ADD COLUMN IF NOT EXISTS valid_until TIMESTAMPTZ DEFAULT NULL;

-- content_simhash: may already exist from migration 079 (P3 simhash feature).
ALTER TABLE memories
    ADD COLUMN IF NOT EXISTS content_simhash BIGINT DEFAULT NULL;

-- Backfill: archived rows → status='archived', freshness_score=0.0.
-- Active rows already have status='active', freshness_score=1.0 from the column DEFAULT.
UPDATE memories SET status = 'archived', freshness_score = 0.0 WHERE archived = true;

CREATE INDEX IF NOT EXISTS idx_memories_status
    ON memories(workspace_id, status) WHERE status != 'archived';

CREATE INDEX IF NOT EXISTS idx_memories_superseded_by
    ON memories(superseded_by) WHERE superseded_by IS NOT NULL;

-- Partial index for simhash proximity queries (PG14+ bit_count).
-- IF NOT EXISTS: may already exist from migration 079.
CREATE INDEX IF NOT EXISTS idx_memories_simhash
    ON memories(workspace_id, content_simhash) WHERE content_simhash IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_memories_valid_until
    ON memories(valid_until) WHERE valid_until IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_memories_valid_until;
DROP INDEX IF EXISTS idx_memories_simhash;
DROP INDEX IF EXISTS idx_memories_superseded_by;
DROP INDEX IF EXISTS idx_memories_status;

ALTER TABLE memories DROP COLUMN IF EXISTS content_simhash;
ALTER TABLE memories DROP COLUMN IF EXISTS valid_until;
ALTER TABLE memories DROP COLUMN IF EXISTS valid_from;
ALTER TABLE memories DROP COLUMN IF EXISTS superseded_by;
ALTER TABLE memories DROP COLUMN IF EXISTS freshness_score;
ALTER TABLE memories DROP COLUMN IF EXISTS status;
