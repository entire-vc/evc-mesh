package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	pkgmetrics "github.com/entire-vc/evc-mesh/pkg/metrics"
)

// ---------------------------------------------------------------------------
// search_mode plumbing — Recall must report the mode it was actually SERVED in.
//
// Recall fails open when the embedder dies: it returns BM25-only results with a
// 200 and a log line. These tests pin the observable half of that fix — the mode
// reported to callers — so a degraded recall can never again masquerade as a
// healthy one (which is what lets a CI gate blame a PR for an infra outage).
// ---------------------------------------------------------------------------

// stubEmbedder is a live embedder whose Embed can be made to fail.
type stubEmbedder struct {
	vec []float32
	err error
}

func (s *stubEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.vec, nil
}

func (s *stubEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = s.vec
	}
	return out, nil
}

func (s *stubEmbedder) Model() string { return "stub-embed" }
func (s *stubEmbedder) Dimensions() int {
	return len(s.vec)
}

func hitRepo() *mockMemoryRepo {
	hit := domain.ScoredMemory{
		Memory: domain.Memory{
			ID:              uuid.New(),
			Key:             "bm25-hit",
			FreshnessScore:  1.0,
			ImportanceScore: 0.8,
		},
		Score: 1.0,
	}
	return &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{hit}, nil
		},
	}
}

func recallOpts() domain.RecallOpts {
	return domain.RecallOpts{Query: "test", WorkspaceID: uuid.New(), Limit: 10}
}

// A healthy embedder + a working vector search = the dense arm ran = "hybrid".
func TestRecall_SearchMode_HybridWhenDenseArmRuns(t *testing.T) {
	repo := hitRepo()
	svc := NewMemoryService(repo, &mockMemoryEdgeRepo{}, &stubEmbedder{vec: []float32{0.1, 0.2, 0.3}})

	results, mode, err := svc.Recall(context.Background(), recallOpts())

	require.NoError(t, err)
	require.NotEmpty(t, results)
	assert.Equal(t, domain.SearchModeHybrid, mode)
	assert.False(t, mode.Degraded(), "a full hybrid search is not degraded")
}

// THE FAIL-OPEN. The embedder errors (prod: OpenRouter 402, out of credit).
// Recall must still succeed on BM25 alone — and must SAY SO.
func TestRecall_SearchMode_BM25OnlyWhenEmbedderFails(t *testing.T) {
	repo := hitRepo()
	svc := NewMemoryService(repo, &mockMemoryEdgeRepo{}, &stubEmbedder{err: errors.New("402 out of credit")})

	results, mode, err := svc.Recall(context.Background(), recallOpts())

	// Fail OPEN: the caller still gets results and no error…
	require.NoError(t, err)
	require.NotEmpty(t, results, "recall must still serve BM25 results when the embedder is down")
	// …but the degradation is no longer invisible.
	assert.Equal(t, domain.SearchModeBM25Only, mode)
	assert.True(t, mode.Degraded())
}

// The embedder works but the vector query itself fails: the dense arm still did
// not contribute, so the honest mode is bm25-only.
func TestRecall_SearchMode_BM25OnlyWhenVectorSearchFails(t *testing.T) {
	repo := hitRepo()
	repo.vectorSearchFn = func(_ context.Context, _ []float32, _ uuid.UUID, _ *uuid.UUID, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
		return nil, errors.New("pgvector: connection reset")
	}
	svc := NewMemoryService(repo, &mockMemoryEdgeRepo{}, &stubEmbedder{vec: []float32{0.1, 0.2}})

	_, mode, err := svc.Recall(context.Background(), recallOpts())

	require.NoError(t, err)
	assert.Equal(t, domain.SearchModeBM25Only, mode)
	assert.True(t, mode.Degraded())
}

// No embedder configured (noop) is a permanent, legitimate bm25-only deployment.
// It must report bm25-only too — the mode describes what SERVED the call, not
// what someone hoped was configured. (A baseline snapped here is comparable only
// with other bm25-only runs — that is exactly the point.)
func TestRecall_SearchMode_BM25OnlyWithNoopEmbedder(t *testing.T) {
	svc := newMemoryService(hitRepo()) // nil embedder → NoopEmbedder

	_, mode, err := svc.Recall(context.Background(), recallOpts())

	require.NoError(t, err)
	assert.Equal(t, domain.SearchModeBM25Only, mode)
	assert.True(t, mode.Degraded())
}

// An embedder that returns an empty vector produces no dense arm either.
func TestRecall_SearchMode_BM25OnlyOnEmptyVector(t *testing.T) {
	svc := NewMemoryService(hitRepo(), &mockMemoryEdgeRepo{}, &stubEmbedder{vec: []float32{}})

	_, mode, err := svc.Recall(context.Background(), recallOpts())

	require.NoError(t, err)
	assert.Equal(t, domain.SearchModeBM25Only, mode)
}

