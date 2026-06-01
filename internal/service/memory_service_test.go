package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ---------------------------------------------------------------------------
// mockMemoryRepo
// ---------------------------------------------------------------------------

type mockMemoryRepo struct {
	upsertFn                 func(ctx context.Context, mem *domain.Memory) error
	getByIDFn                func(ctx context.Context, id uuid.UUID) (*domain.Memory, error)
	getByKeyFn               func(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID, agentID *uuid.UUID, key string, scope domain.MemoryScope) (*domain.Memory, error)
	fullTextSearchFn         func(ctx context.Context, query string, wsID uuid.UUID, projID *uuid.UUID, scope string, tags []string, limit int) ([]domain.ScoredMemory, error)
	findByScopeFn            func(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID, scope string, limit int) ([]domain.Memory, error)
	listByWorkspaceProjectFn func(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID) ([]domain.Memory, error)
	deleteFn                 func(ctx context.Context, id uuid.UUID) error
	boostRelevanceFn         func(ctx context.Context, ids []uuid.UUID) error
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

func (m *mockMemoryRepo) FullTextSearch(ctx context.Context, query string, wsID uuid.UUID, projID *uuid.UUID, scope string, tags []string, limit int) ([]domain.ScoredMemory, error) {
	if m.fullTextSearchFn != nil {
		return m.fullTextSearchFn(ctx, query, wsID, projID, scope, tags, limit)
	}
	return nil, nil
}

func (m *mockMemoryRepo) FindByScope(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID, scope string, limit int) ([]domain.Memory, error) {
	if m.findByScopeFn != nil {
		return m.findByScopeFn(ctx, wsID, projID, scope, limit)
	}
	return nil, nil
}

func (m *mockMemoryRepo) ListByWorkspaceProject(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID) ([]domain.Memory, error) {
	if m.listByWorkspaceProjectFn != nil {
		return m.listByWorkspaceProjectFn(ctx, wsID, projID)
	}
	return nil, nil
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

// ---------------------------------------------------------------------------
// Missing mockMemoryRepo stubs (FindByShortID, SetArchived)
// ---------------------------------------------------------------------------

func (m *mockMemoryRepo) FindByShortID(ctx context.Context, wsID uuid.UUID, prefix string) (*domain.Memory, error) {
	return nil, nil
}

func (m *mockMemoryRepo) SetArchived(ctx context.Context, id uuid.UUID, archived bool) error {
	return nil
}

// Verify mockMemoryRepo satisfies the interface at compile time.
var _ repository.MemoryRepository = (*mockMemoryRepo)(nil)

// ---------------------------------------------------------------------------
// mockMemoryEdgeRepo
// ---------------------------------------------------------------------------

type mockMemoryEdgeRepo struct {
	upsertEdgeFn          func(edge *domain.MemoryEdge) error
	getEdgesByMemoryIDFn  func(memID uuid.UUID) ([]domain.MemoryEdge, error)
	decayWeightsFn        func() (int64, error)
	pruneDeadEdgesFn      func() (int64, error)
	reinforceEdgeFn       func(edgeID uuid.UUID) error
}

func (m *mockMemoryEdgeRepo) UpsertEdge(_ context.Context, edge *domain.MemoryEdge) error {
	if m.upsertEdgeFn != nil {
		return m.upsertEdgeFn(edge)
	}
	return nil
}

func (m *mockMemoryEdgeRepo) GetEdgesByMemoryID(_ context.Context, memID, _ uuid.UUID) ([]domain.MemoryEdge, error) {
	if m.getEdgesByMemoryIDFn != nil {
		return m.getEdgesByMemoryIDFn(memID)
	}
	return nil, nil
}

func (m *mockMemoryEdgeRepo) DecayWeights(_ context.Context) (int64, error) {
	if m.decayWeightsFn != nil {
		return m.decayWeightsFn()
	}
	return 0, nil
}

func (m *mockMemoryEdgeRepo) PruneDeadEdges(_ context.Context) (int64, error) {
	if m.pruneDeadEdgesFn != nil {
		return m.pruneDeadEdgesFn()
	}
	return 0, nil
}

func (m *mockMemoryEdgeRepo) ReinforceEdge(_ context.Context, edgeID uuid.UUID) error {
	if m.reinforceEdgeFn != nil {
		return m.reinforceEdgeFn(edgeID)
	}
	return nil
}

// Verify mockMemoryEdgeRepo satisfies the interface at compile time.
var _ repository.MemoryEdgeRepository = (*mockMemoryEdgeRepo)(nil)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newMemoryService(repo *mockMemoryRepo) MemoryService {
	return NewMemoryService(repo, nil, nil) // nil edgeRepo + nil embedder → NoopEmbedder (keyword-only)
}

func newMemoryServiceWithEdges(repo repository.MemoryRepository, edgeRepo *mockMemoryEdgeRepo) MemoryService {
	return NewMemoryService(repo, edgeRepo, nil)
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

	outcome, err := svc.Remember(context.Background(), mem)

	require.NoError(t, err)
	assert.Equal(t, "created", outcome)
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

	outcome, err := svc.Remember(context.Background(), mem)

	require.NoError(t, err)
	assert.Equal(t, "updated", outcome)
	// The service must copy the existing ID onto mem so the DB upsert targets the correct row.
	assert.Equal(t, existingID, mem.ID)
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
		fullTextSearchFn: func(_ context.Context, _ string, _ uuid.UUID, _ *uuid.UUID, _ string, _ []string, _ int) ([]domain.ScoredMemory, error) {
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
		listByWorkspaceProjectFn: func(_ context.Context, gotWsID uuid.UUID, gotProjID *uuid.UUID) ([]domain.Memory, error) {
			assert.Equal(t, wsID, gotWsID)
			require.NotNil(t, gotProjID)
			assert.Equal(t, projID, *gotProjID)
			return stored, nil
		},
	}

	svc := newMemoryService(repo)

	memories, err := svc.GetProjectKnowledge(context.Background(), wsID, &projID)

	require.NoError(t, err)
	assert.Len(t, memories, 2)
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
// TestRemember_Hook1_RelatesTo — tag overlap ≥60% creates relates_to edge
// ---------------------------------------------------------------------------

func TestRemember_Hook1_RelatesTo(t *testing.T) {
	wsID := uuid.New()
	existingID := uuid.New()

	// Existing memory in same scope with overlapping tags.
	existing := domain.Memory{
		ID:          existingID,
		WorkspaceID: wsID,
		Key:         "existing-memory",
		Content:     "some existing content",
		Scope:       domain.ScopeProject,
		Tags:        []string{"kind:decision", "team:mesh", "project:evc"},
	}

	var capturedEdge *domain.MemoryEdge

	memRepo := &mockMemoryRepo{
		findByScopeFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.Memory, error) {
			return []domain.Memory{existing}, nil
		},
	}
	edgeRepo := &mockMemoryEdgeRepo{
		upsertEdgeFn: func(edge *domain.MemoryEdge) error {
			capturedEdge = edge
			return nil
		},
	}

	svc := newMemoryServiceWithEdges(memRepo, edgeRepo)

	newMemID := uuid.New()
	mem := &domain.Memory{
		ID:          newMemID,
		WorkspaceID: wsID,
		Key:         "new-memory",
		Content:     "new content",
		Scope:       domain.ScopeProject,
		// 3 of 3 tags match existing → 100% overlap ≥ 60%
		Tags: []string{"kind:decision", "team:mesh", "project:evc"},
	}

	_, err := svc.Remember(context.Background(), mem)
	require.NoError(t, err)

	require.NotNil(t, capturedEdge, "relates_to edge should have been created")
	assert.Equal(t, domain.EdgeRelatesTo, capturedEdge.RelationshipType)
	assert.Equal(t, newMemID, capturedEdge.MemoryFromID)
	assert.Equal(t, existingID, capturedEdge.MemoryToID)
	assert.InDelta(t, 0.5, capturedEdge.Weight, 0.001)
}

// ---------------------------------------------------------------------------
// TestRemember_Hook1_NoEdgeBelowThreshold — <60% overlap → no edge
// ---------------------------------------------------------------------------

func TestRemember_Hook1_NoEdgeBelowThreshold(t *testing.T) {
	wsID := uuid.New()
	existingID := uuid.New()

	// Existing memory with very different tags — only 1 of 4 will match.
	existing := domain.Memory{
		ID:          existingID,
		WorkspaceID: wsID,
		Key:         "unrelated",
		Content:     "content",
		Scope:       domain.ScopeProject,
		Tags:        []string{"kind:decision", "other-tag-1", "other-tag-2", "other-tag-3"},
	}

	edgeCalled := false
	memRepo := &mockMemoryRepo{
		findByScopeFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.Memory, error) {
			return []domain.Memory{existing}, nil
		},
	}
	edgeRepo := &mockMemoryEdgeRepo{
		upsertEdgeFn: func(edge *domain.MemoryEdge) error {
			edgeCalled = true
			return nil
		},
	}

	svc := newMemoryServiceWithEdges(memRepo, edgeRepo)

	mem := &domain.Memory{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		Key:         "different-memory",
		Content:     "completely different",
		Scope:       domain.ScopeProject,
		// Only 1 of 5 new tags is in existing → 20% overlap, below 60%
		Tags: []string{"kind:decision", "new-tag-a", "new-tag-b", "new-tag-c", "new-tag-d"},
	}

	_, err := svc.Remember(context.Background(), mem)
	require.NoError(t, err)
	assert.False(t, edgeCalled, "no edge should be created when overlap < 60%")
}

// ---------------------------------------------------------------------------
// TestRemember_Hook3_DerivedFrom — kind:incident + short-id in content
// ---------------------------------------------------------------------------

func TestRemember_Hook3_DerivedFrom(t *testing.T) {
	wsID := uuid.New()
	referencedID := uuid.New()
	// Use the first 8 hex chars of referencedID as the short-id in content.
	shortID := referencedID.String()[:8] // always lowercase hex

	referencedMem := &domain.Memory{
		ID:          referencedID,
		WorkspaceID: wsID,
		Key:         "original-incident",
		Content:     "original content",
		Scope:       domain.ScopeWorkspace,
	}

	var capturedEdge *domain.MemoryEdge

	memRepo := &mockMemoryRepo{
		findByScopeFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.Memory, error) {
			return nil, nil // Hook 1 finds nothing
		},
	}
	// Override FindByShortID with a real implementation for this test.
	memRepoWithShortID := &mockMemoryRepoWithShortID{
		mockMemoryRepo: memRepo,
		findByShortIDFn: func(_ context.Context, _ uuid.UUID, prefix string) (*domain.Memory, error) {
			if prefix == shortID {
				return referencedMem, nil
			}
			return nil, nil
		},
	}

	edgeRepo := &mockMemoryEdgeRepo{
		upsertEdgeFn: func(edge *domain.MemoryEdge) error {
			capturedEdge = edge
			return nil
		},
	}

	svc := newMemoryServiceWithEdges(memRepoWithShortID, edgeRepo)

	newMemID := uuid.New()
	mem := &domain.Memory{
		ID:          newMemID,
		WorkspaceID: wsID,
		Key:         "follow-up-incident",
		Content:     "this follows from " + shortID + " which was the root cause",
		Scope:       domain.ScopeWorkspace,
		Tags:        []string{"kind:incident"},
	}

	_, err := svc.Remember(context.Background(), mem)
	require.NoError(t, err)

	require.NotNil(t, capturedEdge, "derived_from edge should have been created")
	assert.Equal(t, domain.EdgeDerivedFrom, capturedEdge.RelationshipType)
	assert.Equal(t, newMemID, capturedEdge.MemoryFromID)
	assert.Equal(t, referencedID, capturedEdge.MemoryToID)
	assert.InDelta(t, 1.0, capturedEdge.Weight, 0.001)
}

