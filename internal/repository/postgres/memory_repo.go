package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

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
	ThreadID        *string                 `db:"thread_id"`
	SourceTaskID    *uuid.UUID              `db:"source_task_id"`
	// Health lifecycle fields (migration 080).
	Status         domain.MemoryStatus `db:"status"`
	FreshnessScore float32             `db:"freshness_score"`
	SupersededBy   *uuid.UUID          `db:"superseded_by"`
	ValidFrom      *time.Time          `db:"valid_from"`
	ValidUntil     *time.Time          `db:"valid_until"`
	ContentSimhash *int64              `db:"content_simhash"`
	Version        int                 `db:"version"`
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
		ThreadID:        r.ThreadID,
		SourceTaskID:    r.SourceTaskID,
		Status:          r.Status,
		FreshnessScore:  r.FreshnessScore,
		SupersededBy:    r.SupersededBy,
		ValidFrom:       r.ValidFrom,
		ValidUntil:      r.ValidUntil,
		ContentSimhash:  r.ContentSimhash,
		Version:         r.Version,
	}
}

const memoryColumns = `id, workspace_id, project_id, agent_id, key, content, scope, tags,
	source_type, source_event_id, source_url, relevance, importance_score, created_at, updated_at, expires_at,
	last_accessed_at, archived, thread_id, source_task_id,
	status, freshness_score, superseded_by, valid_from, valid_until, content_simhash, version`

