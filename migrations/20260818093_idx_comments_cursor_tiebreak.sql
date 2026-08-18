-- +goose NO TRANSACTION
-- +goose Up
-- Covering indexes for the comment cursor sort (created_at DESC, id DESC).
-- Same pattern as idx_tasks_updated_at (migration 057, #a1012e55): comments
-- sharing a created_at (bulk insert, or plain microsecond collision) made
-- ORDER BY c.created_at DESC non-deterministic at page boundaries, and a
-- strict `created_at < cursor` comparison silently dropped the rest of the
-- tie group. CONCURRENTLY avoids a full-table lock on prod. See #c6dc694e.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_comments_author_created
    ON comments (author_id, created_at DESC, id DESC)
    WHERE is_internal = false;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_comments_visible_created
    ON comments (created_at DESC, id DESC)
    WHERE is_internal = false;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_comments_author_created;
DROP INDEX CONCURRENTLY IF EXISTS idx_comments_visible_created;
