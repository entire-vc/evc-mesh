package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// MemoryRepo implements persistent storage for agent memory entries.
type MemoryRepo struct {
	db *sqlx.DB
}

// NewMemoryRepo creates a new MemoryRepo.
func NewMemoryRepo(db *sqlx.DB) *MemoryRepo {
	return &MemoryRepo{db: db}
}

// memoryRow is the DB row representation for the memories table.
// search_vector is a GENERATED STORED column — it is never written explicitly.
type memoryRow struct {
	ID              uuid.UUID               `db:"id"`
	WorkspaceID     uuid.UUID               `db:"workspace_id"`
	ProjectID       *uuid.UUID              `db:"project_id"`
	AgentID         *uuid.UUID              `db:"agent_id"`
	Key             string                  `db:"key"`
	Content         string                  `db:"content"`
	Scope           domain.MemoryScope      `db:"scope"`
	Tags            pq.StringArray          `db:"tags"`
	SourceType      domain.MemorySourceType `db:"source_type"`
	SourceEventID   *uuid.UUID              `db:"source_event_id"`
	SourceURL       *string                 `db:"source_url"`
	Relevance       float32                 `db:"relevance"`
	ImportanceScore float32                 `db:"importance_score"`
	CreatedAt       time.Time               `db:"created_at"`
	UpdatedAt       time.Time               `db:"updated_at"`
	ExpiresAt       *time.Time              `db:"expires_at"`
	LastAccessedAt  *time.Time              `db:"last_accessed_at"`
	Archived        bool                    `db:"archived"`
}

func (r *memoryRow) toDomain() domain.Memory {
	return domain.Memory{
		ID:              r.ID,
		WorkspaceID:     r.WorkspaceID,
		ProjectID:       r.ProjectID,
		AgentID:         r.AgentID,
		Key:             r.Key,
		Content:         r.Content,
		Scope:           r.Scope,
		Tags:            r.Tags,
		SourceType:      r.SourceType,
		SourceEventID:   r.SourceEventID,
		SourceURL:       r.SourceURL,
		Relevance:       r.Relevance,
		ImportanceScore: r.ImportanceScore,
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
		ExpiresAt:       r.ExpiresAt,
		LastAccessedAt:  r.LastAccessedAt,
		Archived:        r.Archived,
	}
}

const memoryColumns = `id, workspace_id, project_id, agent_id, key, content, scope, tags,
	source_type, source_event_id, source_url, relevance, importance_score, created_at, updated_at, expires_at,
	last_accessed_at, archived`

// Upsert inserts a new memory or updates content, tags, relevance, and expires_at on conflict.
// The unique constraint is on (workspace_id, project_id, agent_id, key, scope).
func (r *MemoryRepo) Upsert(ctx context.Context, m *domain.Memory) error {
	if m.ID == uuid.Nil {
		m.ID = uuid.New()
	}
	now := time.Now()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	m.UpdatedAt = now

	tags := m.Tags
	if tags == nil {
		tags = pq.StringArray{}
	}

	// Use ON CONFLICT (id) because the composite unique constraint
	// uq_memory_key_scope doesn't match when project_id or agent_id is NULL
	// (PostgreSQL treats NULLs as distinct in UNIQUE constraints).
	// The service layer sets mem.ID = existing.ID before calling Upsert.
	const q = `
		INSERT INTO memories (
			id, workspace_id, project_id, agent_id, key, content, scope,
			tags, source_type, source_event_id, source_url, relevance, importance_score,
			created_at, updated_at, expires_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12, $13,
			$14, $15, $16
		)
		ON CONFLICT (id) DO UPDATE
			SET content          = EXCLUDED.content,
			    tags             = EXCLUDED.tags,
			    relevance        = EXCLUDED.relevance,
			    importance_score = EXCLUDED.importance_score,
			    source_url       = EXCLUDED.source_url,
			    updated_at       = EXCLUDED.updated_at,
			    expires_at       = EXCLUDED.expires_at
	`
	_, err := r.db.ExecContext(ctx, q,
		m.ID, m.WorkspaceID, m.ProjectID, m.AgentID, m.Key, m.Content, m.Scope,
		tags, m.SourceType, m.SourceEventID, m.SourceURL, m.Relevance, m.ImportanceScore,
		m.CreatedAt, m.UpdatedAt, m.ExpiresAt,
	)
	return err
}