// Degraded() is the single source of truth for the REST `degraded` field.
func TestSearchMode_Degraded(t *testing.T) {
	assert.False(t, domain.SearchModeHybrid.Degraded())
	assert.True(t, domain.SearchModeBM25Only.Degraded())
	assert.True(t, domain.SearchMode("").Degraded(), "an unknown mode is not a healthy one")
}

func recallEmptyValue(t *testing.T, mode domain.SearchMode) float64 {
	t.Helper()
	m := &dto.Metric{}
	c, err := pkgmetrics.MemoryRecallEmptyTotal.GetMetricWithLabelValues(string(mode))
	require.NoError(t, err)
	require.NoError(t, c.Write(m))
	return m.GetCounter().GetValue()
}

// A recall that returns no items moves mesh_memory_recall_empty_total; one that
// returns a hit does not. The second half is the red control for the first.
func TestRecall_EmptyResultIncrementsEmptyCounter(t *testing.T) {
	before := recallEmptyValue(t, domain.SearchModeBM25Only)

	hit := newMemoryService(hitRepo()) // noop embedder → bm25-only
	results, mode, err := hit.Recall(context.Background(), recallOpts())
	require.NoError(t, err)
	require.NotEmpty(t, results)
	require.Equal(t, domain.SearchModeBM25Only, mode)
	assert.Equal(t, before, recallEmptyValue(t, domain.SearchModeBM25Only), "a recall with results must not count as empty")

	empty := newMemoryService(&mockMemoryRepo{})
	results, mode, err = empty.Recall(context.Background(), recallOpts())
	require.NoError(t, err)
	require.Empty(t, results)
	require.Equal(t, domain.SearchModeBM25Only, mode)
	assert.Equal(t, before+1, recallEmptyValue(t, domain.SearchModeBM25Only), "an empty recall must increment the counter by exactly 1")
}

// ─────────────────────────────────────────────────────────────────────────────
// mesh_memory_recall_top_score — the QUALITY counterpart to the empty counter.
// The dense arm always returns nearest neighbours, so on hybrid an unmatchable
// query still returns a page and empty_total stays flat; the top-1 final score
// is what tells those pages apart. Prod-measured anchors (#2c44a087, 30 real
// queries replayed read-only): both-arms winners 0.0144..0.0164 = 1/61,
// dense-only winners exactly 0.7/61 ≈ 0.0115, bm25-only mode tops at 0.3/61.
// ─────────────────────────────────────────────────────────────────────────────

func recallTopScoreHist(t *testing.T, mode domain.SearchMode) *dto.Histogram {
	t.Helper()
	m := &dto.Metric{}
	c, err := pkgmetrics.MemoryRecallTopScore.GetMetricWithLabelValues(string(mode))
	require.NoError(t, err)
	// The vec hands out a bare Observer; only the full Histogram exposes buckets.
	h, ok := c.(prometheus.Histogram)
	require.True(t, ok, "a HistogramVec child must implement prometheus.Histogram")
	require.NoError(t, h.Write(m))
	return m.GetHistogram()
}

func recallTopScoreCount(t *testing.T, mode domain.SearchMode) uint64 {
	t.Helper()
	return recallTopScoreHist(t, mode).GetSampleCount()
}

// recallTopScoreBelow returns the cumulative count of observations ≤ le.
func recallTopScoreBelow(t *testing.T, mode domain.SearchMode, le float64) uint64 {
	t.Helper()
	for _, b := range recallTopScoreHist(t, mode).GetBucket() {
		if b.GetUpperBound() == le {
			return b.GetCumulativeCount()
		}
	}
	t.Fatalf("mesh_memory_recall_top_score has no bucket le=%v", le)
	return 0
}

func vecHit(id uuid.UUID) domain.ScoredMemory {
	return domain.ScoredMemory{
		Memory: domain.Memory{
			ID:              id,
			Key:             "dense-hit",
			FreshnessScore:  1.0,
			ImportanceScore: 0.8,
		},
		Score: 0.9, // raw cosine; RRF uses only the rank
	}
}

