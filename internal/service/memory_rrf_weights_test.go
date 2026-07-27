package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ---------------------------------------------------------------------------
// RRF arm weights are runtime-configurable (task #acb84eaa)
//
// The weights were compile-time constants, which made the required weight sweep
// impossible without a rebuild per grid point. They are now resolved once per service
// from the environment. These tests pin the plumbing — NOT the values: choosing values
// is the sweep's job, and it is blocked on #2c087b2a (an honest candidate pool) because
// sweeping against a pool that post-filters 35–70% of the fixtures optimises noise.
// ---------------------------------------------------------------------------

func TestRRFWeights_DefaultToProductionValues(t *testing.T) {
	svc := NewMemoryService(&mockMemoryRepo{}, nil, nil).(*memoryService)
	assert.Equal(t, 0.7, svc.rrfVectorWeight, "default vector weight must not drift silently")
	assert.Equal(t, 0.3, svc.rrfTextWeight, "default text weight must not drift silently")
}

func TestRRFWeights_ReadFromEnv(t *testing.T) {
	t.Setenv("MEMORY_RECALL_RRF_VECTOR_WEIGHT", "0.5")
	t.Setenv("MEMORY_RECALL_RRF_TEXT_WEIGHT", "0.5")

	svc := NewMemoryService(&mockMemoryRepo{}, nil, nil).(*memoryService)
	assert.Equal(t, 0.5, svc.rrfVectorWeight)
	assert.Equal(t, 0.5, svc.rrfTextWeight)
}

// A zero weight is a legitimate setting — "disable this arm" is a grid point the sweep
// needs in order to measure each arm alone. It must NOT be treated as "unset".
func TestRRFWeights_ZeroIsHonoured(t *testing.T) {
	t.Setenv("MEMORY_RECALL_RRF_VECTOR_WEIGHT", "0")

	svc := NewMemoryService(&mockMemoryRepo{}, nil, nil).(*memoryService)
	assert.Equal(t, 0.0, svc.rrfVectorWeight, "0 must disable the arm, not fall back to the default")
	assert.Equal(t, 0.3, svc.rrfTextWeight, "the other arm must be untouched")
}

func TestRRFWeights_InvalidAndNegativeFallBackToDefault(t *testing.T) {
	for _, bad := range []string{"not-a-number", "-0.5", ""} {
		t.Run("value="+bad, func(t *testing.T) {
			t.Setenv("MEMORY_RECALL_RRF_VECTOR_WEIGHT", bad)
			svc := NewMemoryService(&mockMemoryRepo{}, nil, nil).(*memoryService)
			assert.Equal(t, 0.7, svc.rrfVectorWeight,
				"a bad weight must fall back to the default, never to 0 (which would silently disable the arm)")
		})
	}
}

// The weights must actually reach the fusion, not just sit on the struct. Equal weights
// are the case the sweep cares about first: they are what restores a BM25-rank-3 gold
// above a dense-only tail row (0.5/63 > 0.5/137), so if the plumbing were inert this
// test would still see the dense row win.
func TestRRFWeights_ReachTheFusion_EqualWeightsFlipTheOrder(t *testing.T) {
	wsID := uuid.New()

	// textOnly: returned by BM25 at rank 3, absent from the dense arm — the gold shape.
	textOnly := memWithScope(wsID, domain.ScopeWorkspace)
	// denseTail: absent from BM25, returned by the dense arm from the deep tail.
	denseTail := memWithScope(wsID, domain.ScopeWorkspace)

	const denseTailRank = 77

	newRepo := func() *mockMemoryRepo {
		return &mockMemoryRepo{
			fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
				// Two filler rows so textOnly lands at rank 3.
				return []domain.ScoredMemory{
					{Memory: memWithScope(wsID, domain.ScopeWorkspace), Score: 0.9},
					{Memory: memWithScope(wsID, domain.ScopeWorkspace), Score: 0.8},
					{Memory: textOnly, Score: 0.7},
				}, nil
			},
			vectorSearchFn: func(_ context.Context, _ []float32, _ uuid.UUID, _ *uuid.UUID, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
				out := make([]domain.ScoredMemory, 0, denseTailRank)
				for i := 0; i < denseTailRank-1; i++ {
					out = append(out, domain.ScoredMemory{Memory: memWithScope(wsID, domain.ScopeWorkspace), Score: 0.5})
				}
				out = append(out, domain.ScoredMemory{Memory: denseTail, Score: 0.4})
				return out, nil
			},
		}
	}

	rankOf := func(t *testing.T, results []domain.ScoredMemory, id uuid.UUID) int {
		t.Helper()
		for i, r := range results {
			if r.ID == id {
				return i + 1
			}
		}
		return -1
	}

	run := func(t *testing.T) []domain.ScoredMemory {
		t.Helper()
		svc := NewMemoryService(newRepo(), nil, &stubEmbedder{vec: []float32{1, 0}})
		results, mode, err := svc.Recall(context.Background(), domain.RecallOpts{
			Query:       "probe",
			WorkspaceID: wsID,
			Limit:       100,
		})
		require.NoError(t, err)
		require.Equal(t, domain.SearchModeHybrid, mode)
		return results
	}

	t.Run("0.7/0.3 — dense tail outranks a BM25 rank-3 hit", func(t *testing.T) {
		results := run(t)
		textRank := rankOf(t, results, textOnly.ID)
		denseRank := rankOf(t, results, denseTail.ID)
		require.Positive(t, textRank)
		require.Positive(t, denseRank)
		assert.Less(t, denseRank, textRank,
			"with the current weights a dense row from rank %d is expected to beat BM25 rank 3 — "+
				"this is the defect under investigation, pinned so a weight change is visible", denseTailRank)
	})

	t.Run("0.5/0.5 — the BM25 hit is restored above it", func(t *testing.T) {
		t.Setenv("MEMORY_RECALL_RRF_VECTOR_WEIGHT", "0.5")
		t.Setenv("MEMORY_RECALL_RRF_TEXT_WEIGHT", "0.5")

		results := run(t)
		textRank := rankOf(t, results, textOnly.ID)
		denseRank := rankOf(t, results, denseTail.ID)
		require.Positive(t, textRank)
		require.Positive(t, denseRank)
		assert.Less(t, textRank, denseRank,
			"equal weights must put the BM25 rank-3 hit above the dense tail row (0.5/63 > 0.5/137); "+
				"if this fails the env weights are not reaching reciprocalRankFusion")
	})
}
