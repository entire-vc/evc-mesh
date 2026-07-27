-- +goose Up
-- Per-chunk embeddings for long memories (ADR-0002). The prod embedder
-- silently truncates input at 512 tokens, so a memory longer than that only
-- ever had its first ~15% embedded — see #e8063a65. One row per chunk lets
-- VectorSearch score every chunk and take the max per memory_id, and is the
-- only storage shape that survives an eventual move to pgvector/ANN (which
-- requires one vector per row; a vector-array column can never be indexed).
--
-- embedding is TEXT holding base64(little-endian float32 bytes), not a JSON
-- array — measured 66x cheaper to decode than JSON (618ns vs 41028ns for a
-- 384-dim vector) and chunking multiplies vector count ~1.75x, so encoding
-- cost is the dominant expense at this row count, not the cosine math itself.
--
-- chunk_start/chunk_end are BYTE offsets (half-open [start, end)) into
-- memories.content, matching how Postgres substring() and most tooling index
-- text — NOT rune offsets, even though the chunker that produces them
-- operates on runes internally to avoid splitting a multi-byte character.
--
-- No FK-owned copy of memories' scope/workspace/tags filters: the read path
-- joins to memories for eligibility filtering, which is already applied
-- before this table is touched — denormalizing those columns here would only
-- create a second copy that can drift, and a stale copy here would mean
-- serving archived or expired memory content, a correctness bug, not a
-- performance one.
CREATE TABLE IF NOT EXISTS memory_chunks (
	id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	memory_id       UUID NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
	chunk_idx       INT NOT NULL CHECK (chunk_idx >= 0),
	chunk_start     INT NOT NULL CHECK (chunk_start >= 0),
	chunk_end       INT NOT NULL,
	embedding       TEXT NOT NULL,
	embedding_model TEXT NOT NULL,
	embedding_dim   INT NOT NULL,
	created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT uq_memory_chunks_memory_idx UNIQUE (memory_id, chunk_idx),
	CONSTRAINT chk_memory_chunks_range CHECK (chunk_end > chunk_start)
);

-- No separate index on memory_id: uq_memory_chunks_memory_idx's own unique
-- index is (memory_id, chunk_idx), so memory_id-only lookups (the delete+
-- reinsert write path, the read path's per-memory chunk fetch) already use
-- it as a leading-column match — a second single-column index would just be
-- a redundant write cost.

-- +goose Down
DROP TABLE IF EXISTS memory_chunks;
