package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ---------------------------------------------------------------------------
// Recall eligibility filters reach BOTH arms (task #2c087b2a)
//
// The defect these cover: scope and tags were applied to the vector arm only, and
// tags_any to neither — it ran as a post-filter over whatever the two arms had
// already truncated to. Three consequences, one per test group below:
//
//  1. scope was unenforced on every row the BM25 arm contributed, and — since the
//     BM25 arm is the ONLY arm in bm25-only mode (the fail-open path taken whenever
//     the embedder is down) — unenforced entirely in that mode.
//  2. the candidate pool was "the first poolSize rows of the workspace", so an
//     eligible row ranked below that cut was gone before the post-filter ran and
//     could not be recovered. The effective haystack shrinks as unrelated memories
//     are written.
//  3. pinned injection and graph expansion bypassed the filter through side doors.
//
// Every test here fails against the pre-fix code.
// ---------------------------------------------------------------------------

// recallFilterProbe captures the filter each arm was called with.
type recallFilterProbe struct {
	bm25Filter   domain.MemorySearchFilter
	vectorFilter domain.MemorySearchFilter
	bm25Called   bool
	vectorCalled bool
}

func memWithScope(wsID uuid.UUID, scope domain.MemoryScope, tags ...string) domain.Memory {
	return domain.Memory{
		ID:              uuid.New(),
		WorkspaceID:     wsID,
		Content:         "probe content",
		Key:             "probe",
		Scope:           scope,
		Tags:            tags,
		Status:          domain.MemoryStatusActive,
		FreshnessScore:  1.0,
		ImportanceScore: 0.8,
	}
}

// TestRecall_ScopeAndTagsAny_ReachBothArms is the primary assertion: the eligibility
// predicates are handed to BOTH retrieval arms as query parameters, so each arm's
// candidate pool is drawn from the eligible set rather than filtered afterwards.
//
// Pre-fix this failed twice over: the BM25 arm took no filter argument at all, and
// TagsAny was passed to neither arm.
func TestRecall_ScopeAndTagsAny_ReachBothArms(t *testing.T) {
	wsID := uuid.New()
	probe := &recallFilterProbe{}

	repo := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, filter domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
			probe.bm25Called = true
			probe.bm25Filter = filter
			return nil, nil
		},
		vectorSearchFn: func(_ context.Context, _ []float32, _ uuid.UUID, _ *uuid.UUID, filter domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
			probe.vectorCalled = true
			probe.vectorFilter = filter
			return nil, nil
		},
	}

	svc := NewMemoryService(repo, nil, &stubEmbedder{vec: []float32{0.1, 0.2, 0.3}})

	_, _, err := svc.Recall(context.Background(), domain.RecallOpts{
		Query:       "canonical decision",
		WorkspaceID: wsID,
		Scope:       domain.ScopeAgent,
		Tags:        []string{"kind:decision"},
		TagsAny:     []string{"pavel-decision", "canonical"},
		Limit:       10,
	})
	require.NoError(t, err)

	require.True(t, probe.bm25Called, "BM25 arm must run")
	require.True(t, probe.vectorCalled, "vector arm must run")

	want := domain.MemorySearchFilter{
		Scope:   string(domain.ScopeAgent),
		Tags:    []string{"kind:decision"},
		TagsAny: []string{"pavel-decision", "canonical"},
	}

	assert.Equal(t, want, probe.bm25Filter,
		"scope/tags/tags_any must reach the BM25 arm as query predicates, not a post-filter")
	assert.Equal(t, want, probe.vectorFilter,
		"scope/tags/tags_any must reach the vector arm as query predicates")
}

// TestRecall_ScopeEnforced_BM25Only is the mode that mattered most: with no embedder the
// vector arm never runs, so BM25 is the only arm and — pre-fix — the only arm that was
// never given scope. That made scope wholly unenforced on the fail-open path.
//
// The mock arm deliberately returns out-of-scope rows (simulating the pre-fix SQL, which
// had no scope predicate). No row with a different scope may reach the caller.
func TestRecall_ScopeEnforced_BM25Only(t *testing.T) {
	wsID := uuid.New()

	agentRow := memWithScope(wsID, domain.ScopeAgent)
	workspaceRow := memWithScope(wsID, domain.ScopeWorkspace)
	projectRow := memWithScope(wsID, domain.ScopeProject)

	var gotFilter domain.MemorySearchFilter
	repo := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, filter domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
			gotFilter = filter
			return []domain.ScoredMemory{
				{Memory: workspaceRow, Score: 0.9}, // ineligible, ranked highest
				{Memory: agentRow, Score: 0.5},
				{Memory: projectRow, Score: 0.3}, // ineligible
			}, nil
		},
	}

	// nil embedder => noop => bm25-only. This is the shape of a live embedder outage.
	svc := NewMemoryService(repo, nil, nil)

	results, mode, err := svc.Recall(context.Background(), domain.RecallOpts{
		Query:       "commute",
		WorkspaceID: wsID,
		Scope:       domain.ScopeAgent,
		Limit:       10,
	})
	require.NoError(t, err)
	require.Equal(t, domain.SearchModeBM25Only, mode, "no embedder must degrade to bm25-only")

	assert.Equal(t, string(domain.ScopeAgent), gotFilter.Scope,
		"the BM25 arm must receive scope even when it is the only arm")

	for _, r := range results {
		assert.Equal(t, domain.ScopeAgent, r.Scope,
			"scope=agent must never return a %s-scoped row (memory %s)", r.Scope, r.ID)
	}
	require.Len(t, results, 1)
	assert.Equal(t, agentRow.ID, results[0].ID)
}

