package service

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// clearRecallGraphCache removes all entries from the package-level cache so
// tests start with a clean slate.
func clearRecallGraphCache() {
	recallGraphCache.Range(func(k, v interface{}) bool {
		recallGraphCache.Delete(k)
		return true
	})
}

// mockGetNeighborsFn wraps a controllable GetNeighbors implementation.
// Embed it into mockMemoryEdgeRepo by replacing the inner field at test time.
type mockGetNeighborsFn struct {
	mockMemoryEdgeRepo
	getNeighborsFn func(ctx context.Context, ids []uuid.UUID, threshold float64) ([]domain.MemoryEdge, error)
}

func (m *mockGetNeighborsFn) GetNeighbors(ctx context.Context, ids []uuid.UUID, threshold float64) ([]domain.MemoryEdge, error) {
	if m.getNeighborsFn != nil {
		return m.getNeighborsFn(ctx, ids, threshold)
	}
	return nil, nil
}

// seedOnlyMemRepo returns a mockMemoryRepo whose fullTextSearchFn returns a
// single ScoredMemory with the supplied fields. getByIDFn is set so graph
// expansion can fetch expanded nodes.
func seedOnlyMemRepo(id uuid.UUID, content string, importance float32, score float64) *mockMemoryRepo {
	return &mockMemoryRepo{
		fullTextSearchFn: func(_ context.Context, _ string, _ uuid.UUID, _ *uuid.UUID, _ string, _ []string, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{
				{
					Memory: domain.Memory{
						ID:              id,
						Content:         content,
						ImportanceScore: importance,
					},
					Score: score,
				},
			}, nil
		},
	}
}

// baseRecallGraphOpts returns a minimal valid RecallGraphOpts.
func baseRecallGraphOpts(wsID uuid.UUID) domain.RecallGraphOpts {
	return domain.RecallGraphOpts{
		Query:       "test query",
		WorkspaceID: wsID,
		Hops:        1,
	}
}

// ---------------------------------------------------------------------------
// 1. TestRecallGraph_EmptyQueryReturnsError
// ---------------------------------------------------------------------------

func TestRecallGraph_EmptyQueryReturnsError(t *testing.T) {
	clearRecallGraphCache()

	svc := newMemoryService(&mockMemoryRepo{})

	opts := domain.RecallGraphOpts{
		Query:       "",
		WorkspaceID: uuid.New(),
	}

	_, err := svc.RecallGraph(context.Background(), opts)
	require.Error(t, err)

	apiErr, ok := err.(*apierror.Error)
	require.True(t, ok, "expected *apierror.Error, got %T", err)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode())
}

// ---------------------------------------------------------------------------
// 2. TestRecallGraph_NoSeedsReturnsNilResults
// ---------------------------------------------------------------------------

func TestRecallGraph_NoSeedsReturnsNilResults(t *testing.T) {
	clearRecallGraphCache()

	repo := &mockMemoryRepo{
		fullTextSearchFn: func(_ context.Context, _ string, _ uuid.UUID, _ *uuid.UUID, _ string, _ []string, _ int) ([]domain.ScoredMemory, error) {
			return nil, nil
		},
	}

	svc := newMemoryService(repo)
	opts := baseRecallGraphOpts(uuid.New())

	results, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)
	assert.Nil(t, results)
}

// ---------------------------------------------------------------------------
// 3. TestRecallGraph_SeedHasZeroHopDistance
// ---------------------------------------------------------------------------

func TestRecallGraph_SeedHasZeroHopDistance(t *testing.T) {
	clearRecallGraphCache()

	wsID := uuid.New()
	seedID := uuid.New()
	repo := seedOnlyMemRepo(seedID, "seed content", 0.8, 0.9)

	svc := newMemoryService(repo)
	opts := baseRecallGraphOpts(wsID)

	results, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, 0, results[0].HopDistance)
}

// ---------------------------------------------------------------------------
// 4. TestRecallGraph_SeedLabeledViaRecall
// ---------------------------------------------------------------------------

