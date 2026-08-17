-- +goose NO TRANSACTION
-- +goose Up
-- Expression GIN index for the 'english'-dictionary FTS arm of memory recall.
--
-- MemoryRepo.FullTextSearchRanked (and its OR-token fallback) deliberately does
-- NOT use the stored memories.search_vector column: that column is built with
-- the 'simple' dictionary (migration 20260315041), so it has no stemming and no
-- stopword removal. The ranked arm re-computes the tsvector on the fly with the
-- 'english' dictionary instead — which, without a matching index, means a seq
-- scan that recomputes to_tsvector for EVERY live row on EVERY /memories/search
-- call (measured: ~800 ms of the 1.3-1.7 s endpoint on ~3.5k rows).
--
-- The expression below must stay byte-identical to the one in
-- internal/repository/postgres/memory_repo.go (FullTextSearchRanked and
-- ftsRankedORFallback), or the planner will not match it and will silently fall
-- back to the seq scan.
--
-- Not a partial index: the WHERE clauses of the two queries differ (archived /
-- status / expires_at / scope / tags are all dynamic), and the recheck on a GIN
-- bitmap scan is cheap compared with recomputing the vector.
--
-- CONCURRENTLY so prod does not take a write lock on memories.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_memories_fts_english
    ON memories USING GIN (to_tsvector('english', content || ' ' || COALESCE(key, '')));

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_memories_fts_english;
