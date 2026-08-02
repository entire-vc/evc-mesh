package postgres

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// No //go:build integration tag on purpose — same reason as memory_repo_backfill_db_test.go:
// the "Go coverage" gate runs untagged `go test $pkg`, so a tagged test here would never
// execute in the very check that is supposed to cover it. CI provides Postgres (ci.yml
// services), so these run for real; locally they skip when no DB is reachable.

// chunkedMemory inserts a memory plus one chunk row whose chunk_end is chunkEnd, which is the
// only thing ListNeedingRechunk's predicate reads. Every test below differs only in what
// chunkEnd is set to relative to the content — that IS the scheme signal.
func chunkedMemory(t *testing.T, repo *MemoryRepo, wsID uuid.UUID, key, content string, chunkEnd int) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	mem := &domain.Memory{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		Key:         key + "-" + uuid.New().String()[:8],
		Content:     content,
		Scope:       domain.ScopeWorkspace,
		SourceType:  domain.SourceAgent,
	}
	require.NoError(t, repo.Upsert(ctx, mem))
	chunkRepo := NewMemoryChunkRepo(repo.db)
	require.NoError(t, chunkRepo.ReplaceChunks(ctx, mem.ID, []domain.MemoryChunk{
		{ChunkIdx: 0, ChunkStart: 0, ChunkEnd: chunkEnd, Embedding: "vec", EmbeddingModel: "m", EmbeddingDim: 4},
	}))
	return mem.ID
}

func idsOf(memories []domain.Memory) map[uuid.UUID]bool {
	out := make(map[uuid.UUID]bool, len(memories))
	for _, m := range memories {
		out[m.ID] = true
	}
	return out
}

// The core discrimination: a chunk covering the pre-#494 composite (key + " " + content +
// " " + tags) is stale; a chunk covering content alone is current. Both rows have chunks and
// both carry a model name, so neither ListNotYetChunked nor ListNeedingEmbedding can tell
// them apart — that is why this predicate exists.
func TestMemoryRepoDB_ListNeedingRechunk_SelectsCompositeSchemeOnly(t *testing.T) {
	repo, wsID := setupMemoryRepoChunkingDBTest(t)
	ctx := context.Background()

	content := "the quick brown fox jumps over the lazy dog"
	// Old scheme: the chunker was fed key+" "+content+" "+tags, so the last chunk_end runs
	// past the end of content. Any value > len(content) reproduces that shape.
	stale := chunkedMemory(t, repo, wsID, "composite-scheme", content, len(content)+21)
	// New scheme: chunk offsets index content and the last one lands exactly on its end.
	current := chunkedMemory(t, repo, wsID, "content-scheme", content, len(content))

	got, err := repo.ListNeedingRechunk(ctx, wsID, 0)
	require.NoError(t, err, "limit<=0 must default rather than error")
	ids := idsOf(got)

	assert.True(t, ids[stale], "a memory chunked over the composite string must be selected")
	assert.False(t, ids[current], "a memory chunked over content alone must NOT be selected — it is already correct, and re-embedding it forever is the non-convergence failure this predicate has to avoid")
}

// A chunk_end SHORTER than content is stale too: the offsets no longer describe the text.
// This is the shape of the 6 prod rows whose content or tags were edited after their last
// embed — the predicate has to catch a mismatch in either direction, not just overshoot.
func TestMemoryRepoDB_ListNeedingRechunk_SelectsOffsetsShorterThanContent(t *testing.T) {
	repo, wsID := setupMemoryRepoChunkingDBTest(t)
	ctx := context.Background()

	content := "content that grew after the chunks were written"
	short := chunkedMemory(t, repo, wsID, "edited-after-embed", content, len(content)-10)

	got, err := repo.ListNeedingRechunk(ctx, wsID, 0)
	require.NoError(t, err)
	assert.True(t, idsOf(got)[short], "offsets that stop short of content are stale and must be selected")
}