// GetByID returns a memory by its primary key, or nil if not found.
// It touches last_accessed_at (1h idempotency window) as a side-effect.
func (r *MemoryRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Memory, error) {
	var row memoryRow
	err := r.db.GetContext(ctx, &row,
		fmt.Sprintf(`SELECT %s FROM memories WHERE id = $1`, memoryColumns),
		id,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	m := row.toDomain()

	// Touch last_accessed_at — only if not recently updated (1-hour window to avoid write amplification).
	_, _ = r.db.ExecContext(ctx,
		`UPDATE memories SET last_accessed_at = NOW()
		 WHERE id = $1
		   AND (last_accessed_at IS NULL OR last_accessed_at < NOW() - INTERVAL '1 hour')`,
		id,
	)

	return &m, nil
}

// GetByKey returns a memory by its composite natural key, or nil if not found.
// Pass nil for projectID or agentID when those dimensions are not scoped.
func (r *MemoryRepo) GetByKey(ctx context.Context, workspaceID uuid.UUID, projectID, agentID *uuid.UUID, key string, scope domain.MemoryScope) (*domain.Memory, error) {
	var row memoryRow
	err := r.db.GetContext(ctx, &row,
		fmt.Sprintf(`SELECT %s FROM memories
			WHERE workspace_id = $1
			  AND key          = $2
			  AND scope        = $3
			  AND (project_id = $4 OR ($4::uuid IS NULL AND project_id IS NULL))
			  AND (agent_id   = $5 OR ($5::uuid IS NULL AND agent_id   IS NULL))`, memoryColumns),
		workspaceID, key, scope, projectID, agentID,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	m := row.toDomain()
	return &m, nil
}

// scoredMemoryRow is used for full-text search results that include a rank score.
type scoredMemoryRow struct {
	memoryRow
	Score float64 `db:"score"`
}

// FullTextSearch returns memories ranked by relevance to query using PostgreSQL ts_rank_cd.
// Results are further filtered by scope, tags (overlap), and expiry.
func (r *MemoryRepo) FullTextSearch(ctx context.Context, query string, workspaceID uuid.UUID, projectID *uuid.UUID, scope string, tags []string, limit int) ([]domain.ScoredMemory, error) {
	if limit <= 0 {
		limit = 20
	}

	args := []interface{}{workspaceID, query} // $1, $2
	conditions := []string{
		"workspace_id = $1",
		"search_vector @@ plainto_tsquery('simple', $2)",
		"(expires_at IS NULL OR expires_at > NOW())",
		"archived = false",
	}
	argIdx := 3

	if scope != "" {
		conditions = append(conditions, fmt.Sprintf("scope = $%d", argIdx))
		args = append(args, scope)
		argIdx++
	}
	if projectID != nil {
		conditions = append(conditions, fmt.Sprintf("project_id = $%d", argIdx))
		args = append(args, *projectID)
		argIdx++
	}
	if len(tags) > 0 {
		conditions = append(conditions, fmt.Sprintf("tags && $%d", argIdx))
		args = append(args, pq.Array(tags))
		argIdx++
	}

	args = append(args, limit)
	limitIdx := argIdx

	q := fmt.Sprintf(`
		SELECT %s,
		       ts_rank_cd(search_vector, plainto_tsquery('simple', $2)) AS score
		FROM memories
		WHERE %s
		ORDER BY score DESC, relevance DESC
		LIMIT $%d`,
		memoryColumns,
		joinAnd(conditions),
		limitIdx,
	)

	var rows []scoredMemoryRow
	if err := r.db.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, err
	}

	result := make([]domain.ScoredMemory, len(rows))
	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		result[i] = domain.ScoredMemory{
			Memory: row.toDomain(),
			Score:  row.Score,
		}
		ids[i] = row.ID
	}

	// Batch-touch last_accessed_at for all returned hits (1-hour idempotency window).
	if len(ids) > 0 {
		_, _ = r.db.ExecContext(ctx,
			`UPDATE memories SET last_accessed_at = NOW()
			 WHERE id = ANY($1)
			   AND (last_accessed_at IS NULL OR last_accessed_at < NOW() - INTERVAL '1 hour')`,
			pq.Array(ids),
		)
	}

	return result, nil
}

