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
// RecallStats — the arm ROW COUNTS, which search_mode cannot express.
//
// search_mode reports that the dense arm RAN (embed OK, VectorSearch returned
// no error). It says nothing about the arm having matched anything, and those
// two are not the same state: after the chunked-embed write path landed, every
// new memory carried a NULL embedding, VectorSearch matched zero rows across the
// whole corpus, and recall reported `hybrid` / `degraded: false` while being
// served entirely by BM25. The CI recall gate scored its best-ever run off that.
//
// DenseRows is the number that tells the two apart. These tests pin the
// distinction itself — that hybrid+0 is REACHABLE and reported — because a stat
// that cannot go to zero while the mode stays healthy would prove nothing.
// ---------------------------------------------------------------------------

func vecRepoWith(rows int) *mockMemoryRepo {
	repo := hitRepo()
	repo.vectorSearchFn = func(_ context.Context, _ []float32, _ uuid.UUID, _ *uuid.UUID, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
		out := make([]domain.ScoredMemory, 0, rows)
		for i := 0; i < rows; i++ {
			out = append(out, domain.ScoredMemory{
				Memory: domain.Memory{
					ID:              uuid.New(),
					Key:             "vec-hit",
					FreshnessScore:  1.0,
					ImportanceScore: 0.8,
				},
				Score: 0.9,
			})
		}
		return out, nil
	}
	return repo
}

// THE DEFECT, stated as a test. The embedder is healthy and VectorSearch returns
// cleanly — so the mode is, correctly, "hybrid" and Degraded() is false. But the
// arm matched NOTHING, and that is now visible instead of inferred.
//
// This is the shape the gate keys on: `hybrid` with DenseRows == 0 is a dead arm
// wearing a healthy label.
func TestRecallWithStats_HybridWithZeroDenseRows(t *testing.T) {
	svc := NewMemoryService(vecRepoWith(0), &mockMemoryEdgeRepo{}, &stubEmbedder{vec: []float32{0.1, 0.2, 0.3}})

	results, stats, err := svc.RecallWithStats(context.Background(), recallOpts())

	require.NoError(t, err)
	require.NotEmpty(t, results, "BM25 still served the recall")
	// The mode is genuinely hybrid — nothing errored. That is precisely why the
	// mode alone could not catch this.
	assert.Equal(t, domain.SearchModeHybrid, stats.Mode)
	assert.False(t, stats.Mode.Degraded())
	// …and the row count is what says the dense arm contributed nothing.
	assert.Equal(t, 0, stats.DenseRows, "vector arm matched no rows")
	assert.Positive(t, stats.SparseRows, "BM25 arm did match")
}

// The positive control for the test above: with the same healthy embedder and a
// vector arm that DOES match, DenseRows is non-zero. Without this, a DenseRows
// that was hard-wired to 0 (or never assigned) would pass the zero-case test and
// pin the gate permanently at "dense arm empty".
func TestRecallWithStats_HybridCountsDenseRows(t *testing.T) {
	svc := NewMemoryService(vecRepoWith(3), &mockMemoryEdgeRepo{}, &stubEmbedder{vec: []float32{0.1, 0.2, 0.3}})

	_, stats, err := svc.RecallWithStats(context.Background(), recallOpts())

	require.NoError(t, err)
	assert.Equal(t, domain.SearchModeHybrid, stats.Mode)
	assert.Equal(t, 3, stats.DenseRows)
}

// Counted per-ARM, not post-merge. The merged/filtered result set is a different
// number from what the arms returned, and reporting the former would defeat the
// purpose: a dense arm that returned 3 candidates none of which survived the
// downstream filters is a working arm, and must not read as an empty one.
func TestRecallWithStats_DenseRowsCountsTheArmNotTheMergedResult(t *testing.T) {
	repo := vecRepoWith(5)
	svc := NewMemoryService(repo, &mockMemoryEdgeRepo{}, &stubEmbedder{vec: []float32{0.1, 0.2, 0.3}})

	// A filter no candidate carries: everything is dropped after the merge.
	opts := recallOpts()
	opts.TagsAny = []string{"a-tag-no-candidate-has"}

	results, stats, err := svc.RecallWithStats(context.Background(), opts)

	require.NoError(t, err)
	assert.Empty(t, results, "post-filter removed every candidate")
	assert.Equal(t, 5, stats.DenseRows, "the ARM still returned 5 — the filter is downstream")
}

// A dead embedder means the dense arm never ran at all: bm25-only, zero dense
// rows. Distinct from the case above — same DenseRows, different Mode — which is
// why the gate rule is (hybrid AND 0), not (0) alone. A permanently bm25-only
// deployment is a legitimate configuration, not a defect to alert on.
func TestRecallWithStats_BM25OnlyReportsZeroDenseRows(t *testing.T) {
	svc := NewMemoryService(hitRepo(), &mockMemoryEdgeRepo{}, &stubEmbedder{err: errors.New("402 out of credit")})

	_, stats, err := svc.RecallWithStats(context.Background(), recallOpts())

	require.NoError(t, err)
	assert.Equal(t, domain.SearchModeBM25Only, stats.Mode)
	assert.Equal(t, 0, stats.DenseRows)
	assert.Positive(t, stats.SparseRows)
}

// Recall is a wrapper over RecallWithStats and must stay observationally
// identical for its ~25 existing callers: same items, same mode, same error.
func TestRecall_IsAThinWrapperOverRecallWithStats(t *testing.T) {
	svc := NewMemoryService(vecRepoWith(2), &mockMemoryEdgeRepo{}, &stubEmbedder{vec: []float32{0.1, 0.2, 0.3}})

	itemsA, mode, errA := svc.Recall(context.Background(), recallOpts())
	itemsB, stats, errB := svc.RecallWithStats(context.Background(), recallOpts())

	require.NoError(t, errA)
	require.NoError(t, errB)
	assert.Equal(t, mode, stats.Mode)
	assert.Len(t, itemsA, len(itemsB))
}

// The validation error must survive the wrapper unchanged — an empty query is a
// 400, not an empty-stats 200.
func TestRecallWithStats_EmptyQueryStillValidates(t *testing.T) {
	svc := NewMemoryService(hitRepo(), &mockMemoryEdgeRepo{}, &stubEmbedder{vec: []float32{0.1}})

	_, stats, err := svc.RecallWithStats(context.Background(), domain.RecallOpts{WorkspaceID: uuid.New()})
	require.Error(t, err)
	assert.Equal(t, domain.RecallStats{}, stats)

	_, mode, wrapErr := svc.Recall(context.Background(), domain.RecallOpts{WorkspaceID: uuid.New()})
	require.Error(t, wrapErr)
	assert.Equal(t, domain.SearchMode(""), mode)
}
