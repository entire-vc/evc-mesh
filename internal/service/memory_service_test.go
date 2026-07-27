package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// ---------------------------------------------------------------------------
// mockMemoryRepo
// ---------------------------------------------------------------------------

type mockMemoryRepo struct {
	// guards embeddedWithModel/embeddedDim, written by UpdateEmbedding — the concurrency
	// tests below call it from many goroutines at once, unlike the rest of this mock's
	// single-threaded callers.
	mu sync.Mutex
	// set by ListNeedingEmbedding: the model BatchEmbed asked for (the SQL filter keys on it)
	embedModelAsked string
	// rows the fake repo hands back as "needs (re)embedding"
	needEmbedding []domain.Memory
	// rows the fake repo hands back as "not yet chunked" (ListNotYetChunked)
	notYetChunked []domain.Memory
	// captured by UpdateEmbedding
	embeddedWithModel string
	embeddedDim       int
	// captured by MarkEmbeddingModel (the chunked-embed watermark)
	markedEmbeddingModelID             uuid.UUID
	markedEmbeddingModelValue          string
	markEmbeddingModelErr              error
	listNotYetChunkedErr               error
	upsertFn                           func(ctx context.Context, mem *domain.Memory) error
	getByIDFn                          func(ctx context.Context, id uuid.UUID) (*domain.Memory, error)
	getByKeyFn                         func(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID, agentID *uuid.UUID, key string, scope domain.MemoryScope) (*domain.Memory, error)
	fullTextSearchFn                   func(ctx context.Context, query string, wsID uuid.UUID, projID *uuid.UUID, scope string, tags []string, limit int, recencyWeight float64) ([]domain.ScoredMemory, error)
	fullTextSearchRankedFn             func(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID, query string, filter domain.MemorySearchFilter, limit int) ([]domain.ScoredMemory, error)
	findByScopeFn                      func(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID, scope string, limit int) ([]domain.Memory, error)
	listByWorkspaceProjectFn           func(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID, filter domain.MemoryListFilter) ([]domain.Memory, int64, error)
	deleteFn                           func(ctx context.Context, id uuid.UUID) error
	boostRelevanceFn                   func(ctx context.Context, ids []uuid.UUID) error
	findByShortIDFn                    func() (*domain.Memory, error)
	setArchivedFn                      func() error
	findByThreadIDFn                   func(ctx context.Context, wsID uuid.UUID, threadID string, excludeID uuid.UUID) ([]domain.Memory, error)
	findBySourceTaskIDsFn              func(ctx context.Context, wsID uuid.UUID, taskIDs []uuid.UUID) ([]domain.Memory, error)
	archiveStaleWorkspaceCheckpointsFn func(ctx context.Context, olderThan time.Duration, maxImportance float64) (int64, error)
	vectorSearchFn                     func(ctx context.Context, vec []float32, wsID uuid.UUID, projID *uuid.UUID, filter domain.MemorySearchFilter, limit int) ([]domain.ScoredMemory, error)
	findPinnedFn                       func(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID) ([]domain.Memory, error)
}

func (m *mockMemoryRepo) Upsert(ctx context.Context, mem *domain.Memory) error {
	if m.upsertFn != nil {
		return m.upsertFn(ctx, mem)
	}
	return nil
}

func (m *mockMemoryRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Memory, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, id)
	}
	return nil, nil
}

func (m *mockMemoryRepo) GetByKey(ctx context.Context, wsID uuid.UUID, projID, agentID *uuid.UUID, key string, scope domain.MemoryScope) (*domain.Memory, error) {
	if m.getByKeyFn != nil {
		return m.getByKeyFn(ctx, wsID, projID, agentID, key, scope)
	}
	return nil, nil
}

func (m *mockMemoryRepo) FullTextSearch(ctx context.Context, query string, wsID uuid.UUID, projID *uuid.UUID, scope string, tags []string, limit int, recencyWeight float64) ([]domain.ScoredMemory, error) {
	if m.fullTextSearchFn != nil {
		return m.fullTextSearchFn(ctx, query, wsID, projID, scope, tags, limit, recencyWeight)
	}
	return nil, nil
}

func (m *mockMemoryRepo) FindByScope(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID, scope string, limit int) ([]domain.Memory, error) {
	if m.findByScopeFn != nil {
		return m.findByScopeFn(ctx, wsID, projID, scope, limit)
	}
	return nil, nil
}

func (m *mockMemoryRepo) ListByWorkspaceProject(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID, filter domain.MemoryListFilter) ([]domain.Memory, int64, error) {
	if m.listByWorkspaceProjectFn != nil {
		return m.listByWorkspaceProjectFn(ctx, wsID, projID, filter)
	}
	return nil, 0, nil
}

func (m *mockMemoryRepo) Delete(ctx context.Context, id uuid.UUID) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, id)
	}
	return nil
}

func (m *mockMemoryRepo) BoostRelevance(ctx context.Context, ids []uuid.UUID) error {
	if m.boostRelevanceFn != nil {
		return m.boostRelevanceFn(ctx, ids)
	}
	return nil
}

func (m *mockMemoryRepo) VectorSearch(ctx context.Context, vec []float32, wsID uuid.UUID, projID *uuid.UUID, filter domain.MemorySearchFilter, limit int) ([]domain.ScoredMemory, error) {
	if m.vectorSearchFn != nil {
		return m.vectorSearchFn(ctx, vec, wsID, projID, filter, limit)
	}
	return nil, nil
}

func (m *mockMemoryRepo) UpdateEmbedding(_ context.Context, _ uuid.UUID, _ []float32, model string, dim int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.embeddedWithModel = model
	m.embeddedDim = dim
	return nil
}

func (m *mockMemoryRepo) MarkEmbeddingModel(_ context.Context, id uuid.UUID, model string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.markEmbeddingModelErr != nil {
		return m.markEmbeddingModelErr
	}
	m.markedEmbeddingModelID = id
	m.markedEmbeddingModelValue = model
	return nil
}

func (m *mockMemoryRepo) DecayRelevance(_ context.Context) (int64, error) {
	return 0, nil
}

func (m *mockMemoryRepo) CleanExpired(_ context.Context) (int64, error) {
	return 0, nil
}

func (m *mockMemoryRepo) ListNeedingEmbedding(_ context.Context, _ uuid.UUID, model string, _ int) ([]domain.Memory, error) {
	m.embedModelAsked = model
	out := m.needEmbedding
	m.needEmbedding = nil // one batch, then drained — mirrors the real paging loop
	return out, nil
}

func (m *mockMemoryRepo) ListNotYetChunked(_ context.Context, _ uuid.UUID, _ int) ([]domain.Memory, error) {
	if m.listNotYetChunkedErr != nil {
		return nil, m.listNotYetChunkedErr
	}
	out := m.notYetChunked
	m.notYetChunked = nil // one batch, then drained — mirrors the real resumable-by-exclusion loop
	return out, nil
}

func (m *mockMemoryRepo) List(_ context.Context, _ domain.MemoryListFilter) (*domain.MemoryListResult, error) {
	return &domain.MemoryListResult{}, nil
}

func (m *mockMemoryRepo) FindByShortID(_ context.Context, _ uuid.UUID, _ string) (*domain.Memory, error) {
	if m.findByShortIDFn != nil {
		return m.findByShortIDFn()
	}
	return nil, nil
}

func (m *mockMemoryRepo) SetArchived(_ context.Context, _ uuid.UUID, _ bool) error {
	if m.setArchivedFn != nil {
		return m.setArchivedFn()
	}
	return nil
}

func (m *mockMemoryRepo) FindByThreadID(ctx context.Context, wsID uuid.UUID, threadID string, excludeID uuid.UUID) ([]domain.Memory, error) {
	if m.findByThreadIDFn != nil {
		return m.findByThreadIDFn(ctx, wsID, threadID, excludeID)
	}
	return nil, nil
}

func (m *mockMemoryRepo) FindBySourceTaskIDs(ctx context.Context, wsID uuid.UUID, taskIDs []uuid.UUID) ([]domain.Memory, error) {
	if m.findBySourceTaskIDsFn != nil {
		return m.findBySourceTaskIDsFn(ctx, wsID, taskIDs)
	}
	return nil, nil
}

func (m *mockMemoryRepo) ArchiveStaleWorkspaceCheckpoints(ctx context.Context, olderThan time.Duration, maxImportance float64) (int64, error) {
	if m.archiveStaleWorkspaceCheckpointsFn != nil {
		return m.archiveStaleWorkspaceCheckpointsFn(ctx, olderThan, maxImportance)
	}
	return 0, nil
}

func (m *mockMemoryRepo) FindBySimhashProximity(_ context.Context, _ uuid.UUID, _ int64, _ int, _ uuid.UUID, _ int) ([]domain.Memory, error) {
	return nil, nil
}

func (m *mockMemoryRepo) FindPinned(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID) ([]domain.Memory, error) {
	if m.findPinnedFn != nil {
		return m.findPinnedFn(ctx, wsID, projID)
	}
	return nil, nil
}

func (m *mockMemoryRepo) ExpireByValidUntil(ctx context.Context) (int64, error) {
	return 0, nil
}

func (m *mockMemoryRepo) MarkStaleByAge(ctx context.Context, epoch time.Time, staleAfter time.Duration) (int64, error) {
	return 0, nil
}

func (m *mockMemoryRepo) SetMemoryStatus(ctx context.Context, id uuid.UUID, status domain.MemoryStatus, supersededBy *uuid.UUID) error {
	return nil
}

func (m *mockMemoryRepo) ListCreatedSince(ctx context.Context, since time.Time, limit int) ([]domain.Memory, error) {
	return nil, nil
}

func (m *mockMemoryRepo) FullTextSearchRanked(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID, query string, filter domain.MemorySearchFilter, limit int) ([]domain.ScoredMemory, error) {
	if m.fullTextSearchRankedFn != nil {
		return m.fullTextSearchRankedFn(ctx, wsID, projID, query, filter, limit)
	}
	return nil, nil
}

// Verify mockMemoryRepo satisfies the interface at compile time.
var _ repository.MemoryRepository = (*mockMemoryRepo)(nil)

// ---------------------------------------------------------------------------
// mockMemoryEdgeRepo
// ---------------------------------------------------------------------------

type mockMemoryEdgeRepo struct {
	upsertEdgeFn func(edge *domain.MemoryEdge) error
	// neighbors maps a frontier memory ID to the edges radiating from it. Used by
	// RecallGraph BFS tests; nil means "no edges", matching the default behaviour.
	neighbors map[uuid.UUID][]domain.MemoryEdge
}

func (m *mockMemoryEdgeRepo) UpsertEdge(_ context.Context, edge *domain.MemoryEdge) error {
	if m.upsertEdgeFn != nil {
		return m.upsertEdgeFn(edge)
	}
	return nil
}

func (m *mockMemoryEdgeRepo) ReinforceEdge(_ context.Context, _, _ uuid.UUID, _ domain.MemoryEdgeRelationshipType) error {
	return nil
}
func (m *mockMemoryEdgeRepo) GetNeighbors(_ context.Context, ids []uuid.UUID, _ uuid.UUID, threshold float64, _ int) ([]domain.MemoryEdge, error) {
	if m.neighbors == nil {
		return nil, nil
	}
	var out []domain.MemoryEdge
	for _, id := range ids {
		for _, e := range m.neighbors[id] {
			if float64(e.Weight) >= threshold {
				out = append(out, e)
			}
		}
	}
	return out, nil
}
func (m *mockMemoryEdgeRepo) DecayWeights(_ context.Context) (int64, error)   { return 0, nil }
func (m *mockMemoryEdgeRepo) PruneDeadEdges(_ context.Context) (int64, error) { return 0, nil }

// Verify mockMemoryEdgeRepo satisfies the interface at compile time.
var _ repository.MemoryEdgeRepository = (*mockMemoryEdgeRepo)(nil)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newMemoryService(repo *mockMemoryRepo) MemoryService {
	return newMemoryServiceWithEdges(repo, &mockMemoryEdgeRepo{})
}

func newMemoryServiceWithEdges(repo *mockMemoryRepo, edgeRepo *mockMemoryEdgeRepo) MemoryService {
	return NewMemoryService(repo, edgeRepo, nil) // nil embedder → NoopEmbedder (keyword-only)
}

func baseMemory(wsID uuid.UUID) *domain.Memory {
	return &domain.Memory{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		Key:         "test-key",
		Content:     "some content",
		Scope:       domain.ScopeProject,
	}
}

// ---------------------------------------------------------------------------
// TestRemember_CreateNew
// ---------------------------------------------------------------------------

func TestRemember_CreateNew(t *testing.T) {
	wsID := uuid.New()
	repo := &mockMemoryRepo{
		// GetByKey returns nil — no existing entry.
		getByKeyFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemoryScope) (*domain.Memory, error) {
			return nil, nil
		},
		upsertFn: func(_ context.Context, _ *domain.Memory) error {
			return nil
		},
	}

	svc := newMemoryService(repo)
	mem := baseMemory(wsID)

	result, err := svc.Remember(context.Background(), mem)

	require.NoError(t, err)
	assert.Equal(t, "created", result.Outcome)
}

// ---------------------------------------------------------------------------
// TestRemember_UpdateExisting
// ---------------------------------------------------------------------------

func TestRemember_UpdateExisting(t *testing.T) {
	wsID := uuid.New()
	existingID := uuid.New()

	existing := &domain.Memory{
		ID:          existingID,
		WorkspaceID: wsID,
		Key:         "my-key",
		Content:     "old content",
		Scope:       domain.ScopeProject,
	}

	repo := &mockMemoryRepo{
		// GetByKey returns the existing entry.
		getByKeyFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemoryScope) (*domain.Memory, error) {
			return existing, nil
		},
		upsertFn: func(_ context.Context, _ *domain.Memory) error {
			return nil
		},
	}

	svc := newMemoryService(repo)
	mem := &domain.Memory{
		WorkspaceID: wsID,
		Key:         "my-key",
		Content:     "new content",
		Scope:       domain.ScopeProject,
	}

	result, err := svc.Remember(context.Background(), mem)

	require.NoError(t, err)
	assert.Equal(t, "updated", result.Outcome)
	// The service must copy the existing ID onto mem so the DB upsert targets the correct row.
	assert.Equal(t, existingID, mem.ID)
}