// FindByScope returns memories for a workspace/project filtered by scope, ordered by relevance descending.
func (r *MemoryRepo) FindByScope(ctx context.Context, workspaceID uuid.UUID, projectID *uuid.UUID, scope string, limit int) ([]domain.Memory, error) {
	if limit <= 0 {
		limit = 50
	}

	args := []interface{}{workspaceID} // $1
	conditions := []string{
		"workspace_id = $1",
		"(expires_at IS NULL OR expires_at > NOW())",
		"archived = false",
	}
	argIdx := 2

	if scope != "" {
		conditions = append(conditions, fmt.Sprintf("scope = $%d", argIdx))
		args = append(args, scope)
		argIdx++
	}
	if projectID != nil {
		conditions = append(conditions, fmt.Sprintf("project_id = $%d", argIdx))
		args = append(args, *projectID)
		argIdx++
	}
	args = append(args, limit)
	limitIdx := argIdx

	q := fmt.Sprintf(`
		SELECT %s FROM memories
		WHERE %s
		ORDER BY relevance DESC, updated_at DESC
		LIMIT $%d`,
		memoryColumns,
		joinAnd(conditions),
		limitIdx,
	)

	var rows []memoryRow
	if err := r.db.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, err
	}

	memories := make([]domain.Memory, len(rows))
	for i, row := range rows {
		memories[i] = row.toDomain()
	}
	return memories, nil
}

// ListByWorkspaceProject returns all non-expired memories for a workspace (and optional project).
// When projectID is nil, all workspace-scoped memories are returned regardless of project.
// Used by the get_project_knowledge MCP tool.
func (r *MemoryRepo) ListByWorkspaceProject(ctx context.Context, workspaceID uuid.UUID, projectID *uuid.UUID) ([]domain.Memory, error) {
	args := []interface{}{workspaceID}
	conditions := []string{
		"workspace_id = $1",
		"(expires_at IS NULL OR expires_at > NOW())",
		"archived = false",
	}

	if projectID != nil {
		conditions = append(conditions, "project_id = $2")
		args = append(args, *projectID)
	}

	q := fmt.Sprintf(`
		SELECT %s
		FROM memories
		WHERE %s
		ORDER BY relevance DESC, updated_at DESC`,
		memoryColumns,
		joinAnd(conditions),
	)

	var rows []memoryRow
	if err := r.db.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, err
	}
	memories := make([]domain.Memory, len(rows))
	for i, row := range rows {
		memories[i] = row.toDomain()
	}
	return memories, nil
}