// ---------------------------------------------------------------------------
// TestSupersede_CreatesEdgeAndArchives — svc.Supersede creates edge + archives old
// ---------------------------------------------------------------------------

func TestSupersede_CreatesEdgeAndArchives(t *testing.T) {
	wsID := uuid.New()
	oldID := uuid.New()
	newID := uuid.New()

	oldMem := &domain.Memory{
		ID:          oldID,
		WorkspaceID: wsID,
		Key:         "old-decision",
		Content:     "outdated decision",
		Scope:       domain.ScopeWorkspace,
	}
	newMem := &domain.Memory{
		ID:          newID,
		WorkspaceID: wsID,
		Key:         "new-decision",
		Content:     "updated decision",
		Scope:       domain.ScopeWorkspace,
	}

	var capturedEdge *domain.MemoryEdge
	archivedID := uuid.Nil

	memRepo := &mockMemoryRepo{
		getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
			if id == oldID {
				return oldMem, nil
			}
			if id == newID {
				return newMem, nil
			}
			return nil, nil
		},
	}
	// Override SetArchived.
	memRepoWithArchive := &mockMemoryRepoWithArchive{
		mockMemoryRepo: memRepo,
		setArchivedFn: func(_ context.Context, id uuid.UUID, archived bool) error {
			archivedID = id
			return nil
		},
	}
	edgeRepo := &mockMemoryEdgeRepo{
		upsertEdgeFn: func(edge *domain.MemoryEdge) error {
			capturedEdge = edge
			return nil
		},
	}

	svc := newMemoryServiceWithEdges(memRepoWithArchive, edgeRepo)

	err := svc.Supersede(context.Background(), oldID, newID)
	require.NoError(t, err)

	require.NotNil(t, capturedEdge, "supersedes edge should have been created")
	assert.Equal(t, domain.EdgeSupersedes, capturedEdge.RelationshipType)
	assert.Equal(t, newID, capturedEdge.MemoryFromID)
	assert.Equal(t, oldID, capturedEdge.MemoryToID)

	assert.Equal(t, oldID, archivedID, "old memory should be archived")
}

