package postgres

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// docChunkImportance is the importance_score stamped on doc-chunk hits. Above
// the recall default threshold (0.3) so applyExtendedFilters keeps them, below
// kind:decision-class memories so they never outrank curated knowledge on score.
const docChunkImportance float32 = 0.5

// DocumentChunkRepo implements repository.DocumentChunkRepository.
type DocumentChunkRepo struct {
	db *sqlx.DB
}

func NewDocumentChunkRepo(db *sqlx.DB) *DocumentChunkRepo { return &DocumentChunkRepo{db: db} }

func (r *DocumentChunkRepo) ReplaceChunks(ctx context.Context, documentID, projectID uuid.UUID, version int, chunks []domain.DocumentChunk) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("doc chunks replace: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // best-effort rollback on error path

	// Serialise writers of one document, then refuse to go backwards.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, documentID.String()); err != nil {
		return fmt.Errorf("doc chunks replace: lock: %w", err)
	}
	var stored int
	if err := tx.GetContext(ctx, &stored, `SELECT coalesce(max(doc_version), -1) FROM document_chunks WHERE document_id = $1`, documentID); err != nil {
		return fmt.Errorf("doc chunks replace: read version: %w", err)
	}
	if stored > version {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM document_chunks WHERE document_id = $1`, documentID); err != nil {
		return fmt.Errorf("doc chunks replace: delete: %w", err)
	}
	const ins = `INSERT INTO document_chunks
		(document_id, project_id, chunk_idx, heading, content, doc_version, embedding, embedding_model, embedding_dim)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`
	for _, c := range chunks {
		if _, err := tx.ExecContext(ctx, ins, documentID, projectID, c.ChunkIdx, c.Heading, c.Content, version,
			c.Embedding, c.EmbeddingModel, c.EmbeddingDim); err != nil {
			return fmt.Errorf("doc chunks replace: insert idx=%d: %w", c.ChunkIdx, err)
		}
	}
	return tx.Commit()
}

func (r *DocumentChunkRepo) DeleteByDocument(ctx context.Context, documentID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM document_chunks WHERE document_id = $1`, documentID)
	return err
}

