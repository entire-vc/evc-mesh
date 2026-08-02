package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/embedding"
)

// RechunkStale is the repair job for the pre-#494 corpus (parent e11fe15e AC4). The
// predicate that selects its population lives in the repository and is covered by the DB
// tests in memory_repo_rechunk_db_test.go; what these tests pin is the part a wrong repair
// job gets wrong even with a correct selector — what it writes, and what it reports.

// The whole point of the repair: rows come back re-embedded under B2, key+tags on EVERY
// chunk. If this job ever chunked the composite string again it would faithfully rewrite the
// damage it was built to remove.
func TestRechunkStale_ReembedsWithKeyTagsPrefixOnEveryChunk(t *testing.T) {
	id := uuid.New()
	memRepo := &mockMemoryRepo{
		needRechunk: []domain.Memory{{
			ID:      id,
			Key:     "sol-repair-predicate",
			Content: longTranscript(30),
			Tags:    []string{"memory", "embedding"},
		}},
	}
	embedder := &capturingPrefixEmbedder{dim: 4}
	svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, embedder,
		MemoryWithChunkRepo(newMockMemoryChunkRepo()))

	processed, _, err := svc.RechunkStale(context.Background(), uuid.New(), 0)
	require.NoError(t, err)
	require.Equal(t, 1, processed)

	texts := embedder.allTexts()
	require.Greater(t, len(texts), 1, "fixture must produce multiple chunks or it cannot show per-chunk prefixing")
	for i, text := range texts {
		assert.True(t, strings.HasPrefix(text, "sol-repair-predicate memory embedding "),
			"chunk %d went to the embedder without the key+tags prefix: %.60q", i, text)
	}
}

// The repair must not move updated_at. Nobody edited these memories, and that column drives
// MarkStaleByAge's staleness clock and DecayRelevance's 30-day threshold — a single run over
// the corpus would reset both for every row, irrecoverably (the original timestamps are
// stored nowhere else). Swapping the write-back back to UpdateEmbedding reds this test.
func TestRechunkStale_PreservesUpdatedAt(t *testing.T) {
	id := uuid.New()
	memRepo := &mockMemoryRepo{
		needRechunk: []domain.Memory{{ID: id, Key: "k", Content: "some content"}},
	}
	svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, &switchModelEmbedder{model: "e5-small", dim: 4},
		MemoryWithChunkRepo(newMockMemoryChunkRepo()))

	_, _, err := svc.RechunkStale(context.Background(), uuid.New(), 0)
	require.NoError(t, err)

	assert.Equal(t, []uuid.UUID{id}, memRepo.keptUpdatedAtIDs,
		"the repair path must write the vector through UpdateEmbeddingKeepUpdatedAt")
}

// A write path, by contrast, SHOULD bump updated_at — the caller really did change the
// memory. This is the negative control for the test above: without it, "preserves
// updated_at" would also pass if the preserving variant had simply replaced the normal one
// everywhere, which would silently freeze the timestamp on genuine edits.
func TestBackfillChunks_StillBumpsUpdatedAt(t *testing.T) {
	id := uuid.New()
	memRepo := &mockMemoryRepo{
		notYetChunked: []domain.Memory{{ID: id, Key: "k", Content: "some content"}},
	}
	svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, &switchModelEmbedder{model: "e5-small", dim: 4},
		MemoryWithChunkRepo(newMockMemoryChunkRepo()))

	n, err := svc.BackfillChunks(context.Background(), uuid.New(), 0)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	assert.Empty(t, memRepo.keptUpdatedAtIDs,
		"only the rechunk repair holds updated_at; the ordinary chunked-embed path must keep bumping it")
	assert.Equal(t, id, memRepo.embeddedID, "the backfill still writes the embedding")
}

// `remaining` is read from the database after the batch, never derived from `processed`.
// This is the guard against the failure this whole subtask exists to avoid: a repair job
// whose selector no longer matches the damaged rows reports a healthy count and heals
// nothing. Here the job processes every row it was handed AND the population is still 7 —
// which a `remaining = population - processed` implementation could never report.
func TestRechunkStale_RemainingIsCountedNotDerived(t *testing.T) {
	memRepo := &mockMemoryRepo{
		needRechunk: []domain.Memory{
			{ID: uuid.New(), Key: "a", Content: "content a"},
			{ID: uuid.New(), Key: "b", Content: "content b"},
		},
		rechunkRemaining: 7,
	}
	svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, &switchModelEmbedder{model: "e5-small", dim: 4},
		MemoryWithChunkRepo(newMockMemoryChunkRepo()))

	processed, remaining, err := svc.RechunkStale(context.Background(), uuid.New(), 0)
	require.NoError(t, err)
	assert.Equal(t, 2, processed, "both handed-back rows were re-embedded")
	assert.Equal(t, 7, remaining, "remaining comes from the population count, not from what this call processed")
}