// List executes a richly-filtered query with pagination, tag filters, ordering, and optional
// recency-decay scoring. Total is computed via a separate COUNT(*) query.
func (r *MemoryRepo) List(ctx context.Context, filter domain.MemoryListFilter) (*domain.MemoryListResult, error) {
	if filter.Limit <= 0 {
		filter.Limit = 20
	}
	if filter.Limit > 100 {
		filter.Limit = 100
	}

	args := []interface{}{filter.WorkspaceID} // $1
	conditions := []string{"workspace_id = $1"}
	argIdx := 2

	if !filter.IncludeExpired {
		conditions = append(conditions, "(expires_at IS NULL OR expires_at > NOW())")
	}

	if !filter.IncludeArchived {
		conditions = append(conditions, "archived = false")
	}

	if filter.Scope != "" {
		conditions = append(conditions, fmt.Sprintf("scope = $%d", argIdx))
		args = append(args, filter.Scope)
		argIdx++
	}
	if filter.ProjectID != nil {
		conditions = append(conditions, fmt.Sprintf("project_id = $%d", argIdx))
		args = append(args, *filter.ProjectID)
		argIdx++
	}
	if filter.CreatedBy != nil {
		conditions = append(conditions, fmt.Sprintf("agent_id = $%d", argIdx))
		args = append(args, *filter.CreatedBy)
		argIdx++
	}
	if filter.SourceType != "" {
		conditions = append(conditions, fmt.Sprintf("source_type = $%d", argIdx))
		args = append(args, string(filter.SourceType))
		argIdx++
	}
	if filter.Since != nil {
		conditions = append(conditions, fmt.Sprintf("created_at >= $%d", argIdx))
		args = append(args, *filter.Since)
		argIdx++
	}
	if filter.Until != nil {
		conditions = append(conditions, fmt.Sprintf("created_at <= $%d", argIdx))
		args = append(args, *filter.Until)
		argIdx++
	}
	if filter.RelevanceMin != nil {
		conditions = append(conditions, fmt.Sprintf("relevance >= $%d", argIdx))
		args = append(args, *filter.RelevanceMin)
		argIdx++
	}
	if filter.MinImportance != nil {
		conditions = append(conditions, fmt.Sprintf("importance_score >= $%d", argIdx))
		args = append(args, *filter.MinImportance)
		argIdx++
	}
	if len(filter.Tags) > 0 {
		// AND: memory must contain ALL listed tags
		conditions = append(conditions, fmt.Sprintf("tags @> $%d", argIdx))
		args = append(args, pq.Array(filter.Tags))
		argIdx++
	}
	if len(filter.TagsAny) > 0 {
		// OR: memory must contain AT LEAST ONE of the listed tags
		conditions = append(conditions, fmt.Sprintf("tags && $%d", argIdx))
		args = append(args, pq.Array(filter.TagsAny))
		argIdx++
	}

	// Full-text search predicate (optional).
	var tsRankExpr string
	if filter.Query != "" {
		conditions = append(conditions, fmt.Sprintf(
			"search_vector @@ plainto_tsquery('simple', $%d)", argIdx))
		args = append(args, filter.Query)
		tsRankExpr = fmt.Sprintf("ts_rank_cd(search_vector, plainto_tsquery('simple', $%d))", argIdx)
		argIdx++
	}

	whereClause := joinAnd(conditions)

	// ── COUNT total ───────────────────────────────────────────────────────────
	countQ := "SELECT COUNT(*) FROM memories WHERE " + whereClause
	var total int
	if err := r.db.GetContext(ctx, &total, countQ, args...); err != nil {
		return nil, fmt.Errorf("memory list count: %w", err)
	}

	// ── ORDER BY ──────────────────────────────────────────────────────────────
	decayApplied := false
	var orderExpr string
	switch filter.OrderBy {
	case "relevance:desc":
		orderExpr = "relevance DESC"
	case "created_at:asc":
		orderExpr = "created_at ASC"
	case "decayed_relevance:desc":
		// Recency-weighted relevance: exponential decay with base 0.95 per day.
		orderExpr = "relevance * pow(0.95, EXTRACT(EPOCH FROM (NOW() - created_at)) / 86400) DESC"
		decayApplied = true
	default:
		// default: created_at:desc
		orderExpr = "created_at DESC"
	}

	// When a full-text query is present, break ties using ts_rank.
	if tsRankExpr != "" {
		orderExpr = tsRankExpr + " DESC, " + orderExpr
	}

	// ── LIMIT / OFFSET ────────────────────────────────────────────────────────
	args = append(args, filter.Limit)
	limitIdx := argIdx
	argIdx++
	args = append(args, filter.Offset)
	offsetIdx := argIdx

	q := fmt.Sprintf(`
		SELECT %s
		FROM memories
		WHERE %s
		ORDER BY %s
		LIMIT $%d OFFSET $%d`,
		memoryColumns,
		whereClause,
		orderExpr,
		limitIdx,
		offsetIdx,
	)

	var rows []memoryRow
	if err := r.db.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, fmt.Errorf("memory list: %w", err)
	}

	items := make([]domain.ScoredMemory, len(rows))
	for i, row := range rows {
		items[i] = domain.ScoredMemory{Memory: row.toDomain()}
	}

	return &domain.MemoryListResult{
		Items:        items,
		Total:        total,
		DecayApplied: decayApplied,
	}, nil
}