// Every served recall observes exactly one top score into its mode's series: a
// BM25 rank-1 hit lands at 0.3/61 ≈ 0.0049 (inside le=0.005, NOT the 0 bucket),
// an empty recall observes 0.0 into the lowest bucket. Delete the Observe call
// and both count assertions fail — that is the red control.
func TestRecall_TopScoreObservedPerRecall(t *testing.T) {
	countBefore := recallTopScoreCount(t, domain.SearchModeBM25Only)
	leZeroBefore := recallTopScoreBelow(t, domain.SearchModeBM25Only, 0.001)
	leArmBefore := recallTopScoreBelow(t, domain.SearchModeBM25Only, 0.005)

	hit := newMemoryService(hitRepo()) // noop embedder → bm25-only
	results, mode, err := hit.Recall(context.Background(), recallOpts())
	require.NoError(t, err)
	require.NotEmpty(t, results)
	require.Equal(t, domain.SearchModeBM25Only, mode)
	assert.Equal(t, countBefore+1, recallTopScoreCount(t, domain.SearchModeBM25Only), "a served recall observes exactly one top score")
	assert.Equal(t, leArmBefore+1, recallTopScoreBelow(t, domain.SearchModeBM25Only, 0.005),
		"a BM25-only rank-1 hit scores 0.3/61 ≈ 0.0049 — inside le=0.005")
	assert.Equal(t, leZeroBefore, recallTopScoreBelow(t, domain.SearchModeBM25Only, 0.001),
		"a recall with results must not observe into the 0 bucket")

	empty := newMemoryService(&mockMemoryRepo{})
	results, mode, err = empty.Recall(context.Background(), recallOpts())
	require.NoError(t, err)
	require.Empty(t, results)
	require.Equal(t, domain.SearchModeBM25Only, mode)
	assert.Equal(t, countBefore+2, recallTopScoreCount(t, domain.SearchModeBM25Only))
	assert.Equal(t, leZeroBefore+1, recallTopScoreBelow(t, domain.SearchModeBM25Only, 0.001),
		"an empty recall observes 0.0 into the lowest bucket")
}

// The whole point of the metric, as measured on prod: on hybrid, a page whose
// winner BOTH arms ranked first scores 1/61 ≈ 0.0164, while a page whose winner
// only the dense arm found scores 0.7/61 ≈ 0.0115 — non-empty, but the
// "useless recall" signature that mesh_memory_recall_empty_total cannot see.
// The two must land on opposite sides of the le=0.012 bucket edge.
func TestRecall_TopScoreHybridSeparatesBothArmsFromDenseOnly(t *testing.T) {
	bothBefore := recallTopScoreBelow(t, domain.SearchModeHybrid, 0.017)
	denseOnlyBefore := recallTopScoreBelow(t, domain.SearchModeHybrid, 0.012)

	shared := uuid.New()
	// Both arms must rank the SAME item first: hitRepo() would mint its own id
	// and the fused winner would be dense-only — a different (also useful) case.
	both := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{vecHit(shared)}, nil
		},
		vectorSearchFn: func(_ context.Context, _ []float32, _ uuid.UUID, _ *uuid.UUID, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{vecHit(shared)}, nil
		},
	}
	svcBoth := NewMemoryService(both, &mockMemoryEdgeRepo{}, &stubEmbedder{vec: []float32{0.1, 0.2, 0.3}})
	results, mode, err := svcBoth.Recall(context.Background(), recallOpts())
	require.NoError(t, err)
	require.NotEmpty(t, results)
	require.Equal(t, domain.SearchModeHybrid, mode)
	assert.Equal(t, bothBefore+1, recallTopScoreBelow(t, domain.SearchModeHybrid, 0.017),
		"a both-arms rank-1 winner scores 1/61 ≈ 0.0164 — inside le=0.017")
	assert.Equal(t, denseOnlyBefore, recallTopScoreBelow(t, domain.SearchModeHybrid, 0.012),
		"1/61 ≈ 0.0164 must NOT land in the dense-only bucket")

	denseOnly := &mockMemoryRepo{} // FTS arm returns nothing
	denseOnly.vectorSearchFn = func(_ context.Context, _ []float32, _ uuid.UUID, _ *uuid.UUID, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
		return []domain.ScoredMemory{vecHit(uuid.New())}, nil
	}
	svcDense := NewMemoryService(denseOnly, &mockMemoryEdgeRepo{}, &stubEmbedder{vec: []float32{0.1, 0.2, 0.3}})
	results, mode, err = svcDense.Recall(context.Background(), recallOpts())
	require.NoError(t, err)
	require.NotEmpty(t, results, "the dense arm always returns neighbours — this page is NOT empty")
	require.Equal(t, domain.SearchModeHybrid, mode)
	assert.Equal(t, denseOnlyBefore+1, recallTopScoreBelow(t, domain.SearchModeHybrid, 0.012),
		"a dense-arm-only winner scores 0.7/61 ≈ 0.0115 — inside le=0.012")
}