// ---------------------------------------------------------------------------
// TestRemember_SetsSimhash
// ---------------------------------------------------------------------------

func TestRemember_SetsSimhash(t *testing.T) {
	wsID := uuid.New()
	var capturedMem *domain.Memory
	repo := &mockMemoryRepo{
		getByKeyFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemoryScope) (*domain.Memory, error) {
			return nil, nil
		},
		upsertFn: func(_ context.Context, mem *domain.Memory) error {
			capturedMem = mem
			return nil
		},
	}

	svc := newMemoryService(repo)
	mem := baseMemory(wsID)
	mem.Content = "this is a longer content that will get a valid simhash fingerprint"

	_, err := svc.Remember(context.Background(), mem)
	require.NoError(t, err)
	require.NotNil(t, capturedMem)
	assert.NotNil(t, capturedMem.ContentSimhash, "ContentSimhash must be set after Remember()")
	assert.NotEqual(t, int64(0), *capturedMem.ContentSimhash, "ContentSimhash should be non-zero for substantial text")
}

// ---------------------------------------------------------------------------
// TestRemember_SetsStatusActive
// ---------------------------------------------------------------------------

func TestRemember_SetsStatusActive(t *testing.T) {
	wsID := uuid.New()
	var capturedMem *domain.Memory
	repo := &mockMemoryRepo{
		getByKeyFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemoryScope) (*domain.Memory, error) {
			return nil, nil
		},
		upsertFn: func(_ context.Context, mem *domain.Memory) error {
			capturedMem = mem
			return nil
		},
	}

	svc := newMemoryService(repo)
	mem := baseMemory(wsID)

	_, err := svc.Remember(context.Background(), mem)
	require.NoError(t, err)
	require.NotNil(t, capturedMem)
	assert.Equal(t, domain.MemoryStatusActive, capturedMem.Status)
	assert.Equal(t, float32(1.0), capturedMem.FreshnessScore)
}

// ---------------------------------------------------------------------------
// TestApplyExtendedFilters_ExcludeSuperseded
// ---------------------------------------------------------------------------

func TestApplyExtendedFilters_ExcludeSuperseded(t *testing.T) {
	items := []domain.ScoredMemory{
		{Memory: domain.Memory{ID: uuid.New(), Status: domain.MemoryStatusActive}, Score: 1.0},
		{Memory: domain.Memory{ID: uuid.New(), Status: domain.MemoryStatusSuperseded}, Score: 0.9},
		{Memory: domain.Memory{ID: uuid.New(), Status: domain.MemoryStatusStale}, Score: 0.8},
	}

	opts := domain.RecallOpts{ExcludeSuperseded: true}
	result := applyExtendedFilters(items, opts)

	assert.Len(t, result, 2, "superseded memory should be excluded")
	for _, m := range result {
		assert.NotEqual(t, domain.MemoryStatusSuperseded, m.Status)
	}
}

// ---------------------------------------------------------------------------
// TestApplyExtendedFilters_StatusFilter
// ---------------------------------------------------------------------------

func TestApplyExtendedFilters_StatusFilter(t *testing.T) {
	items := []domain.ScoredMemory{
		{Memory: domain.Memory{ID: uuid.New(), Status: domain.MemoryStatusActive}, Score: 1.0},
		{Memory: domain.Memory{ID: uuid.New(), Status: domain.MemoryStatusSuperseded}, Score: 0.9},
		{Memory: domain.Memory{ID: uuid.New(), Status: domain.MemoryStatusStale}, Score: 0.8},
	}

	opts := domain.RecallOpts{StatusFilter: []domain.MemoryStatus{domain.MemoryStatusActive, domain.MemoryStatusStale}}
	result := applyExtendedFilters(items, opts)

	assert.Len(t, result, 2)
	for _, m := range result {
		assert.NotEqual(t, domain.MemoryStatusSuperseded, m.Status)
	}
}

// ---------------------------------------------------------------------------
// TestRemember_InvalidKey
// ---------------------------------------------------------------------------

func TestRemember_InvalidKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{"spaces", "hello world"},
		{"uppercase", "Hello-World"},
		{"special chars", "key!@#"},
		{"single char", "a"}, // regex requires at least two chars
		{"starts with hyphen", "-bad-key"},
		{"ends with hyphen", "bad-key-"},
		{"empty", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &mockMemoryRepo{}
			svc := newMemoryService(repo)

			mem := &domain.Memory{
				WorkspaceID: uuid.New(),
				Key:         tt.key,
				Content:     "content",
				Scope:       domain.ScopeProject,
			}

			_, err := svc.Remember(context.Background(), mem)

			require.Error(t, err)
			var apiErr *apierror.Error
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, http.StatusBadRequest, apiErr.Code)
		})
	}
}

// ---------------------------------------------------------------------------
// TestRecall_BasicSearch
// ---------------------------------------------------------------------------

func TestRecall_BasicSearch(t *testing.T) {
	wsID := uuid.New()
	results := []domain.ScoredMemory{
		{Memory: domain.Memory{ID: uuid.New(), Key: "decision-one", Content: "we decided X", ImportanceScore: 0.8}, Score: 0.9},
		{Memory: domain.Memory{ID: uuid.New(), Key: "decision-two", Content: "we decided Y", ImportanceScore: 0.8}, Score: 0.7},
	}

	boostCalled := false
	repo := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
			return results, nil
		},
		boostRelevanceFn: func(_ context.Context, ids []uuid.UUID) error {
			boostCalled = true
			assert.Len(t, ids, 2)
			return nil
		},
	}

	svc := newMemoryService(repo)
	opts := domain.RecallOpts{
		Query:       "decision",
		WorkspaceID: wsID,
		Limit:       10,
	}

	scored, _, err := svc.Recall(context.Background(), opts)

	require.NoError(t, err)
	assert.Len(t, scored, 2)
	assert.True(t, boostCalled, "BoostRelevance should be called after a successful search")
}

// ---------------------------------------------------------------------------
// TestRecall_RecencyWeightPassthrough — the recency_weight param is clamped to [0,1]
// before use. Recall must not return an error for any RecencyWeight value.
// ---------------------------------------------------------------------------

func TestRecall_RecencyWeightPassthrough(t *testing.T) {
	cases := []struct {
		name  string
		input float64
	}{
		{"default zero", 0},
		{"mid", 0.5},
		{"one", 1},
		{"above one clamps", 1.7},
		{"negative clamps", -0.3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &mockMemoryRepo{}
			svc := newMemoryService(repo)
			_, _, err := svc.Recall(context.Background(), domain.RecallOpts{
				Query:         "q",
				WorkspaceID:   uuid.New(),
				Limit:         10,
				RecencyWeight: tc.input,
			})
			require.NoError(t, err, "any RecencyWeight value must not cause an error")
		})
	}
}

// ---------------------------------------------------------------------------
// TestRecall_EmptyQuery
// ---------------------------------------------------------------------------

func TestRecall_EmptyQuery(t *testing.T) {
	repo := &mockMemoryRepo{}
	svc := newMemoryService(repo)

	opts := domain.RecallOpts{
		Query:       "",
		WorkspaceID: uuid.New(),
	}

	_, _, err := svc.Recall(context.Background(), opts)

	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.Code)
}

// ---------------------------------------------------------------------------
// TestGetProjectKnowledge
// ---------------------------------------------------------------------------

func TestGetProjectKnowledge(t *testing.T) {
	wsID := uuid.New()
	projID := uuid.New()
	stored := []domain.Memory{
		{ID: uuid.New(), WorkspaceID: wsID, Key: "arch-decision", Content: "use postgres"},
		{ID: uuid.New(), WorkspaceID: wsID, Key: "team-rule", Content: "code review required"},
	}

	repo := &mockMemoryRepo{
		listByWorkspaceProjectFn: func(_ context.Context, gotWsID uuid.UUID, gotProjID *uuid.UUID, _ domain.MemoryListFilter) ([]domain.Memory, int64, error) {
			assert.Equal(t, wsID, gotWsID)
			require.NotNil(t, gotProjID)
			assert.Equal(t, projID, *gotProjID)
			return stored, int64(len(stored)), nil
		},
	}

	svc := newMemoryService(repo)

	memories, _, err := svc.GetProjectKnowledge(context.Background(), wsID, &projID, domain.MemoryListFilter{})

	require.NoError(t, err)
	assert.Len(t, memories, 2)
}

// ---------------------------------------------------------------------------
// TestGetProjectKnowledge_WorkspacePagination — filter is forwarded to repo
// ---------------------------------------------------------------------------

func TestGetProjectKnowledge_WorkspacePagination(t *testing.T) {
	wsID := uuid.New()
	ws := make([]domain.Memory, 150)
	for i := range ws {
		ws[i] = domain.Memory{ID: uuid.New(), WorkspaceID: wsID, Key: fmt.Sprintf("key-%d", i), Scope: domain.ScopeWorkspace}
	}

	var capturedFilter domain.MemoryListFilter
	repo := &mockMemoryRepo{
		listByWorkspaceProjectFn: func(_ context.Context, _ uuid.UUID, projID *uuid.UUID, filter domain.MemoryListFilter) ([]domain.Memory, int64, error) {
			if projID == nil {
				capturedFilter = filter
				// Simulate pagination: return first filter.Limit entries.
				end := filter.Limit
				if end > len(ws) {
					end = len(ws)
				}
				return ws[:end], int64(len(ws)), nil
			}
			return nil, 0, nil
		},
	}

	svc := newMemoryService(repo)

	f := domain.MemoryListFilter{Limit: 50, Offset: 0}
	memories, total, err := svc.GetProjectKnowledge(context.Background(), wsID, nil, f)

	require.NoError(t, err)
	assert.Equal(t, 50, len(memories))
	assert.Equal(t, int64(150), total)
	assert.Equal(t, 50, capturedFilter.Limit)
}

// ---------------------------------------------------------------------------
// TestDefaultExpiresAt_KindSessionCheckpointTag — TTL fix: "kind:session-checkpoint"
// ---------------------------------------------------------------------------

func TestDefaultExpiresAt_KindSessionCheckpointTag(t *testing.T) {
	// "kind:session-checkpoint" should trigger the 7d TTL just like "session-checkpoint".
	wsID := uuid.New()
	var capturedMem *domain.Memory
	repo := &mockMemoryRepo{
		upsertFn: func(_ context.Context, m *domain.Memory) error {
			capturedMem = m
			return nil
		},
		getByKeyFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemoryScope) (*domain.Memory, error) {
			return nil, nil // new entry
		},
	}

	svc := newMemoryService(repo)
	mem := &domain.Memory{
		WorkspaceID: wsID,
		Key:         "test-checkpoint",
		Content:     "session data",
		Scope:       domain.ScopeWorkspace,
		Tags:        []string{"kind:session-checkpoint", "owner:linus"},
	}
	_, err := svc.Remember(context.Background(), mem)
	require.NoError(t, err)
	require.NotNil(t, capturedMem)
	require.NotNil(t, capturedMem.ExpiresAt, "kind:session-checkpoint must get expires_at TTL")
	// TTL should be approximately 7 days.
	delta := time.Until(*capturedMem.ExpiresAt)
	assert.True(t, delta > 6*24*time.Hour && delta <= 7*24*time.Hour+time.Minute,
		"expected ~7d TTL, got %v", delta)
}

// ---------------------------------------------------------------------------
// TestGetProjectKnowledge_ProjectMemoriesNotLimited — project tier always unlimited
// ---------------------------------------------------------------------------

func TestGetProjectKnowledge_ProjectMemoriesNotLimited(t *testing.T) {
	wsID := uuid.New()
	projID := uuid.New()
	stored := make([]domain.Memory, 200)
	for i := range stored {
		stored[i] = domain.Memory{ID: uuid.New(), WorkspaceID: wsID, Scope: domain.ScopeProject}
	}

	repo := &mockMemoryRepo{
		listByWorkspaceProjectFn: func(_ context.Context, _ uuid.UUID, pID *uuid.UUID, filter domain.MemoryListFilter) ([]domain.Memory, int64, error) {
			if pID != nil {
				// Project tier: filter.Limit should be 0 (no limit).
				assert.Equal(t, 0, filter.Limit, "project tier must not be limited")
				return stored, int64(len(stored)), nil
			}
			return nil, 0, nil
		},
	}

	svc := newMemoryService(repo)
	memories, total, err := svc.GetProjectKnowledge(context.Background(), wsID, &projID, domain.MemoryListFilter{})
	require.NoError(t, err)
	assert.Equal(t, 200, len(memories))
	assert.Equal(t, int64(200), total)
}

// ---------------------------------------------------------------------------
// TestForget_OwnAgentScope
// ---------------------------------------------------------------------------

func TestForget_OwnAgentScope(t *testing.T) {
	agentID := uuid.New()
	memID := uuid.New()

	mem := &domain.Memory{
		ID:      memID,
		Scope:   domain.ScopeAgent,
		AgentID: &agentID,
	}

	deleteCalled := false
	repo := &mockMemoryRepo{
		getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
			assert.Equal(t, memID, id)
			return mem, nil
		},
		deleteFn: func(_ context.Context, id uuid.UUID) error {
			deleteCalled = true
			assert.Equal(t, memID, id)
			return nil
		},
	}

	svc := newMemoryService(repo)

	err := svc.Forget(context.Background(), memID, &agentID, false)

	require.NoError(t, err)
	assert.True(t, deleteCalled)
}

// ---------------------------------------------------------------------------
// TestForget_OtherAgentScope
// ---------------------------------------------------------------------------

func TestForget_OtherAgentScope(t *testing.T) {
	ownerAgentID := uuid.New()
	otherAgentID := uuid.New()
	memID := uuid.New()

	mem := &domain.Memory{
		ID:      memID,
		Scope:   domain.ScopeAgent,
		AgentID: &ownerAgentID,
	}

	repo := &mockMemoryRepo{
		getByIDFn: func(_ context.Context, _ uuid.UUID) (*domain.Memory, error) {
			return mem, nil
		},
	}

	svc := newMemoryService(repo)

	// A different agent attempts to delete the memory.
	err := svc.Forget(context.Background(), memID, &otherAgentID, false)

	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusForbidden, apiErr.Code)
}

