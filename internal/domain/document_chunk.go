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
// that carry an up-to-date chunk set. ExcludedDocs are live documents of projects
// kept out of the recall index on purpose (see DocProjectExcluded); they are NOT
// counted in LiveDocs, and are reported so the subtraction is never silent.
type DocIndexStatus struct {
	LiveDocs     int `json:"live_docs"`
	IndexedDocs  int `json:"indexed_docs"`
	ExcludedDocs int `json:"excluded_docs"`
}

// DocViewer is who a recall is served to, as far as Mesh Docs are concerned. Docs
// belong to projects, and projects have members, so a doc chunk may only be shown to
// someone who could open that document: a member of its project, or a human
// workspace owner/admin (AllProjects). The zero value sees NOTHING — a recall path
// that does not say who is asking (internal seeds, graph expansion) gets no docs,
// rather than every doc in the workspace.
type DocViewer struct {
	AgentID     *uuid.UUID
	UserID      *uuid.UUID
	AllProjects bool
}

// IsZero reports a viewer with no identity and no blanket access.
func (v DocViewer) IsZero() bool { return v.AgentID == nil && v.UserID == nil && !v.AllProjects }