func TestRecallGraph_SeedLabeledViaRecall(t *testing.T) {
	clearRecallGraphCache()

	wsID := uuid.New()
	seedID := uuid.New()
	repo := seedOnlyMemRepo(seedID, "seed content", 0.8, 0.9)

	svc := newMemoryService(repo)
	opts := baseRecallGraphOpts(wsID)

	results, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, domain.ProvenanceRecall, results[0].Provenance)
}

// ---------------------------------------------------------------------------
// 5. TestRecallGraph_GraphNeighborHasHopDistanceOne
// ---------------------------------------------------------------------------

func TestRecallGraph_GraphNeighborHasHopDistanceOne(t *testing.T) {
	clearRecallGraphCache()

	wsID := uuid.New()
	seedID := uuid.New()
	neighborID := uuid.New()

	repo := &mockMemoryRepo{
		fullTextSearchFn: func(_ context.Context, _ string, _ uuid.UUID, _ *uuid.UUID, _ string, _ []string, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{
				{Memory: domain.Memory{ID: seedID, Content: "seed", ImportanceScore: 0.8}, Score: 0.9},
			}, nil
		},
		getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
			if id == neighborID {
				return &domain.Memory{ID: neighborID, Content: "neighbor", ImportanceScore: 0.7}, nil
			}
			return nil, nil
		},
	}

	edgeRepo := &mockGetNeighborsFn{
		getNeighborsFn: func(_ context.Context, ids []uuid.UUID, _ float64) ([]domain.MemoryEdge, error) {
			for _, id := range ids {
				if id == seedID {
					return []domain.MemoryEdge{
						{
							ID:           uuid.New(),
							MemoryFromID: seedID,
							MemoryToID:   neighborID,
							Weight:       0.8,
						},
					}, nil
				}
			}
			return nil, nil
		},
	}

	svc := NewMemoryService(repo, edgeRepo, nil)
	opts := baseRecallGraphOpts(wsID)

	results, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)

	var neighbor *domain.RecallGraphResult
	for i := range results {
		if results[i].ID == neighborID {
			neighbor = &results[i]
			break
		}
	}
	require.NotNil(t, neighbor, "neighbor should be in results")
	assert.Equal(t, 1, neighbor.HopDistance)
}

// ---------------------------------------------------------------------------
// 6. TestRecallGraph_GraphNeighborLabeledViaGraph
// ---------------------------------------------------------------------------

func TestRecallGraph_GraphNeighborLabeledViaGraph(t *testing.T) {
	clearRecallGraphCache()

	wsID := uuid.New()
	seedID := uuid.New()
	neighborID := uuid.New()

	repo := &mockMemoryRepo{
		fullTextSearchFn: func(_ context.Context, _ string, _ uuid.UUID, _ *uuid.UUID, _ string, _ []string, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{
				{Memory: domain.Memory{ID: seedID, Content: "seed", ImportanceScore: 0.8}, Score: 0.9},
			}, nil
		},
		getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
			if id == neighborID {
				return &domain.Memory{ID: neighborID, Content: "neighbor", ImportanceScore: 0.7}, nil
			}
			return nil, nil
		},
	}

	edgeRepo := &mockGetNeighborsFn{
		getNeighborsFn: func(_ context.Context, ids []uuid.UUID, _ float64) ([]domain.MemoryEdge, error) {
			for _, id := range ids {
				if id == seedID {
					return []domain.MemoryEdge{
						{
							ID:           uuid.New(),
							MemoryFromID: seedID,
							MemoryToID:   neighborID,
							Weight:       0.8,
						},
					}, nil
				}
			}
			return nil, nil
		},
	}

	svc := NewMemoryService(repo, edgeRepo, nil)
	opts := baseRecallGraphOpts(wsID)

	results, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)

	var neighbor *domain.RecallGraphResult
	for i := range results {
		if results[i].ID == neighborID {
			neighbor = &results[i]
			break
		}
	}
	require.NotNil(t, neighbor, "neighbor should be in results")
	assert.Equal(t, domain.ProvenanceGraph, neighbor.Provenance)
}

// ---------------------------------------------------------------------------
// 7. TestRecallGraph_CompositeScoreSeedTimesEdgeWeight
// ---------------------------------------------------------------------------

