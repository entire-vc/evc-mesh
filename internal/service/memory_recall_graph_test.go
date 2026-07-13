package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// clearRecallGraphCache drains the package-level cache so tests start clean.
func clearRecallGraphCache() {
	recallGraphCache.Range(func(k, v interface{}) bool {
		recallGraphCache.Delete(k)
		return true
	})
}

// ---------------------------------------------------------------------------
// testEdgeRepo — local mock with configurable GetNeighbors
// ---------------------------------------------------------------------------

// testEdgeRepo is a test-local MemoryEdgeRepository mock that allows each
// test to inject custom GetNeighbors behaviour via getNeighborsFn.
type testEdgeRepo struct {
	getNeighborsFn func(ctx context.Context, ids []uuid.UUID, weightThreshold float64, limit int) ([]domain.MemoryEdge, error)
	// gotWorkspaceIDs records the workspace passed to each GetNeighbors call, so a
	// test can assert BFS confines its edge lookups to the caller's tenant.
	gotWorkspaceIDs []uuid.UUID
}

func (r *testEdgeRepo) UpsertEdge(_ context.Context, _ *domain.MemoryEdge) error { return nil }
func (r *testEdgeRepo) ReinforceEdge(_ context.Context, _, _ uuid.UUID, _ domain.MemoryEdgeRelationshipType) error {
	return nil
}
func (r *testEdgeRepo) GetNeighbors(ctx context.Context, ids []uuid.UUID, workspaceID uuid.UUID, weightThreshold float64, limit int) ([]domain.MemoryEdge, error) {
	r.gotWorkspaceIDs = append(r.gotWorkspaceIDs, workspaceID)
	if r.getNeighborsFn != nil {
		return r.getNeighborsFn(ctx, ids, weightThreshold, limit)
	}
	return nil, nil
}
func (r *testEdgeRepo) DecayWeights(_ context.Context) (int64, error)   { return 0, nil }
func (r *testEdgeRepo) PruneDeadEdges(_ context.Context) (int64, error) { return 0, nil }

// Compile-time interface check.
var _ repository.MemoryEdgeRepository = (*testEdgeRepo)(nil)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newGraphService wires up a MemoryService backed by the given mem+edge repos.
func newGraphService(mem *mockMemoryRepo, edge *testEdgeRepo) MemoryService {
	return NewMemoryService(mem, edge, nil)
}

// graphSeed builds a ScoredMemory suitable for use as a RecallGraph seed.
// importance must be >= defaultMinImportance (0.4) to survive the Recall filter.
func graphSeed(id uuid.UUID, importance float32) domain.ScoredMemory {
	return domain.ScoredMemory{
		Memory: domain.Memory{
			ID:              id,
			Content:         "content-" + id.String()[:8],
			ImportanceScore: importance,
			Scope:           domain.ScopeWorkspace, // workspace scope bypasses temporal decay
		},
		Score: 0.9, // raw score; will be re-scored by RRF inside Recall
	}
}

// graphNeighbor builds a Memory for GetByID returns.
//
// wsID is required: memories.workspace_id is NOT NULL, so a memory with no
// workspace is a row that cannot exist. BFS now drops any neighbor outside the
// caller's workspace, and a zero-workspace fixture would be silently dropped —
// making these tests pass for the wrong reason.
func graphNeighbor(wsID, id uuid.UUID, importance float32) *domain.Memory {
	return &domain.Memory{
		ID:              id,
		WorkspaceID:     wsID,
		Content:         "neighbor-" + id.String()[:8],
		ImportanceScore: importance,
		Scope:           domain.ScopeWorkspace,
	}
}

// graphEdge builds a directed MemoryEdge between from→to with the given weight.
func graphEdge(from, to uuid.UUID, weight float32) domain.MemoryEdge {
	return domain.MemoryEdge{
		ID:               uuid.New(),
		MemoryFromID:     from,
		MemoryToID:       to,
		RelationshipType: domain.EdgeRelatesTo,
		Weight:           weight,
	}
}