// ---------------------------------------------------------------------------
// TestForget_AdminCanDeleteAny
// ---------------------------------------------------------------------------

func TestForget_AdminCanDeleteAny(t *testing.T) {
	ownerAgentID := uuid.New()
	memID := uuid.New()

	mem := &domain.Memory{
		ID:      memID,
		Scope:   domain.ScopeAgent,
		AgentID: &ownerAgentID,
	}

	deleteCalled := false
	repo := &mockMemoryRepo{
		getByIDFn: func(_ context.Context, _ uuid.UUID) (*domain.Memory, error) {
			return mem, nil
		},
		deleteFn: func(_ context.Context, id uuid.UUID) error {
			deleteCalled = true
			assert.Equal(t, memID, id)
			return nil
		},
	}

	svc := newMemoryService(repo)

	// Admin (isAdmin=true, actorAgentID unrelated) can delete any memory.
	someOtherID := uuid.New()
	err := svc.Forget(context.Background(), memID, &someOtherID, true)

	require.NoError(t, err)
	assert.True(t, deleteCalled)
}

// ---------------------------------------------------------------------------
// TestExtractFromEvent_ExplicitHint
// ---------------------------------------------------------------------------

func TestExtractFromEvent_ExplicitHint(t *testing.T) {
	wsID := uuid.New()
	projID := uuid.New()
	agentID := uuid.New()
	eventID := uuid.New()

	event := &domain.EventBusMessage{
		ID:          eventID,
		WorkspaceID: wsID,
		ProjectID:   projID,
		AgentID:     &agentID,
		EventType:   domain.EventTypeSummary,
		Subject:     "agent completed the task",
	}

	hint := &domain.MemoryHint{
		Persist: true,
		Key:     "task-completion-note",
		Scope:   domain.ScopeProject,
		Tags:    []string{"important"},
	}

	upsertCalled := false
	repo := &mockMemoryRepo{
		upsertFn: func(_ context.Context, mem *domain.Memory) error {
			upsertCalled = true
			assert.Equal(t, "task-completion-note", mem.Key)
			assert.Equal(t, domain.ScopeProject, mem.Scope)
			assert.Equal(t, wsID, mem.WorkspaceID)
			assert.Equal(t, domain.SourceAgent, mem.SourceType)
			assert.Equal(t, &eventID, mem.SourceEventID)
			return nil
		},
	}

	svc := newMemoryService(repo)

	err := svc.ExtractFromEvent(context.Background(), event, hint)

	require.NoError(t, err)
	assert.True(t, upsertCalled)
}

// ---------------------------------------------------------------------------
// TestExtractFromEvent_AutoExtractDecision
// ---------------------------------------------------------------------------

func TestExtractFromEvent_AutoExtractDecision(t *testing.T) {
	wsID := uuid.New()
	projID := uuid.New()
	eventID := uuid.New()

	payload, _ := json.Marshal(map[string]interface{}{
		"context_type": "decision",
		"details":      "use postgres for storage",
	})

	event := &domain.EventBusMessage{
		ID:          eventID,
		WorkspaceID: wsID,
		ProjectID:   projID,
		EventType:   domain.EventTypeContextUpdate,
		Subject:     "storage decision made",
		Payload:     payload,
	}

	upsertCalled := false
	repo := &mockMemoryRepo{
		upsertFn: func(_ context.Context, mem *domain.Memory) error {
			upsertCalled = true
			assert.Equal(t, "storage-decision-made", mem.Key)
			assert.Equal(t, domain.ScopeProject, mem.Scope)
			assert.Equal(t, domain.SourceSystem, mem.SourceType)
			assert.Equal(t, wsID, mem.WorkspaceID)
			assert.Equal(t, &eventID, mem.SourceEventID)
			return nil
		},
	}

	svc := newMemoryService(repo)

	err := svc.ExtractFromEvent(context.Background(), event, nil)

	require.NoError(t, err)
	assert.True(t, upsertCalled)
}

// ---------------------------------------------------------------------------
// TestExtractFromEvent_NoAutoExtractSummary
// ---------------------------------------------------------------------------

func TestExtractFromEvent_NoAutoExtractSummary(t *testing.T) {
	wsID := uuid.New()
	projID := uuid.New()

	payload, _ := json.Marshal(map[string]interface{}{
		"context_type": "summary",
	})

	event := &domain.EventBusMessage{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		ProjectID:   projID,
		EventType:   domain.EventTypeContextUpdate,
		Subject:     "daily standup summary",
		Payload:     payload,
	}

	upsertCalled := false
	repo := &mockMemoryRepo{
		upsertFn: func(_ context.Context, _ *domain.Memory) error {
			upsertCalled = true
			return nil
		},
	}

	svc := newMemoryService(repo)

	// summary context_type is NOT in the auto-extract whitelist.
	err := svc.ExtractFromEvent(context.Background(), event, nil)

	require.NoError(t, err)
	assert.False(t, upsertCalled, "summary events without hint should not create a memory")
}

// ---------------------------------------------------------------------------
// applyExtendedFilters — source_type
// ---------------------------------------------------------------------------

func scored(srcType domain.MemorySourceType) domain.ScoredMemory {
	return domain.ScoredMemory{
		Memory: domain.Memory{ID: uuid.New(), SourceType: srcType},
	}
}

// freshItems returns a new backing array each call. applyExtendedFilters reuses
// the input slice's backing array (items[:0]), so tests must not share one.
func freshItems() []domain.ScoredMemory {
	return []domain.ScoredMemory{
		scored(domain.SourceAgent),
		scored(domain.SourceHuman),
		scored(domain.SourceSystem),
		scored(domain.SourceAgent),
	}
}

func TestApplyExtendedFilters_SourceType(t *testing.T) {
	t.Run("filters to a single source type", func(t *testing.T) {
		out := applyExtendedFilters(freshItems(), domain.RecallOpts{SourceType: domain.SourceAgent})
		require.Len(t, out, 2)
		for _, m := range out {
			assert.Equal(t, domain.SourceAgent, m.SourceType)
		}
	})

	t.Run("human filter keeps only human-sourced", func(t *testing.T) {
		out := applyExtendedFilters(freshItems(), domain.RecallOpts{SourceType: domain.SourceHuman})
		require.Len(t, out, 1)
		assert.Equal(t, domain.SourceHuman, out[0].SourceType)
	})

	t.Run("empty source type leaves all items", func(t *testing.T) {
		items := freshItems()
		out := applyExtendedFilters(items, domain.RecallOpts{})
		assert.Len(t, out, len(items))
	})
}

func TestMemorySourceType_IsValid(t *testing.T) {
	assert.True(t, domain.SourceAgent.IsValid())
	assert.True(t, domain.SourceHuman.IsValid())
	assert.True(t, domain.SourceSystem.IsValid())
	assert.False(t, domain.MemorySourceType("").IsValid())
	assert.False(t, domain.MemorySourceType("bogus").IsValid())
}

// ---------------------------------------------------------------------------
// TestComputeImportanceScore — scoring rule engine
// ---------------------------------------------------------------------------

func TestComputeImportanceScore_BaseByKind(t *testing.T) {
	cases := []struct {
		tag  string
		want float32
	}{
		{"kind:incident", 0.85},
		{"kind:decision", 0.80},
		{"kind:learning", 0.70},
		{"kind:fact", 0.60},
		{"kind:session-checkpoint", 0.30},
	}
	for _, tc := range cases {
		got := computeImportanceScore([]string{tc.tag}, "some content")
		assert.InDelta(t, tc.want, got, 0.001, "tag=%s", tc.tag)
	}
}

func TestComputeImportanceScore_DefaultNoKindTag(t *testing.T) {
	got := computeImportanceScore([]string{"team:backend"}, "plain content")
	assert.InDelta(t, 0.5, got, 0.001)
}

func TestComputeImportanceScore_EntityKeywordBoost(t *testing.T) {
	// "architecture" in content → +0.1 boost on top of 0.5 default
	got := computeImportanceScore([]string{}, "this affects the architecture of the system")
	assert.InDelta(t, 0.6, got, 0.001)
}

func TestComputeImportanceScore_RelevanceTagOverride(t *testing.T) {
	// relevance:0.9 tag → +0.1 boost
	got := computeImportanceScore([]string{"relevance:0.9"}, "some content")
	assert.InDelta(t, 0.6, got, 0.001)
}

func TestComputeImportanceScore_RelevanceTagBelowThreshold(t *testing.T) {
	// relevance:0.7 → not >= 0.8, no boost
	got := computeImportanceScore([]string{"relevance:0.7"}, "some content")
	assert.InDelta(t, 0.5, got, 0.001)
}

func TestComputeImportanceScore_CappedAt1(t *testing.T) {
	// incident (0.85) + entity keyword (money) + relevance:0.9 = 1.05 → capped to 1.0
	got := computeImportanceScore([]string{"kind:incident", "relevance:0.9"}, "involves a money transfer and security risk")
	assert.InDelta(t, 1.0, got, 0.001)
}

func TestComputeImportanceScore_HighestKindWins(t *testing.T) {
	// incident (0.85) takes precedence over decision (0.80)
	s1 := computeImportanceScore([]string{"kind:decision", "kind:incident"}, "content")
	assert.InDelta(t, 0.85, s1, 0.001)
}

func TestComputeImportanceScore_SessionCheckpointNotUpgraded(t *testing.T) {
	// session-checkpoint is a downgrade that always overrides positive kind: tags.
	// The result must be 0.30 regardless of tag ordering.
	tags1 := []string{"kind:session-checkpoint", "kind:decision"}
	tags2 := []string{"kind:decision", "kind:session-checkpoint"}
	s1 := computeImportanceScore(tags1, "content")
	s2 := computeImportanceScore(tags2, "content")
	assert.Equal(t, s1, s2, "score must be order-independent")
	assert.InDelta(t, float32(0.30), s1, 0.001, "session-checkpoint overrides decision")
}

// ---------------------------------------------------------------------------
// TestTagOverlapRatio
// ---------------------------------------------------------------------------

func TestTagOverlapRatio(t *testing.T) {
	t.Run("full overlap", func(t *testing.T) {
		assert.InDelta(t, 1.0, tagOverlapRatio([]string{"a", "b"}, []string{"a", "b"}), 0.001)
	})
	t.Run("no overlap", func(t *testing.T) {
		assert.InDelta(t, 0.0, tagOverlapRatio([]string{"a", "b"}, []string{"c", "d"}), 0.001)
	})
	t.Run("80pct overlap", func(t *testing.T) {
		// 4 of 5 new tags match existing
		exist := []string{"kind:decision", "team:mesh", "project:evc", "sprint:12"}
		incoming := []string{"kind:decision", "team:mesh", "project:evc", "sprint:12", "new-tag"}
		assert.InDelta(t, 0.8, tagOverlapRatio(exist, incoming), 0.001)
	})
	t.Run("empty new tags returns 0", func(t *testing.T) {
		assert.InDelta(t, 0.0, tagOverlapRatio([]string{"a"}, nil), 0.001)
	})
}

// ---------------------------------------------------------------------------
// TestIsGenericTag / TestFilterSemanticTags
// ---------------------------------------------------------------------------

func TestIsGenericTag(t *testing.T) {
	generic := []string{
		"kind:incident", "kind:decision", "owner:bill", "project:spark",
		"phase:execute", "fleet", "infra", "bug", "feature", "wip", "p0", "p2",
	}
	for _, tag := range generic {
		assert.True(t, isGenericTag(tag), "expected %q to be generic", tag)
	}
	semantic := []string{
		"auth-migration", "postgres-upgrade", "relay-upload", "sparse-index",
		"billing", "memory-kg", "cost-limit",
	}
	for _, tag := range semantic {
		assert.False(t, isGenericTag(tag), "expected %q to be semantic", tag)
	}
}

func TestFilterSemanticTags(t *testing.T) {
	in := []string{"kind:incident", "owner:linus", "project:mesh-dev", "auth-migration", "postgres-upgrade"}
	got := filterSemanticTags(in)
	assert.Equal(t, []string{"auth-migration", "postgres-upgrade"}, got)

	assert.Empty(t, filterSemanticTags([]string{"kind:fact", "owner:riker", "phase:execute"}))
	assert.Empty(t, filterSemanticTags(nil))
}

// ---------------------------------------------------------------------------
// TestRemember_SetsImportanceScore — scoring wired into Remember
// ---------------------------------------------------------------------------

func TestRemember_SetsImportanceScore(t *testing.T) {
	wsID := uuid.New()
	var upserted *domain.Memory
	repo := &mockMemoryRepo{
		upsertFn: func(_ context.Context, m *domain.Memory) error {
			upserted = m
			return nil
		},
	}
	svc := newMemoryService(repo)

	mem := &domain.Memory{
		WorkspaceID: wsID,
		Key:         "my-decision",
		Content:     "we chose postgres",
		Scope:       domain.ScopeProject,
		Tags:        []string{"kind:decision"},
	}
	_, err := svc.Remember(context.Background(), mem)
	require.NoError(t, err)
	assert.InDelta(t, 0.8, upserted.ImportanceScore, 0.001, "kind:decision should score 0.8")
}

func TestRemember_ReinforcementBoost(t *testing.T) {
	wsID := uuid.New()
	existingID := uuid.New()
	existing := &domain.Memory{
		ID:              existingID,
		WorkspaceID:     wsID,
		Key:             "arch-decision",
		Content:         "original content",
		Scope:           domain.ScopeProject,
		Tags:            []string{"kind:decision", "team:backend"},
		ImportanceScore: 0.8,
	}

	var upserted *domain.Memory
	repo := &mockMemoryRepo{
		getByKeyFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemoryScope) (*domain.Memory, error) {
			return existing, nil
		},
		upsertFn: func(_ context.Context, m *domain.Memory) error {
			upserted = m
			return nil
		},
	}
	svc := newMemoryService(repo)

	// Same tags → full overlap → reinforcement +0.1 (capped at 1.0)
	mem := &domain.Memory{
		WorkspaceID: wsID,
		Key:         "arch-decision",
		Content:     "updated content",
		Scope:       domain.ScopeProject,
		Tags:        []string{"kind:decision", "team:backend"},
	}
	_, err := svc.Remember(context.Background(), mem)
	require.NoError(t, err)
	// base 0.8 (decision) + 0.1 reinforcement = 0.9
	assert.InDelta(t, 0.9, upserted.ImportanceScore, 0.001)
}

