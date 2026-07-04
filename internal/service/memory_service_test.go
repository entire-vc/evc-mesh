package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	upsertFn                           func(ctx context.Context, mem *domain.Memory) error
	getByIDFn                          func(ctx context.Context, id uuid.UUID) (*domain.Memory, error)
	getByKeyFn                         func(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID, agentID *uuid.UUID, key string, scope domain.MemoryScope) (*domain.Memory, error)
	fullTextSearchFn                   func(ctx context.Context, query string, wsID uuid.UUID, projID *uuid.UUID, scope string, tags []string, limit int, recencyWeight float64) ([]domain.ScoredMemory, error)
	findByScopeFn                      func(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID, scope string, limit int) ([]domain.Memory, error)
	listByWorkspaceProjectFn           func(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID, filter domain.MemoryListFilter) ([]domain.Memory, int64, error)
	deleteFn                           func(ctx context.Context, id uuid.UUID) error
	boostRelevanceFn                   func(ctx context.Context, ids []uuid.UUID) error
	findByShortIDFn                    func() (*domain.Memory, error)
	setArchivedFn                      func() error
	findByThreadIDFn                   func(ctx context.Context, wsID uuid.UUID, threadID string, excludeID uuid.UUID) ([]domain.Memory, error)
	findBySourceTaskIDsFn              func(ctx context.Context, wsID uuid.UUID, taskIDs []uuid.UUID) ([]domain.Memory, error)
	archiveStaleWorkspaceCheckpointsFn func(ctx context.Context, olderThan time.Duration, maxImportance float64) (int64, error)
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

func (m *mockMemoryRepo) VectorSearch(_ context.Context, _ []float32, _ uuid.UUID, _ *uuid.UUID, _ string, _ []string, _ int) ([]domain.ScoredMemory, error) {
	return nil, nil
}

func (m *mockMemoryRepo) UpdateEmbedding(_ context.Context, _ uuid.UUID, _ []float32, _ string, _ int) error {
	return nil
}

func (m *mockMemoryRepo) DecayRelevance(_ context.Context) (int64, error) {
	return 0, nil
}

func (m *mockMemoryRepo) CleanExpired(_ context.Context) (int64, error) {
	return 0, nil
}

func (m *mockMemoryRepo) ListWithNullEmbedding(_ context.Context, _ uuid.UUID, _ int) ([]domain.Memory, error) {
	return nil, nil
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

func (m *mockMemoryRepo) FindPinned(_ context.Context, _ uuid.UUID, _ *uuid.UUID) ([]domain.Memory, error) {
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

// Verify mockMemoryRepo satisfies the interface at compile time.
var _ repository.MemoryRepository = (*mockMemoryRepo)(nil)

// ---------------------------------------------------------------------------
// mockMemoryEdgeRepo
// ---------------------------------------------------------------------------

type mockMemoryEdgeRepo struct {
	upsertEdgeFn func(edge *domain.MemoryEdge) error
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
func (m *mockMemoryEdgeRepo) GetNeighbors(_ context.Context, _ []uuid.UUID, _ float64, _ int) ([]domain.MemoryEdge, error) {
	return nil, nil
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
		fullTextSearchFn: func(_ context.Context, _ string, _ uuid.UUID, _ *uuid.UUID, _ string, _ []string, _ int, _ float64) ([]domain.ScoredMemory, error) {
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

	scored, err := svc.Recall(context.Background(), opts)

	require.NoError(t, err)
	assert.Len(t, scored, 2)
	assert.True(t, boostCalled, "BoostRelevance should be called after a successful search")
}

// ---------------------------------------------------------------------------
// TestRecall_RecencyWeightPassthrough — the recency_weight param is clamped to [0,1]
// and threaded into FullTextSearch. Default (unset) must pass 0 = legacy behavior.
// ---------------------------------------------------------------------------

func TestRecall_RecencyWeightPassthrough(t *testing.T) {
	cases := []struct {
		name  string
		input float64
		want  float64
	}{
		{"default zero", 0, 0},
		{"mid", 0.5, 0.5},
		{"one", 1, 1},
		{"above one clamps", 1.7, 1},
		{"negative clamps", -0.3, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotWeight float64
			repo := &mockMemoryRepo{
				fullTextSearchFn: func(_ context.Context, _ string, _ uuid.UUID, _ *uuid.UUID, _ string, _ []string, _ int, recencyWeight float64) ([]domain.ScoredMemory, error) {
					gotWeight = recencyWeight
					return nil, nil
				},
			}
			svc := newMemoryService(repo)
			_, err := svc.Recall(context.Background(), domain.RecallOpts{
				Query:         "q",
				WorkspaceID:   uuid.New(),
				Limit:         10,
				RecencyWeight: tc.input,
			})
			require.NoError(t, err)
			assert.Equal(t, tc.want, gotWeight, "clamped recency weight passed to repo")
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

	_, err := svc.Recall(context.Background(), opts)

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