// TestRecallGraph_CompositeScoreSeedTimesEdgeWeight verifies the composite score
// formula: neighbor.CompositeScore == seed.CompositeScore * edge.Weight.
// Because Recall() transforms raw scores via RRF, we capture the seed's actual
// CompositeScore from the results and verify the relationship holds precisely.
func TestRecallGraph_CompositeScoreSeedTimesEdgeWeight(t *testing.T) {
	clearRecallGraphCache()

	wsID := uuid.New()
	seedID := uuid.New()
	neighborID := uuid.New()

	const edgeWeight = float32(0.8)

	repo := &mockMemoryRepo{
		fullTextSearchFn: func(_ context.Context, _ string, _ uuid.UUID, _ *uuid.UUID, _ string, _ []string, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{
				{Memory: domain.Memory{ID: seedID, Content: "seed", ImportanceScore: 0.8}, Score: 0.9},
			}, nil
		},
		getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
			if id == neighborID {
				return &domain.Memory{ID: neighborID, Content: "neighbor", ImportanceScore: 0.6}, nil
			}
			return nil, nil
		},
	}

	edgeRepo := &mockGetNeighborsFn{
		getNeighborsFn: func(_ context.Context, ids []uuid.UUID, _ float64) ([]domain.MemoryEdge, error) {
			for _, id := range ids {
				if id == seedID {
					return []domain.MemoryEdge{
						{
							ID:           uuid.New(),
							MemoryFromID: seedID,
							MemoryToID:   neighborID,
							Weight:       edgeWeight,
						},
					}, nil
				}
			}
			return nil, nil
		},
	}

	svc := NewMemoryService(repo, edgeRepo, nil)
	opts := baseRecallGraphOpts(wsID)

	results, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)

	var seed, neighbor *domain.RecallGraphResult
	for i := range results {
		switch results[i].ID {
		case seedID:
			seed = &results[i]
		case neighborID:
			neighbor = &results[i]
		}
	}
	require.NotNil(t, seed, "seed must be in results")
	require.NotNil(t, neighbor, "neighbor must be in results")

	// CompositeScore of neighbor must equal seed's CompositeScore * edge weight.
	expected := seed.CompositeScore * float64(edgeWeight)
	assert.InDelta(t, expected, neighbor.CompositeScore, 1e-9)
}

// ---------------------------------------------------------------------------
// 8. TestRecallGraph_CompositeScoreTwoHopChain
// ---------------------------------------------------------------------------

// TestRecallGraph_CompositeScoreTwoHopChain verifies a two-hop chain:
// seed → hop1 (w=0.8) → hop2 (w=0.6). hop2.CompositeScore must equal
// seed.CompositeScore * hop1Weight * hop2Weight, and HopDistance must be 2.
func TestRecallGraph_CompositeScoreTwoHopChain(t *testing.T) {
	clearRecallGraphCache()

	wsID := uuid.New()
	seedID := uuid.New()
	hop1ID := uuid.New()
	hop2ID := uuid.New()

	const hop1Weight = float32(0.8)
	const hop2Weight = float32(0.6)

	callCount := int32(0)

	repo := &mockMemoryRepo{
		fullTextSearchFn: func(_ context.Context, _ string, _ uuid.UUID, _ *uuid.UUID, _ string, _ []string, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{
				{Memory: domain.Memory{ID: seedID, Content: "seed", ImportanceScore: 0.9}, Score: 1.0},
			}, nil
		},
		getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
			switch id {
			case hop1ID:
				return &domain.Memory{ID: hop1ID, Content: "hop1", ImportanceScore: 0.7}, nil
			case hop2ID:
				return &domain.Memory{ID: hop2ID, Content: "hop2", ImportanceScore: 0.6}, nil
			}
			return nil, nil
		},
	}

	edgeRepo := &mockGetNeighborsFn{
		getNeighborsFn: func(_ context.Context, ids []uuid.UUID, _ float64) ([]domain.MemoryEdge, error) {
			n := atomic.AddInt32(&callCount, 1)
			if n == 1 {
				// hop 1: seed → hop1
				for _, id := range ids {
					if id == seedID {
						return []domain.MemoryEdge{
							{
								ID:           uuid.New(),
								MemoryFromID: seedID,
								MemoryToID:   hop1ID,
								Weight:       hop1Weight,
							},
						}, nil
					}
				}
			} else {
				// hop 2: hop1 → hop2
				for _, id := range ids {
					if id == hop1ID {
						return []domain.MemoryEdge{
							{
								ID:           uuid.New(),
								MemoryFromID: hop1ID,
								MemoryToID:   hop2ID,
								Weight:       hop2Weight,
							},
						}, nil
					}
				}
			}
			return nil, nil
		},
	}

	svc := NewMemoryService(repo, edgeRepo, nil)
	opts := domain.RecallGraphOpts{
		Query:       "test query",
		WorkspaceID: wsID,
		Hops:        2,
	}

	results, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)

	var seed, hop1, hop2 *domain.RecallGraphResult
	for i := range results {
		switch results[i].ID {
		case seedID:
			seed = &results[i]
		case hop1ID:
			hop1 = &results[i]
		case hop2ID:
			hop2 = &results[i]
		}
	}
	require.NotNil(t, seed, "seed must be in results")
	require.NotNil(t, hop1, "hop1 must be in results")
	require.NotNil(t, hop2, "hop2 must be in results")

	// hop2.CompositeScore == seed.CompositeScore * hop1Weight * hop2Weight
	expected := seed.CompositeScore * float64(hop1Weight) * float64(hop2Weight)
	assert.InDelta(t, expected, hop2.CompositeScore, 1e-9)
	assert.Equal(t, 2, hop2.HopDistance)
}