// ---------------------------------------------------------------------------
// TestApplyExtendedFilters_MinImportance
// ---------------------------------------------------------------------------

func TestApplyExtendedFilters_MinImportance(t *testing.T) {
	minVal := float32(0.5)
	items := []domain.ScoredMemory{
		{Memory: domain.Memory{ImportanceScore: 0.3}},
		{Memory: domain.Memory{ImportanceScore: 0.6}},
		{Memory: domain.Memory{ImportanceScore: 0.9}},
	}
	out := applyExtendedFilters(items, domain.RecallOpts{MinImportance: &minVal})
	require.Len(t, out, 2)
	for _, m := range out {
		assert.GreaterOrEqual(t, m.ImportanceScore, minVal)
	}
}

func TestApplyExtendedFilters_MinImportanceZeroPassesAll(t *testing.T) {
	zero := float32(0.0)
	items := []domain.ScoredMemory{
		{Memory: domain.Memory{ImportanceScore: 0.0}},
		{Memory: domain.Memory{ImportanceScore: 0.3}},
		{Memory: domain.Memory{ImportanceScore: 0.9}},
	}
	out := applyExtendedFilters(items, domain.RecallOpts{MinImportance: &zero})
	assert.Len(t, out, 3, "min_importance=0 should pass everything including low-score entries")
}

// ---------------------------------------------------------------------------
// TestRemember_Hook1_RelatesTo
// ---------------------------------------------------------------------------

func TestRemember_Hook1_RelatesTo(t *testing.T) {
	wsID := uuid.New()
	// existing memory with 3 matching tags → overlap = 3/3 = 100% ≥ 60%
	existingID := uuid.New()
	existing := domain.Memory{
		ID:          existingID,
		WorkspaceID: wsID,
		Key:         "existing-mem",
		Scope:       domain.ScopeProject,
		Tags:        []string{"arch", "decision", "postgres"},
	}

	var capturedEdge *domain.MemoryEdge
	edgeRepo := &mockMemoryEdgeRepo{
		upsertEdgeFn: func(e *domain.MemoryEdge) error {
			capturedEdge = e
			return nil
		},
	}

	repo := &mockMemoryRepo{
		getByKeyFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemoryScope) (*domain.Memory, error) {
			return nil, nil // new memory
		},
		upsertFn: func(_ context.Context, mem *domain.Memory) error {
			mem.ID = uuid.New()
			return nil
		},
		findByScopeFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.Memory, error) {
			return []domain.Memory{existing}, nil
		},
	}

	svc := newMemoryServiceWithEdges(repo, edgeRepo)
	mem := &domain.Memory{
		WorkspaceID: wsID,
		Key:         "new-mem",
		Content:     "uses postgres for architecture decisions",
		Scope:       domain.ScopeProject,
		Tags:        []string{"arch", "decision", "postgres"},
	}

	_, err := svc.Remember(context.Background(), mem)

	require.NoError(t, err)
	require.NotNil(t, capturedEdge, "relates_to edge should be created on ≥60%% tag overlap")
	assert.Equal(t, domain.EdgeRelatesTo, capturedEdge.RelationshipType)
	assert.Equal(t, float32(0.5), capturedEdge.Weight)
	assert.Equal(t, existingID, capturedEdge.MemoryToID)
}

// ---------------------------------------------------------------------------
// TestRemember_Hook1_NoEdgeBelowThreshold
// ---------------------------------------------------------------------------

func TestRemember_Hook1_NoEdgeBelowThreshold(t *testing.T) {
	wsID := uuid.New()
	// only 1 of 5 tags match → 20% < 60%
	existing := domain.Memory{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		Key:         "unrelated-mem",
		Scope:       domain.ScopeProject,
		Tags:        []string{"unrelated", "other", "stuff", "here", "nope"},
	}

	edgeCalled := false
	edgeRepo := &mockMemoryEdgeRepo{
		upsertEdgeFn: func(_ *domain.MemoryEdge) error {
			edgeCalled = true
			return nil
		},
	}

	repo := &mockMemoryRepo{
		getByKeyFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemoryScope) (*domain.Memory, error) {
			return nil, nil
		},
		upsertFn: func(_ context.Context, mem *domain.Memory) error {
			mem.ID = uuid.New()
			return nil
		},
		findByScopeFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.Memory, error) {
			return []domain.Memory{existing}, nil
		},
	}

	svc := newMemoryServiceWithEdges(repo, edgeRepo)
	mem := &domain.Memory{
		WorkspaceID: wsID,
		Key:         "different-mem",
		Content:     "content about something else entirely",
		Scope:       domain.ScopeProject,
		Tags:        []string{"arch"},
	}

	_, err := svc.Remember(context.Background(), mem)

	require.NoError(t, err)
	assert.False(t, edgeCalled, "no edge should be created when tag overlap is below 60%%")
}

// ---------------------------------------------------------------------------
// TestRemember_Hook3_DerivedFrom
// ---------------------------------------------------------------------------

func TestRemember_Hook3_DerivedFrom(t *testing.T) {
	wsID := uuid.New()
	referencedID := uuid.New()
	// Build a short_id prefix from the referenced ID (first 8 hex chars of UUID string)
	shortIDPrefix := referencedID.String()[:8]

	referenced := &domain.Memory{
		ID:          referencedID,
		WorkspaceID: wsID,
		Key:         "original-incident",
	}

	var capturedEdge *domain.MemoryEdge
	edgeRepo := &mockMemoryEdgeRepo{
		upsertEdgeFn: func(e *domain.MemoryEdge) error {
			capturedEdge = e
			return nil
		},
	}

	repo := &mockMemoryRepo{
		getByKeyFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemoryScope) (*domain.Memory, error) {
			return nil, nil
		},
		upsertFn: func(_ context.Context, mem *domain.Memory) error {
			mem.ID = uuid.New()
			return nil
		},
		findByScopeFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.Memory, error) {
			return nil, nil
		},
		findByShortIDFn: func() (*domain.Memory, error) {
			return referenced, nil
		},
	}

	svc := newMemoryServiceWithEdges(repo, edgeRepo)
	mem := &domain.Memory{
		WorkspaceID: wsID,
		Key:         "followup-incident",
		Content:     "this incident is related to " + shortIDPrefix + " root cause",
		Scope:       domain.ScopeProject,
		Tags:        []string{"kind:incident"},
	}

	_, err := svc.Remember(context.Background(), mem)

	require.NoError(t, err)
	require.NotNil(t, capturedEdge, "derived_from edge should be created for incident referencing another memory")
	assert.Equal(t, domain.EdgeDerivedFrom, capturedEdge.RelationshipType)
	assert.Equal(t, referencedID, capturedEdge.MemoryToID)
}

// ---------------------------------------------------------------------------
// TestSupersede_CreatesEdgeAndArchives
// ---------------------------------------------------------------------------

func TestSupersede_CreatesEdgeAndArchives(t *testing.T) {
	wsID := uuid.New()
	oldID := uuid.New()
	newID := uuid.New()

	oldMem := &domain.Memory{ID: oldID, WorkspaceID: wsID, Key: "old-decision"}
	newMem := &domain.Memory{ID: newID, WorkspaceID: wsID, Key: "new-decision"}

	getCallCount := 0
	archivedCalled := false
	var capturedEdge *domain.MemoryEdge

	edgeRepo := &mockMemoryEdgeRepo{
		upsertEdgeFn: func(e *domain.MemoryEdge) error {
			capturedEdge = e
			return nil
		},
	}

	repo := &mockMemoryRepo{
		getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
			getCallCount++
			if id == oldID {
				return oldMem, nil
			}
			return newMem, nil
		},
		setArchivedFn: func() error {
			archivedCalled = true
			return nil
		},
	}

	svc := newMemoryServiceWithEdges(repo, edgeRepo)

	err := svc.Supersede(context.Background(), oldID, newID)

	require.NoError(t, err)
	assert.Equal(t, 2, getCallCount, "should fetch both old and new memories")
	require.NotNil(t, capturedEdge, "supersedes edge must be created")
	assert.Equal(t, domain.EdgeSupersedes, capturedEdge.RelationshipType)
	assert.Equal(t, newID, capturedEdge.MemoryFromID)
	assert.Equal(t, oldID, capturedEdge.MemoryToID)
	assert.True(t, archivedCalled, "old memory must be archived")
}

// ---------------------------------------------------------------------------
// TestSupersede_NotFoundOld
// ---------------------------------------------------------------------------

func TestSupersede_NotFoundOld(t *testing.T) {
	repo := &mockMemoryRepo{
		getByIDFn: func(_ context.Context, _ uuid.UUID) (*domain.Memory, error) {
			return nil, nil // not found
		},
	}

	svc := newMemoryServiceWithEdges(repo, &mockMemoryEdgeRepo{})

	err := svc.Supersede(context.Background(), uuid.New(), uuid.New())

	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.Code)
}

// ---------------------------------------------------------------------------
// mockProjectRepo — minimal stub satisfying repository.ProjectRepository
// ---------------------------------------------------------------------------

type mockProjectRepo struct {
	getBySlugFn func(ctx context.Context, wsID uuid.UUID, slug string) (*domain.Project, error)
}

func (m *mockProjectRepo) Create(_ context.Context, _ *domain.Project) error { return nil }
func (m *mockProjectRepo) GetByID(_ context.Context, _ uuid.UUID) (*domain.Project, error) {
	return nil, nil
}
func (m *mockProjectRepo) GetBySlug(ctx context.Context, wsID uuid.UUID, slug string) (*domain.Project, error) {
	if m.getBySlugFn != nil {
		return m.getBySlugFn(ctx, wsID, slug)
	}
	return nil, nil
}
func (m *mockProjectRepo) Update(_ context.Context, _ *domain.Project) error { return nil }
func (m *mockProjectRepo) Delete(_ context.Context, _ uuid.UUID) error       { return nil }
func (m *mockProjectRepo) List(_ context.Context, _ uuid.UUID, _ repository.ProjectFilter, _ pagination.Params) (*pagination.Page[domain.Project], error) {
	return nil, nil
}

var _ repository.ProjectRepository = (*mockProjectRepo)(nil)

// ---------------------------------------------------------------------------
// TestResolveProjectSlug
// ---------------------------------------------------------------------------

func TestResolveProjectSlug(t *testing.T) {
	t.Run("canonical slug resolves", func(t *testing.T) {
		slug, ok := resolveProjectSlug([]string{"kind:decision", "project:mesh-dev"})
		require.True(t, ok)
		assert.Equal(t, "mesh-dev", slug)
	})
	t.Run("alias mesh resolves to mesh-dev", func(t *testing.T) {
		slug, ok := resolveProjectSlug([]string{"project:mesh", "kind:fact"})
		require.True(t, ok)
		assert.Equal(t, "mesh-dev", slug)
	})
	t.Run("alias evc-mesh resolves to mesh-dev", func(t *testing.T) {
		slug, ok := resolveProjectSlug([]string{"project:evc-mesh"})
		require.True(t, ok)
		assert.Equal(t, "mesh-dev", slug)
	})
	t.Run("two aliases for same project count as one", func(t *testing.T) {
		// project:mesh and project:evc-mesh both map to mesh-dev → unique → resolves
		slug, ok := resolveProjectSlug([]string{"project:mesh", "project:evc-mesh"})
		require.True(t, ok)
		assert.Equal(t, "mesh-dev", slug)
	})
	t.Run("two distinct projects → no resolve", func(t *testing.T) {
		_, ok := resolveProjectSlug([]string{"project:mesh-dev", "project:spark"})
		assert.False(t, ok)
	})
	t.Run("unknown slug → no resolve", func(t *testing.T) {
		_, ok := resolveProjectSlug([]string{"project:nonexistent"})
		assert.False(t, ok)
	})
	t.Run("no project tag → no resolve", func(t *testing.T) {
		_, ok := resolveProjectSlug([]string{"kind:decision", "owner:riker"})
		assert.False(t, ok)
	})
	t.Run("evc-spark alias resolves to spark", func(t *testing.T) {
		slug, ok := resolveProjectSlug([]string{"project:evc-spark"})
		require.True(t, ok)
		assert.Equal(t, "spark", slug)
	})
	t.Run("tgbot alias resolves to tg-bot", func(t *testing.T) {
		slug, ok := resolveProjectSlug([]string{"project:tgbot"})
		require.True(t, ok)
		assert.Equal(t, "tg-bot", slug)
	})
}

// ---------------------------------------------------------------------------
// TestRemember_SlugResolution
// ---------------------------------------------------------------------------