// freshOpts returns RecallGraphOpts with a fresh WorkspaceID to avoid cache collisions.
func freshOpts(query string) domain.RecallGraphOpts {
	return domain.RecallGraphOpts{
		Query:       query,
		WorkspaceID: uuid.New(),
		Hops:        1,
	}
}

// ---------------------------------------------------------------------------
// 1. TestRecallGraph_EmptyQuery
// ---------------------------------------------------------------------------

func TestRecallGraph_EmptyQuery(t *testing.T) {
	clearRecallGraphCache()
	svc := newGraphService(&mockMemoryRepo{}, &testEdgeRepo{})
	_, err := svc.RecallGraph(context.Background(), domain.RecallGraphOpts{
		WorkspaceID: uuid.New(),
	})
	require.Error(t, err)

	var apiErr *apierror.Error
	require.True(t, errors.As(err, &apiErr), "expected *apierror.Error, got %T", err)
	assert.Equal(t, http.StatusBadRequest, apiErr.Code)
	assert.NotEmpty(t, apiErr.Validation["query"])
}

// ---------------------------------------------------------------------------
// 2. TestRecallGraph_NoSeeds_ReturnsNil
// ---------------------------------------------------------------------------

func TestRecallGraph_NoSeeds_ReturnsNil(t *testing.T) {
	clearRecallGraphCache()
	mem := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.ScoredMemory, error) {
			return nil, nil
		},
	}
	svc := newGraphService(mem, &testEdgeRepo{})
	results, err := svc.RecallGraph(context.Background(), freshOpts("no seeds here"))
	require.NoError(t, err)
	assert.Nil(t, results)
}

// ---------------------------------------------------------------------------
// 3. TestRecallGraph_SeedsOnly_NoEdges
// ---------------------------------------------------------------------------

func TestRecallGraph_SeedsOnly_NoEdges(t *testing.T) {
	clearRecallGraphCache()
	id1, id2 := uuid.New(), uuid.New()
	seeds := []domain.ScoredMemory{
		graphSeed(id1, 0.7),
		graphSeed(id2, 0.5),
	}
	mem := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.ScoredMemory, error) {
			return seeds, nil
		},
	}
	svc := newGraphService(mem, &testEdgeRepo{})

	results, err := svc.RecallGraph(context.Background(), freshOpts("seeds only"))
	require.NoError(t, err)
	require.Len(t, results, 2, "both seeds should appear")

	ids := []uuid.UUID{results[0].ID, results[1].ID}
	assert.Contains(t, ids, id1)
	assert.Contains(t, ids, id2)

	for _, r := range results {
		assert.Equal(t, domain.ProvenanceRecall, r.Provenance, "seeds must be via:recall")
		assert.Equal(t, 0, r.HopDistance, "seeds must have hop_distance=0")
	}
}

// ---------------------------------------------------------------------------
// 4. TestRecallGraph_Provenance
// ---------------------------------------------------------------------------

func TestRecallGraph_Provenance(t *testing.T) {
	clearRecallGraphCache()
	opts := freshOpts("provenance")
	seedID := uuid.New()
	neighborID := uuid.New()

	mem := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{graphSeed(seedID, 0.6)}, nil
		},
		getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
			if id == neighborID {
				return graphNeighbor(opts.WorkspaceID, neighborID, 0.5), nil
			}
			return nil, nil
		},
	}
	er := &testEdgeRepo{
		getNeighborsFn: func(_ context.Context, _ []uuid.UUID, _ float64, _ int) ([]domain.MemoryEdge, error) {
			return []domain.MemoryEdge{graphEdge(seedID, neighborID, 0.8)}, nil
		},
	}
	svc := newGraphService(mem, er)

	results, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)
	require.Len(t, results, 2)

	provMap := map[uuid.UUID]domain.RecallGraphProvenance{}
	for _, r := range results {
		provMap[r.ID] = r.Provenance
	}
	assert.Equal(t, domain.ProvenanceRecall, provMap[seedID], "seed must be via:recall")
	assert.Equal(t, domain.ProvenanceGraph, provMap[neighborID], "expanded node must be via:graph")
}