// ---------------------------------------------------------------------------
// 9. TestRecallGraph_DeduplicateBestScoreWins
// ---------------------------------------------------------------------------

func TestRecallGraph_DeduplicateBestScoreWins(t *testing.T) {
	clearRecallGraphCache()

	wsID := uuid.New()
	seed1ID := uuid.New()
	seed2ID := uuid.New()
	sharedNeighborID := uuid.New()

	// seed1 reaches shared with score 0.9 * 0.8 = 0.72
	// seed2 reaches shared with score 0.5 * 0.9 = 0.45
	// best path = 0.72

	repo := &mockMemoryRepo{
		fullTextSearchFn: func(_ context.Context, _ string, _ uuid.UUID, _ *uuid.UUID, _ string, _ []string, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{
				{Memory: domain.Memory{ID: seed1ID, Content: "seed1", ImportanceScore: 0.8}, Score: 0.9},
				{Memory: domain.Memory{ID: seed2ID, Content: "seed2", ImportanceScore: 0.8}, Score: 0.5},
			}, nil
		},
		getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
			if id == sharedNeighborID {
				return &domain.Memory{ID: sharedNeighborID, Content: "shared", ImportanceScore: 0.7}, nil
			}
			return nil, nil
		},
	}

	edgeRepo := &mockGetNeighborsFn{
		getNeighborsFn: func(_ context.Context, ids []uuid.UUID, _ float64) ([]domain.MemoryEdge, error) {
			edges := make([]domain.MemoryEdge, 0)
			for _, id := range ids {
				if id == seed1ID {
					edges = append(edges, domain.MemoryEdge{
						ID: uuid.New(), MemoryFromID: seed1ID, MemoryToID: sharedNeighborID, Weight: 0.8,
					})
				}
				if id == seed2ID {
					edges = append(edges, domain.MemoryEdge{
						ID: uuid.New(), MemoryFromID: seed2ID, MemoryToID: sharedNeighborID, Weight: 0.9,
					})
				}
			}
			return edges, nil
		},
	}

	svc := NewMemoryService(repo, edgeRepo, nil)
	opts := baseRecallGraphOpts(wsID)

	results, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)

	// sharedNeighborID must appear exactly once
	var count int
	var best *domain.RecallGraphResult
	for i := range results {
		if results[i].ID == sharedNeighborID {
			count++
			best = &results[i]
		}
	}
	require.Equal(t, 1, count, "shared neighbor must appear exactly once")

	// Best path: seed1 (higher score, wins RRF rank 0 among seeds) * 0.8.
	// We capture the actual seed1 score from results and verify the relationship.
	var seed1 *domain.RecallGraphResult
	for i := range results {
		if results[i].ID == seed1ID {
			seed1 = &results[i]
			break
		}
	}
	require.NotNil(t, seed1, "seed1 must be in results")
	expectedBest := seed1.CompositeScore * 0.8
	assert.InDelta(t, expectedBest, best.CompositeScore, 1e-9)
}