// ---------------------------------------------------------------------------
// TestSupersede_NotFoundOld — old memory not found → 404
// ---------------------------------------------------------------------------

func TestSupersede_NotFoundOld(t *testing.T) {
	wsID := uuid.New()
	_ = wsID

	memRepo := &mockMemoryRepo{
		getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
			return nil, nil // not found
		},
	}
	edgeRepo := &mockMemoryEdgeRepo{}
	svc := newMemoryServiceWithEdges(memRepo, edgeRepo)

	err := svc.Supersede(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.Code)
}

// ---------------------------------------------------------------------------
// Helper mock variants that override FindByShortID / SetArchived
// ---------------------------------------------------------------------------

// mockMemoryRepoWithShortID wraps mockMemoryRepo and overrides FindByShortID.
type mockMemoryRepoWithShortID struct {
	*mockMemoryRepo
	findByShortIDFn func(ctx context.Context, wsID uuid.UUID, prefix string) (*domain.Memory, error)
}

func (m *mockMemoryRepoWithShortID) FindByShortID(ctx context.Context, wsID uuid.UUID, prefix string) (*domain.Memory, error) {
	if m.findByShortIDFn != nil {
		return m.findByShortIDFn(ctx, wsID, prefix)
	}
	return nil, nil
}

var _ repository.MemoryRepository = (*mockMemoryRepoWithShortID)(nil)

// mockMemoryRepoWithArchive wraps mockMemoryRepo and overrides SetArchived.
type mockMemoryRepoWithArchive struct {
	*mockMemoryRepo
	setArchivedFn func(ctx context.Context, id uuid.UUID, archived bool) error
}

func (m *mockMemoryRepoWithArchive) SetArchived(ctx context.Context, id uuid.UUID, archived bool) error {
	if m.setArchivedFn != nil {
		return m.setArchivedFn(ctx, id, archived)
	}
	return nil
}

var _ repository.MemoryRepository = (*mockMemoryRepoWithArchive)(nil)
