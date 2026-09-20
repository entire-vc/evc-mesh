-- +goose Up
-- Recall index over Mesh Docs (#154450b1, audit §4.5). One row per chunk of a
-- document, chunked by H2 section (long sections are split further by the same
-- sliding window memories use). Purely additive: nothing reads this table until
-- DOC_INDEX_RECALL is switched on, and nothing writes to it until
-- DOC_INDEX_WRITE is (§1b — schema lands before the code that uses it).
--
-- Kept OUT of `memories` on purpose: a doc chunk mirrored into memories would
-- inherit every memories query (list, stats, review triage, graph) and change
-- their behaviour. A separate table keeps memories' rules untouched.
--
-- search_vector uses the 'english' dictionary, the same one memories' BM25 arm
-- uses, so the two arms' ts_rank_cd scores are comparable.
-- embedding is base64(le float32), same encoding as memory_chunks; nullable so
-- an embedder outage still leaves the chunk findable by BM25.
CREATE TABLE IF NOT EXISTS document_chunks (
	id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	document_id     UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
	project_id      UUID NOT NULL,
	chunk_idx       INT NOT NULL CHECK (chunk_idx >= 0),
	heading         TEXT NOT NULL DEFAULT '',
	content         TEXT NOT NULL,
	doc_version     INT NOT NULL DEFAULT 0,
	search_vector   TSVECTOR GENERATED ALWAYS AS (
		to_tsvector('english', coalesce(heading, '') || ' ' || content)
	) STORED,
	embedding       TEXT,
	embedding_model TEXT,
	embedding_dim   INT,
	created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT uq_document_chunks_doc_idx UNIQUE (document_id, chunk_idx)
);

CREATE INDEX IF NOT EXISTS idx_document_chunks_search ON document_chunks USING GIN (search_vector);
CREATE INDEX IF NOT EXISTS idx_document_chunks_project ON document_chunks (project_id);

-- +goose Down
DROP TABLE IF EXISTS document_chunks;