// ---------------------------------------------------------------------------
// 5. TestRecallGraph_CompositeScore_1Hop
// ---------------------------------------------------------------------------

// TestRecallGraph_CompositeScore_1Hop verifies the composite score formula:
// neighbor_composite = parent_composite × edge_weight.
// Because RecallGraph seeds go through Recall() / RRF (which re-scores), we
// verify the ratio rather than an absolute value.
func TestRecallGraph_CompositeScore_1Hop(t *testing.T) {
	clearRecallGraphCache()
	opts := freshOpts("composite score 1hop")
	seedID := uuid.New()
	neighborID := uuid.New()

	const edgeWeight = float32(0.5)

	mem := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{graphSeed(seedID, 0.6)}, nil
		},
		getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
			if id == neighborID {
				return graphNeighbor(opts.WorkspaceID, neighborID, 0.5), nil
			}
			return nil, nil
		},
	}
	er := &testEdgeRepo{
		getNeighborsFn: func(_ context.Context, _ []uuid.UUID, _ float64, _ int) ([]domain.MemoryEdge, error) {
			return []domain.MemoryEdge{graphEdge(seedID, neighborID, edgeWeight)}, nil
		},
	}
	svc := newGraphService(mem, er)

	results, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)
	require.Len(t, results, 2)

	scoreMap := map[uuid.UUID]float64{}
	for _, r := range results {
		scoreMap[r.ID] = r.CompositeScore
	}

	// neighbor_composite = seed_composite × edge_weight
	expectedNeighbor := scoreMap[seedID] * float64(edgeWeight)
	assert.InDelta(t, expectedNeighbor, scoreMap[neighborID], 1e-9,
		"neighbor CompositeScore must equal seed_score × edge_weight")
}

// ---------------------------------------------------------------------------
// 6. TestRecallGraph_LowImportanceSuppressed
// ---------------------------------------------------------------------------

func TestRecallGraph_LowImportanceSuppressed(t *testing.T) {
	clearRecallGraphCache()
	opts := freshOpts("low importance suppressed")
	seedID := uuid.New()
	neighborID := uuid.New()

	mem := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{graphSeed(seedID, 0.6)}, nil
		},
		getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
			if id == neighborID {
				// importance 0.3 < graphMinImportance (0.4) → must be dropped
				return graphNeighbor(opts.WorkspaceID, neighborID, 0.3), nil
			}
			return nil, nil
		},
	}
	er := &testEdgeRepo{
		getNeighborsFn: func(_ context.Context, _ []uuid.UUID, _ float64, _ int) ([]domain.MemoryEdge, error) {
			return []domain.MemoryEdge{graphEdge(seedID, neighborID, 0.9)}, nil
		},
	}
	svc := newGraphService(mem, er)

	results, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)
	require.Len(t, results, 1, "low-importance graph node must be dropped")
	assert.Equal(t, seedID, results[0].ID)
	assert.Equal(t, domain.ProvenanceRecall, results[0].Provenance)
}

// ---------------------------------------------------------------------------
// 7. TestRecallGraph_LowImportanceAllowed_ForSeeds
// ---------------------------------------------------------------------------

// TestRecallGraph_LowImportanceAllowed_ForSeeds verifies that the graphMinImportance
// filter (0.4) is NOT applied to seeds (ProvenanceRecall nodes). The Recall step has
// its own defaultMinImportance filter (also 0.4), so seeds at exactly 0.4 pass both
// checks. What matters is that seeds survive without going through GetByID + graphMinImportance.
func TestRecallGraph_LowImportanceAllowed_ForSeeds(t *testing.T) {
	clearRecallGraphCache()
	seedID := uuid.New()

	mem := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.ScoredMemory, error) {
			// importance 0.4 — at the boundary: survives Recall's defaultMinImportance filter.
			return []domain.ScoredMemory{graphSeed(seedID, 0.4)}, nil
		},
	}
	svc := newGraphService(mem, &testEdgeRepo{})

	results, err := svc.RecallGraph(context.Background(), freshOpts("seed at importance boundary"))
	require.NoError(t, err)
	require.Len(t, results, 1, "seed at importance=0.4 must be kept")
	assert.Equal(t, seedID, results[0].ID)
	assert.Equal(t, domain.ProvenanceRecall, results[0].Provenance,
		"seed must be labeled via:recall, never sent through graphMinImportance check")
}