// Upsert inserts a new memory or updates content, tags, relevance, and expires_at on conflict.
// The unique constraint is on (workspace_id, project_id, agent_id, key, scope).
//
// Every write goes through one transaction that bumps memories.version and
// appends the matching memory_revisions row. The two cannot come apart: a
// version with no snapshot would be a claim of history we cannot produce, and a
// snapshot with no version bump would let a conditional write succeed against
// content that had already moved.
//
// When intent.ExpectedVersion is non-nil the write is conditional and returns
// *domain.MemoryVersionConflictError if the stored version has moved. Nil keeps
// the previous last-write-wins behaviour, so callers that have not opted in are
// unaffected.
func (r *MemoryRepo) Upsert(ctx context.Context, m *domain.Memory, intent domain.MemoryWriteIntent) (err error) {
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

	// Default health fields so the NOT NULL constraint on status is always satisfied.
	if m.Status == "" {
		m.Status = domain.MemoryStatusActive
	}
	if m.FreshnessScore == 0 && m.Status == domain.MemoryStatusActive {
		m.FreshnessScore = 1.0
	}

	// Use ON CONFLICT (id) because the composite unique constraint
	// uq_memory_key_scope doesn't match when project_id or agent_id is NULL
	// (PostgreSQL treats NULLs as distinct in UNIQUE constraints).
	// The service layer sets mem.ID = existing.ID before calling Upsert.
	const q = `
		INSERT INTO memories (
			id, workspace_id, project_id, agent_id, key, content, scope,
			tags, source_type, source_event_id, source_url, relevance, importance_score,
			created_at, updated_at, expires_at, thread_id, source_task_id,
			status, freshness_score, superseded_by, valid_from, valid_until, content_simhash,
			version
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12, $13,
			$14, $15, $16, $17, $18,
			$19, $20, $21, $22, $23, $24,
			$25
		)
		ON CONFLICT (id) DO UPDATE
			SET content          = EXCLUDED.content,
			    tags             = EXCLUDED.tags,
			    relevance        = EXCLUDED.relevance,
			    importance_score = EXCLUDED.importance_score,
			    source_url       = EXCLUDED.source_url,
			    updated_at       = EXCLUDED.updated_at,
			    expires_at       = EXCLUDED.expires_at,
			    thread_id        = EXCLUDED.thread_id,
			    source_task_id   = EXCLUDED.source_task_id,
			    status           = EXCLUDED.status,
			    freshness_score  = EXCLUDED.freshness_score,
			    superseded_by    = EXCLUDED.superseded_by,
			    valid_from       = EXCLUDED.valid_from,
			    valid_until      = EXCLUDED.valid_until,
			    content_simhash  = EXCLUDED.content_simhash,
			    version          = EXCLUDED.version
	`
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Lock the existing row (if any) for the duration of the transaction, so a
	// concurrent writer cannot read the same version, decide it matches, and
	// bump on top of us. Without FOR UPDATE the check below would be advisory:
	// both racers could read version N, both find it equal to their
	// expectation, and both write N+1 — the exact failure the expected-version
	// argument exists to prevent.
	//
	// ⚠️ Not covered by a red test. Removing this clause leaves
	// TestConcurrentWrite_StaleExpectedVersionIsRefused green at 2 and at 8
	// concurrent writers (measured 2026-08-20): they serialise through the pool
	// anyway, so the comparison alone catches them. The clause is kept as the
	// correct guard for a two-statement read-modify-write, not because the
	// suite proves it. If you are tempted to delete it because "the tests still
	// pass" — that is precisely the evidence this note exists to pre-empt.
	var storedVersion int
	found := true
	if selErr := tx.GetContext(ctx, &storedVersion,
		`SELECT version FROM memories WHERE id = $1 FOR UPDATE`, m.ID); selErr != nil {
		if !errors.Is(selErr, sql.ErrNoRows) {
			err = selErr
			return err
		}
		found = false
	}

	if intent.ExpectedVersion != nil {
		// A conditional write against a key that does not exist is also a
		// conflict: the caller believes it is editing something, and creating
		// it instead would silently turn an edit into a create.
		actual := 0
		if found {
			actual = storedVersion
		}
		if actual != *intent.ExpectedVersion {
			err = &domain.MemoryVersionConflictError{
				Key:      m.Key,
				Expected: *intent.ExpectedVersion,
				Actual:   actual,
			}
			return err
		}
	}

	action := intent.Action
	if action == "" {
		if found {
			action = domain.MemoryActionUpdated
		} else {
			action = domain.MemoryActionCreated
		}
	}
	m.Version = storedVersion + 1
	if !found {
		m.Version = 1
	}

	if _, err = tx.ExecContext(ctx, q,
		m.ID, m.WorkspaceID, m.ProjectID, m.AgentID, m.Key, m.Content, m.Scope,
		tags, m.SourceType, m.SourceEventID, m.SourceURL, m.Relevance, m.ImportanceScore,
		m.CreatedAt, m.UpdatedAt, m.ExpiresAt, m.ThreadID, m.SourceTaskID,
		m.Status, m.FreshnessScore, m.SupersededBy, m.ValidFrom, m.ValidUntil, m.ContentSimhash,
		m.Version,
	); err != nil {
		return err
	}

	// Empty reason is stored as NULL, not as "". The CHECK rejects blank, and
	// the two states mean different things: NULL is "written before the reason
	// requirement existed", blank would be "someone answered the question with
	// nothing".
	var reason *string
	if trimmed := strings.TrimSpace(intent.Reason); trimmed != "" {
		reason = &trimmed
	}

	if _, err = tx.ExecContext(ctx, `
		INSERT INTO memory_revisions (memory_id, version, content, tags, action, reason, actor_agent_id, workspace_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		m.ID, m.Version, m.Content, tags, action, reason, intent.ActorAgentID, m.WorkspaceID,
	); err != nil {
		return err
	}

	err = tx.Commit()
	return err
}

// AppendRevision records one revision row directly, without touching the
// memories row. Used by the forget path, which needs to snapshot content it is
// about to delete: there is no version to bump because the row is going away.
func (r *MemoryRepo) AppendRevision(ctx context.Context, rev domain.MemoryRevision) error {
	tags := rev.Tags
	if tags == nil {
		tags = pq.StringArray{}
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO memory_revisions (memory_id, version, content, tags, action, reason, actor_agent_id, workspace_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		rev.MemoryID, rev.Version, rev.Content, tags, rev.Action, rev.Reason, rev.ActorAgentID, rev.WorkspaceID)
	return err
}

// ListRevisions returns the recorded history of one memory, newest version
// first, capped at limit (limit <= 0 means 50).
func (r *MemoryRepo) ListRevisions(ctx context.Context, memoryID uuid.UUID, limit int) ([]domain.MemoryRevision, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows []domain.MemoryRevision
	err := r.db.SelectContext(ctx, &rows, `
		SELECT id, memory_id, version, content, tags, action, reason, actor_agent_id, created_at, workspace_id
		FROM memory_revisions
		WHERE memory_id = $1
		ORDER BY version DESC
		LIMIT $2`, memoryID, limit)
	if err != nil {
		return nil, err
	}
	return rows, nil
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

// GetByKey returns a memory by its natural key, or nil if not found. The
// identity dimensions considered follow the declared scope — not every
// non-nil pointer the caller happens to pass — so that "workspace-scoped"
// actually means one row per (workspace, key) regardless of which project_id
// or agent_id incidentally got attached to a given write, and likewise for
// project/agent scope. See domain.ScopeWorkspace/ScopeProject/ScopeAgent.
//
// The `status != 'superseded' AND archived = false` predicate and the
// `ORDER BY updated_at DESC LIMIT 1` are both load-bearing, not cosmetic
// (task #2c0154db, finding F1). Before them, this query had no status filter
// at all: after a collapse (cmd/collapse-memories) leaves a winner `active`
// and losers `superseded` under the SAME natural key, the predicate-less
// query matched every one of them and sqlx.GetContext silently took whatever
// row Postgres's heap scan happened to return first — no ORDER BY existed to
// make that deterministic. On prod that was measured to be the RETIRED row
// in every checked case, so Upsert's `ON CONFLICT (id) DO UPDATE SET
// status = EXCLUDED.status` resurrected it and zeroed its `superseded_by` —
// exactly the audit trail the no-hard-delete rule exists to preserve — and
// then a real remember() on the same key hit the new unique index (23505),
// unable to fix the very record the identity fix was written to unblock.
// The predicate here MUST match what FullTextSearch/vector search/recall
// treat as "current" (memory_repo.go's other `status != 'superseded' AND
// archived = false` sites) — NOT `status = 'active'`, which would wrongly
// exclude `review_needed` rows that reads still surface as live (see F2).
func (r *MemoryRepo) GetByKey(ctx context.Context, workspaceID uuid.UUID, projectID, agentID *uuid.UUID, key string, scope domain.MemoryScope) (*domain.Memory, error) {
	const currentPredicate = "status != 'superseded' AND archived = false"
	var query string
	args := []interface{}{workspaceID, key, scope}

	switch scope {
	case domain.ScopeProject:
		query = fmt.Sprintf(`SELECT %s FROM memories
			WHERE workspace_id = $1 AND key = $2 AND scope = $3
			  AND (project_id = $4 OR ($4::uuid IS NULL AND project_id IS NULL))
			  AND %s
			ORDER BY updated_at DESC LIMIT 1`, memoryColumns, currentPredicate)
		args = append(args, projectID)
	case domain.ScopeAgent:
		query = fmt.Sprintf(`SELECT %s FROM memories
			WHERE workspace_id = $1 AND key = $2 AND scope = $3
			  AND (agent_id = $4 OR ($4::uuid IS NULL AND agent_id IS NULL))
			  AND %s
			ORDER BY updated_at DESC LIMIT 1`, memoryColumns, currentPredicate)
		args = append(args, agentID)
	default: // domain.ScopeWorkspace (and any future/unknown scope defaults to the widest, safest identity)
		query = fmt.Sprintf(`SELECT %s FROM memories
			WHERE workspace_id = $1 AND key = $2 AND scope = $3
			  AND %s
			ORDER BY updated_at DESC LIMIT 1`, memoryColumns, currentPredicate)
	}

	var row memoryRow
	err := r.db.GetContext(ctx, &row, query, args...)
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
//
// recencyWeight blends an exponential recency-decay factor into the ranking (see
// applyRecencyBlend). recencyWeight <= 0 is the legacy path: results keep the exact
// FTS-only ordering (ORDER BY score DESC, relevance DESC) and their raw ts_rank scores,
// byte-for-byte identical to the pre-recency behavior.
func (r *MemoryRepo) FullTextSearch(ctx context.Context, query string, workspaceID uuid.UUID, projectID *uuid.UUID, scope string, tags []string, limit int, recencyWeight float64) ([]domain.ScoredMemory, error) {
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

	// Phase 2: OR-token fallback when the strict AND query returns too few results.
	// This handles phrasing-sensitive misses such as "by fixing migrate-gate DSN"
	// (filler words break plainto_tsquery AND logic with the 'simple' dictionary).
	// Errors in the fallback are non-fatal; we continue with whatever AND found.
	if len(rows) < minFTSHits {
		if orRows, err2 := r.ftsORFallback(ctx, query, workspaceID, projectID, scope, tags, limit, rows); err2 == nil {
			rows = orRows
		}
	}

	// Recency-aware re-ranking (WI-1b). Applied AFTER the AND+OR merge so it covers
	// both paths consistently. A no-op when recencyWeight <= 0 (legacy ordering preserved).
	applyRecencyBlend(rows, recencyWeight, time.Now())

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

// minFTSHits is the strict-AND hit count below which the OR-token fallback kicks in.
// Both the 'simple' (FullTextSearch) and 'english' (FullTextSearchRanked) arms use it.
const minFTSHits = 3

// orScoreMultiplier discounts OR-only hits so exact AND matches always outrank them.
const orScoreMultiplier = 0.8

// tokenizeForORQuery converts a natural-language string into a PostgreSQL OR-tsquery fragment
// suitable for passing to to_tsquery('simple', fragment). Tokens are split on any non-alphanumeric
// character so that "migrate-gate" → "migrate | gate", preventing tsquery parse errors.
// Tokens shorter than 3 runes and duplicates are dropped.
func tokenizeForORQuery(input string) string {
	fields := strings.FieldsFunc(strings.ToLower(input), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	seen := make(map[string]bool)
	tokens := make([]string, 0, len(fields))
	for _, f := range fields {
		if len([]rune(f)) < 3 || seen[f] {
			continue
		}
		seen[f] = true
		tokens = append(tokens, f)
	}
	return strings.Join(tokens, " | ")
}

// ftsORFallback runs a token-level OR full-text search and merges results with existing AND results.
// OR-only results receive a score discount (0.8×) so exact AND matches rank above partial OR matches.
func (r *MemoryRepo) ftsORFallback(
	ctx context.Context,
	query string,
	workspaceID uuid.UUID,
	projectID *uuid.UUID,
	scope string,
	tags []string,
	limit int,
	andRows []scoredMemoryRow,
) ([]scoredMemoryRow, error) {
	orFragment := tokenizeForORQuery(query)
	if orFragment == "" {
		return andRows, nil
	}

	args := []interface{}{workspaceID, orFragment} // $1=workspace_id, $2=OR tsquery fragment
	conditions := []string{
		"workspace_id = $1",
		"search_vector @@ to_tsquery('simple', $2)",
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
		       ts_rank_cd(search_vector, to_tsquery('simple', $2)) AS score
		FROM memories
		WHERE %s
		ORDER BY score DESC, relevance DESC
		LIMIT $%d`,
		memoryColumns,
		joinAnd(conditions),
		limitIdx,
	)

	var orRows []scoredMemoryRow
	if err := r.db.SelectContext(ctx, &orRows, q, args...); err != nil {
		return andRows, fmt.Errorf("fts or fallback: %w", err)
	}
	if len(orRows) == 0 {
		return andRows, nil
	}

	return mergeORScoredRows(andRows, orRows, limit), nil
}

// mergeORScoredRows merges strict-AND hits with OR-token hits onto ONE comparable scale.
//
// AND and OR scores are NOT the same quantity even though both come from ts_rank_cd: the
// AND arm ranks by cover density against a plainto_tsquery (an AND of every query term —
// rewards proximity, penalizes spread), the OR arm ranks by additive term-overlap against a
// to_tsquery OR-fragment (roughly proportional to how many distinct terms matched). Sorting
// raw AND scores next to raw-but-discounted OR scores compares numbers with different units.
//
// Measured live (task f13865d5, fixture from recency_control.py): a row matching ALL FOUR
// query terms scored 0.025 on the AND (cover-density) scale while four PARTIAL matches
// scored 0.08-0.24 on the discounted OR scale — the exact match ranked dead last, the
// opposite of the "exact AND matches always outrank partial ones" invariant this function's
// old comment claimed but never enforced.
//
// Fix: every row is ranked on the OR arm's scale. A row present in BOTH andRows and orRows
// (a genuine exact match — it satisfied the OR query too) keeps its OR-scale score
// UNDISCOUNTED, since it earned every term. A row present only in orRows is a partial match
// and is discounted by orScoreMultiplier, as before. Rows already present in andRows are
// never duplicated (an ID can only appear once in the merged set); an AND row absent from
// orRows (possible if the OR query's own LIMIT truncated it) falls back to its raw AND
// score — the least-bad option when there is nothing on the OR scale to compare it against.
func mergeORScoredRows(andRows, orRows []scoredMemoryRow, limit int) []scoredMemoryRow {
	orScoreByID := make(map[uuid.UUID]float64, len(orRows))
	for _, row := range orRows {
		orScoreByID[row.ID] = row.Score
	}

	idSeen := make(map[uuid.UUID]bool, len(andRows))
	merged := make([]scoredMemoryRow, len(andRows), len(andRows)+len(orRows))
	for i, row := range andRows {
		idSeen[row.ID] = true
		if orScore, ok := orScoreByID[row.ID]; ok {
			row.Score = orScore
		}
		merged[i] = row
	}

	for _, row := range orRows {
		if idSeen[row.ID] {
			continue
		}
		row.Score *= orScoreMultiplier
		merged = append(merged, row)
		idSeen[row.ID] = true
	}

	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Score > merged[j].Score
	})
	if limit > 0 && len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

// recencyHalfLifeDays is the half-life (in days) for the recency-decay factor blended
// into full-text ranking by applyRecencyBlend. Tunable — metronix uses 30d.
const recencyHalfLifeDays = 30.0

// applyRecencyBlend reorders rows in place by a blend of their (min-max normalized)
// full-text score and an exponential recency-decay factor:
//
//	recency  = exp(-Δt · ln2 / halfLife)      // Δt = now − updated_at, halfLife = 30d
//	blended  = (1−w)·normFTS + w·recency      // w = recencyWeight, clamped to [0,1]
//
// The FTS scores are normalized to 0..1 across the result set so the two terms are
// comparable. Rows keep their original Score value — only their order changes — so the
// downstream RRF stage (which ranks by position) picks up the recency signal while the
// reported score semantics stay as raw ts_rank.
//
// When recencyWeight <= 0 this is a strict no-op: neither order nor scores change, giving
// byte-for-byte identical output to the legacy FTS-only path. `now` is injected for testability.
func applyRecencyBlend(rows []scoredMemoryRow, recencyWeight float64, now time.Time) {
	if recencyWeight <= 0 || len(rows) < 2 {
		return
	}
	if recencyWeight > 1 {
		recencyWeight = 1
	}

	// Min-max range of the raw FTS scores across the result set.
	minScore, maxScore := rows[0].Score, rows[0].Score
	for i := 1; i < len(rows); i++ {
		if rows[i].Score < minScore {
			minScore = rows[i].Score
		}
		if rows[i].Score > maxScore {
			maxScore = rows[i].Score
		}
	}
	span := maxScore - minScore
	lambda := math.Ln2 / recencyHalfLifeDays

	type ranked struct {
		row     scoredMemoryRow
		blended float64
		fts     float64
	}
	scored := make([]ranked, len(rows))
	for i := range rows {
		normFTS := 1.0 // when all scores are equal, FTS term is neutral and recency decides
		if span > 0 {
			normFTS = (rows[i].Score - minScore) / span
		}
		ageDays := now.Sub(rows[i].UpdatedAt).Hours() / 24
		if ageDays < 0 {
			ageDays = 0 // clock skew / future timestamps → treat as brand new
		}
		recency := math.Exp(-lambda * ageDays)
		scored[i] = ranked{
			row:     rows[i],
			blended: (1-recencyWeight)*normFTS + recencyWeight*recency,
			fts:     rows[i].Score,
		}
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].blended != scored[j].blended {
			return scored[i].blended > scored[j].blended
		}
		// Deterministic tie-breaks: higher raw FTS first, then more recently updated.
		if scored[i].fts != scored[j].fts {
			return scored[i].fts > scored[j].fts
		}
		return scored[i].row.UpdatedAt.After(scored[j].row.UpdatedAt)
	})

	for i := range scored {
		rows[i] = scored[i].row
	}
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

// ListByWorkspaceProject returns non-expired memories for a workspace/project pair with
// optional pagination. When projectID is non-nil, all matching project memories are returned
// without limit (they are small). When projectID is nil (workspace-tier), filter.Limit,
// filter.Offset, filter.MinImportance, and filter.TagsAny are applied. Limit=0 means no limit.
// Returns the total matching count before pagination is applied.
func (r *MemoryRepo) ListByWorkspaceProject(ctx context.Context, workspaceID uuid.UUID, projectID *uuid.UUID, filter domain.MemoryListFilter) ([]domain.Memory, int64, error) {
	args := []interface{}{workspaceID}
	conditions := []string{
		"workspace_id = $1",
		"(expires_at IS NULL OR expires_at > NOW())",
		"archived = false",
	}
	argIdx := 2

	if projectID != nil {
		conditions = append(conditions, fmt.Sprintf("project_id = $%d", argIdx))
		args = append(args, *projectID)
		argIdx++
	}

	// Workspace-tier only: apply optional importance + tag filters.
	if projectID == nil {
		if filter.MinImportance != nil {
			conditions = append(conditions, fmt.Sprintf("importance_score >= $%d", argIdx))
			args = append(args, *filter.MinImportance)
			argIdx++
		}
		if len(filter.TagsAny) > 0 {
			conditions = append(conditions, fmt.Sprintf("tags && $%d", argIdx))
			args = append(args, pq.Array(filter.TagsAny))
			argIdx++
		}
	}

	where := joinAnd(conditions)

	// COUNT query for total before pagination.
	var total int64
	if err := r.db.GetContext(ctx, &total, "SELECT COUNT(*) FROM memories WHERE "+where, args...); err != nil {
		return nil, 0, err
	}

	// Data query — paginate when Limit > 0.
	limit := filter.Limit
	if limit > 500 {
		limit = 500
	}
	dataArgs := args
	q := fmt.Sprintf(
		"SELECT %s FROM memories WHERE %s ORDER BY importance_score DESC, relevance DESC, updated_at DESC",
		memoryColumns, where,
	)
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
		dataArgs = append(dataArgs, limit, filter.Offset)
	}

	var rows []memoryRow
	if err := r.db.SelectContext(ctx, &rows, q, dataArgs...); err != nil {
		return nil, 0, err
	}
	memories := make([]domain.Memory, len(rows))
	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		memories[i] = row.toDomain()
		ids[i] = row.ID
	}
	// Batch-touch last_accessed_at (1-hour idempotency window).
	if len(ids) > 0 {
		_, _ = r.db.ExecContext(ctx,
			`UPDATE memories SET last_accessed_at = NOW()
			 WHERE id = ANY($1)
			   AND (last_accessed_at IS NULL OR last_accessed_at < NOW() - INTERVAL '1 hour')`,
			pq.Array(ids),
		)
	}
	return memories, total, nil
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

	if filter.ExcludeSuperseded {
		conditions = append(conditions, "status != 'superseded'")
	}

	if len(filter.StatusFilter) > 0 {
		placeholders := make([]string, len(filter.StatusFilter))
		for i, s := range filter.StatusFilter {
			args = append(args, string(s))
			placeholders[i] = fmt.Sprintf("$%d", argIdx)
			argIdx++
		}
		conditions = append(conditions, fmt.Sprintf("status IN (%s)", strings.Join(placeholders, ", ")))
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
	// Normalised so that a bare key and its suffixed form cannot mean different
	// things here than they do in the recall service's decay predicate — the two
	// used to disagree about `decayed_relevance` (#655c6d12).
	switch domain.CanonicalOrderBy(filter.OrderBy) {
	case domain.OrderByRelevanceDesc:
		orderExpr = "relevance DESC"
	case domain.OrderByCreatedAtAsc:
		orderExpr = "created_at ASC"
	case domain.OrderByDecayedRelevanceDesc:
		// Recency-weighted relevance incorporating freshness_score and configurable half-life.
		// Formula: relevance * freshness_score * exp(-Δt * ln2 / half_life_days)
		halfLife := filter.HalfLifeDays
		if halfLife <= 0 {
			halfLife = 30.0
		}
		args = append(args, halfLife)
		orderExpr = fmt.Sprintf(
			"relevance * freshness_score * EXP(-EXTRACT(EPOCH FROM (NOW() - created_at)) / 86400.0 * 0.693147 / $%d) DESC",
			argIdx,
		)
		argIdx++
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
	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		items[i] = domain.ScoredMemory{Memory: row.toDomain()}
		ids[i] = row.ID
	}
	// Batch-touch last_accessed_at (1-hour idempotency window, same pattern as FullTextSearch).
	if len(ids) > 0 {
		_, _ = r.db.ExecContext(ctx,
			`UPDATE memories SET last_accessed_at = NOW()
			 WHERE id = ANY($1)
			   AND (last_accessed_at IS NULL OR last_accessed_at < NOW() - INTERVAL '1 hour')`,
			pq.Array(ids),
		)
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

// vectorCandidatePoolCap bounds how many embedded memories are pulled for the
// in-Go cosine ranking in VectorSearch. It is intentionally large enough to
// cover the entire current corpus so the candidate set is effectively complete
// and relevance-neutral. It exists only as a latency/memory backstop for very
// large workspaces; when a corpus approaches this size, switch to pgvector/ANN.
//
// The cap counts MEMORIES, not chunks — it is applied by the candidate query,
// which never joins memory_chunks. Capping a chunk-level query at the same
// number would silently shrink the reachable corpus by the average chunks-per-
// memory factor (~1.75x measured, see ADR-0002): the same 20000 would cover
// roughly 11400 memories instead of 20000, and the loss would show up as
// missing recall results, not as an error.
const vectorCandidatePoolCap = 20000

// appendMemorySearchFilter appends the scope/tags eligibility predicates shared by BOTH
// retrieval arms of Recall (FullTextSearchRanked and VectorSearch) to a WHERE-clause
// builder. Having one builder is the point: the two arms previously disagreed about which
// rows were even eligible — the BM25 arm applied neither scope nor tags, so scope was
// unenforced on every row it contributed and, in bm25-only mode, unenforced entirely.
//
// Tag semantics match repository List exactly: Tags is AND (`tags @>`), TagsAny is OR
// (`tags &&`). The vector arm previously used `&&` for Tags, silently widening an AND
// filter into an OR one.
//
// Returns the extended conditions, args, and the next free placeholder index.
func appendMemorySearchFilter(
	conditions []string,
	args []interface{},
	argIdx int,
	f domain.MemorySearchFilter,
) (outConditions []string, outArgs []interface{}, nextArgIdx int) {
	if f.Scope != "" {
		conditions = append(conditions, fmt.Sprintf("scope = $%d", argIdx))
		args = append(args, f.Scope)
		argIdx++
	}
	if len(f.Tags) > 0 {
		// AND: memory must contain ALL listed tags.
		conditions = append(conditions, fmt.Sprintf("tags @> $%d", argIdx))
		args = append(args, pq.Array(f.Tags))
		argIdx++
	}
	if len(f.TagsAny) > 0 {
		// OR: memory must contain AT LEAST ONE of the listed tags.
		conditions = append(conditions, fmt.Sprintf("tags && $%d", argIdx))
		args = append(args, pq.Array(f.TagsAny))
		argIdx++
	}
	return conditions, args, argIdx
}

// VectorSearch performs application-level cosine similarity search without the
// pgvector extension — similarity is computed in Go (pgvector is not merely
// uninstalled on this deployment, it is absent from pg_available_extensions, so
// a vector column is an infra change rather than a schema change; see ADR-0002).
//
// It runs in three phases, which is what keeps it affordable now that one memory
// owns many vectors:
//
//  1. Pick eligible candidate memories — filtered by workspace/project and the
//     shared scope/tags eligibility filter, capped at vectorCandidatePoolCap.
//     IDs only: no content, no vectors.
//  2. Score every chunk of those memories, then reduce to at most one row per
//     memory by max-over-chunks (bestChunkPerMemory).
//  3. Hydrate only the surviving top-N memories.
//
// Phase 3 is the reason the phases exist. The previous single-query version
// selected `content` for the entire candidate pool in order to return `limit`
// (typically 20) rows — on an unscoped recall that is megabytes of text hauled
// out of Postgres and discarded. Chunking would have made that worse, since it
// multiplies vectors per memory ~1.75x. Fetching content only for rows that
// survived ranking makes the chunked path cheaper than the unchunked one it
// replaces, rather than more expensive.
//
// Scoring is max-over-chunks, deliberately not sum or mean: a memory's relevance
// is that of its best-matching passage. Aggregating instead would rank a long
// memory with many mediocre chunks above a short one that answers the query
// exactly — which is the opposite of the reason chunking was introduced.
func (r *MemoryRepo) VectorSearch(ctx context.Context, queryVec []float32, workspaceID uuid.UUID, projectID *uuid.UUID, filter domain.MemorySearchFilter, limit int) ([]domain.ScoredMemory, error) {
	if limit <= 0 {
		limit = 20
	}

	candidateIDs, err := r.vectorCandidateIDs(ctx, workspaceID, projectID, filter)
	if err != nil {
		return nil, err
	}
	if len(candidateIDs) == 0 {
		return nil, nil
	}

	scores, err := r.scoreCandidateVectors(ctx, candidateIDs, queryVec)
	if err != nil {
		return nil, err
	}
	if len(scores) == 0 {
		return nil, nil
	}

	// Reduce per-chunk scores to one row per memory, THEN truncate. Truncating
	// the chunk list first would let a single long memory's chunks fill the page
	// and starve every other memory out of the result.
	return r.hydrateScoredMemories(ctx, bestChunkPerMemory(scores, limit))
}

// vectorCandidateIDs returns the IDs of memories eligible for vector ranking,
// newest first, capped at vectorCandidatePoolCap.
//
// The candidate set MUST be relevance-neutral: ordering by `relevance DESC`
// biases the vector pool toward frequently-recalled docs (BoostRelevance pins
// them at 1.0), excluding semantically-relevant but less-recalled memories from
// vector recall entirely — the exact opposite of what vector search is for. We
// therefore pull a recency-ordered cap large enough to cover the whole corpus
// today. If the corpus ever outgrows the cap, the oldest memories degrade out
// gracefully (and that is the trigger to adopt pgvector/ANN — see #2285c9be).
func (r *MemoryRepo) vectorCandidateIDs(ctx context.Context, workspaceID uuid.UUID, projectID *uuid.UUID, filter domain.MemorySearchFilter) ([]uuid.UUID, error) {
	args := []interface{}{workspaceID} // $1
	conditions := []string{
		"workspace_id = $1",
		// A memory is reachable through EITHER storage shape. Requiring
		// `embedding IS NOT NULL` alone — as this query did before chunking —
		// silently hides every chunked memory from the dense arm, because the
		// chunked write path deliberately leaves memories.embedding untouched
		// and stores the real vectors in memory_chunks. That failure is
		// invisible: recall still returns results, just fewer and worse ones,
		// having quietly degraded to BM25-only for the affected rows.
		"(embedding IS NOT NULL OR EXISTS (SELECT 1 FROM memory_chunks c WHERE c.memory_id = memories.id))",
		"(expires_at IS NULL OR expires_at > NOW())",
		"archived = false",
	}
	argIdx := 2

	if projectID != nil {
		conditions = append(conditions, fmt.Sprintf("project_id = $%d", argIdx))
		args = append(args, *projectID)
		argIdx++
	}
	conditions, args, argIdx = appendMemorySearchFilter(conditions, args, argIdx, filter)

	args = append(args, vectorCandidatePoolCap)

	q := fmt.Sprintf(`
		SELECT id FROM memories
		WHERE %s
		ORDER BY updated_at DESC
		LIMIT $%d`,
		joinAnd(conditions),
		argIdx,
	)

	var ids []uuid.UUID
	if err := r.db.SelectContext(ctx, &ids, q, args...); err != nil {
		return nil, fmt.Errorf("vector search: select candidates: %w", err)
	}
	return ids, nil
}

// scoreCandidateVectors computes the cosine similarity of queryVec against every
// stored vector belonging to the given memories, returning one chunkScore per
// vector (not per memory — the reduction to one row per memory happens in
// bestChunkPerMemory).
//
// Two storage shapes are read, and the split between them is what makes the
// chunk backfill resumable: a memory that has chunk rows is scored from those
// and its legacy memories.embedding is ignored entirely, while a memory with no
// chunks yet still ranks off its legacy vector. So a half-finished backfill
// degrades per-memory rather than blanking the dense arm, and the legacy branch
// costs nothing once the backfill completes — it simply matches no rows, which
// is also what makes it safe to delete later (expand/migrate/contract).
//
// A memory is never scored from both shapes. The legacy vector is an embedding
// of the same content truncated at the embedder's window, so counting it
// alongside the chunks would let the truncated vector outrank the memory's own
// best chunk.
func (r *MemoryRepo) scoreCandidateVectors(ctx context.Context, candidateIDs []uuid.UUID, queryVec []float32) ([]chunkScore, error) {
	const chunkQ = `
		SELECT memory_id, chunk_idx, embedding
		FROM memory_chunks
		WHERE memory_id = ANY($1)`

	var chunkRows []struct {
		MemoryID  uuid.UUID `db:"memory_id"`
		ChunkIdx  int       `db:"chunk_idx"`
		Embedding string    `db:"embedding"`
	}
	if err := r.db.SelectContext(ctx, &chunkRows, chunkQ, pq.Array(candidateIDs)); err != nil {
		return nil, fmt.Errorf("vector search: select chunk vectors: %w", err)
	}

	scores := make([]chunkScore, 0, len(chunkRows)+len(candidateIDs))
	for _, row := range chunkRows {
		vec, err := domain.DecodeEmbedding(row.Embedding)
		if err != nil {
			// Skip corrupted embeddings silently, as the pre-chunking path did:
			// one unreadable chunk must not fail a whole recall.
			continue
		}
		scores = append(scores, chunkScore{
			memoryID: row.MemoryID,
			chunkIdx: row.ChunkIdx,
			score:    cosineSimilarity(queryVec, vec),
		})
	}

	// Legacy single-vector fallback for memories not yet chunked. NOT EXISTS is
	// what enforces "never both shapes for one memory".
	const legacyQ = `
		SELECT id, embedding
		FROM memories
		WHERE id = ANY($1)
		  AND embedding IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM memory_chunks c WHERE c.memory_id = memories.id)`

	var legacyRows []struct {
		ID        uuid.UUID `db:"id"`
		Embedding string    `db:"embedding"`
	}
	if err := r.db.SelectContext(ctx, &legacyRows, legacyQ, pq.Array(candidateIDs)); err != nil {
		return nil, fmt.Errorf("vector search: select legacy vectors: %w", err)
	}

	for _, row := range legacyRows {
		if row.Embedding == "" {
			continue
		}
		// Legacy vectors are JSON float arrays, not the base64 encoding used by
		// memory_chunks (ADR-0002 changed the encoding for new writes only).
		var vec []float32
		if err := json.Unmarshal([]byte(row.Embedding), &vec); err != nil {
			continue
		}
		scores = append(scores, chunkScore{
			memoryID: row.ID,
			chunkIdx: 0,
			score:    cosineSimilarity(queryVec, vec),
		})
	}

	return scores, nil
}

// hydrateScoredMemories loads the full memory rows for an already-ranked list and
// returns them in that ranking order.
//
// Order is restored from `ranked`, not taken from the query: `WHERE id = ANY(...)`
// returns rows in whatever order Postgres finds convenient, so relying on it
// would scramble the ranking this function exists to preserve.
//
// The full memoryColumns set is selected here, including the health-lifecycle
// columns (status, freshness_score, ...) that the old embedding-carrying column
// list omitted. That omission was not cosmetic: downstream, applyExtendedFilters
// drops superseded memories by checking Memory.Status, and the vector arm was
// handing it rows whose Status was the zero value — so ExcludeSuperseded held
// for BM25 hits (which filter in SQL) and silently did not for vector-only hits.
// Freshness decay had the same hole, since FreshnessScore arrived as 0 and the
// scorer reads 0 as "pre-lifecycle, treat as 1.0", i.e. no penalty.
func (r *MemoryRepo) hydrateScoredMemories(ctx context.Context, ranked []chunkScore) ([]domain.ScoredMemory, error) {
	if len(ranked) == 0 {
		return nil, nil
	}

	ids := make([]uuid.UUID, len(ranked))
	for i, cs := range ranked {
		ids[i] = cs.memoryID
	}

	var rows []memoryRow
	q := fmt.Sprintf(`SELECT %s FROM memories WHERE id = ANY($1)`, memoryColumns)
	if err := r.db.SelectContext(ctx, &rows, q, pq.Array(ids)); err != nil {
		return nil, fmt.Errorf("vector search: hydrate ranked memories: %w", err)
	}

	byID := make(map[uuid.UUID]domain.Memory, len(rows))
	for i := range rows {
		byID[rows[i].ID] = rows[i].toDomain()
	}

	result := make([]domain.ScoredMemory, 0, len(ranked))
	for _, cs := range ranked {
		mem, ok := byID[cs.memoryID]
		if !ok {
			// Deleted between the ranking and hydration queries — drop it rather
			// than emitting a zero-valued Memory.
			continue
		}
		result = append(result, domain.ScoredMemory{Memory: mem, Score: cs.score})
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

// UpdateEmbeddingKeepUpdatedAt is UpdateEmbedding minus the `updated_at = NOW()` bump — see
// the interface doc for why a repair job must not touch that column. `updated_at = updated_at`
// is written explicitly rather than omitted: this is a deliberate hold, not an oversight, and
// DecayRelevance already states the same intent the same way.
func (r *MemoryRepo) UpdateEmbeddingKeepUpdatedAt(ctx context.Context, id uuid.UUID, vec []float32, model string, dim int) error {
	encoded, err := json.Marshal(vec)
	if err != nil {
		return fmt.Errorf("update embedding (keep updated_at): encode vector: %w", err)
	}
	_, err = r.db.ExecContext(ctx,
		`UPDATE memories
		 SET embedding       = $1,
		     embedding_model = $2,
		     embedding_dim   = $3,
		     updated_at      = updated_at
		 WHERE id = $4`,
		string(encoded), model, dim, id,
	)
	return err
}

// MarkEmbeddingModel sets embedding_model without touching embedding/embedding_dim — see the
// interface doc: not called by the chunked embed path (embedChunked uses UpdateEmbedding),
// kept as a general repo primitive.
func (r *MemoryRepo) MarkEmbeddingModel(ctx context.Context, id uuid.UUID, model string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE memories SET embedding_model = $1, updated_at = NOW() WHERE id = $2`,
		model, id,
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

// ArchiveStaleWorkspaceCheckpoints sets archived=true for workspace-scoped session-checkpoint
// memories older than olderThan with importance_score < maxImportance.
// Entries tagged canonical/pavel-decision/kind:decision/kind:incident are never touched.
func (r *MemoryRepo) ArchiveStaleWorkspaceCheckpoints(ctx context.Context, olderThan time.Duration, maxImportance float64) (int64, error) {
	cutoff := time.Now().Add(-olderThan)
	result, err := r.db.ExecContext(ctx, `
		UPDATE memories SET archived = true
		WHERE scope = 'workspace'
		  AND (
		    tags @> ARRAY['kind:session-checkpoint']::text[]
		    OR tags @> ARRAY['session-checkpoint']::text[]
		  )
		  AND importance_score < $1
		  AND created_at < $2
		  AND archived = false
		  AND NOT (
		    tags @> ARRAY['canonical']::text[]
		    OR tags @> ARRAY['pavel-decision']::text[]
		    OR tags @> ARRAY['kind:canonical-decision']::text[]
		    OR tags @> ARRAY['kind:decision']::text[]
		    OR tags @> ARRAY['kind:incident']::text[]
		  )
	`, maxImportance, cutoff)
	if err != nil {
		return 0, fmt.Errorf("archive stale workspace checkpoints: %w", err)
	}
	return result.RowsAffected()
}

// FindPinned returns all non-archived, non-expired memories tagged kind:pinned in workspaceID.
// If projectID is non-nil, returns pinned memories from both workspace and project scope.
func (r *MemoryRepo) FindPinned(ctx context.Context, workspaceID uuid.UUID, projectID *uuid.UUID) ([]domain.Memory, error) {
	q := fmt.Sprintf(`SELECT %s FROM memories
		WHERE workspace_id = $1
		  AND tags @> ARRAY['kind:pinned']::text[]
		  AND archived = false
		  AND (expires_at IS NULL OR expires_at > NOW())`, memoryColumns)
	args := []interface{}{workspaceID}

	if projectID != nil {
		q += ` AND (scope = 'workspace' OR (scope = 'project' AND project_id = $2))`
		args = append(args, *projectID)
	} else {
		q += ` AND scope = 'workspace'`
	}
	q += ` ORDER BY importance_score DESC, updated_at DESC`

	var rows []memoryRow
	if err := r.db.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, fmt.Errorf("find pinned memories: %w", err)
	}
	memories := make([]domain.Memory, 0, len(rows))
	for _, row := range rows {
		memories = append(memories, row.toDomain())
	}
	return memories, nil
}

// ExpireByValidUntil archives memories whose valid_until has passed. It sets
// status='archived' and freshness_score=0.0 for at most 500 rows per call.
// Memories already archived or superseded are skipped, making the call idempotent.
func (r *MemoryRepo) ExpireByValidUntil(ctx context.Context) (int64, error) {
	// LIMIT is not directly supported in an UPDATE, so scope the update via a
	// ctid subquery. This preserves the 500-row batch cap while remaining idempotent.
	result, err := r.db.ExecContext(ctx, `
		UPDATE memories
		SET status = 'archived', freshness_score = 0.0, updated_at = NOW()
		WHERE ctid IN (
			SELECT ctid FROM memories
			WHERE valid_until IS NOT NULL
			  AND valid_until <= NOW()
			  AND status NOT IN ('archived', 'superseded')
			LIMIT 500
		)
	`)
	if err != nil {
		return 0, fmt.Errorf("expire by valid_until: %w", err)
	}
	return result.RowsAffected()
}

// MarkStaleByAge marks active memories as stale when they have not been updated in
// staleAfter and were created after epoch. The epoch gate prevents a mass-stale
// avalanche on first deploy (nothing created before the epoch is eligible).
// High-importance memories (importance_score >= 0.8) are protected. Batch cap 500.
func (r *MemoryRepo) MarkStaleByAge(ctx context.Context, epoch time.Time, staleAfter time.Duration) (int64, error) {
	cutoff := time.Now().Add(-staleAfter)
	result, err := r.db.ExecContext(ctx, `
		UPDATE memories
		SET status = 'stale', freshness_score = 0.25, updated_at = NOW()
		WHERE ctid IN (
			SELECT ctid FROM memories
			WHERE status = 'active'
			  AND updated_at < $1
			  AND created_at > $2
			  AND importance_score < 0.8
			LIMIT 500
		)
	`, cutoff, epoch)
	if err != nil {
		return 0, fmt.Errorf("mark stale by age: %w", err)
	}
	return result.RowsAffected()
}

// SetMemoryStatus updates the status, freshness_score, and superseded_by of a single
// memory. freshness_score is derived from status.StatusFreshnessScore().
func (r *MemoryRepo) SetMemoryStatus(ctx context.Context, id uuid.UUID, status domain.MemoryStatus, supersededBy *uuid.UUID) error {
	freshness := status.StatusFreshnessScore()
	_, err := r.db.ExecContext(ctx, `
		UPDATE memories
		SET status          = $1,
		    freshness_score = $2,
		    superseded_by   = $3,
		    updated_at      = NOW()
		WHERE id = $4
	`, status, freshness, supersededBy, id)
	if err != nil {
		return fmt.Errorf("set memory status: %w", err)
	}
	return nil
}

// ListCreatedSince returns memories that have a stored embedding and were created at
// or after since, ordered by created_at DESC and capped at limit. Used by the
// reconciler linker phase to find recently written memories for dedup analysis.
func (r *MemoryRepo) ListCreatedSince(ctx context.Context, since time.Time, limit int) ([]domain.Memory, error) {
	if limit <= 0 {
		limit = 50
	}
	q := fmt.Sprintf(`
		SELECT %s FROM memories
		WHERE created_at >= $1
		  AND embedding IS NOT NULL
		ORDER BY created_at DESC
		LIMIT $2`,
		memoryColumns,
	)
	var rows []memoryRow
	if err := r.db.SelectContext(ctx, &rows, q, since, limit); err != nil {
		return nil, fmt.Errorf("memory list created since: %w", err)
	}
	memories := make([]domain.Memory, len(rows))
	for i, row := range rows {
		memories[i] = row.toDomain()
	}
	return memories, nil
}

// ListNeedingEmbedding returns up to limit memories that need to be (re)embedded with the
// currently configured model. Excluded (does NOT need embedding) when EITHER holds for the
// given model: memories.embedding is populated with a matching embedding_model, OR the
// memory already has a memory_chunks row with a matching embedding_model. The chunk check is
// independent of the embedding column on purpose (#84b0694d/#7cf0f3be, live regression):
// an earlier revision relied on embedding_model alone as a watermark while leaving
// memories.embedding NULL, and the embedding-column disjunct below matched it forever —
// reindex re-embedded the same rows every run without ever converging. Checking chunk
// freshness directly means this predicate stays correct regardless of whether the write path
// also populates memories.embedding (today it does, as a stopgap — see embedChunked's doc).
// Vectors/chunks from another model live in a different vector space, score 0 in
// cosineSimilarity (dimension guard), and are therefore invisible to semantic recall — this
// is what lets an operator switch embedding provider/model and have the corpus re-embed
// itself on the next batch run, instead of silently stranding it.
func (r *MemoryRepo) ListNeedingEmbedding(ctx context.Context, workspaceID uuid.UUID, model string, limit int) ([]domain.Memory, error) {
	if limit <= 0 {
		limit = 100
	}
	q := fmt.Sprintf(`
		SELECT %s FROM memories m
		WHERE m.workspace_id = $1
		  AND (m.expires_at IS NULL OR m.expires_at > now())
		  AND (m.embedding IS NULL OR m.embedding_model IS DISTINCT FROM $2)
		  AND NOT EXISTS (
		    SELECT 1 FROM memory_chunks c WHERE c.memory_id = m.id AND c.embedding_model = $2
		  )
		ORDER BY m.updated_at DESC
		LIMIT $3`,
		memoryColumns,
	)
	var rows []memoryRow
	if err := r.db.SelectContext(ctx, &rows, q, workspaceID, model, limit); err != nil {
		return nil, fmt.Errorf("memory list needing embedding: %w", err)
	}
	memories := make([]domain.Memory, len(rows))
	for i, row := range rows {
		memories[i] = row.toDomain()
	}
	return memories, nil
}

// ListNotYetChunked returns up to limit memories in workspaceID with no memory_chunks rows —
// see the interface doc for why this is a separate query from ListNeedingEmbedding.
func (r *MemoryRepo) ListNotYetChunked(ctx context.Context, workspaceID uuid.UUID, limit int) ([]domain.Memory, error) {
	if limit <= 0 {
		limit = 100
	}
	q := fmt.Sprintf(`
		SELECT %s FROM memories m
		WHERE m.workspace_id = $1
		  AND (m.expires_at IS NULL OR m.expires_at > now())
		  AND NOT EXISTS (SELECT 1 FROM memory_chunks c WHERE c.memory_id = m.id)
		ORDER BY m.updated_at DESC
		LIMIT $2`,
		memoryColumns,
	)
	var rows []memoryRow
	if err := r.db.SelectContext(ctx, &rows, q, workspaceID, limit); err != nil {
		return nil, fmt.Errorf("memory list not yet chunked: %w", err)
	}
	memories := make([]domain.Memory, len(rows))
	for i, row := range rows {
		memories[i] = row.toDomain()
	}
	return memories, nil
}

// chunkOffsetsStalePredicate is the SQL condition identifying a memory whose stored chunk
// offsets do not index its CURRENT content — the discriminating signal for "embedded under
// the pre-#494 scheme", and the shared body of ListNeedingRechunk and CountNeedingRechunk
// (one predicate, two callers, so the repair job and the convergence check can never drift
// apart and disagree about what "damaged" means).
//
// Why offsets and not a version watermark: chunkText always covers its whole input, so the
// largest chunk_end equals the byte length of the text that was actually chunked. Before
// #494 that text was the composite `key + " " + content + " " + tags`; since #494 it is
// content alone. So `max(chunk_end) <> octet_length(content)` reads the physical trace of
// which string the write path fed the chunker, rather than trusting a column the write path
// set about itself — the failure mode of #84b0694d, where embedding_model was stamped as a
// watermark while memories.embedding stayed NULL and the row was never actually repaired.
// Measured against prod before this shipped: 3254 of 3260 chunked memories matched the
// composite length EXACTLY (99.82%, the same reconstruction rate #38bb958c measured), 0
// matched content length, and the 6 that matched neither had content or tags edited after
// their last embed — stale offsets, correctly in the population.
//
// The predicate is therefore broader than "old scheme" in a way that is desirable: it means
// "these offsets no longer describe this content", which also catches a row edited without a
// re-embed. It is self-converging by construction — a repaired row's offsets index content,
// so it leaves the population and no cursor is needed.
//
// Honest limit: it detects content-only CHUNKING, the observable signature of the #494 fix,
// not the key+tags PREFIX itself. A row chunked on content but embedded without the prefix
// would be invisible here; no code path in main can produce one (embedChunked does both in
// the same call), but a future one that splits them must not rely on this predicate alone.
const chunkOffsetsStalePredicate = `
	m.workspace_id = $1
	AND (m.expires_at IS NULL OR m.expires_at > now())
	AND EXISTS (SELECT 1 FROM memory_chunks c WHERE c.memory_id = m.id)
	AND (SELECT max(c.chunk_end) FROM memory_chunks c WHERE c.memory_id = m.id)
	    IS DISTINCT FROM octet_length(m.content)`

// ListNeedingRechunk returns up to limit memories in workspaceID whose chunk offsets no
// longer index their content — see chunkOffsetsStalePredicate for what that establishes and
// what it does not.
//
// Deliberately NOT a change to ListNeedingEmbedding, and deliberately not ListNotYetChunked:
// these rows HAVE chunks (so ListNotYetChunked excludes them by construction — the exact trap
// recorded in `repair-job-selector-excludes-damaged-rows`) and they carry the current model's
// name (so ListNeedingEmbedding's model-mismatch filter never selects them either). Widening
// an existing repair predicate was the other option and was rejected: BatchEmbed's
// non-chunked branch (chunkRepo == nil) would then select these rows forever and never clear
// them, because writing memories.embedding alone cannot move a chunk offset.
//
// ORDER BY updated_at DESC matches its sibling queries. Convergence does not depend on the
// order: the population is CLOSED — every write path since #494 chunks content alone, so no
// new row can enter it — which is what makes "returned < limit ⇒ drained" reachable here,
// unlike the churning-population case in #b052cdda where fixtures kept jumping the queue.
func (r *MemoryRepo) ListNeedingRechunk(ctx context.Context, workspaceID uuid.UUID, limit int) ([]domain.Memory, error) {
	if limit <= 0 {
		limit = 100
	}
	q := fmt.Sprintf(`
		SELECT %s FROM memories m
		WHERE %s
		ORDER BY m.updated_at DESC
		LIMIT $2`,
		memoryColumns, chunkOffsetsStalePredicate,
	)
	var rows []memoryRow
	if err := r.db.SelectContext(ctx, &rows, q, workspaceID, limit); err != nil {
		return nil, fmt.Errorf("memory list needing rechunk: %w", err)
	}
	memories := make([]domain.Memory, len(rows))
	for i, row := range rows {
		memories[i] = row.toDomain()
	}
	return memories, nil
}

// CountNeedingRechunk returns how many memories in workspaceID currently satisfy
// ListNeedingRechunk's predicate — the whole remaining population, uncapped by any batch
// limit. It exists so convergence is judged by a direct count of the damaged population
// rather than by the repair endpoint's own "how many did I process" counter: a job whose
// selector misses the damaged rows reports a confident number and heals nothing
// (`solution-repair-job-selector-excludes-damaged-rows`). Both numbers are returned by the
// endpoint precisely so they can disagree visibly.
func (r *MemoryRepo) CountNeedingRechunk(ctx context.Context, workspaceID uuid.UUID) (int, error) {
	q := fmt.Sprintf(`SELECT count(*) FROM memories m WHERE %s`, chunkOffsetsStalePredicate)
	var n int
	if err := r.db.GetContext(ctx, &n, q, workspaceID); err != nil {
		return 0, fmt.Errorf("memory count needing rechunk: %w", err)
	}
	return n, nil
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

// FullTextSearchRanked runs a BM25-style full-text search using the PostgreSQL 'english'
// dictionary and ts_rank_cd. Unlike FullTextSearch (which uses the pre-built search_vector
// with the 'simple' dictionary), this function re-computes the tsvector on-the-fly from
// content and key for better linguistic accuracy (stemming, stopword removal).
//
// ExcludeSuperseded is always applied: status='superseded' entries never appear in results.
// Both archived and expired memories are excluded. The result is ordered by ts_rank_cd DESC
// and capped at limit. Batch-touches last_accessed_at for all returned rows.
//
// Used as the sparse (BM25) arm of the RRF fusion in service.Recall.
//
// The scope/tags eligibility filter is applied here as SQL, not downstream: this arm
// truncates to `limit` rows by ts_rank_cd, so filtering after the fact would silently
// discard eligible rows that ranked below the cut and could never get them back.
func (r *MemoryRepo) FullTextSearchRanked(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID, query string, filter domain.MemorySearchFilter, limit int) ([]domain.ScoredMemory, error) {
	if limit <= 0 {
		limit = 20
	}

	args := []interface{}{wsID, query} // $1=workspace_id, $2=query
	conditions := []string{
		"workspace_id = $1",
		"(expires_at IS NULL OR expires_at > NOW())",
		"archived = false",
		"status != 'superseded'",
		"to_tsvector('english', content || ' ' || COALESCE(key, '')) @@ plainto_tsquery('english', $2)",
	}
	argIdx := 3

	if projID != nil {
		conditions = append(conditions, fmt.Sprintf("project_id = $%d", argIdx))
		args = append(args, *projID)
		argIdx++
	}
	conditions, args, argIdx = appendMemorySearchFilter(conditions, args, argIdx, filter)

	args = append(args, limit)
	limitIdx := argIdx

	q := fmt.Sprintf(`
		SELECT %s,
		       ts_rank_cd(
		           to_tsvector('english', content || ' ' || COALESCE(key, '')),
		           plainto_tsquery('english', $2)
		       ) AS score
		FROM memories
		WHERE %s
		ORDER BY score DESC
		LIMIT $%d`,
		memoryColumns,
		joinAnd(conditions),
		limitIdx,
	)

	var rows []scoredMemoryRow
	if err := r.db.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, fmt.Errorf("fts ranked: %w", err)
	}

	// OR-token fallback when the strict AND query returns too few results — mirrors the
	// FullTextSearch ('simple') path. This arm is not just a nicety: it is the *sole* arm
	// left standing whenever the dense/embedding arm is down (service.Recall fails open to
	// "bm25-only" on an embedding error, e.g. HTTP 402 from the provider). plainto_tsquery
	// ANDs every term, so a 7-word natural-language wake-up query then matches nothing and
	// recall returns zero rows. Relaxing to token-level OR degrades gracefully instead.
	// Errors in the fallback are non-fatal; we continue with whatever AND found.
	if len(rows) < minFTSHits {
		if orRows, err2 := r.ftsRankedORFallback(ctx, wsID, projID, query, filter, limit, rows); err2 == nil {
			rows = orRows
		}
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

	// Batch-touch last_accessed_at (1-hour idempotency window) over the FINAL merged set.
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

// ftsRankedORFallback is the FullTextSearchRanked sibling of ftsORFallback: it runs a
// token-level OR search and merges the hits into andRows, discounting OR-only rows.
//
// It cannot reuse ftsORFallback because that one targets the pre-built search_vector with
// the 'simple' dictionary, whereas this arm re-computes the tsvector on the fly with the
// 'english' dictionary. The WHERE clause below mirrors FullTextSearchRanked's predicates
// exactly (workspace, expiry, archived, status != 'superseded', optional project, and the
// scope/tags eligibility filter) — dropping any of them would leak superseded/archived/
// other-project/out-of-scope rows in via the fallback. That risk is not theoretical here:
// this fallback is the widening path, so it is precisely where an unfiltered query would
// pull the most ineligible rows in.
func (r *MemoryRepo) ftsRankedORFallback(
	ctx context.Context,
	wsID uuid.UUID,
	projID *uuid.UUID,
	query string,
	filter domain.MemorySearchFilter,
	limit int,
	andRows []scoredMemoryRow,
) ([]scoredMemoryRow, error) {
	orFragment := tokenizeForORQuery(query)
	if orFragment == "" {
		return andRows, nil
	}

	args := []interface{}{wsID, orFragment} // $1=workspace_id, $2=OR tsquery fragment
	conditions := []string{
		"workspace_id = $1",
		"(expires_at IS NULL OR expires_at > NOW())",
		"archived = false",
		"status != 'superseded'",
		"to_tsvector('english', content || ' ' || COALESCE(key, '')) @@ to_tsquery('english', $2)",
	}
	argIdx := 3

	if projID != nil {
		conditions = append(conditions, fmt.Sprintf("project_id = $%d", argIdx))
		args = append(args, *projID)
		argIdx++
	}
	conditions, args, argIdx = appendMemorySearchFilter(conditions, args, argIdx, filter)

	args = append(args, limit)
	limitIdx := argIdx

	q := fmt.Sprintf(`
		SELECT %s,
		       ts_rank_cd(
		           to_tsvector('english', content || ' ' || COALESCE(key, '')),
		           to_tsquery('english', $2)
		       ) AS score
		FROM memories
		WHERE %s
		ORDER BY score DESC
		LIMIT $%d`,
		memoryColumns,
		joinAnd(conditions),
		limitIdx,
	)

	var orRows []scoredMemoryRow
	if err := r.db.SelectContext(ctx, &orRows, q, args...); err != nil {
		return andRows, fmt.Errorf("fts ranked or fallback: %w", err)
	}
	if len(orRows) == 0 {
		return andRows, nil
	}

	return mergeORScoredRows(andRows, orRows, limit), nil
}

// FindByShortID returns the first non-archived memory in workspaceID whose UUID text
// representation starts with prefix (6–12 lowercase hex chars, no dashes).
// UUIDs in Postgres are formatted as "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx" — the first
// 8 characters are pure hex digits, so a 6–8 char prefix matches the leading hex segment.
// Returns nil without error when no match exists.
func (r *MemoryRepo) FindByShortID(ctx context.Context, workspaceID uuid.UUID, prefix string) (*domain.Memory, error) {
	if len(prefix) < 6 || len(prefix) > 12 {
		return nil, nil
	}
	var row memoryRow
	err := r.db.GetContext(ctx, &row,
		fmt.Sprintf(`SELECT %s FROM memories WHERE workspace_id = $1 AND id::text LIKE $2 || '%%' AND archived = false LIMIT 1`, memoryColumns),
		workspaceID, prefix,
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

// SetArchived sets the archived flag on a memory by ID.
// Used by the supersede hook to mark the superseded memory as archived.
func (r *MemoryRepo) SetArchived(ctx context.Context, id uuid.UUID, archived bool) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE memories SET archived = $2, updated_at = NOW() WHERE id = $1`,
		id, archived,
	)
	return err
}

// FindByThreadID returns non-archived memories in workspaceID whose thread_id matches,
// excluding the memory identified by excludeID. Used by Amendment 2 to find candidates
// for same-thread relates_to edges.
func (r *MemoryRepo) FindByThreadID(ctx context.Context, workspaceID uuid.UUID, threadID string, excludeID uuid.UUID) ([]domain.Memory, error) {
	var rows []memoryRow
	err := r.db.SelectContext(ctx, &rows,
		fmt.Sprintf(`SELECT %s FROM memories
			WHERE workspace_id = $1
			  AND thread_id    = $2
			  AND id          != $3
			  AND archived     = false
			  AND (expires_at IS NULL OR expires_at > NOW())
			ORDER BY created_at DESC
			LIMIT 100`, memoryColumns),
		workspaceID, threadID, excludeID,
	)
	if err != nil {
		return nil, err
	}
	memories := make([]domain.Memory, len(rows))
	for i, row := range rows {
		memories[i] = row.toDomain()
	}
	return memories, nil
}

// FindBySourceTaskIDs returns non-archived memories in workspaceID whose source_task_id
// is one of the given task UUIDs. Used by Amendment 3 (task-graph bridge) to find
// memories from related tasks (parent / depends_on) for derived_from edges.
func (r *MemoryRepo) FindBySourceTaskIDs(ctx context.Context, workspaceID uuid.UUID, sourceTaskIDs []uuid.UUID) ([]domain.Memory, error) {
	if len(sourceTaskIDs) == 0 {
		return nil, nil
	}
	var rows []memoryRow
	err := r.db.SelectContext(ctx, &rows,
		fmt.Sprintf(`SELECT %s FROM memories
			WHERE workspace_id  = $1
			  AND source_task_id = ANY($2::uuid[])
			  AND archived       = false
			  AND (expires_at IS NULL OR expires_at > NOW())
			ORDER BY created_at DESC
			LIMIT 200`, memoryColumns),
		workspaceID, pq.Array(sourceTaskIDs),
	)
	if err != nil {
		return nil, err
	}
	memories := make([]domain.Memory, len(rows))
	for i, row := range rows {
		memories[i] = row.toDomain()
	}
	return memories, nil
}