func (r *DocumentChunkRepo) PurgeDeleted(ctx context.Context) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM document_chunks c USING documents d WHERE d.id = c.document_id AND d.deleted_at IS NOT NULL`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

type docChunkHit struct {
	ID         uuid.UUID `db:"id"`
	DocumentID uuid.UUID `db:"document_id"`
	ProjectID  uuid.UUID `db:"project_id"`
	Slug       string    `db:"slug"`
	Title      string    `db:"title"`
	Heading    string    `db:"heading"`
	Content    string    `db:"content"`
	Version    int       `db:"doc_version"`
	UpdatedAt  time.Time `db:"updated_at"`
	Score      float64   `db:"score"`
}

func (h docChunkHit) toScored(wsID uuid.UUID) domain.ScoredMemory {
	docID := h.DocumentID
	proj := h.ProjectID
	key := "doc/" + h.Slug
	if h.Heading != "" {
		key += "#" + h.Heading
	}
	return domain.ScoredMemory{
		Memory: domain.Memory{
			ID:              h.ID,
			WorkspaceID:     wsID,
			ProjectID:       &proj,
			Key:             key,
			Content:         h.Content,
			Scope:           domain.ScopeWorkspace,
			Tags:            []string{"source:doc"},
			SourceType:      domain.SourceDoc,
			Relevance:       1,
			ImportanceScore: docChunkImportance,
			CreatedAt:       h.UpdatedAt,
			UpdatedAt:       h.UpdatedAt,
			Status:          domain.MemoryStatusActive,
			FreshnessScore:  1,
			SourceDocID:     &docID,
			DocSlug:         h.Slug,
			DocHeading:      h.Heading,
		},
		Score: h.Score,
	}
}

const docChunkSelect = `c.id, c.document_id, c.project_id, d.slug, d.title, c.heading, c.content, c.doc_version, d.updated_at`
const docChunkFrom = `FROM document_chunks c
	JOIN documents d ON d.id = c.document_id AND d.deleted_at IS NULL
	JOIN projects p ON p.id = d.project_id AND p.workspace_id = $1`

func (r *DocumentChunkRepo) FullTextSearch(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID, query string, limit int) ([]domain.ScoredMemory, error) {
	if limit <= 0 {
		limit = 20
	}
	run := func(tsq string) ([]docChunkHit, error) {
		args := []interface{}{wsID, query}
		where := "c.search_vector @@ " + tsq
		if projID != nil {
			args = append(args, *projID)
			where += fmt.Sprintf(" AND c.project_id = $%d", len(args))
		}
		args = append(args, limit)
		q := fmt.Sprintf(`SELECT %s, ts_rank_cd(c.search_vector, %s) AS score %s WHERE %s ORDER BY score DESC LIMIT $%d`,
			docChunkSelect, tsq, docChunkFrom, where, len(args))
		var rows []docChunkHit
		err := r.db.SelectContext(ctx, &rows, q, args...)
		return rows, err
	}
	rows, err := run(`plainto_tsquery('english', $2)`)
	if err != nil {
		return nil, fmt.Errorf("doc chunks fts: %w", err)
	}
	if len(rows) < minFTSHits {
		// Same relaxation memories' BM25 arm applies: AND → OR of the tokens.
		if orRows, err2 := run(`to_tsquery('english', regexp_replace(plainto_tsquery('english', $2)::text, ' & ', ' | ', 'g'))`); err2 == nil {
			rows = orRows
		}
	}
	out := make([]domain.ScoredMemory, len(rows))
	for i, h := range rows {
		out[i] = h.toScored(wsID)
	}
	return out, nil
}

func (r *DocumentChunkRepo) VectorSearch(ctx context.Context, queryVec []float32, wsID uuid.UUID, projID *uuid.UUID, limit int) ([]domain.ScoredMemory, error) {
	if limit <= 0 {
		limit = 20
	}
	args := []interface{}{wsID}
	where := "c.embedding IS NOT NULL"
	if projID != nil {
		args = append(args, *projID)
		where += fmt.Sprintf(" AND c.project_id = $%d", len(args))
	}
	var emb []struct {
		ID        uuid.UUID `db:"id"`
		Embedding string    `db:"embedding"`
	}
	q := fmt.Sprintf(`SELECT c.id, c.embedding %s WHERE %s`, docChunkFrom, where)
	if err := r.db.SelectContext(ctx, &emb, q, args...); err != nil {
		return nil, fmt.Errorf("doc chunks vector: candidates: %w", err)
	}
	type sc struct {
		id    uuid.UUID
		score float64
	}
	scores := make([]sc, 0, len(emb))
	for _, e := range emb {
		vec, err := domain.DecodeEmbedding(e.Embedding)
		if err != nil || len(vec) != len(queryVec) {
			continue
		}
		scores = append(scores, sc{e.ID, cosineSimilarity(queryVec, vec)})
	}
	slices.SortFunc(scores, func(a, b sc) int {
		switch {
		case a.score > b.score:
			return -1
		case a.score < b.score:
			return 1
		}
		return 0
	})
	if len(scores) > limit {
		scores = scores[:limit]
	}
	if len(scores) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(scores))
	for i, s := range scores {
		ids[i] = s.id
	}
	hq, hargs, err := sqlx.In(fmt.Sprintf(`SELECT %s, 0::float8 AS score FROM document_chunks c
		JOIN documents d ON d.id = c.document_id WHERE c.id IN (?)`, docChunkSelect), ids)
	if err != nil {
		return nil, err
	}
	var hits []docChunkHit
	if err := r.db.SelectContext(ctx, &hits, r.db.Rebind(hq), hargs...); err != nil {
		return nil, fmt.Errorf("doc chunks vector: hydrate: %w", err)
	}
	byID := make(map[uuid.UUID]docChunkHit, len(hits))
	for _, h := range hits {
		byID[h.ID] = h
	}
	out := make([]domain.ScoredMemory, 0, len(scores))
	for _, s := range scores {
		if h, ok := byID[s.id]; ok {
			h.Score = s.score
			out = append(out, h.toScored(wsID))
		}
	}
	return out, nil
}

func (r *DocumentChunkRepo) ListStale(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID, limit int) ([]domain.Document, error) {
	if limit <= 0 {
		limit = 50
	}
	args := []interface{}{wsID}
	cond := []string{"d.deleted_at IS NULL"}
	if projID != nil {
		args = append(args, *projID)
		cond = append(cond, fmt.Sprintf("d.project_id = $%d", len(args)))
	}
	args = append(args, limit)
	q := fmt.Sprintf(`SELECT d.id, d.project_id, d.slug, d.title, d.storage_key, d.version
		FROM documents d JOIN projects p ON p.id = d.project_id AND p.workspace_id = $1
		WHERE %s AND NOT EXISTS (SELECT 1 FROM document_chunks c WHERE c.document_id = d.id AND c.doc_version = d.version)
		ORDER BY d.updated_at DESC LIMIT $%d`, strings.Join(cond, " AND "), len(args))
	var docs []domain.Document
	if err := r.db.SelectContext(ctx, &docs, q, args...); err != nil {
		return nil, fmt.Errorf("doc chunks list stale: %w", err)
	}
	return docs, nil
}

func (r *DocumentChunkRepo) Status(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID) (domain.DocIndexStatus, error) {
	args := []interface{}{wsID}
	cond := "d.deleted_at IS NULL"
	if projID != nil {
		args = append(args, *projID)
		cond += " AND d.project_id = $2"
	}
	var st domain.DocIndexStatus
	q := fmt.Sprintf(`SELECT count(*) AS live_docs,
		count(*) FILTER (WHERE EXISTS (SELECT 1 FROM document_chunks c WHERE c.document_id = d.id AND c.doc_version = d.version)) AS indexed_docs
		FROM documents d JOIN projects p ON p.id = d.project_id AND p.workspace_id = $1 WHERE %s`, cond)
	err := r.db.QueryRowxContext(ctx, q, args...).Scan(&st.LiveDocs, &st.IndexedDocs)
	return st, err
}