// ---------------------------------------------------------------------------
// 8. TestRecallGraph_DeduplicateBestPath
// ---------------------------------------------------------------------------

func TestRecallGraph_DeduplicateBestPath(t *testing.T) {
	clearRecallGraphCache()
	opts := freshOpts("dedup best path")
	seed1 := uuid.New()
	seed2 := uuid.New()
	sharedNeighbor := uuid.New()

	mem := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{
				graphSeed(seed1, 0.6),
				graphSeed(seed2, 0.5),
			}, nil
		},
		getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
			if id == sharedNeighbor {
				return graphNeighbor(opts.WorkspaceID, sharedNeighbor, 0.7), nil
			}
			return nil, nil
		},
	}

	// seed1 → sharedNeighbor via weight 0.1 (low path)
	// seed2 → sharedNeighbor via weight 0.9 (better path)
	er := &testEdgeRepo{
		getNeighborsFn: func(_ context.Context, ids []uuid.UUID, _ float64, _ int) ([]domain.MemoryEdge, error) {
			edges := []domain.MemoryEdge{}
			for _, id := range ids {
				if id == seed1 {
					edges = append(edges, graphEdge(seed1, sharedNeighbor, 0.1))
				}
				if id == seed2 {
					edges = append(edges, graphEdge(seed2, sharedNeighbor, 0.9))
				}
			}
			return edges, nil
		},
	}
	svc := newGraphService(mem, er)

	results, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)

	var neighborResults []domain.RecallGraphResult
	for _, r := range results {
		if r.ID == sharedNeighbor {
			neighborResults = append(neighborResults, r)
		}
	}
	require.Len(t, neighborResults, 1, "sharedNeighbor must appear exactly once")

	// Determine each seed's composite score from results (after RRF).
	var seed2Score float64
	for _, r := range results {
		if r.ID == seed2 {
			seed2Score = r.CompositeScore
		}
	}
	expectedBest := seed2Score * 0.9 // best path: seed2 × 0.9
	assert.InDelta(t, expectedBest, neighborResults[0].CompositeScore, 1e-9,
		"composite score must reflect the best (seed2 × 0.9) path")
}

// ---------------------------------------------------------------------------
// 9. TestRecallGraph_SortedByCompositeScore
// ---------------------------------------------------------------------------

func TestRecallGraph_SortedByCompositeScore(t *testing.T) {
	clearRecallGraphCache()
	id1 := uuid.New()
	id2 := uuid.New()
	id3 := uuid.New()

	// Three seeds with varying raw scores; RRF re-ranks them but ordering by
	// RRF score descending should be stable.
	mem := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{
				graphSeed(id1, 0.5),
				graphSeed(id2, 0.5),
				graphSeed(id3, 0.5),
			}, nil
		},
	}
	svc := newGraphService(mem, &testEdgeRepo{})

	results, err := svc.RecallGraph(context.Background(), freshOpts("sorted results"))
	require.NoError(t, err)
	require.Len(t, results, 3)

	// Regardless of absolute values, results must be in non-increasing order.
	for i := 1; i < len(results); i++ {
		assert.GreaterOrEqual(t, results[i-1].CompositeScore, results[i].CompositeScore,
			"results must be sorted descending by CompositeScore (index %d vs %d)", i-1, i)
	}
}

// ---------------------------------------------------------------------------
// 10. TestRecallGraph_HopDistance
// ---------------------------------------------------------------------------