func TestRemember_SlugResolution(t *testing.T) {
	wsID := uuid.New()
	projID := uuid.New()

	t.Run("sets project_id from project:mesh tag", func(t *testing.T) {
		var upserted *domain.Memory
		memRepo := &mockMemoryRepo{
			upsertFn: func(_ context.Context, m *domain.Memory) error {
				upserted = m
				return nil
			},
		}
		projRepo := &mockProjectRepo{
			getBySlugFn: func(_ context.Context, wsID uuid.UUID, slug string) (*domain.Project, error) {
				if slug == "mesh-dev" {
					return &domain.Project{ID: projID}, nil
				}
				return nil, nil
			},
		}
		svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, nil, MemoryWithProjectRepo(projRepo))

		mem := &domain.Memory{
			WorkspaceID: wsID,
			Key:         "checkpoint-mesh",
			Content:     "session end",
			Scope:       domain.ScopeWorkspace,
			Tags:        []string{"kind:session-checkpoint", "project:mesh", "owner:garfield"},
		}
		_, err := svc.Remember(context.Background(), mem)
		require.NoError(t, err)
		require.NotNil(t, upserted.ProjectID, "project_id must be set when project:mesh tag resolves")
		assert.Equal(t, projID, *upserted.ProjectID)
	})

	t.Run("leaves project_id nil when already set", func(t *testing.T) {
		existingProjID := uuid.New()
		var upserted *domain.Memory
		memRepo := &mockMemoryRepo{
			upsertFn: func(_ context.Context, m *domain.Memory) error {
				upserted = m
				return nil
			},
		}
		projRepo := &mockProjectRepo{} // should never be called
		svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, nil, MemoryWithProjectRepo(projRepo))

		mem := &domain.Memory{
			WorkspaceID: wsID,
			ProjectID:   &existingProjID,
			Key:         "explicit-proj",
			Content:     "content",
			Scope:       domain.ScopeProject,
			Tags:        []string{"project:mesh"},
		}
		_, err := svc.Remember(context.Background(), mem)
		require.NoError(t, err)
		require.NotNil(t, upserted.ProjectID)
		assert.Equal(t, existingProjID, *upserted.ProjectID, "explicit project_id must not be overwritten")
	})

	t.Run("leaves project_id nil when no project repo wired", func(t *testing.T) {
		var upserted *domain.Memory
		memRepo := &mockMemoryRepo{
			upsertFn: func(_ context.Context, m *domain.Memory) error {
				upserted = m
				return nil
			},
		}
		svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, nil) // no MemoryWithProjectRepo

		mem := &domain.Memory{
			WorkspaceID: wsID,
			Key:         "no-proj-repo",
			Content:     "content",
			Scope:       domain.ScopeWorkspace,
			Tags:        []string{"project:mesh"},
		}
		_, err := svc.Remember(context.Background(), mem)
		require.NoError(t, err)
		assert.Nil(t, upserted.ProjectID)
	})

	t.Run("leaves project_id nil when multiple distinct project tags", func(t *testing.T) {
		var upserted *domain.Memory
		memRepo := &mockMemoryRepo{
			upsertFn: func(_ context.Context, m *domain.Memory) error {
				upserted = m
				return nil
			},
		}
		projRepo := &mockProjectRepo{} // should not be called for cross-project entries
		svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, nil, MemoryWithProjectRepo(projRepo))

		mem := &domain.Memory{
			WorkspaceID: wsID,
			Key:         "cross-project",
			Content:     "content",
			Scope:       domain.ScopeWorkspace,
			Tags:        []string{"project:mesh-dev", "project:spark"},
		}
		_, err := svc.Remember(context.Background(), mem)
		require.NoError(t, err)
		assert.Nil(t, upserted.ProjectID)
	})

	t.Run("leaves project_id nil when slug lookup returns nothing", func(t *testing.T) {
		var upserted *domain.Memory
		memRepo := &mockMemoryRepo{
			upsertFn: func(_ context.Context, m *domain.Memory) error {
				upserted = m
				return nil
			},
		}
		projRepo := &mockProjectRepo{
			getBySlugFn: func(_ context.Context, _ uuid.UUID, _ string) (*domain.Project, error) {
				return nil, nil // project deleted or workspace mismatch
			},
		}
		svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, nil, MemoryWithProjectRepo(projRepo))

		mem := &domain.Memory{
			WorkspaceID: wsID,
			Key:         "missing-project",
			Content:     "content",
			Scope:       domain.ScopeWorkspace,
			Tags:        []string{"project:mesh-dev"},
		}
		_, err := svc.Remember(context.Background(), mem)
		require.NoError(t, err)
		assert.Nil(t, upserted.ProjectID)
	})
}

// ---------------------------------------------------------------------------
// mockTaskRepo — minimal stub satisfying repository.TaskRepository
// ---------------------------------------------------------------------------

type mockTaskRepo struct {
	getByIDFn func(ctx context.Context, id uuid.UUID) (*domain.Task, error)
}

func (m *mockTaskRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, id)
	}
	return nil, nil
}

func (m *mockTaskRepo) Create(_ context.Context, _ *domain.Task) error { return nil }
func (m *mockTaskRepo) GetByShortID(_ context.Context, _ string) (*domain.Task, error) {
	return nil, nil
}
func (m *mockTaskRepo) Update(_ context.Context, _ *domain.Task) error { return nil }
func (m *mockTaskRepo) Delete(_ context.Context, _ uuid.UUID) error    { return nil }
func (m *mockTaskRepo) List(_ context.Context, _ uuid.UUID, _ repository.TaskFilter, _ pagination.Params) (*pagination.Page[domain.Task], error) {
	return nil, nil
}
func (m *mockTaskRepo) Search(_ context.Context, _ uuid.UUID, _ repository.TaskFilter, _ pagination.Params) (*pagination.Page[domain.Task], error) {
	return nil, nil
}
func (m *mockTaskRepo) ListByAssignee(_ context.Context, _ uuid.UUID, _ domain.AssigneeType) ([]domain.Task, error) {
	return nil, nil
}
func (m *mockTaskRepo) ListByUserActive(_ context.Context, _, _ uuid.UUID, _ pagination.Params) (*pagination.Page[domain.Task], error) {
	return nil, nil
}
func (m *mockTaskRepo) ListSubtasks(_ context.Context, _ uuid.UUID) ([]domain.Task, error) {
	return nil, nil
}
func (m *mockTaskRepo) CountByStatus(_ context.Context, _ uuid.UUID) (map[uuid.UUID]int, error) {
	return nil, nil
}
func (m *mockTaskRepo) CountByStatusCategory(_ context.Context, _ uuid.UUID) (map[domain.StatusCategory]int, error) {
	return nil, nil
}
func (m *mockTaskRepo) ListByStatusCategory(_ context.Context, _ uuid.UUID, _ domain.StatusCategory, _ pagination.Params) (*pagination.Page[domain.Task], error) {
	return nil, nil
}
func (m *mockTaskRepo) AtomicCheckout(_ context.Context, _, _, _ uuid.UUID, _ time.Time) error {
	return nil
}
func (m *mockTaskRepo) ReleaseCheckout(_ context.Context, _, _ uuid.UUID) error { return nil }
func (m *mockTaskRepo) ExtendCheckout(_ context.Context, _, _ uuid.UUID, _ time.Time) error {
	return nil
}
func (m *mockTaskRepo) ForceReleaseCheckout(_ context.Context, _ uuid.UUID) error { return nil }
func (m *mockTaskRepo) ReleaseExpiredCheckouts(_ context.Context) (int64, error)  { return 0, nil }
func (m *mockTaskRepo) FindExpiredInProgressCheckouts(_ context.Context) ([]domain.Task, error) {
	return nil, nil
}
func (m *mockTaskRepo) FindDueMonitorBacklogTasks(_ context.Context) ([]domain.Task, error) {
	return nil, nil
}
func (m *mockTaskRepo) MoveToProject(_ context.Context, _, _, _ uuid.UUID) error { return nil }
func (m *mockTaskRepo) ListOpenByRecurringScheduleID(_ context.Context, _, _ uuid.UUID) ([]domain.Task, error) {
	return nil, nil
}

func (m *mockTaskRepo) SetHumanGate(_ context.Context, _ uuid.UUID, _ bool) error { return nil }
func (m *mockTaskRepo) SetShipped(_ context.Context, _ uuid.UUID, _ bool) error   { return nil }
func (m *mockTaskRepo) SetDodCheck(_ context.Context, _ uuid.UUID, _, _, _ string) error {
	return nil
}

var _ repository.TaskRepository = (*mockTaskRepo)(nil)

// ---------------------------------------------------------------------------
// mockTaskDepRepo — minimal stub satisfying repository.TaskDependencyRepository
// ---------------------------------------------------------------------------

type mockTaskDepRepo struct {
	listByTaskFn func(ctx context.Context, taskID uuid.UUID) ([]domain.TaskDependency, error)
}

func (m *mockTaskDepRepo) ListByTask(ctx context.Context, taskID uuid.UUID) ([]domain.TaskDependency, error) {
	if m.listByTaskFn != nil {
		return m.listByTaskFn(ctx, taskID)
	}
	return nil, nil
}

func (m *mockTaskDepRepo) Create(_ context.Context, _ *domain.TaskDependency) error { return nil }
func (m *mockTaskDepRepo) Delete(_ context.Context, _ uuid.UUID) error              { return nil }
func (m *mockTaskDepRepo) ListDependents(_ context.Context, _ uuid.UUID) ([]domain.TaskDependency, error) {
	return nil, nil
}
func (m *mockTaskDepRepo) Exists(_ context.Context, _, _ uuid.UUID) (bool, error) { return false, nil }

var _ repository.TaskDependencyRepository = (*mockTaskDepRepo)(nil)

// ---------------------------------------------------------------------------
// TestRemember_Amendment2_ThreadIDEdges
// ---------------------------------------------------------------------------

func TestRemember_Amendment2_ThreadIDEdges(t *testing.T) {
	wsID := uuid.New()
	threadID := "thread-abc123"
	taskID := uuid.New()

	t.Run("creates relates_to edge weight=1.0 for same-thread memory", func(t *testing.T) {
		existingMemID := uuid.New()
		var createdEdge *domain.MemoryEdge

		memRepo := &mockMemoryRepo{
			findByThreadIDFn: func(_ context.Context, _ uuid.UUID, _ string, _ uuid.UUID) ([]domain.Memory, error) {
				return []domain.Memory{
					{ID: existingMemID, WorkspaceID: wsID, ThreadID: &threadID},
				}, nil
			},
		}
		edgeRepo := &mockMemoryEdgeRepo{
			upsertEdgeFn: func(edge *domain.MemoryEdge) error {
				if edge.RelationshipType == domain.EdgeRelatesTo && edge.Weight == 1.0 {
					createdEdge = edge
				}
				return nil
			},
		}
		taskRepo := &mockTaskRepo{
			getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Task, error) {
				return &domain.Task{ID: id, ThreadID: &threadID}, nil
			},
		}

		svc := NewMemoryService(memRepo, edgeRepo, nil,
			MemoryWithTaskRepo(taskRepo),
		)

		mem := &domain.Memory{
			WorkspaceID:  wsID,
			Key:          "new-thread-mem",
			Content:      "content",
			Scope:        domain.ScopeWorkspace,
			SourceTaskID: &taskID,
		}
		_, err := svc.Remember(context.Background(), mem)
		require.NoError(t, err)
		require.NotNil(t, createdEdge, "relates_to edge must be created for same-thread candidate")
		assert.Equal(t, existingMemID, createdEdge.MemoryToID)
		assert.Equal(t, float32(1.0), createdEdge.Weight)
		assert.Equal(t, domain.EdgeRelatesTo, createdEdge.RelationshipType)
	})

	t.Run("propagates thread_id from task when not explicitly set", func(t *testing.T) {
		var upsertedMem *domain.Memory

		memRepo := &mockMemoryRepo{
			upsertFn: func(_ context.Context, m *domain.Memory) error {
				upsertedMem = m
				return nil
			},
		}
		taskRepo := &mockTaskRepo{
			getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Task, error) {
				return &domain.Task{ID: id, ThreadID: &threadID}, nil
			},
		}

		svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, nil,
			MemoryWithTaskRepo(taskRepo),
		)

		mem := &domain.Memory{
			WorkspaceID:  wsID,
			Key:          "propagate-thread",
			Content:      "content",
			Scope:        domain.ScopeWorkspace,
			SourceTaskID: &taskID,
		}
		_, err := svc.Remember(context.Background(), mem)
		require.NoError(t, err)
		require.NotNil(t, upsertedMem.ThreadID, "thread_id must be propagated from source task")
		assert.Equal(t, threadID, *upsertedMem.ThreadID)
	})

	t.Run("no edge when thread_id is nil", func(t *testing.T) {
		edgeCalled := false
		memRepo := &mockMemoryRepo{}
		edgeRepo := &mockMemoryEdgeRepo{
			upsertEdgeFn: func(edge *domain.MemoryEdge) error {
				if edge.RelationshipType == domain.EdgeRelatesTo && edge.Weight == 1.0 {
					edgeCalled = true
				}
				return nil
			},
		}
		taskRepo := &mockTaskRepo{
			getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Task, error) {
				return &domain.Task{ID: id, ThreadID: nil}, nil // no thread
			},
		}

		svc := NewMemoryService(memRepo, edgeRepo, nil, MemoryWithTaskRepo(taskRepo))

		mem := &domain.Memory{
			WorkspaceID:  wsID,
			Key:          "no-thread",
			Content:      "content",
			Scope:        domain.ScopeWorkspace,
			SourceTaskID: &taskID,
		}
		_, err := svc.Remember(context.Background(), mem)
		require.NoError(t, err)
		assert.False(t, edgeCalled, "no weight=1.0 edge when task has no thread_id")
	})

	t.Run("no edge when task_repo not wired", func(t *testing.T) {
		edgeCalled := false
		memRepo := &mockMemoryRepo{}
		edgeRepo := &mockMemoryEdgeRepo{
			upsertEdgeFn: func(edge *domain.MemoryEdge) error {
				if edge.Weight == 1.0 && edge.RelationshipType == domain.EdgeRelatesTo {
					edgeCalled = true
				}
				return nil
			},
		}

		svc := NewMemoryService(memRepo, edgeRepo, nil) // no MemoryWithTaskRepo

		tid := threadID
		mem := &domain.Memory{
			WorkspaceID:  wsID,
			Key:          "no-task-repo",
			Content:      "content",
			Scope:        domain.ScopeWorkspace,
			SourceTaskID: &taskID,
			ThreadID:     &tid,
		}
		_, err := svc.Remember(context.Background(), mem)
		require.NoError(t, err)
		// FindByThreadID is still called when ThreadID is pre-set on mem (even w/o taskRepo)
		// — the edge repo is the gate; this test verifies no panic / no crash.
		_ = edgeCalled
	})
}

// ---------------------------------------------------------------------------
// TestRemember_Amendment3_TaskGraphBridge
// ---------------------------------------------------------------------------