// chunk_start/chunk_end are BYTE offsets (runeOffsetToByteOffset), so the predicate must
// compare against octet_length, not char_length. On an ASCII fixture the two are identical
// and this distinction is invisible — which is exactly why the fixture here is Cyrillic:
// swap octet_length for char_length in the predicate and this test goes red, while every
// ASCII test above stays green.
func TestMemoryRepoDB_ListNeedingRechunk_ComparesByteLengthNotRuneLength(t *testing.T) {
	repo, wsID := setupMemoryRepoChunkingDBTest(t)
	ctx := context.Background()

	content := "чанки хранят байтовые смещения, а не рунные"
	byteLen := len(content)
	runeLen := len([]rune(content))
	require.NotEqual(t, byteLen, runeLen, "fixture must be multi-byte or it cannot discriminate")

	correct := chunkedMemory(t, repo, wsID, "cyrillic-bytes", content, byteLen)
	runeOffsets := chunkedMemory(t, repo, wsID, "cyrillic-runes", content, runeLen)

	got, err := repo.ListNeedingRechunk(ctx, wsID, 0)
	require.NoError(t, err)
	ids := idsOf(got)

	assert.False(t, ids[correct], "byte offsets covering the whole content are current — selecting them would re-embed the entire non-ASCII corpus on every run")
	assert.True(t, ids[runeOffsets], "rune-valued offsets do not describe the byte layout and must be selected")
}

// The three repair predicates must partition, not overlap: a memory with no chunks at all
// belongs to BackfillChunks/ListNotYetChunked. Claiming it here would double-embed it, and
// worse, would hide the difference between "never chunked" and "chunked wrong".
func TestMemoryRepoDB_ListNeedingRechunk_ExcludesUnchunkedMemories(t *testing.T) {
	repo, wsID := setupMemoryRepoChunkingDBTest(t)
	ctx := context.Background()

	unchunked := &domain.Memory{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		Key:         "never-chunked-" + uuid.New().String()[:8],
		Content:     "content",
		Scope:       domain.ScopeWorkspace,
		SourceType:  domain.SourceAgent,
	}
	require.NoError(t, repo.Upsert(ctx, unchunked))

	got, err := repo.ListNeedingRechunk(ctx, wsID, 0)
	require.NoError(t, err)
	assert.False(t, idsOf(got)[unchunked.ID], "a memory with no chunks is ListNotYetChunked's population, not this one")
}

func TestMemoryRepoDB_ListNeedingRechunk_ExcludesExpired(t *testing.T) {
	repo, wsID := setupMemoryRepoChunkingDBTest(t)
	ctx := context.Background()

	past := time.Now().Add(-time.Hour)
	mem := &domain.Memory{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		Key:         "expired-stale-" + uuid.New().String()[:8],
		Content:     "expired content",
		Scope:       domain.ScopeWorkspace,
		SourceType:  domain.SourceAgent,
		ExpiresAt:   &past,
	}
	require.NoError(t, repo.Upsert(ctx, mem))
	chunkRepo := NewMemoryChunkRepo(repo.db)
	require.NoError(t, chunkRepo.ReplaceChunks(ctx, mem.ID, []domain.MemoryChunk{
		{ChunkIdx: 0, ChunkStart: 0, ChunkEnd: len(mem.Content) + 30, Embedding: "vec", EmbeddingModel: "m", EmbeddingDim: 4},
	}))

	got, err := repo.ListNeedingRechunk(ctx, wsID, 0)
	require.NoError(t, err)
	assert.False(t, idsOf(got)[mem.ID], "an expired memory serves no read path — repairing it spends the embedder on a row nobody can retrieve")

	n, err := repo.CountNeedingRechunk(ctx, wsID)
	require.NoError(t, err)
	assert.Zero(t, n, "the count must apply the same expiry filter as the selection, or convergence can never be reached")
}

// The count reports the whole remaining population, not the page. A convergence loop driven
// by a batch-capped number stops early and reports success over rows it never looked at.
func TestMemoryRepoDB_CountNeedingRechunk_IgnoresBatchLimit(t *testing.T) {
	repo, wsID := setupMemoryRepoChunkingDBTest(t)
	ctx := context.Background()

	content := "some content that was chunked under the old composite scheme"
	for i := 0; i < 3; i++ {
		chunkedMemory(t, repo, wsID, "population", content, len(content)+15)
	}

	page, err := repo.ListNeedingRechunk(ctx, wsID, 1)
	require.NoError(t, err)
	assert.Len(t, page, 1, "the limit must cap the page")

	n, err := repo.CountNeedingRechunk(ctx, wsID)
	require.NoError(t, err)
	assert.Equal(t, 3, n, "the count must see the whole population regardless of any batch limit")
}