func TestRecallGraph_HopDistance(t *testing.T) {
	clearRecallGraphCache()
	opts := freshOpts("hop distance check")
	seedID := uuid.New()
	hop1ID := uuid.New()

	mem := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{graphSeed(seedID, 0.6)}, nil
		},
		getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
			if id == hop1ID {
				return graphNeighbor(opts.WorkspaceID, hop1ID, 0.5), nil
			}
			return nil, nil
		},
	}
	er := &testEdgeRepo{
		getNeighborsFn: func(_ context.Context, ids []uuid.UUID, _ float64, _ int) ([]domain.MemoryEdge, error) {
			for _, id := range ids {
				if id == seedID {
					return []domain.MemoryEdge{graphEdge(seedID, hop1ID, 0.7)}, nil
				}
			}
			return nil, nil
		},
	}
	svc := newGraphService(mem, er)

	results, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)

	distMap := map[uuid.UUID]int{}
	for _, r := range results {
		distMap[r.ID] = r.HopDistance
	}
	assert.Equal(t, 0, distMap[seedID], "seed must have hop_distance=0")
	assert.Equal(t, 1, distMap[hop1ID], "1-hop neighbor must have hop_distance=1")
}

// ---------------------------------------------------------------------------
// 11. TestRecallGraph_HopsDefault
// ---------------------------------------------------------------------------

func TestRecallGraph_HopsDefault(t *testing.T) {
	clearRecallGraphCache()
	seedID := uuid.New()

	mem := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{graphSeed(seedID, 0.7)}, nil
		},
	}

	getNeighborsCalls := 0
	er := &testEdgeRepo{
		getNeighborsFn: func(_ context.Context, _ []uuid.UUID, _ float64, _ int) ([]domain.MemoryEdge, error) {
			getNeighborsCalls++
			// Return empty so nextFrontier drains immediately after hop 1.
			return nil, nil
		},
	}
	svc := newGraphService(mem, er)

	opts := domain.RecallGraphOpts{
		Query:       "hops default " + uuid.New().String(),
		WorkspaceID: uuid.New(),
		Hops:        0, // zero → must normalize to 2 internally
	}
	_, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)

	// With empty GetNeighbors, the BFS drains after the first hop.
	// At least 1 call must have happened, proving Hops was not left at 0.
	assert.GreaterOrEqual(t, getNeighborsCalls, 1,
		"GetNeighbors must be called when Hops normalizes to 2")
}

// ---------------------------------------------------------------------------
// 12. TestRecallGraph_CacheHit
// ---------------------------------------------------------------------------

func TestRecallGraph_CacheHit(t *testing.T) {
	clearRecallGraphCache()

	taskID := uuid.New()
	wsID := uuid.New()
	searchCalls := 0

	mem := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.ScoredMemory, error) {
			searchCalls++
			return []domain.ScoredMemory{graphSeed(uuid.New(), 0.6)}, nil
		},
	}
	svc := newGraphService(mem, &testEdgeRepo{})

	opts := domain.RecallGraphOpts{
		Query:       "cache-hit-test-" + taskID.String(),
		WorkspaceID: wsID,
		Hops:        1,
		TaskID:      &taskID,
	}

	res1, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)
	assert.Equal(t, 1, searchCalls, "first call must invoke FullTextSearch")

	res2, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)
	assert.Equal(t, 1, searchCalls, "second call with identical opts must use cache (no new FullTextSearch call)")
	assert.Equal(t, len(res1), len(res2))
}

// ---------------------------------------------------------------------------
// 13. TestRecallGraph_GetNeighborsError
// ---------------------------------------------------------------------------

func TestRecallGraph_GetNeighborsError(t *testing.T) {
	clearRecallGraphCache()
	seedID := uuid.New()
	sentinel := errors.New("db: connection lost")

	mem := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{graphSeed(seedID, 0.6)}, nil
		},
	}
	er := &testEdgeRepo{
		getNeighborsFn: func(_ context.Context, _ []uuid.UUID, _ float64, _ int) ([]domain.MemoryEdge, error) {
			return nil, sentinel
		},
	}
	svc := newGraphService(mem, er)

	_, err := svc.RecallGraph(context.Background(), freshOpts("get neighbors error"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel), "error must wrap the sentinel returned by GetNeighbors")
}