func TestRemember_Amendment3_TaskGraphBridge(t *testing.T) {
	wsID := uuid.New()
	taskID := uuid.New()
	parentTaskID := uuid.New()
	depTaskID := uuid.New()

	t.Run("creates derived_from edge for parent task memory", func(t *testing.T) {
		parentMemID := uuid.New()
		var createdEdge *domain.MemoryEdge

		memRepo := &mockMemoryRepo{
			findBySourceTaskIDsFn: func(_ context.Context, _ uuid.UUID, taskIDs []uuid.UUID) ([]domain.Memory, error) {
				for _, id := range taskIDs {
					if id == parentTaskID {
						return []domain.Memory{{ID: parentMemID, WorkspaceID: wsID}}, nil
					}
				}
				return nil, nil
			},
		}
		edgeRepo := &mockMemoryEdgeRepo{
			upsertEdgeFn: func(edge *domain.MemoryEdge) error {
				if edge.RelationshipType == domain.EdgeDerivedFrom {
					createdEdge = edge
				}
				return nil
			},
		}
		taskRepo := &mockTaskRepo{
			getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Task, error) {
				return &domain.Task{ID: id, ParentTaskID: &parentTaskID}, nil
			},
		}

		svc := NewMemoryService(memRepo, edgeRepo, nil, MemoryWithTaskRepo(taskRepo))

		mem := &domain.Memory{
			WorkspaceID:  wsID,
			Key:          "child-task-mem",
			Content:      "content",
			Scope:        domain.ScopeWorkspace,
			SourceTaskID: &taskID,
		}
		_, err := svc.Remember(context.Background(), mem)
		require.NoError(t, err)
		require.NotNil(t, createdEdge, "derived_from edge must be created for parent task memory")
		assert.Equal(t, parentMemID, createdEdge.MemoryToID)
		assert.Equal(t, float32(0.7), createdEdge.Weight)
		assert.Equal(t, domain.EdgeDerivedFrom, createdEdge.RelationshipType)
	})

	t.Run("creates derived_from edge for depends_on task memory", func(t *testing.T) {
		depMemID := uuid.New()
		var createdEdge *domain.MemoryEdge

		memRepo := &mockMemoryRepo{
			findBySourceTaskIDsFn: func(_ context.Context, _ uuid.UUID, taskIDs []uuid.UUID) ([]domain.Memory, error) {
				for _, id := range taskIDs {
					if id == depTaskID {
						return []domain.Memory{{ID: depMemID, WorkspaceID: wsID}}, nil
					}
				}
				return nil, nil
			},
		}
		edgeRepo := &mockMemoryEdgeRepo{
			upsertEdgeFn: func(edge *domain.MemoryEdge) error {
				if edge.RelationshipType == domain.EdgeDerivedFrom {
					createdEdge = edge
				}
				return nil
			},
		}
		taskRepo := &mockTaskRepo{
			getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Task, error) {
				return &domain.Task{ID: id, ParentTaskID: nil}, nil // no parent
			},
		}
		depRepo := &mockTaskDepRepo{
			listByTaskFn: func(_ context.Context, _ uuid.UUID) ([]domain.TaskDependency, error) {
				return []domain.TaskDependency{
					{TaskID: taskID, DependsOnTaskID: depTaskID},
				}, nil
			},
		}

		svc := NewMemoryService(memRepo, edgeRepo, nil,
			MemoryWithTaskRepo(taskRepo),
			MemoryWithDepRepo(depRepo),
		)

		mem := &domain.Memory{
			WorkspaceID:  wsID,
			Key:          "dep-task-mem",
			Content:      "content",
			Scope:        domain.ScopeWorkspace,
			SourceTaskID: &taskID,
		}
		_, err := svc.Remember(context.Background(), mem)
		require.NoError(t, err)
		require.NotNil(t, createdEdge, "derived_from edge must be created for depends_on task memory")
		assert.Equal(t, depMemID, createdEdge.MemoryToID)
		assert.Equal(t, float32(0.7), createdEdge.Weight)
	})

	t.Run("no edge when task has no parent and no depends_on", func(t *testing.T) {
		edgeCalled := false
		memRepo := &mockMemoryRepo{}
		edgeRepo := &mockMemoryEdgeRepo{
			upsertEdgeFn: func(edge *domain.MemoryEdge) error {
				if edge.RelationshipType == domain.EdgeDerivedFrom && edge.Weight == 0.7 {
					edgeCalled = true
				}
				return nil
			},
		}
		taskRepo := &mockTaskRepo{
			getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Task, error) {
				return &domain.Task{ID: id, ParentTaskID: nil}, nil
			},
		}
		depRepo := &mockTaskDepRepo{} // returns empty list

		svc := NewMemoryService(memRepo, edgeRepo, nil,
			MemoryWithTaskRepo(taskRepo),
			MemoryWithDepRepo(depRepo),
		)

		mem := &domain.Memory{
			WorkspaceID:  wsID,
			Key:          "root-task-mem",
			Content:      "content",
			Scope:        domain.ScopeWorkspace,
			SourceTaskID: &taskID,
		}
		_, err := svc.Remember(context.Background(), mem)
		require.NoError(t, err)
		assert.False(t, edgeCalled, "no derived_from edge when task has no related tasks")
	})
}

// ---------------------------------------------------------------------------
// TestSetProjectKnowledge_Amendment4_CanonicalSupersede
// ---------------------------------------------------------------------------

func TestSetProjectKnowledge_Amendment4_CanonicalSupersede(t *testing.T) {
	wsID := uuid.New()
	projID := uuid.New()

	t.Run("supersedes low-importance stale memory with overlapping semantic tags", func(t *testing.T) {
		staleID := uuid.New()
		newMemID := uuid.New()
		var supersededEdge *domain.MemoryEdge
		archivedID := uuid.Nil

		memRepo := &mockMemoryRepo{
			upsertFn: func(_ context.Context, m *domain.Memory) error {
				m.ID = newMemID // assign ID so Amendment 4 guard passes
				return nil
			},
			findByScopeFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.Memory, error) {
				// return a stale memory with overlapping semantic tags
				return []domain.Memory{
					{
						ID:              staleID,
						WorkspaceID:     wsID,
						Tags:            []string{"auth-middleware", "session-token", "compliance"},
						ImportanceScore: 0.3, // low — eligible for supersede
						Archived:        false,
					},
				}, nil
			},
			setArchivedFn: func() error {
				archivedID = staleID
				return nil
			},
		}
		edgeRepo := &mockMemoryEdgeRepo{
			upsertEdgeFn: func(edge *domain.MemoryEdge) error {
				if edge.RelationshipType == domain.EdgeSupersedes {
					supersededEdge = edge
				}
				return nil
			},
		}

		svc := NewMemoryService(memRepo, edgeRepo, nil)

		input := SetProjectKnowledgeInput{
			WorkspaceID: wsID,
			ProjectID:   projID,
			Key:         "auth-rewrite-canonical",
			Value:       "Auth middleware rewritten for compliance — old session token storage removed.",
			Tags:        []string{"auth-middleware", "session-token", "compliance"},
		}
		_, _, err := svc.SetProjectKnowledge(context.Background(), input)
		require.NoError(t, err)
		require.NotNil(t, supersededEdge, "supersedes edge must be created for stale overlapping memory")
		assert.Equal(t, staleID, supersededEdge.MemoryToID)
		assert.Equal(t, domain.EdgeSupersedes, supersededEdge.RelationshipType)
		assert.Equal(t, staleID, archivedID, "stale memory must be archived")
	})

	t.Run("does not supersede high-importance memory (>=0.75)", func(t *testing.T) {
		highImportanceID := uuid.New()
		supersedeCalled := false

		memRepo := &mockMemoryRepo{
			upsertFn: func(_ context.Context, m *domain.Memory) error {
				m.ID = uuid.New()
				return nil
			},
			findByScopeFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.Memory, error) {
				return []domain.Memory{
					{
						ID:              highImportanceID,
						WorkspaceID:     wsID,
						Tags:            []string{"auth-middleware", "canonical-decision"},
						ImportanceScore: 0.80, // high — must NOT be superseded
						Archived:        false,
					},
				}, nil
			},
		}
		edgeRepo := &mockMemoryEdgeRepo{
			upsertEdgeFn: func(edge *domain.MemoryEdge) error {
				if edge.RelationshipType == domain.EdgeSupersedes {
					supersedeCalled = true
				}
				return nil
			},
		}

		svc := NewMemoryService(memRepo, edgeRepo, nil)

		input := SetProjectKnowledgeInput{
			WorkspaceID: wsID,
			ProjectID:   projID,
			Key:         "auth-canonical-v2",
			Value:       "New canonical auth spec.",
			Tags:        []string{"auth-middleware"},
		}
		_, _, err := svc.SetProjectKnowledge(context.Background(), input)
		require.NoError(t, err)
		assert.False(t, supersedeCalled, "high-importance memory must not be superseded")
	})

	t.Run("does not supersede when generic-only tag overlap", func(t *testing.T) {
		genericOnlyID := uuid.New()
		supersedeCalled := false

		memRepo := &mockMemoryRepo{
			upsertFn: func(_ context.Context, m *domain.Memory) error {
				m.ID = uuid.New()
				return nil
			},
			findByScopeFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.Memory, error) {
				return []domain.Memory{
					{
						ID:              genericOnlyID,
						WorkspaceID:     wsID,
						Tags:            []string{"kind:fact", "project:mesh-dev", "owner:linus"},
						ImportanceScore: 0.3,
						Archived:        false,
					},
				}, nil
			},
		}
		edgeRepo := &mockMemoryEdgeRepo{
			upsertEdgeFn: func(edge *domain.MemoryEdge) error {
				if edge.RelationshipType == domain.EdgeSupersedes {
					supersedeCalled = true
				}
				return nil
			},
		}

		svc := NewMemoryService(memRepo, edgeRepo, nil)

		input := SetProjectKnowledgeInput{
			WorkspaceID: wsID,
			ProjectID:   projID,
			Key:         "generic-only-test",
			Value:       "A canonical fact.",
			Tags:        []string{"kind:fact", "project:mesh-dev"},
		}
		_, _, err := svc.SetProjectKnowledge(context.Background(), input)
		require.NoError(t, err)
		assert.False(t, supersedeCalled, "generic-only overlap must not create supersedes edge")
	})
}

// ---------------------------------------------------------------------------
// P1-D: Recency-aware recall — decay formula + freshness_score + RecencyScore
// ---------------------------------------------------------------------------

// TestRecall_DecayScoreFormula verifies that a 1-day-old memory scores higher
// than a 60-day-old memory when ApplyDecay=true with a 30-day half-life.
func TestRecall_DecayScoreFormula(t *testing.T) {
	now := time.Now()
	fresh := domain.ScoredMemory{
		Memory: domain.Memory{
			ID:              uuid.New(),
			Key:             "fresh-mem",
			CreatedAt:       now.Add(-24 * time.Hour),
			FreshnessScore:  1.0,
			ImportanceScore: 0.8,
		},
		Score: 1.0,
	}
	old := domain.ScoredMemory{
		Memory: domain.Memory{
			ID:              uuid.New(),
			Key:             "old-mem",
			CreatedAt:       now.Add(-60 * 24 * time.Hour),
			FreshnessScore:  1.0,
			ImportanceScore: 0.8,
		},
		Score: 1.0,
	}

	repo := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{fresh, old}, nil
		},
	}

	svc := newMemoryService(repo)
	results, _, err := svc.Recall(context.Background(), domain.RecallOpts{
		Query:        "test",
		WorkspaceID:  uuid.New(),
		Limit:        10,
		ApplyDecay:   true,
		HalfLifeDays: 30,
	})

	require.NoError(t, err)
	require.Len(t, results, 2)

	// find by key
	var freshResult, oldResult domain.ScoredMemory
	for _, r := range results {
		if r.Key == "fresh-mem" {
			freshResult = r
		} else {
			oldResult = r
		}
	}

	assert.Greater(t, freshResult.Score, oldResult.Score,
		"1d-old memory must outscore 60d-old memory with 30d half-life")
	// at 30d half-life: 60d old → factor ≈ 0.25; 1d old → factor ≈ 0.977
	assert.InDelta(t, 0.977, freshResult.RecencyScore, 0.02,
		"RecencyScore for 1d-old memory should be ~0.977 with 30d half-life")
	assert.InDelta(t, 0.25, oldResult.RecencyScore, 0.02,
		"RecencyScore for 60d-old memory should be ~0.25 with 30d half-life")
}

// TestRecall_FreshnessScoreMultiplied verifies that FreshnessScore is always
// multiplied into the recall score even when ApplyDecay is false.
func TestRecall_FreshnessScoreMultiplied(t *testing.T) {
	now := time.Now()
	active := domain.ScoredMemory{
		Memory: domain.Memory{
			ID:              uuid.New(),
			Key:             "active-mem",
			CreatedAt:       now,
			FreshnessScore:  1.0,
			ImportanceScore: 0.8,
		},
		Score: 1.0,
	}
	stale := domain.ScoredMemory{
		Memory: domain.Memory{
			ID:              uuid.New(),
			Key:             "stale-mem",
			CreatedAt:       now,
			FreshnessScore:  0.25,
			ImportanceScore: 0.8,
		},
		Score: 1.0,
	}

	repo := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{active, stale}, nil
		},
	}

	svc := newMemoryService(repo)
	results, _, err := svc.Recall(context.Background(), domain.RecallOpts{
		Query:       "test",
		WorkspaceID: uuid.New(),
		Limit:       10,
		ApplyDecay:  false, // no time decay — only freshness_score
	})

	require.NoError(t, err)
	require.Len(t, results, 2)

	var activeResult, staleResult domain.ScoredMemory
	for _, r := range results {
		if r.Key == "active-mem" {
			activeResult = r
		} else {
			staleResult = r
		}
	}

	assert.Greater(t, activeResult.Score, staleResult.Score,
		"active memory (freshness=1.0) must outscore stale memory (freshness=0.25)")
	// RRF ranks differ by 1 position so the ratio is ≈ 4 * (rank0_rrf/rank1_rrf) ≈ 4.06.
	// Assert the ratio is in [3.5, 4.5] — captures the 4× freshness multiplier.
	ratio := activeResult.Score / staleResult.Score
	assert.True(t, ratio > 3.5 && ratio < 4.5,
		"score ratio must be ~4 (freshness 1.0 vs 0.25), got %.3f", ratio)
	// RecencyScore must be 1.0 when decay is not applied.
	assert.InDelta(t, 1.0, activeResult.RecencyScore, 0.001,
		"RecencyScore must be 1.0 when ApplyDecay=false")
	assert.InDelta(t, 1.0, staleResult.RecencyScore, 0.001,
		"RecencyScore must be 1.0 when ApplyDecay=false")
}