// ---------------------------------------------------------------------------
// 10. TestRecallGraph_GraphNodeDroppedBelowImportanceThreshold
// ---------------------------------------------------------------------------

func TestRecallGraph_GraphNodeDroppedBelowImportanceThreshold(t *testing.T) {
	clearRecallGraphCache()

	wsID := uuid.New()
	seedID := uuid.New()
	lowImportanceID := uuid.New()

	repo := &mockMemoryRepo{
		fullTextSearchFn: func(_ context.Context, _ string, _ uuid.UUID, _ *uuid.UUID, _ string, _ []string, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{
				{Memory: domain.Memory{ID: seedID, Content: "seed", ImportanceScore: 0.8}, Score: 0.9},
			}, nil
		},
		getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
			if id == lowImportanceID {
				// importance_score = 0.3 — below graphMinImportance (0.4)
				return &domain.Memory{ID: lowImportanceID, Content: "low importance", ImportanceScore: 0.3}, nil
			}
			return nil, nil
		},
	}

	edgeRepo := &mockGetNeighborsFn{
		getNeighborsFn: func(_ context.Context, ids []uuid.UUID, _ float64) ([]domain.MemoryEdge, error) {
			for _, id := range ids {
				if id == seedID {
					return []domain.MemoryEdge{
						{ID: uuid.New(), MemoryFromID: seedID, MemoryToID: lowImportanceID, Weight: 0.9},
					}, nil
				}
			}
			return nil, nil
		},
	}

	svc := NewMemoryService(repo, edgeRepo, nil)
	opts := baseRecallGraphOpts(wsID)

	results, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)

	for _, r := range results {
		assert.NotEqual(t, lowImportanceID, r.ID, "low-importance graph node must be dropped")
	}
}

// ---------------------------------------------------------------------------
// 11. TestRecallGraph_GraphNodeKeptAtImportanceThreshold
// ---------------------------------------------------------------------------

func TestRecallGraph_GraphNodeKeptAtImportanceThreshold(t *testing.T) {
	clearRecallGraphCache()

	wsID := uuid.New()
	seedID := uuid.New()
	boundaryID := uuid.New()

	repo := &mockMemoryRepo{
		fullTextSearchFn: func(_ context.Context, _ string, _ uuid.UUID, _ *uuid.UUID, _ string, _ []string, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{
				{Memory: domain.Memory{ID: seedID, Content: "seed", ImportanceScore: 0.8}, Score: 0.9},
			}, nil
		},
		getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
			if id == boundaryID {
				// importance_score = 0.4 — exactly at graphMinImportance threshold → keep
				return &domain.Memory{ID: boundaryID, Content: "boundary", ImportanceScore: 0.4}, nil
			}
			return nil, nil
		},
	}

	edgeRepo := &mockGetNeighborsFn{
		getNeighborsFn: func(_ context.Context, ids []uuid.UUID, _ float64) ([]domain.MemoryEdge, error) {
			for _, id := range ids {
				if id == seedID {
					return []domain.MemoryEdge{
						{ID: uuid.New(), MemoryFromID: seedID, MemoryToID: boundaryID, Weight: 0.9},
					}, nil
				}
			}
			return nil, nil
		},
	}

	svc := NewMemoryService(repo, edgeRepo, nil)
	opts := baseRecallGraphOpts(wsID)

	results, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)

	var found bool
	for _, r := range results {
		if r.ID == boundaryID {
			found = true
			break
		}
	}
	assert.True(t, found, "node with importance_score=0.4 (exactly at threshold) must be kept")
}

// ---------------------------------------------------------------------------
// 12. TestRecallGraph_SeedNotFilteredByImportanceScore
// ---------------------------------------------------------------------------