// A page saved entirely by pinned injection is non-empty (the caller got rows,
// empty_total stays flat) but retrieval found NOTHING — the top score must
// observe 0.0, not the synthetic 2.0 sentinel pinned rows carry.
func TestRecall_TopScoreExcludesSyntheticPinned(t *testing.T) {
	countBefore := recallTopScoreCount(t, domain.SearchModeBM25Only)
	leZeroBefore := recallTopScoreBelow(t, domain.SearchModeBM25Only, 0.001)
	leSentinelBefore := recallTopScoreBelow(t, domain.SearchModeBM25Only, 0.030)
	emptyBefore := recallEmptyValue(t, domain.SearchModeBM25Only)

	pinned := &mockMemoryRepo{} // both retrieval arms return nothing
	pinned.findPinnedFn = func(_ context.Context, _ uuid.UUID, _ *uuid.UUID) ([]domain.Memory, error) {
		return []domain.Memory{{ID: uuid.New(), Key: "kind:pinned row", FreshnessScore: 1.0, ImportanceScore: 1.0}}, nil
	}
	svc := newMemoryService(pinned) // noop embedder → bm25-only

	results, mode, err := svc.Recall(context.Background(), recallOpts())
	require.NoError(t, err)
	require.NotEmpty(t, results, "pinned injection surfaces the row despite empty retrieval")
	require.Equal(t, domain.SearchModeBM25Only, mode)
	assert.Equal(t, emptyBefore, recallEmptyValue(t, domain.SearchModeBM25Only),
		"a pinned-only page is not empty for the counter…")
	assert.Equal(t, countBefore+1, recallTopScoreCount(t, domain.SearchModeBM25Only),
		"…but it IS one more served recall for the histogram")
	assert.Equal(t, leZeroBefore+1, recallTopScoreBelow(t, domain.SearchModeBM25Only, 0.001),
		"and its top score is 0.0: no retrieval row reached the page")
	// A 2.0 sentinel would land ABOVE every finite bucket, so "observations above
	// le=0.030" (count minus last finite cumulative) is the sentinel detector.
	deltaCount := recallTopScoreCount(t, domain.SearchModeBM25Only) - countBefore
	deltaAboveLast := deltaCount - (recallTopScoreBelow(t, domain.SearchModeBM25Only, 0.030) - leSentinelBefore)
	assert.EqualValues(t, uint64(0), deltaAboveLast,
		"nothing may be observed above le=0.030 — the synthetic 2.0 pinned sentinel must never reach the histogram")
}

// The displacement half of the pinned story (#2c44a087 review P1): retrieval
// DID find something, but eligible pinned rows fill the whole limit and the
// final trim discards every retrieval row. The caller's page is pinned-only —
// retrieval contributed nothing to what was actually served — so the top score
// must observe 0.0, not the pre-injection retrieval score. Capturing before
// the injection+trim (the original implementation) fails exactly this test.
func TestRecall_TopScorePinnedDisplacementObservedAsZero(t *testing.T) {
	leZeroBefore := recallTopScoreBelow(t, domain.SearchModeBM25Only, 0.001)
	leArmBefore := recallTopScoreBelow(t, domain.SearchModeBM25Only, 0.005)

	pinned := hitRepo() // BM25 arm returns a rank-1 hit (0.3/61)…
	pinned.findPinnedFn = func(_ context.Context, _ uuid.UUID, _ *uuid.UUID) ([]domain.Memory, error) {
		return []domain.Memory{{ID: uuid.New(), Key: "kind:pinned row", FreshnessScore: 1.0, ImportanceScore: 1.0}}, nil
	}
	svc := newMemoryService(pinned) // noop embedder → bm25-only

	// …and limit=1 lets the single pinned row displace that hit entirely.
	countBefore := recallTopScoreCount(t, domain.SearchModeBM25Only)
	opts := recallOpts()
	opts.Limit = 1
	results, mode, err := svc.Recall(context.Background(), opts)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, domain.SearchModeBM25Only, mode)
	require.Equal(t, "kind:pinned row", results[0].Key, "the pinned row must be the one the caller received")

	assert.Equal(t, countBefore+1, recallTopScoreCount(t, domain.SearchModeBM25Only), "exactly one observation for the served recall")
	// le=0.001 is the discriminator: 0.0 lands inside it, the displaced 0.3/61
	// hit (0.0049) does NOT. A pre-injection capture (the P1 bug) leaves this
	// bucket flat while le=0.005 moves — that inversion is the red control.
	assert.Equal(t, leZeroBefore+1, recallTopScoreBelow(t, domain.SearchModeBM25Only, 0.001),
		"the observed top score is 0.0: no retrieval row reached the served page, even though retrieval found one pre-injection")
	assert.Equal(t, leArmBefore+1, recallTopScoreBelow(t, domain.SearchModeBM25Only, 0.005),
		"buckets are cumulative — a 0.0 observation also lands inside le=0.005 (this pins that it is 0.0 and not something in (0.001, 0.005])")
}