// One memory failing to embed must not abort the batch (same convention as BatchEmbed), and
// must not be counted as processed — otherwise a corpus that cannot converge still reports
// a full batch every run.
func TestRechunkStale_EmbedFailureIsSkippedNotCounted(t *testing.T) {
	failing, ok := uuid.New(), uuid.New()
	memRepo := &mockMemoryRepo{
		needRechunk: []domain.Memory{
			{ID: failing, Key: "k", Content: "content"},
			{ID: ok, Key: "k", Content: "content"},
		},
		rechunkRemaining: 1,
	}
	svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, &failsFirstMemoryEmbedder{dim: 4},
		MemoryWithChunkRepo(newMockMemoryChunkRepo()))

	processed, remaining, err := svc.RechunkStale(context.Background(), uuid.New(), 0)
	require.NoError(t, err, "one row's failure must not fail the batch")
	assert.Equal(t, 1, processed, "only the row that actually re-embedded counts")
	assert.Equal(t, 1, remaining, "the row that failed is still in the population, and the count says so")
}

// Chunking not configured is a normal state, not an error — but it must not look like
// convergence. An operator pointing this at a deployment with chunking off would otherwise
// read `remaining: 0` as "corpus already repaired" when nothing was even examined.
func TestRechunkStale_NoChunkRepo_ReportsPopulationInsteadOfZero(t *testing.T) {
	memRepo := &mockMemoryRepo{rechunkRemaining: 42}
	svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, &switchModelEmbedder{model: "e5-small", dim: 4})

	processed, remaining, err := svc.RechunkStale(context.Background(), uuid.New(), 0)
	require.NoError(t, err, "an unconfigured chunk repo is a no-op, not an error")
	assert.Zero(t, processed)
	assert.Equal(t, 42, remaining, "the population must still be reported so 'nothing happened' is not read as 'nothing to do'")
}

func TestRechunkStale_NoopEmbedder_ReportsPopulationInsteadOfZero(t *testing.T) {
	memRepo := &mockMemoryRepo{rechunkRemaining: 5}
	svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, embedding.NewNoopEmbedder(),
		MemoryWithChunkRepo(newMockMemoryChunkRepo()))

	processed, remaining, err := svc.RechunkStale(context.Background(), uuid.New(), 0)
	require.NoError(t, err)
	assert.Zero(t, processed)
	assert.Equal(t, 5, remaining)
}

func TestRechunkStale_ListError_IsWrappedAndReturned(t *testing.T) {
	memRepo := &mockMemoryRepo{listNeedingRechunkErr: errors.New("simulated db failure")}
	svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, &switchModelEmbedder{model: "e5-small", dim: 4},
		MemoryWithChunkRepo(newMockMemoryChunkRepo()))

	_, _, err := svc.RechunkStale(context.Background(), uuid.New(), 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rechunk stale: list")
	assert.Contains(t, err.Error(), "simulated db failure")
}

// A count that fails must surface as an error rather than as remaining=0 — the one value an
// operator's convergence loop reads as "done".
func TestRechunkStale_CountError_IsReturnedRatherThanReportedAsConverged(t *testing.T) {
	memRepo := &mockMemoryRepo{
		needRechunk:            []domain.Memory{{ID: uuid.New(), Key: "k", Content: "content"}},
		countNeedingRechunkErr: errors.New("simulated db failure"),
	}
	svc := NewMemoryService(memRepo, &mockMemoryEdgeRepo{}, &switchModelEmbedder{model: "e5-small", dim: 4},
		MemoryWithChunkRepo(newMockMemoryChunkRepo()))

	_, remaining, err := svc.RechunkStale(context.Background(), uuid.New(), 0)
	require.Error(t, err)
	assert.Zero(t, remaining)
	assert.Contains(t, err.Error(), "count remaining")
}