// TestRecall_ScopeEnforced_HybridBM25Arm covers the hybrid case: even with the vector
// arm healthy and correctly filtered, rows entering through the BM25 arm were unfiltered,
// so a scope leak arrived via fusion.
func TestRecall_ScopeEnforced_HybridBM25Arm(t *testing.T) {
	wsID := uuid.New()
	agentRow := memWithScope(wsID, domain.ScopeAgent)
	leakedRow := memWithScope(wsID, domain.ScopeWorkspace)

	repo := &mockMemoryRepo{
		// Simulates the pre-fix BM25 SQL: no scope predicate, so it leaks.
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{
				{Memory: leakedRow, Score: 0.95},
				{Memory: agentRow, Score: 0.4},
			}, nil
		},
		vectorSearchFn: func(_ context.Context, _ []float32, _ uuid.UUID, _ *uuid.UUID, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{{Memory: agentRow, Score: 0.8}}, nil
		},
	}

	svc := NewMemoryService(repo, nil, &stubEmbedder{vec: []float32{1, 0}})

	results, mode, err := svc.Recall(context.Background(), domain.RecallOpts{
		Query:       "study abroad",
		WorkspaceID: wsID,
		Scope:       domain.ScopeAgent,
		Limit:       10,
	})
	require.NoError(t, err)
	require.Equal(t, domain.SearchModeHybrid, mode)

	for _, r := range results {
		assert.Equal(t, domain.ScopeAgent, r.Scope,
			"a %s-scoped row reached the client through the BM25 arm", r.Scope)
	}
}

// TestRecall_TagsAny_EnforcedOnResults pins the OR-filter semantics end to end. Pre-fix
// TagsAny reached neither arm; it was applied only after both had truncated.
func TestRecall_TagsAny_EnforcedOnResults(t *testing.T) {
	wsID := uuid.New()
	wanted := memWithScope(wsID, domain.ScopeWorkspace, "pavel-decision")
	unwanted := memWithScope(wsID, domain.ScopeWorkspace, "kind:session-checkpoint")

	var gotFilter domain.MemorySearchFilter
	repo := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, filter domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
			gotFilter = filter
			return []domain.ScoredMemory{
				{Memory: unwanted, Score: 0.9},
				{Memory: wanted, Score: 0.5},
			}, nil
		},
	}
	svc := NewMemoryService(repo, nil, nil)

	results, _, err := svc.Recall(context.Background(), domain.RecallOpts{
		Query:       "what did we decide",
		WorkspaceID: wsID,
		TagsAny:     []string{"pavel-decision"},
		Limit:       10,
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"pavel-decision"}, gotFilter.TagsAny,
		"tags_any must reach the arm as a query predicate — this is the C4 canonical-decision read path")
	require.Len(t, results, 1)
	assert.Equal(t, wanted.ID, results[0].ID)
}

// TestRecall_Tags_AreAndSemantics guards a semantics bug found alongside the main defect:
// the vector arm built `tags && …` (overlap = OR) for opts.Tags, which is documented and
// implemented everywhere else as AND (`tags @> …`). A row carrying only one of two
// required tags must not match.
func TestRecall_Tags_AreAndSemantics(t *testing.T) {
	wsID := uuid.New()
	both := memWithScope(wsID, domain.ScopeWorkspace, "kind:decision", "project:mesh")
	onlyOne := memWithScope(wsID, domain.ScopeWorkspace, "kind:decision")

	repo := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{
				{Memory: onlyOne, Score: 0.9},
				{Memory: both, Score: 0.5},
			}, nil
		},
	}
	svc := NewMemoryService(repo, nil, nil)

	results, _, err := svc.Recall(context.Background(), domain.RecallOpts{
		Query:       "decision",
		WorkspaceID: wsID,
		Tags:        []string{"kind:decision", "project:mesh"},
		Limit:       10,
	})
	require.NoError(t, err)
	require.Len(t, results, 1, "Tags is an AND filter: a row missing one required tag must not match")
	assert.Equal(t, both.ID, results[0].ID)
}