// Delete removes a memory entry by ID.
func (r *MemoryRepo) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM memories WHERE id = $1`, id)
	return err
}

// BoostRelevance increments the relevance of the given memory IDs by 0.1, capped at 1.0.
// This is called when a recalled memory is subsequently used by an agent (positive feedback).
func (r *MemoryRepo) BoostRelevance(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE memories
		 SET relevance  = LEAST(relevance + 0.1, 1.0),
		     updated_at = NOW()
		 WHERE id = ANY($1)`,
		pq.Array(ids),
	)
	return err
}

// embeddingRow extends memoryRow with the raw embedding TEXT column.
type embeddingRow struct {
	memoryRow
	EmbeddingJSON  *string `db:"embedding"`
	EmbeddingModel *string `db:"embedding_model"`
	EmbeddingDim   *int    `db:"embedding_dim"`
}

const memoryColumnsWithEmbedding = `id, workspace_id, project_id, agent_id, key, content, scope, tags,
	source_type, source_event_id, source_url, relevance, importance_score, created_at, updated_at, expires_at,
	last_accessed_at, archived,
	embedding, embedding_model, embedding_dim`

// VectorSearch performs application-level cosine similarity search.
// Embeddings are stored as JSON-encoded float32 arrays in the embedding TEXT column.
// This approach works without the pgvector extension — similarity is computed in Go.
// Results are filtered by workspace/project/scope/tags and sorted by cosine similarity.
func (r *MemoryRepo) VectorSearch(ctx context.Context, queryVec []float32, workspaceID uuid.UUID, projectID *uuid.UUID, scope string, tags []string, limit int) ([]domain.ScoredMemory, error) {
	if limit <= 0 {
		limit = 20
	}

	args := []interface{}{workspaceID} // $1
	conditions := []string{
		"workspace_id = $1",
		"embedding IS NOT NULL",
		"(expires_at IS NULL OR expires_at > NOW())",
		"archived = false",
	}
	argIdx := 2

	if scope != "" {
		conditions = append(conditions, fmt.Sprintf("scope = $%d", argIdx))
		args = append(args, scope)
		argIdx++
	}
	if projectID != nil {
		conditions = append(conditions, fmt.Sprintf("project_id = $%d", argIdx))
		args = append(args, *projectID)
		argIdx++
	}
	if len(tags) > 0 {
		conditions = append(conditions, fmt.Sprintf("tags && $%d", argIdx))
		args = append(args, pq.Array(tags))
		argIdx++
	}

	// Fetch candidates. We pull more than limit to have room for similarity ranking.
	candidateLimit := limit * 5
	args = append(args, candidateLimit)

	q := fmt.Sprintf(`
		SELECT %s FROM memories
		WHERE %s
		ORDER BY relevance DESC
		LIMIT $%d`,
		memoryColumnsWithEmbedding,
		joinAnd(conditions),
		argIdx,
	)

	var rows []embeddingRow
	if err := r.db.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, fmt.Errorf("vector search: select candidates: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}

	// Decode embeddings and compute cosine similarity in application code.
	type candidate struct {
		mem   domain.Memory
		score float64
	}
	candidates := make([]candidate, 0, len(rows))

	for _, row := range rows {
		if row.EmbeddingJSON == nil || *row.EmbeddingJSON == "" {
			continue
		}
		var vec []float32
		if err := json.Unmarshal([]byte(*row.EmbeddingJSON), &vec); err != nil {
			// Skip corrupted embeddings silently.
			continue
		}
		sim := cosineSimilarity(queryVec, vec)
		candidates = append(candidates, candidate{
			mem:   row.toDomain(),
			score: sim,
		})
	}

	// Sort by descending similarity.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	result := make([]domain.ScoredMemory, len(candidates))
	for i, c := range candidates {
		result[i] = domain.ScoredMemory{
			Memory: c.mem,
			Score:  c.score,
		}
	}
	return result, nil
}

// UpdateEmbedding stores the JSON-encoded embedding vector for a memory.
func (r *MemoryRepo) UpdateEmbedding(ctx context.Context, id uuid.UUID, vec []float32, model string, dim int) error {
	encoded, err := json.Marshal(vec)
	if err != nil {
		return fmt.Errorf("update embedding: encode vector: %w", err)
	}
	_, err = r.db.ExecContext(ctx,
		`UPDATE memories
		 SET embedding       = $1,
		     embedding_model = $2,
		     embedding_dim   = $3,
		     updated_at      = NOW()
		 WHERE id = $4`,
		string(encoded), model, dim, id,
	)
	return err
}

// DecayRelevance reduces relevance by 0.05 for agent-scope memories that have not been
// updated in more than 30 days. Workspace and project scope memories are exempt.
// The floor is 0.1 — relevance never decays below that value.
// Returns the count of rows updated.
func (r *MemoryRepo) DecayRelevance(ctx context.Context) (int64, error) {
	result, err := r.db.ExecContext(ctx, `
		UPDATE memories
		SET relevance  = GREATEST(relevance - 0.05, 0.1),
		    updated_at = updated_at
		WHERE updated_at < now() - interval '30 days'
		  AND relevance > 0.1
		  AND scope = 'agent'
		  AND expires_at IS NULL
	`)
	if err != nil {
		return 0, fmt.Errorf("memory decay relevance: %w", err)
	}
	return result.RowsAffected()
}

// CleanExpired deletes all memory rows whose expires_at is non-null and in the past.
// Returns the count of rows deleted.
func (r *MemoryRepo) CleanExpired(ctx context.Context) (int64, error) {
	result, err := r.db.ExecContext(ctx,
		`DELETE FROM memories WHERE expires_at IS NOT NULL AND expires_at < now()`,
	)
	if err != nil {
		return 0, fmt.Errorf("memory clean expired: %w", err)
	}
	return result.RowsAffected()
}

// ListWithNullEmbedding returns up to limit memories whose embedding column is NULL.
// Used by the batch embedding job to find un-embedded memories.
func (r *MemoryRepo) ListWithNullEmbedding(ctx context.Context, workspaceID uuid.UUID, limit int) ([]domain.Memory, error) {
	if limit <= 0 {
		limit = 100
	}
	q := fmt.Sprintf(`
		SELECT %s FROM memories
		WHERE workspace_id = $1
		  AND embedding IS NULL
		  AND (expires_at IS NULL OR expires_at > now())
		ORDER BY updated_at DESC
		LIMIT $2`,
		memoryColumns,
	)
	var rows []memoryRow
	if err := r.db.SelectContext(ctx, &rows, q, workspaceID, limit); err != nil {
		return nil, fmt.Errorf("memory list null embedding: %w", err)
	}
	memories := make([]domain.Memory, len(rows))
	for i, row := range rows {
		memories[i] = row.toDomain()
	}
	return memories, nil
}

// FindByShortID resolves the first memory whose UUID hex string starts with the given prefix
// within the given workspace. Returns nil, nil if not found.
func (r *MemoryRepo) FindByShortID(ctx context.Context, workspaceID uuid.UUID, prefix string) (*domain.Memory, error) {
	var row memoryRow
	err := r.db.GetContext(ctx, &row,
		fmt.Sprintf(`SELECT %s FROM memories WHERE workspace_id = $1 AND id::text LIKE $2 LIMIT 1`, memoryColumns),
		workspaceID, prefix+"%",
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("memory find by short id: %w", err)
	}
	m := row.toDomain()
	return &m, nil
}

// SetArchived sets or clears the archived flag on a memory.
func (r *MemoryRepo) SetArchived(ctx context.Context, id uuid.UUID, archived bool) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE memories SET archived = $1, updated_at = NOW() WHERE id = $2`,
		archived, id,
	)
	if err != nil {
		return fmt.Errorf("memory set archived: %w", err)
	}
	return nil
}

// cosineSimilarity returns the cosine similarity between two float32 vectors.
// Returns 0 when either vector is zero-length or the lengths differ.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		fa, fb := float64(a[i]), float64(b[i])
		dot += fa * fb
		normA += fa * fa
		normB += fb * fb
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