// TestRecallGraph_SeedNotFilteredByImportanceScore verifies that RecallGraph
// does NOT apply an additional importance_score gate on seed nodes beyond what
// Recall() already applies. A seed with importance_score=0.4 (the Recall floor)
// must appear in the output — RecallGraph must not silently drop it.
func TestRecallGraph_SeedNotFilteredByImportanceScore(t *testing.T) {
	clearRecallGraphCache()

	wsID := uuid.New()
	seedID := uuid.New()

	// importance_score = 0.4 — exactly at the Recall() floor; RecallGraph must
	// not apply an additional gate that would remove this seed.
	repo := seedOnlyMemRepo(seedID, "boundary importance seed", 0.4, 0.5)

	svc := newMemoryService(repo)
	opts := baseRecallGraphOpts(wsID)

	results, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)

	var found bool
	for _, r := range results {
		if r.ID == seedID {
			found = true
			break
		}
	}
	assert.True(t, found, "seed at importance_score floor (0.4) must appear in results — RecallGraph must not add a second filter on seeds")
}

// ---------------------------------------------------------------------------
// 13. TestRecallGraph_ResultsOrderedByCompositeScoreDesc
// ---------------------------------------------------------------------------

func TestRecallGraph_ResultsOrderedByCompositeScoreDesc(t *testing.T) {
	clearRecallGraphCache()

	wsID := uuid.New()
	id1 := uuid.New()
	id2 := uuid.New()
	id3 := uuid.New()

	repo := &mockMemoryRepo{
		fullTextSearchFn: func(_ context.Context, _ string, _ uuid.UUID, _ *uuid.UUID, _ string, _ []string, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{
				{Memory: domain.Memory{ID: id1, Content: "low", ImportanceScore: 0.6}, Score: 0.3},
				{Memory: domain.Memory{ID: id2, Content: "high", ImportanceScore: 0.9}, Score: 0.9},
				{Memory: domain.Memory{ID: id3, Content: "mid", ImportanceScore: 0.7}, Score: 0.6},
			}, nil
		},
	}

	svc := newMemoryService(repo)
	opts := baseRecallGraphOpts(wsID)

	results, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)
	require.Len(t, results, 3)

	for i := 1; i < len(results); i++ {
		assert.GreaterOrEqual(t, results[i-1].CompositeScore, results[i].CompositeScore,
			"results[%d].CompositeScore=%.4f should be >= results[%d].CompositeScore=%.4f",
			i-1, results[i-1].CompositeScore, i, results[i].CompositeScore)
	}
}

// ---------------------------------------------------------------------------
// 14. TestRecallGraph_CacheReturnsSameResults
// ---------------------------------------------------------------------------

func TestRecallGraph_CacheReturnsSameResults(t *testing.T) {
	clearRecallGraphCache()

	wsID := uuid.New()
	seedID := uuid.New()
	taskID := uuid.New()

	callCount := int32(0)

	repo := &mockMemoryRepo{
		fullTextSearchFn: func(_ context.Context, _ string, _ uuid.UUID, _ *uuid.UUID, _ string, _ []string, _ int) ([]domain.ScoredMemory, error) {
			atomic.AddInt32(&callCount, 1)
			return []domain.ScoredMemory{
				{Memory: domain.Memory{ID: seedID, Content: "cached seed", ImportanceScore: 0.8}, Score: 0.85},
			}, nil
		},
	}

	svc := newMemoryService(repo)
	opts := domain.RecallGraphOpts{
		Query:       "cache test query",
		WorkspaceID: wsID,
		Hops:        1,
		TaskID:      &taskID,
	}

	results1, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)
	require.NotEmpty(t, results1)

	results2, err := svc.RecallGraph(context.Background(), opts)
	require.NoError(t, err)
	require.NotEmpty(t, results2)

	// fullTextSearchFn must have been called exactly once — second call hits cache.
	assert.Equal(t, int32(1), atomic.LoadInt32(&callCount), "fullTextSearchFn must be called exactly once (2nd call hits cache)")

	// Results must be identical.
	require.Equal(t, len(results1), len(results2))
	for i := range results1 {
		assert.Equal(t, results1[i].ID, results2[i].ID)
		assert.InDelta(t, results1[i].CompositeScore, results2[i].CompositeScore, 1e-9)
	}
}
