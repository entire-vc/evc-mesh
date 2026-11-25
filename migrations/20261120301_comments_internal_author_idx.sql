-- +goose NO TRANSACTION
-- +goose Up
-- The recall usage job (memory_read_usage, U3) reads one agent's comments in a
-- two-hour window. idx_comments_author_created covers only is_internal = false;
-- this partial index is its counterpart for the few internal comments, so the
-- job never seq-scans comments for them.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_comments_internal_author_created
    ON comments (author_id, created_at)
    WHERE is_internal = true;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_comments_internal_author_created;