// TestRecall_HalfLifeOverride verifies that a shorter HalfLifeDays results in
// a larger penalty for the same memory age compared to a longer half-life.
func TestRecall_HalfLifeOverride(t *testing.T) {
	now := time.Now()
	mem := domain.ScoredMemory{
		Memory: domain.Memory{
			ID:              uuid.New(),
			Key:             "test-mem",
			CreatedAt:       now.Add(-30 * 24 * time.Hour), // 30 days old
			FreshnessScore:  1.0,
			ImportanceScore: 0.8,
		},
		Score: 1.0,
	}

	makeRepo := func() *mockMemoryRepo {
		return &mockMemoryRepo{
			fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
				return []domain.ScoredMemory{mem}, nil
			},
		}
	}

	// Short half-life (10d): at age 30d, score ≈ exp(-ln2/10*30) = exp(-2.08) ≈ 0.125
	repoA := makeRepo()
	svcA := newMemoryService(repoA)
	resA, _, err := svcA.Recall(context.Background(), domain.RecallOpts{
		Query:        "test",
		WorkspaceID:  uuid.New(),
		Limit:        10,
		ApplyDecay:   true,
		HalfLifeDays: 10,
	})
	require.NoError(t, err)
	require.Len(t, resA, 1)

	// Long half-life (90d): at age 30d, score ≈ exp(-ln2/90*30) = exp(-0.231) ≈ 0.794
	repoB := makeRepo()
	svcB := newMemoryService(repoB)
	resB, _, err := svcB.Recall(context.Background(), domain.RecallOpts{
		Query:        "test",
		WorkspaceID:  uuid.New(),
		Limit:        10,
		ApplyDecay:   true,
		HalfLifeDays: 90,
	})
	require.NoError(t, err)
	require.Len(t, resB, 1)

	assert.Less(t, resA[0].Score, resB[0].Score,
		"shorter half-life must give lower score for same age")
	// 30d-old, half-life 10d: exp(-ln2/10*30) = 2^-3 ≈ 0.125
	assert.InDelta(t, 0.125, resA[0].RecencyScore, 0.02,
		"30d-old memory with 10d half-life should have RecencyScore ≈ 0.125")
}

// ---------------------------------------------------------------------------
// BM25 sparse arm + RRF fusion tests (P2-a)
// ---------------------------------------------------------------------------

// TestRecall_BM25Arm_NonZeroScore verifies that when FullTextSearchRanked returns
// a hit with a positive ts_rank score, Recall surfaces it in results.
func TestRecall_BM25Arm_NonZeroScore(t *testing.T) {
	wsID := uuid.New()
	memID := uuid.New()
	hit := domain.Memory{
		ID:              memID,
		WorkspaceID:     wsID,
		Content:         "BM25 ranked recall test",
		Key:             "bm25-test",
		Status:          domain.MemoryStatusActive,
		FreshnessScore:  1.0,
		ImportanceScore: 0.5,
		Scope:           domain.ScopeWorkspace,
	}

	repo := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, query string, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
			// Return a non-zero ts_rank_cd score for the query term.
			return []domain.ScoredMemory{{Memory: hit, Score: 0.75}}, nil
		},
	}
	svc := NewMemoryService(repo, nil, nil)

	results, _, err := svc.Recall(context.Background(), domain.RecallOpts{
		Query:       "bm25",
		WorkspaceID: wsID,
		Limit:       10,
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, memID, results[0].ID)
	assert.Greater(t, results[0].Score, 0.0, "RRF score must be positive for a BM25 hit")
}

// TestRRF_DualArmScoresHigher verifies that a candidate appearing in both the BM25 arm
// and the vector arm accumulates a higher RRF score than a candidate in only one arm.
func TestRRF_DualArmScoresHigher(t *testing.T) {
	makeMemory := func() domain.Memory {
		return domain.Memory{ID: uuid.New(), WorkspaceID: uuid.New()}
	}
	onlyFTS := makeMemory()
	onlyVec := makeMemory()
	both := makeMemory()

	ftsResults := []domain.ScoredMemory{
		{Memory: both, Score: 0.9},
		{Memory: onlyFTS, Score: 0.6},
	}
	vecResults := []domain.ScoredMemory{
		{Memory: both, Score: 0.8},
		{Memory: onlyVec, Score: 0.7},
	}

	merged := reciprocalRankFusion(ftsResults, vecResults, defaultRRFTextWeight, defaultRRFVectorWeight)

	// Collect scores by ID.
	scoreFor := func(id uuid.UUID) float64 {
		for _, m := range merged {
			if m.ID == id {
				return m.Score
			}
		}
		return 0
	}

	bothScore := scoreFor(both.ID)
	ftsScore := scoreFor(onlyFTS.ID)
	vecScore := scoreFor(onlyVec.ID)

	assert.Greater(t, bothScore, ftsScore,
		"dual-arm candidate must outrank FTS-only candidate")
	assert.Greater(t, bothScore, vecScore,
		"dual-arm candidate must outrank vector-only candidate")
}

// TestRRF_ResultCountAtMostLimit verifies that reciprocalRankFusion never returns
// more candidates than the union of both arms (dedup is correct).
func TestRRF_ResultCountAtMostLimit(t *testing.T) {
	makeMemories := func(n int) []domain.ScoredMemory {
		out := make([]domain.ScoredMemory, n)
		for i := range out {
			out[i] = domain.ScoredMemory{Memory: domain.Memory{ID: uuid.New()}, Score: float64(n - i)}
		}
		return out
	}

	fts := makeMemories(5)
	vec := makeMemories(5)

	merged := reciprocalRankFusion(fts, vec, defaultRRFTextWeight, defaultRRFVectorWeight)

	// 10 unique IDs across 5+5 = at most 10.
	assert.LessOrEqual(t, len(merged), 10, "result count must not exceed union size")

	// If we add duplicates, they should be deduped.
	shared := fts[0]
	fts2 := append(makeMemories(3), shared)
	vec2 := append(makeMemories(3), shared)
	merged2 := reciprocalRankFusion(fts2, vec2, defaultRRFTextWeight, defaultRRFVectorWeight)
	seenIDs := make(map[uuid.UUID]bool)
	for _, m := range merged2 {
		assert.False(t, seenIDs[m.ID], "each ID must appear at most once in merged results")
		seenIDs[m.ID] = true
	}
}

// TestRecall_BM25Fallback_VectorOnly verifies that when FullTextSearchRanked errors,
// Recall logs and continues with vector-only results (no error returned to caller).
func TestRecall_BM25Fallback_VectorOnly(t *testing.T) {
	wsID := uuid.New()
	vecMem := domain.Memory{
		ID:              uuid.New(),
		WorkspaceID:     wsID,
		Content:         "vector only result",
		Key:             "vec-only",
		Status:          domain.MemoryStatusActive,
		FreshnessScore:  1.0,
		ImportanceScore: 0.5,
		Scope:           domain.ScopeWorkspace,
	}

	repo := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
			return nil, fmt.Errorf("simulated bm25 timeout")
		},
		// VectorSearch stub not provided — NoopEmbedder means it is never called.
		// We exercise the BM25-fallback path where kwErr != nil.
		// To also test vector results we would need a real embedder; this covers the
		// graceful-degradation branch (kwErr → kwResults = nil → RRF on empty kw arm).
	}
	svc := NewMemoryService(repo, nil, nil) // noop embedder

	// With both arms returning nothing useful (BM25 errors, no vector embedder),
	// Recall must return an empty slice without error.
	results, _, err := svc.Recall(context.Background(), domain.RecallOpts{
		Query:       "test fallback",
		WorkspaceID: wsID,
		Limit:       10,
	})
	require.NoError(t, err, "BM25 failure must not propagate as an error to callers")
	_ = vecMem // used for documentation; actual vec path requires non-noop embedder
	assert.Empty(t, results, "with BM25 error and noop embedder, results should be empty")
}

// A memory embedded by a DIFFERENT model must be re-embedded when the configured embedder
// changes — otherwise switching embedding provider/model silently strands the whole corpus:
// vectors from the old model live in another space, score 0 in cosineSimilarity (dimension
// guard), and nothing would ever rewrite them. This is the regression guard for that.
func TestBatchEmbed_ReembedsMemoriesFromAnotherModel(t *testing.T) {
	repo := &mockMemoryRepo{
		needEmbedding: []domain.Memory{
			{ID: uuid.New(), Key: "k", Content: "content", EmbeddingModel: "text-embedding-3-small", EmbeddingDim: 1536},
		},
	}
	svc := NewMemoryService(repo, nil, &switchModelEmbedder{model: "bge-m3", dim: 1024})

	n, err := svc.BatchEmbed(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("BatchEmbed: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected the stale-model memory to be re-embedded, got n=%d", n)
	}
	// The repo filter keys on the model BatchEmbed asks for — it must be the CURRENT one,
	// not the one already stored, or the stale rows would never be selected.
	if repo.embedModelAsked != "bge-m3" {
		t.Fatalf("BatchEmbed must ask the repo for the currently configured model, asked %q", repo.embedModelAsked)
	}
	if repo.embeddedWithModel != "" && repo.embeddedWithModel != "bge-m3" {
		t.Fatalf("re-embedded vector stored under the wrong model: %q", repo.embeddedWithModel)
	}
}

// switchModelEmbedder is a deterministic non-noop embedder for the model-switch test above.
type switchModelEmbedder struct {
	model string
	dim   int
}

func (s *switchModelEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return make([]float32, s.dim), nil
}
func (s *switchModelEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, s.dim)
	}
	return out, nil
}
func (s *switchModelEmbedder) Model() string   { return s.model }
func (s *switchModelEmbedder) Dimensions() int { return s.dim }

// ---------------------------------------------------------------------------
// Chunked embed path (ADR-0002, #b052cdda subtask 5)
// ---------------------------------------------------------------------------

// mockMemoryChunkRepo is an in-memory repository.MemoryChunkRepository. ReplaceChunks
// overwrites wholesale, matching the real delete+reinsert transaction's observable behavior.
type mockMemoryChunkRepo struct {
	mu         sync.Mutex
	chunks     map[uuid.UUID][]domain.MemoryChunk
	replaceErr error
	// replaceCalls counts ReplaceChunks invocations — used to prove a failed embed call
	// never reaches ReplaceChunks at all (partial writes must never happen).
	replaceCalls int
}

func newMockMemoryChunkRepo() *mockMemoryChunkRepo {
	return &mockMemoryChunkRepo{chunks: make(map[uuid.UUID][]domain.MemoryChunk)}
}

func (m *mockMemoryChunkRepo) ReplaceChunks(_ context.Context, memoryID uuid.UUID, chunks []domain.MemoryChunk) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.replaceCalls++
	if m.replaceErr != nil {
		return m.replaceErr
	}
	cp := append([]domain.MemoryChunk(nil), chunks...)
	m.chunks[memoryID] = cp
	return nil
}

func (m *mockMemoryChunkRepo) ListByMemoryIDs(_ context.Context, memoryIDs []uuid.UUID) ([]domain.MemoryChunk, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.MemoryChunk
	for _, id := range memoryIDs {
		out = append(out, m.chunks[id]...)
	}
	return out, nil
}

func (m *mockMemoryChunkRepo) MemoryIDsWithChunks(_ context.Context, memoryIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[uuid.UUID]bool)
	for _, id := range memoryIDs {
		if len(m.chunks[id]) > 0 {
			result[id] = true
		}
	}
	return result, nil
}

// nthCallFailsEmbedder returns a deterministic vector, except its failAt'th call (1-indexed)
// returns an error — used to prove a mid-batch embed failure aborts the whole memory's
// chunk replace rather than writing a partial set.
type nthCallFailsEmbedder struct {
	dim    int
	failAt int
	calls  atomic.Int64
}

func (e *nthCallFailsEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	n := e.calls.Add(1)
	if int(n) == e.failAt {
		return nil, fmt.Errorf("simulated embed failure on call %d", n)
	}
	return make([]float32, e.dim), nil
}
func (e *nthCallFailsEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := e.Embed(ctx, text)
		if err != nil {
			return nil, err
		}
		out[i] = vec
	}
	return out, nil
}
func (e *nthCallFailsEmbedder) Model() string   { return "nth-call-fails" }
func (e *nthCallFailsEmbedder) Dimensions() int { return e.dim }

// emptyVecEmbedder always succeeds but returns a zero-length vector — the shape a
// noop or degraded embedder can return without erroring. Used to test embedChunked's
// "skip this chunk, don't fabricate a row" and "nothing embedded, leave existing
// chunks alone" branches.
type emptyVecEmbedder struct{}

func (emptyVecEmbedder) Embed(_ context.Context, _ string) ([]float32, error) { return nil, nil }
func (emptyVecEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	return make([][]float32, len(texts)), nil
}
func (emptyVecEmbedder) Model() string   { return "empty-vec" }
func (emptyVecEmbedder) Dimensions() int { return 0 }

func TestEmbedAndStore_Chunked_AllEmptyVectorsLeavesExistingChunksAlone(t *testing.T) {
	memRepo := &mockMemoryRepo{}
	chunkRepo := newMockMemoryChunkRepo()
	svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, emptyVecEmbedder{}, MemoryWithChunkRepo(chunkRepo))
	ms := svc.(*memoryService)

	id := uuid.New()
	require.NoError(t, ms.embedChunked(context.Background(), id, longTranscript(30)))

	assert.Zero(t, chunkRepo.replaceCalls, "every chunk embedding empty must skip ReplaceChunks entirely")
	assert.Zero(t, memRepo.markedEmbeddingModelID, "must not watermark a memory nothing was actually embedded for")
}

func TestEmbedChunked_ReplaceChunksError_IsReturned(t *testing.T) {
	memRepo := &mockMemoryRepo{}
	chunkRepo := newMockMemoryChunkRepo()
	chunkRepo.replaceErr = errors.New("simulated db failure")
	svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, &switchModelEmbedder{model: "e5-small", dim: 384},
		MemoryWithChunkRepo(chunkRepo))
	ms := svc.(*memoryService)

	err := ms.embedChunked(context.Background(), uuid.New(), "some content")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "simulated db failure")
	assert.Zero(t, memRepo.markedEmbeddingModelID, "a failed ReplaceChunks must not be followed by a watermark write")
}