// The service-level test can only prove WHICH write-back the repair chose; whether that
// write-back actually holds the column is a property of the SQL, and only real Postgres can
// answer it. This is the test that would catch `updated_at = updated_at` being dropped in a
// future edit of that statement — the corpus-wide, unrecoverable failure.
func TestMemoryRepoDB_UpdateEmbeddingKeepUpdatedAt_HoldsTimestampAndStillWritesVector(t *testing.T) {
	repo, wsID := setupMemoryRepoChunkingDBTest(t)
	ctx := context.Background()

	mem := &domain.Memory{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		Key:         "keeps-timestamp-" + uuid.New().String()[:8],
		Content:     "content",
		Scope:       domain.ScopeWorkspace,
		SourceType:  domain.SourceAgent,
	}
	require.NoError(t, repo.Upsert(ctx, mem))

	var before time.Time
	require.NoError(t, repo.db.GetContext(ctx, &before, `SELECT updated_at FROM memories WHERE id = $1`, mem.ID))

	// Postgres resolves NOW() to microseconds; without a gap a bumped timestamp could
	// compare equal to the original and the assertion would pass on a broken statement.
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, repo.UpdateEmbeddingKeepUpdatedAt(ctx, mem.ID, []float32{0.1, 0.2}, "e5-small", 2))

	var after time.Time
	var model string
	var dim int
	require.NoError(t, repo.db.QueryRowxContext(ctx,
		`SELECT updated_at, embedding_model, embedding_dim FROM memories WHERE id = $1`, mem.ID,
	).Scan(&after, &model, &dim))

	assert.True(t, before.Equal(after), "updated_at moved: %s → %s", before, after)
	assert.Equal(t, "e5-small", model, "the embedding itself must still be written")
	assert.Equal(t, 2, dim)

	// Positive control: the ordinary variant on the same row DOES move it, so the assertion
	// above is discriminating rather than a statement about the clock.
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, repo.UpdateEmbedding(ctx, mem.ID, []float32{0.3, 0.4}, "e5-small", 2))
	var bumped time.Time
	require.NoError(t, repo.db.GetContext(ctx, &bumped, `SELECT updated_at FROM memories WHERE id = $1`, mem.ID))
	assert.True(t, bumped.After(after), "UpdateEmbedding must still bump updated_at on the write path")
}

func TestMemoryRepoDB_UpdateEmbeddingKeepUpdatedAt_UnencodableVectorIsReported(t *testing.T) {
	repo, wsID := setupMemoryRepoChunkingDBTest(t)
	ctx := context.Background()

	mem := &domain.Memory{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		Key:         "bad-vector-" + uuid.New().String()[:8],
		Content:     "content",
		Scope:       domain.ScopeWorkspace,
		SourceType:  domain.SourceAgent,
	}
	require.NoError(t, repo.Upsert(ctx, mem))

	inf := float32(math.Inf(1)) // encoding/json refuses non-finite floats
	err := repo.UpdateEmbeddingKeepUpdatedAt(ctx, mem.ID, []float32{inf}, "e5-small", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encode vector")
}

// A failed query must surface as an error, never as an empty page or a zero count — the
// latter reads as "converged" to the caller driving the repair loop.
func TestMemoryRepoDB_RechunkQueries_FailureIsAnErrorNotAnEmptyAnswer(t *testing.T) {
	repo, wsID := setupMemoryRepoChunkingDBTest(t)
	dead, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := repo.ListNeedingRechunk(dead, wsID, 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list needing rechunk")

	n, err := repo.CountNeedingRechunk(dead, wsID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "count needing rechunk")
	assert.Zero(t, n, "a failed count must not hand back a number the caller could act on")
}

// Repairing a row must remove it from the population — otherwise the job re-selects the same
// rows forever and `remaining` never falls, the non-convergence shape of #84b0694d.
func TestMemoryRepoDB_ListNeedingRechunk_ConvergesOnceOffsetsIndexContent(t *testing.T) {
	repo, wsID := setupMemoryRepoChunkingDBTest(t)
	ctx := context.Background()

	content := "content whose chunks start out covering the composite string"
	id := chunkedMemory(t, repo, wsID, "converges", content, len(content)+18)

	before, err := repo.CountNeedingRechunk(ctx, wsID)
	require.NoError(t, err)
	require.Equal(t, 1, before, "precondition: the row starts inside the population, or this test proves nothing")

	// What a real repair pass writes: chunks re-cut over content alone.
	chunkRepo := NewMemoryChunkRepo(repo.db)
	require.NoError(t, chunkRepo.ReplaceChunks(ctx, id, []domain.MemoryChunk{
		{ChunkIdx: 0, ChunkStart: 0, ChunkEnd: len(content), Embedding: "vec", EmbeddingModel: "m", EmbeddingDim: 4},
	}))

	after, err := repo.CountNeedingRechunk(ctx, wsID)
	require.NoError(t, err)
	assert.Zero(t, after, "a repaired row must leave the population")
}
