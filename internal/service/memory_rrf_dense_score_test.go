package service

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ---------------------------------------------------------------------------
// DenseScore plumbing through reciprocalRankFusion (#116875c2)
//
// The vector arm's Score is a raw cosine at VectorSearch time. The mesh-mcp
// noise gate needs exactly that value to tell a paraphrased on-topic query
// from garbage — the fused RRF score is rank-only and cannot. These tests pin
// the plumbing: cosine in, DenseScore out, nil preserved for sparse-only
// rows, and the fused Score arithmetic untouched by the new field.
// ---------------------------------------------------------------------------

func denseScored(id uuid.UUID, score float64) domain.ScoredMemory {
	return domain.ScoredMemory{Memory: domain.Memory{ID: id}, Score: score}
}

func TestRRFDenseScore_CarriedFromVectorArm(t *testing.T) {
	both := uuid.New()    // ranked by both arms
	kwOnly := uuid.New()  // BM25 hit the vector arm never saw
	vecOnly := uuid.New() // semantic hit with no lexical overlap

	kw := []domain.ScoredMemory{
		denseScored(both, 0.31), // ts_rank — irrelevant to DenseScore
		denseScored(kwOnly, 0.22),
	}
	vec := []domain.ScoredMemory{
		denseScored(both, 0.83), // cosine
		denseScored(vecOnly, 0.57),
	}

	merged := reciprocalRankFusion(kw, vec, 0.3, 0.7)
	require.Len(t, merged, 3)
	byID := map[uuid.UUID]domain.ScoredMemory{}
	for _, m := range merged {
		byID[m.ID] = m
	}

	// Fused scores stay the plain RRF sums — carrying DenseScore must not move them.
	assert.InDelta(t, 0.3*(1.0/(rrfK+1))+0.7*(1.0/(rrfK+1)), byID[both].Score, 1e-12)
	assert.InDelta(t, 0.3*(1.0/(rrfK+2)), byID[kwOnly].Score, 1e-12)
	assert.InDelta(t, 0.7*(1.0/(rrfK+2)), byID[vecOnly].Score, 1e-12)

	// Dense cosine survives fusion for everything the vector arm scored…
	require.NotNil(t, byID[both].DenseScore)
	assert.InDelta(t, 0.83, *byID[both].DenseScore, 1e-12)
	require.NotNil(t, byID[vecOnly].DenseScore)
	assert.InDelta(t, 0.57, *byID[vecOnly].DenseScore, 1e-12)

	// …and stays nil for sparse-only rows: "the dense arm did not score it"
	// must never be reported as "scored zero".
	assert.Nil(t, byID[kwOnly].DenseScore)
}

func TestRRFDenseScore_JSONShape(t *testing.T) {
	dense := 0.42
	b, err := json.Marshal(domain.ScoredMemory{Score: 0.012, DenseScore: &dense})
	require.NoError(t, err)
	assert.Contains(t, string(b), `"dense_score":0.42`)

	b, err = json.Marshal(domain.ScoredMemory{Score: 0.012})
	require.NoError(t, err)
	assert.NotContains(t, string(b), `dense_score`)
}