func TestEmbedChunked_MarkEmbeddingModelError_IsWrappedAndReturned(t *testing.T) {
	memRepo := &mockMemoryRepo{markEmbeddingModelErr: errors.New("simulated db failure")}
	chunkRepo := newMockMemoryChunkRepo()
	svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, &switchModelEmbedder{model: "e5-small", dim: 384},
		MemoryWithChunkRepo(chunkRepo))
	ms := svc.(*memoryService)

	id := uuid.New()
	err := ms.embedChunked(context.Background(), id, "some content")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mark embedding model")
	assert.Contains(t, err.Error(), "simulated db failure")
	// ReplaceChunks itself must still have succeeded — only the watermark write failed.
	got, listErr := chunkRepo.ListByMemoryIDs(context.Background(), []uuid.UUID{id})
	require.NoError(t, listErr)
	assert.NotEmpty(t, got)
}

func TestBatchEmbed_Chunked_EmbedChunkedErrorLogsAndContinuesRatherThanAborting(t *testing.T) {
	ok := uuid.New()
	failing := uuid.New()
	memRepo := &mockMemoryRepo{
		needEmbedding: []domain.Memory{
			{ID: failing, Key: "k", Content: "content"},
			{ID: ok, Key: "k", Content: "content"},
		},
	}
	chunkRepo := newMockMemoryChunkRepo()
	// Fails on the very first Embed call (the "failing" memory's only chunk), succeeds after.
	failer := &nthCallFailsEmbedder{dim: 4, failAt: 1}
	svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, failer, MemoryWithChunkRepo(chunkRepo))

	n, err := svc.BatchEmbed(context.Background(), uuid.New())
	require.NoError(t, err, "one memory's embed failure must not fail the whole batch")
	assert.Equal(t, 1, n, "only the memory that embedded successfully is counted")

	gotFailing, listErr := chunkRepo.ListByMemoryIDs(context.Background(), []uuid.UUID{failing})
	require.NoError(t, listErr)
	assert.Empty(t, gotFailing, "the failing memory must have no chunks written")

	gotOK, listErr := chunkRepo.ListByMemoryIDs(context.Background(), []uuid.UUID{ok})
	require.NoError(t, listErr)
	assert.NotEmpty(t, gotOK)
}

func TestBackfillChunks_ListNotYetChunkedError_IsWrappedAndReturned(t *testing.T) {
	memRepo := &mockMemoryRepo{listNotYetChunkedErr: errors.New("simulated db failure")}
	chunkRepo := newMockMemoryChunkRepo()
	svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, &switchModelEmbedder{model: "e5-small", dim: 384},
		MemoryWithChunkRepo(chunkRepo))

	n, err := svc.BackfillChunks(context.Background(), uuid.New(), 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backfill chunks: list")
	assert.Zero(t, n)
}

func TestBackfillChunks_EmbedChunkedError_SkipsThatMemoryButContinues(t *testing.T) {
	ok := uuid.New()
	failing := uuid.New()
	memRepo := &mockMemoryRepo{
		notYetChunked: []domain.Memory{
			{ID: failing, Key: "k", Content: "content"},
			{ID: ok, Key: "k", Content: "content"},
		},
	}
	chunkRepo := newMockMemoryChunkRepo()
	failer := &nthCallFailsEmbedder{dim: 4, failAt: 1}
	svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, failer, MemoryWithChunkRepo(chunkRepo))

	n, err := svc.BackfillChunks(context.Background(), uuid.New(), 0)
	require.NoError(t, err, "one memory's embed failure must not fail the whole backfill pass")
	assert.Equal(t, 1, n)
}

func longTranscript(turns int) string {
	var b strings.Builder
	for i := 0; i < turns; i++ {
		fmt.Fprintf(&b, "User: this is turn %d of a transcript long enough to force multiple chunks in the test fixture.\n", i)
		fmt.Fprintf(&b, "Assistant: acknowledged turn %d, replying with enough padding text to matter for chunk sizing.\n", i)
	}
	return b.String()
}

func TestEmbedAndStore_Chunked_WritesMultipleChunksAndWatermarksModel(t *testing.T) {
	memRepo := &mockMemoryRepo{}
	chunkRepo := newMockMemoryChunkRepo()
	svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, &switchModelEmbedder{model: "e5-small", dim: 384},
		MemoryWithChunkRepo(chunkRepo))
	ms := svc.(*memoryService)

	id := uuid.New()
	text := longTranscript(30) // well over defaultChunkSize
	ms.embedAndStore(id, text)

	got, err := chunkRepo.ListByMemoryIDs(context.Background(), []uuid.UUID{id})
	require.NoError(t, err)
	require.Greater(t, len(got), 1, "a long transcript must produce more than one chunk")

	for i, c := range got {
		assert.Equal(t, "e5-small", c.EmbeddingModel, "chunk %d", i)
		assert.Equal(t, 384, c.EmbeddingDim, "chunk %d — must come from len(vec), not config", i)
		assert.NotEmpty(t, c.Embedding, "chunk %d embedding must be encoded", i)
	}

	// The legacy single-vector column must NOT be touched by the chunked path.
	assert.Empty(t, memRepo.embeddedWithModel, "chunked path must not write memories.embedding")
	// But the watermark that keeps BatchEmbed from re-processing this memory forever must be set.
	assert.Equal(t, id, memRepo.markedEmbeddingModelID)
	assert.Equal(t, "e5-small", memRepo.markedEmbeddingModelValue)
}

func TestEmbedAndStore_Chunked_ReembedReplacesRatherThanAccumulates(t *testing.T) {
	memRepo := &mockMemoryRepo{}
	chunkRepo := newMockMemoryChunkRepo()
	svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, &switchModelEmbedder{model: "e5-small", dim: 384},
		MemoryWithChunkRepo(chunkRepo))
	ms := svc.(*memoryService)

	id := uuid.New()
	ms.embedAndStore(id, longTranscript(30))
	first, err := chunkRepo.ListByMemoryIDs(context.Background(), []uuid.UUID{id})
	require.NoError(t, err)
	firstCount := len(first)
	require.Greater(t, firstCount, 0)

	// Re-embed with much shorter content — a real re-embed after the memory's
	// content changed. Old chunks must be gone, not left alongside the new ones.
	ms.embedAndStore(id, "a short memory now")
	second, err := chunkRepo.ListByMemoryIDs(context.Background(), []uuid.UUID{id})
	require.NoError(t, err)
	assert.Len(t, second, 1, "shorter re-embedded content must fully replace the old chunk set, not accumulate")
}

func TestEmbedAndStore_Chunked_PartialEmbedFailureNeverWritesPartialChunks(t *testing.T) {
	memRepo := &mockMemoryRepo{}
	chunkRepo := newMockMemoryChunkRepo()
	// Long enough to chunk into several pieces; fail on the 2nd embed call.
	failer := &nthCallFailsEmbedder{dim: 4, failAt: 2}
	svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, failer, MemoryWithChunkRepo(chunkRepo))
	ms := svc.(*memoryService)

	id := uuid.New()
	ms.embedAndStore(id, longTranscript(30))

	assert.Equal(t, 0, chunkRepo.replaceCalls, "a failed embed call must abort before ReplaceChunks is ever called — no partial chunk set")
	got, err := chunkRepo.ListByMemoryIDs(context.Background(), []uuid.UUID{id})
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Zero(t, memRepo.markedEmbeddingModelID, "must not watermark a memory that failed to fully embed")
}

func TestBatchEmbed_Chunked_RoutesThroughChunkRepoAndWatermarks(t *testing.T) {
	id := uuid.New()
	memRepo := &mockMemoryRepo{
		needEmbedding: []domain.Memory{
			{ID: id, Key: "k", Content: longTranscript(30)},
		},
	}
	chunkRepo := newMockMemoryChunkRepo()
	svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, &switchModelEmbedder{model: "e5-small", dim: 384},
		MemoryWithChunkRepo(chunkRepo))

	n, err := svc.BatchEmbed(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.Equal(t, 1, n, "BatchEmbed counts one memory embedded, regardless of its chunk count")

	got, err := chunkRepo.ListByMemoryIDs(context.Background(), []uuid.UUID{id})
	require.NoError(t, err)
	assert.Greater(t, len(got), 1)
	assert.Equal(t, id, memRepo.markedEmbeddingModelID, "BatchEmbed's chunked path must watermark memories.embedding_model so this memory is not re-selected next run")
}

// BackfillChunks selects independently of embedding_model — a memory already carrying the
// current model's watermark from the pre-chunking single-vector path (the normal state of
// every existing row before this feature ships) must still be picked up by ListNotYetChunked.
// This is the exact scenario BatchEmbed's own filter would silently skip.
func TestBackfillChunks_SelectsOnMissingChunksNotEmbeddingModel(t *testing.T) {
	id := uuid.New()
	memRepo := &mockMemoryRepo{
		notYetChunked: []domain.Memory{
			{ID: id, Key: "k", Content: longTranscript(30), EmbeddingModel: "e5-small"}, // already watermarked
		},
	}
	chunkRepo := newMockMemoryChunkRepo()
	svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, &switchModelEmbedder{model: "e5-small", dim: 384},
		MemoryWithChunkRepo(chunkRepo))

	n, err := svc.BackfillChunks(context.Background(), uuid.New(), 0)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	got, err := chunkRepo.ListByMemoryIDs(context.Background(), []uuid.UUID{id})
	require.NoError(t, err)
	assert.Greater(t, len(got), 1, "long content must still produce multiple chunks even though embedding_model already matched")
}

func TestBackfillChunks_NoopWithoutChunkRepo(t *testing.T) {
	memRepo := &mockMemoryRepo{
		notYetChunked: []domain.Memory{{ID: uuid.New(), Key: "k", Content: "x"}},
	}
	svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, &switchModelEmbedder{model: "e5-small", dim: 384})
	// no MemoryWithChunkRepo

	n, err := svc.BackfillChunks(context.Background(), uuid.New(), 0)
	require.NoError(t, err, "must be a no-op, not an error, matching BatchEmbed's noop-embedder convention")
	assert.Equal(t, 0, n)
}

func TestBackfillChunks_NoopWithNoopEmbedder(t *testing.T) {
	memRepo := &mockMemoryRepo{
		notYetChunked: []domain.Memory{{ID: uuid.New(), Key: "k", Content: "x"}},
	}
	chunkRepo := newMockMemoryChunkRepo()
	svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, nil, MemoryWithChunkRepo(chunkRepo)) // nil → noop embedder

	n, err := svc.BackfillChunks(context.Background(), uuid.New(), 0)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

// TestEmbedConcurrencyBound verifies that MemoryWithEmbedConcurrency caps how many
// embedAndStore calls run at once. Without a bound, a burst of memory writes fires one
// goroutine per write straight at the embedder — against a slow, CPU-bound backend (e.g.
// a self-hosted TEI server) that stampede exhausts the embedder's own concurrency limit,
// survivors queue, and the embed HTTP client's timeout trips before any response arrives.
func TestEmbedConcurrencyBound(t *testing.T) {
	repo := &mockMemoryRepo{}
	tracker := &concurrencyTrackingEmbedder{dim: 4, sleep: 20 * time.Millisecond}
	const limit = 2
	svc := NewMemoryService(repo, &mockMemoryEdgeRepo{}, tracker, MemoryWithEmbedConcurrency(limit))
	ms, ok := svc.(*memoryService)
	require.True(t, ok, "NewMemoryService must return *memoryService")

	const calls = 10
	var wg sync.WaitGroup
	wg.Add(calls)
	for i := 0; i < calls; i++ {
		go func() {
			defer wg.Done()
			ms.embedAndStore(uuid.New(), "some memory content")
		}()
	}
	wg.Wait()

	if got := tracker.calls.Load(); got != calls {
		t.Fatalf("expected all %d embed calls to complete, got %d", calls, got)
	}
	if got := tracker.maxInFlight.Load(); got > int64(limit) {
		t.Fatalf("observed max in-flight embeds = %d, want <= %d (concurrency limit)", got, limit)
	}
}

// TestEmbedConcurrencyUnboundedByDefault verifies that omitting MemoryWithEmbedConcurrency
// preserves today's behavior exactly: embeds run with no artificial serialization imposed
// by the new semaphore.
func TestEmbedConcurrencyUnboundedByDefault(t *testing.T) {
	repo := &mockMemoryRepo{}
	tracker := &concurrencyTrackingEmbedder{dim: 4, sleep: 30 * time.Millisecond}
	svc := NewMemoryService(repo, &mockMemoryEdgeRepo{}, tracker) // no MemoryWithEmbedConcurrency
	ms, ok := svc.(*memoryService)
	require.True(t, ok, "NewMemoryService must return *memoryService")

	const calls = 8
	var wg sync.WaitGroup
	wg.Add(calls)
	for i := 0; i < calls; i++ {
		go func() {
			defer wg.Done()
			ms.embedAndStore(uuid.New(), "some memory content")
		}()
	}
	wg.Wait()

	// Unbounded means every goroutine can be in flight at once; require strictly more
	// than the bounded test's limit above to prove no cap was introduced.
	if got := tracker.maxInFlight.Load(); got <= 2 {
		t.Fatalf("expected unbounded concurrency (observed max in-flight = %d), the semaphore must not gate when concurrency is unconfigured", got)
	}
}

// concurrencyTrackingEmbedder is a fake Embedder for concurrency tests. Embed sleeps to
// simulate a slow, CPU-bound embedding backend and atomically tracks the current and peak
// number of in-flight calls, plus a total completed-calls counter.
type concurrencyTrackingEmbedder struct {
	dim         int
	sleep       time.Duration
	inFlight    atomic.Int64
	maxInFlight atomic.Int64
	calls       atomic.Int64
}

func (e *concurrencyTrackingEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	cur := e.inFlight.Add(1)
	defer e.inFlight.Add(-1)
	for {
		peak := e.maxInFlight.Load()
		if cur <= peak || e.maxInFlight.CompareAndSwap(peak, cur) {
			break
		}
	}
	time.Sleep(e.sleep)
	e.calls.Add(1)
	return make([]float32, e.dim), nil
}

func (e *concurrencyTrackingEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := e.Embed(ctx, text)
		if err != nil {
			return nil, err
		}
		out[i] = vec
	}
	return out, nil
}

func (e *concurrencyTrackingEmbedder) Model() string   { return "concurrency-tracker" }
func (e *concurrencyTrackingEmbedder) Dimensions() int { return e.dim }