// ---------------------------------------------------------------------------
// 14. TestRecallGraph_WeightThreshold_Passed
// ---------------------------------------------------------------------------

func TestRecallGraph_WeightThreshold_Passed(t *testing.T) {
	clearRecallGraphCache()
	seedID := uuid.New()
	const wantThreshold = 0.65

	mem := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{graphSeed(seedID, 0.7)}, nil
		},
	}

	var gotThreshold float64
	er := &testEdgeRepo{
		getNeighborsFn: func(_ context.Context, _ []uuid.UUID, weightThreshold float64, _ int) ([]domain.MemoryEdge, error) {
			gotThreshold = weightThreshold
			return nil, nil
		},
	}
	svc := newGraphService(mem, er)

	opts := domain.RecallGraphOpts{
		Query:           "weight threshold " + uuid.New().String(),
		WorkspaceID:     uuid.New(),
		Hops:            1,
		WeightThreshold: wantThreshold,
	}
	_, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)
	assert.InDelta(t, wantThreshold, gotThreshold, 1e-9,
		"GetNeighbors must receive the exact WeightThreshold from RecallGraphOpts")
}

// ---------------------------------------------------------------------------
// Tenant confinement of BFS expansion
// ---------------------------------------------------------------------------

// TestRecallGraph_EdgeLookupIsWorkspaceScoped pins that BFS asks the edge repo
// only for edges inside the caller's workspace. GetNeighbors' SQL previously had
// no workspace predicate, so same-workspace expansion held by convention (every
// UpsertEdge call site happened to derive endpoints from same-workspace searches)
// rather than by the query.
func TestRecallGraph_EdgeLookupIsWorkspaceScoped(t *testing.T) {
	clearRecallGraphCache()
	opts := freshOpts("edge lookup is workspace scoped")
	seedID := uuid.New()

	mem := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{graphSeed(seedID, 0.6)}, nil
		},
	}
	er := &testEdgeRepo{}
	svc := newGraphService(mem, er)

	_, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)

	require.NotEmpty(t, er.gotWorkspaceIDs, "BFS must query the edge repo")
	for _, got := range er.gotWorkspaceIDs {
		assert.Equal(t, opts.WorkspaceID, got,
			"edge lookup must be confined to the caller's workspace")
	}
}

// TestRecallGraph_ForeignNeighborDropped is the second lock on the same door:
// even if an edge pointing out of the tenant exists (no DB constraint forbids
// one), the memory it resolves to must not enter the result set. memRepo.GetByID
// is a primary-key lookup with no workspace predicate, so without the check in
// BFS this foreign memory's content would be returned to the caller.
func TestRecallGraph_ForeignNeighborDropped(t *testing.T) {
	clearRecallGraphCache()
	opts := freshOpts("foreign neighbor dropped")
	seedID := uuid.New()
	foreignNeighborID := uuid.New()
	foreignWS := uuid.New()

	mem := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{graphSeed(seedID, 0.6)}, nil
		},
		getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
			if id == foreignNeighborID {
				// A memory in ANOTHER tenant, reached via a cross-workspace edge.
				m := graphNeighbor(foreignWS, foreignNeighborID, 0.9)
				m.Content = "tenant B data"
				return m, nil
			}
			return nil, nil
		},
	}
	er := &testEdgeRepo{
		getNeighborsFn: func(_ context.Context, _ []uuid.UUID, _ float64, _ int) ([]domain.MemoryEdge, error) {
			return []domain.MemoryEdge{graphEdge(seedID, foreignNeighborID, 0.9)}, nil
		},
	}
	svc := newGraphService(mem, er)

	results, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)

	for _, r := range results {
		assert.NotEqual(t, foreignNeighborID, r.ID,
			"SECURITY: a memory from another workspace was reached via graph expansion")
		assert.NotContains(t, r.Content, "tenant B data",
			"SECURITY: foreign workspace content leaked through BFS hop-expansion")
	}
	assert.Len(t, results, 1, "only the seed should survive")
}
