package domain

import "github.com/google/uuid"

// DocumentChunk is one indexed slice of a Mesh Doc (table document_chunks).
// Embedding is nil when the embedder was unavailable at write time; the chunk is
// then reachable by the BM25 arm only.
type DocumentChunk struct {
	ID             uuid.UUID `db:"id"`
	DocumentID     uuid.UUID `db:"document_id"`
	ProjectID      uuid.UUID `db:"project_id"`
	ChunkIdx       int       `db:"chunk_idx"`
	Heading        string    `db:"heading"`
	Content        string    `db:"content"`
	DocVersion     int       `db:"doc_version"`
	Embedding      *string   `db:"embedding"`
	EmbeddingModel *string   `db:"embedding_model"`
	EmbeddingDim   *int      `db:"embedding_dim"`
}

// DocIndexStatus reports backfill progress: live documents against documents
// that carry an up-to-date chunk set.
type DocIndexStatus struct {
	LiveDocs    int `json:"live_docs"`
	IndexedDocs int `json:"indexed_docs"`
}