// TestRecall_PinnedMemories_RespectScope closes the pinned side door. Pinned rows bypass
// ranking and importance thresholds by design — they must not also bypass scope, or the
// one exempt path reintroduces the leak.
func TestRecall_PinnedMemories_RespectScope(t *testing.T) {
	wsID := uuid.New()
	agentPinned := memWithScope(wsID, domain.ScopeAgent, domain.KindPinned)
	workspacePinned := memWithScope(wsID, domain.ScopeWorkspace, domain.KindPinned)

	repo := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
			return nil, nil
		},
		findPinnedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID) ([]domain.Memory, error) {
			return []domain.Memory{agentPinned, workspacePinned}, nil
		},
	}
	svc := NewMemoryService(repo, nil, nil)

	results, _, err := svc.Recall(context.Background(), domain.RecallOpts{
		Query:       "anything",
		WorkspaceID: wsID,
		Scope:       domain.ScopeAgent,
		Limit:       10,
	})
	require.NoError(t, err)

	for _, r := range results {
		assert.Equal(t, domain.ScopeAgent, r.Scope,
			"a pinned %s-scoped row was injected into a scope=agent recall", r.Scope)
	}
	require.Len(t, results, 1)
	assert.Equal(t, agentPinned.ID, results[0].ID)
}

// TestRecall_BM25ArmDropped_StillFiltersVectorRows verifies the mirror-image degradation:
// when the BM25 arm errors, recall serves vector-only — and the filter must still hold.
func TestRecall_BM25ArmDropped_StillFiltersVectorRows(t *testing.T) {
	wsID := uuid.New()
	agentRow := memWithScope(wsID, domain.ScopeAgent)
	leaked := memWithScope(wsID, domain.ScopeProject)

	repo := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
			return nil, errors.New("fts timeout")
		},
		vectorSearchFn: func(_ context.Context, _ []float32, _ uuid.UUID, _ *uuid.UUID, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{
				{Memory: leaked, Score: 0.9},
				{Memory: agentRow, Score: 0.6},
			}, nil
		},
	}
	svc := NewMemoryService(repo, nil, &stubEmbedder{vec: []float32{1, 0}})

	results, _, err := svc.Recall(context.Background(), domain.RecallOpts{
		Query:       "probe",
		WorkspaceID: wsID,
		Scope:       domain.ScopeAgent,
		Limit:       10,
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, agentRow.ID, results[0].ID)
}

// TestRecallGraph_ExpandedNeighboursRespectScope closes the last side door: memory_edges
// carry no scope, so BFS from an in-scope seed can reach an out-of-scope memory. Pre-fix
// RecallGraph had no scope parameter at all, so every neighbour was returned regardless.
func TestRecallGraph_ExpandedNeighboursRespectScope(t *testing.T) {
	wsID := uuid.New()
	seed := memWithScope(wsID, domain.ScopeAgent)
	inScopeNeighbour := memWithScope(wsID, domain.ScopeAgent)
	outOfScopeNeighbour := memWithScope(wsID, domain.ScopeWorkspace)

	byID := map[uuid.UUID]domain.Memory{
		seed.ID:                seed,
		inScopeNeighbour.ID:    inScopeNeighbour,
		outOfScopeNeighbour.ID: outOfScopeNeighbour,
	}

	repo := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{{Memory: seed, Score: 0.9}}, nil
		},
		getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
			if m, ok := byID[id]; ok {
				return &m, nil
			}
			return nil, nil
		},
	}

	edges := &mockMemoryEdgeRepo{
		neighbors: map[uuid.UUID][]domain.MemoryEdge{
			seed.ID: {
				{MemoryFromID: seed.ID, MemoryToID: inScopeNeighbour.ID, Weight: 0.9},
				{MemoryFromID: seed.ID, MemoryToID: outOfScopeNeighbour.ID, Weight: 0.9},
			},
		},
	}

	svc := NewMemoryService(repo, edges, nil)

	results, err := svc.RecallGraph(context.Background(), domain.RecallGraphOpts{
		Query:           "graph probe",
		WorkspaceID:     wsID,
		Hops:            1,
		WeightThreshold: 0.1,
		Scope:           string(domain.ScopeAgent),
	})
	require.NoError(t, err)

	for _, r := range results {
		assert.NotEqual(t, outOfScopeNeighbour.ID, r.ID,
			"a workspace-scoped neighbour was reached by BFS from an agent-scoped seed")
	}
}
