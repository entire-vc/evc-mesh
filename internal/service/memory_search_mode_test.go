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
