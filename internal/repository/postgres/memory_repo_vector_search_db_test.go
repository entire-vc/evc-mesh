package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// DB-level tests for the chunked VectorSearch read path.
//
// No //go:build integration tag on purpose — same reasoning as
// memory_repo_chunking_db_test.go. The "Go coverage" gate scores the diff using
// an untagged `go test $pkg`, so a test hidden behind the integration tag would
// leave this file's changes measured as uncovered while still passing locally.
//
// Every test builds its own workspace, so the candidate pool is naturally scoped
// and these tests neither see nor are seen by rows from any other test.
//
// Vectors are 3-dimensional and hand-chosen so the expected cosine values are
// obvious by inspection against the query vector [1,0,0]:
//
//	[1,0,0]       → 1.0   (exact)
//	[0.8,0.6,0]   → 0.8
//	[0.6,0.8,0]   → 0.6
//	[0,1,0]       → 0.0   (orthogonal)
var (
	queryVec      = []float32{1, 0, 0}
	vecExact      = []float32{1, 0, 0}
	vecStrong     = []float32{0.8, 0.6, 0}
	vecWeak       = []float32{0.6, 0.8, 0}
	vecOrthogonal = []float32{0, 1, 0}
)

func setupVectorSearchDBTest(t *testing.T) (*MemoryRepo, *MemoryChunkRepo, uuid.UUID) {
	t.Helper()
	db := userRepoTestDB(t)

	wsRepo := NewWorkspaceRepo(db)
	ws := &domain.Workspace{
		ID:      uuid.New(),
		Name:    "vector-search-db-test-ws",
		Slug:    "vec-search-db-" + uuid.New().String()[:8],
		OwnerID: uuid.New(),
	}
	require.NoError(t, wsRepo.Create(context.Background(), ws))

	return NewMemoryRepo(db), NewMemoryChunkRepo(db), ws.ID
}

// newMemory inserts a memory with no vector of any kind.
func newMemory(t *testing.T, repo *MemoryRepo, wsID uuid.UUID, key string) *domain.Memory {
	t.Helper()
	mem := &domain.Memory{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		Key:         key + "-" + uuid.New().String()[:8],
		Content:     "content of " + key,
		Scope:       domain.ScopeWorkspace,
		SourceType:  domain.SourceAgent,
	}
	require.NoError(t, repo.Upsert(context.Background(), mem, domain.MemoryWriteIntent{}))
	return mem
}

// withChunks attaches one chunk row per vector, in order, to an existing memory.
func withChunks(t *testing.T, chunkRepo *MemoryChunkRepo, memID uuid.UUID, vecs ...[]float32) {
	t.Helper()
	chunks := make([]domain.MemoryChunk, 0, len(vecs))
	for i, v := range vecs {
		chunks = append(chunks, domain.MemoryChunk{
			ChunkIdx: i,
			// chk_memory_chunks_range requires chunk_end > chunk_start.
			ChunkStart:     i * 100,
			ChunkEnd:       i*100 + 100,
			Embedding:      domain.EncodeEmbedding(v),
			EmbeddingModel: "test-model",
			EmbeddingDim:   len(v),
		})
	}
	require.NoError(t, chunkRepo.ReplaceChunks(context.Background(), memID, chunks))
}

// scoreOf returns the score of the given memory in a result set, and whether it
// is present at all.
func scoreOf(results []domain.ScoredMemory, id uuid.UUID) (float64, bool) {
	for _, r := range results {
		if r.ID == id {
			return r.Score, true
		}
	}
	return 0, false
}

func countOf(results []domain.ScoredMemory, id uuid.UUID) int {
	n := 0
	for _, r := range results {
		if r.ID == id {
			n++
		}
	}
	return n
}

func search(t *testing.T, repo *MemoryRepo, wsID uuid.UUID, limit int) []domain.ScoredMemory {
	t.Helper()
	res, err := repo.VectorSearch(context.Background(), queryVec, wsID, nil, domain.MemorySearchFilter{}, limit)
	require.NoError(t, err)
	return res
}

// A memory whose vectors live only in memory_chunks must be reachable. This is
// the live regression the chunked write path introduced: it deliberately leaves
// memories.embedding NULL, while VectorSearch still required it to be non-NULL,
// so every newly-chunked memory silently vanished from the dense arm.
func TestVectorSearchDB_ChunkedMemoryWithNullEmbeddingIsReachable(t *testing.T) {
	repo, chunkRepo, wsID := setupVectorSearchDBTest(t)

	mem := newMemory(t, repo, wsID, "chunked-only")
	withChunks(t, chunkRepo, mem.ID, vecExact)

	// Guard the premise: this memory really does have no legacy vector, so the
	// test cannot pass through the fallback branch by accident.
	var embedding *string
	require.NoError(t, repo.db.GetContext(context.Background(), &embedding,
		`SELECT embedding FROM memories WHERE id = $1`, mem.ID))
	require.Nil(t, embedding, "premise: chunked memory must have NULL memories.embedding")

	results := search(t, repo, wsID, 10)

	score, found := scoreOf(results, mem.ID)
	require.True(t, found, "a chunk-only memory must be reachable by vector search")
	assert.InDelta(t, 1.0, score, 0.0001)
}

// Ranking is max-over-chunks. A memory with one excellent chunk must beat a
// memory with several merely-good ones — summing or averaging inverts this, and
// would reward length over relevance.
func TestVectorSearchDB_RanksByBestChunkNotByAggregate(t *testing.T) {
	repo, chunkRepo, wsID := setupVectorSearchDBTest(t)

	// best chunk 1.0; mean 0.5; sum 1.0
	sharp := newMemory(t, repo, wsID, "sharp")
	withChunks(t, chunkRepo, sharp.ID, vecExact, vecOrthogonal)

	// best chunk 0.8; mean 0.8; sum 2.4
	diffuse := newMemory(t, repo, wsID, "diffuse")
	withChunks(t, chunkRepo, diffuse.ID, vecStrong, vecStrong, vecStrong)

	results := search(t, repo, wsID, 10)
	require.Len(t, results, 2)

	assert.Equal(t, sharp.ID, results[0].ID,
		"memory with the single best-matching chunk must rank first (sum would rank 'diffuse' 2.4 > 1.0, mean 0.8 > 0.5)")
	assert.InDelta(t, 1.0, results[0].Score, 0.0001)
	assert.InDelta(t, 0.8, results[1].Score, 0.0001)
}

// One memory never occupies more than one result slot, however many chunks it
// owns. Everything downstream — reciprocalRankFusion above all — is rank-based
// and assumes one entry per memory.
func TestVectorSearchDB_ManyChunksYieldExactlyOneSlot(t *testing.T) {
	repo, chunkRepo, wsID := setupVectorSearchDBTest(t)

	mem := newMemory(t, repo, wsID, "many-chunks")
	withChunks(t, chunkRepo, mem.ID, vecExact, vecStrong, vecWeak, vecStrong, vecExact)

	results := search(t, repo, wsID, 10)

	assert.Len(t, results, 1)
	assert.Equal(t, 1, countOf(results, mem.ID), "a memory must never yield more than one result slot")
	assert.InDelta(t, 1.0, results[0].Score, 0.0001, "the surviving slot carries the best chunk's score")
}

// The limit bounds MEMORIES, and is applied after the per-memory reduction.
// Truncating the chunk list first would let one long memory's chunks fill the
// whole page and starve every other memory out of the results.
func TestVectorSearchDB_LimitBoundsMemoriesNotChunks(t *testing.T) {
	repo, chunkRepo, wsID := setupVectorSearchDBTest(t)

	// A long memory with more chunks than the entire page size.
	hog := newMemory(t, repo, wsID, "hog")
	withChunks(t, chunkRepo, hog.ID, vecExact, vecExact, vecExact, vecExact, vecExact, vecExact)

	other1 := newMemory(t, repo, wsID, "other-1")
	withChunks(t, chunkRepo, other1.ID, vecStrong)
	other2 := newMemory(t, repo, wsID, "other-2")
	withChunks(t, chunkRepo, other2.ID, vecWeak)

	results := search(t, repo, wsID, 3)

	require.Len(t, results, 3, "three distinct memories qualify, so a page of 3 must hold three of them")
	seen := map[uuid.UUID]bool{}
	for _, r := range results {
		assert.False(t, seen[r.ID], "the page must not contain the same memory twice")
		seen[r.ID] = true
	}
	assert.True(t, seen[hog.ID] && seen[other1.ID] && seen[other2.ID],
		"every memory must be represented; the 6-chunk memory must not crowd the others out")
}

// Results must come back in ranking order. Hydration re-fetches rows with
// `WHERE id = ANY(...)`, which returns them in an arbitrary order — so the
// ordering has to be restored from the ranking, not inherited from the query.
func TestVectorSearchDB_ResultsAreOrderedByDescendingScore(t *testing.T) {
	repo, chunkRepo, wsID := setupVectorSearchDBTest(t)

	// Insert in an order unrelated to the score order.
	mid := newMemory(t, repo, wsID, "mid")
	withChunks(t, chunkRepo, mid.ID, vecStrong)
	best := newMemory(t, repo, wsID, "best")
	withChunks(t, chunkRepo, best.ID, vecExact)
	worst := newMemory(t, repo, wsID, "worst")
	withChunks(t, chunkRepo, worst.ID, vecWeak)

	results := search(t, repo, wsID, 10)
	require.Len(t, results, 3)

	assert.Equal(t, []uuid.UUID{best.ID, mid.ID, worst.ID},
		[]uuid.UUID{results[0].ID, results[1].ID, results[2].ID})
	for i := 1; i < len(results); i++ {
		assert.GreaterOrEqual(t, results[i-1].Score, results[i].Score, "scores must be non-increasing")
	}
}

// A memory not yet chunked still ranks off its legacy memories.embedding. This
// is what makes the backfill resumable: a half-finished migration degrades
// per-memory instead of blanking the dense arm.
func TestVectorSearchDB_UnchunkedMemoryFallsBackToLegacyVector(t *testing.T) {
	repo, chunkRepo, wsID := setupVectorSearchDBTest(t)
	ctx := context.Background()

	legacy := newMemory(t, repo, wsID, "legacy-only")
	require.NoError(t, repo.UpdateEmbedding(ctx, legacy.ID, vecExact, "test-model", 3))

	chunked := newMemory(t, repo, wsID, "chunked")
	withChunks(t, chunkRepo, chunked.ID, vecWeak)

	results := search(t, repo, wsID, 10)
	require.Len(t, results, 2, "both storage shapes must be searchable during a partial backfill")

	score, found := scoreOf(results, legacy.ID)
	require.True(t, found, "an unchunked memory must still rank off memories.embedding")
	assert.InDelta(t, 1.0, score, 0.0001)
	assert.Equal(t, legacy.ID, results[0].ID)
}

// Once a memory has chunks, its legacy vector is ignored entirely — not merged,
// not added as a second slot. The legacy vector embeds the same content
// truncated at the embedder's 512-token window, so counting it alongside the
// chunks would let the truncated vector outrank the memory's own best chunk.
func TestVectorSearchDB_ChunkedMemoryIgnoresItsLegacyVector(t *testing.T) {
	repo, chunkRepo, wsID := setupVectorSearchDBTest(t)
	ctx := context.Background()

	both := newMemory(t, repo, wsID, "both-shapes")
	// Legacy vector is a perfect match; every chunk is only a weak one. If the
	// legacy vector were still counted, this memory would score 1.0.
	require.NoError(t, repo.UpdateEmbedding(ctx, both.ID, vecExact, "test-model", 3))
	withChunks(t, chunkRepo, both.ID, vecWeak, vecWeak)

	results := search(t, repo, wsID, 10)

	require.Len(t, results, 1)
	assert.Equal(t, 1, countOf(results, both.ID), "the two shapes must not produce two slots")
	assert.InDelta(t, 0.6, results[0].Score, 0.0001,
		"score must come from the best chunk (0.6), not from the legacy vector (1.0)")
}

// Hydration must load the health-lifecycle columns. The old vector-arm column
// list omitted them, so every vector hit arrived with Status="" and
// FreshnessScore=0 — which silently defeated ExcludeSuperseded downstream (it
// compares Status) and skipped the freshness penalty (0 is read as "pre-
// lifecycle, treat as 1.0").
func TestVectorSearchDB_HydrationLoadsLifecycleColumns(t *testing.T) {
	repo, chunkRepo, wsID := setupVectorSearchDBTest(t)

	mem := newMemory(t, repo, wsID, "lifecycle")
	withChunks(t, chunkRepo, mem.ID, vecExact)

	results := search(t, repo, wsID, 10)
	require.Len(t, results, 1)

	assert.Equal(t, domain.MemoryStatusActive, results[0].Status,
		"Status must be hydrated — ExcludeSuperseded downstream filters on it")
	assert.InDelta(t, 1.0, float64(results[0].FreshnessScore), 0.0001,
		"FreshnessScore must be hydrated — the recall scorer multiplies by it")
	assert.NotEmpty(t, results[0].Content, "content must be hydrated for the surviving rows")
}

// Eligibility is still enforced on the candidate query, for chunked memories too
// — having chunks must not become a way around archived/expired/workspace
// filtering.
func TestVectorSearchDB_IneligibleChunkedMemoriesAreExcluded(t *testing.T) {
	repo, chunkRepo, wsID := setupVectorSearchDBTest(t)
	ctx := context.Background()

	visible := newMemory(t, repo, wsID, "visible")
	withChunks(t, chunkRepo, visible.ID, vecStrong)

	archived := newMemory(t, repo, wsID, "archived")
	withChunks(t, chunkRepo, archived.ID, vecExact)
	_, err := repo.db.ExecContext(ctx, `UPDATE memories SET archived = true WHERE id = $1`, archived.ID)
	require.NoError(t, err)

	expired := newMemory(t, repo, wsID, "expired")
	withChunks(t, chunkRepo, expired.ID, vecExact)
	_, err = repo.db.ExecContext(ctx,
		`UPDATE memories SET expires_at = NOW() - INTERVAL '1 hour' WHERE id = $1`, expired.ID)
	require.NoError(t, err)

	results := search(t, repo, wsID, 10)

	require.Len(t, results, 1)
	assert.Equal(t, visible.ID, results[0].ID)
	_, archivedFound := scoreOf(results, archived.ID)
	assert.False(t, archivedFound, "archived memories must stay excluded even when chunked")
	_, expiredFound := scoreOf(results, expired.ID)
	assert.False(t, expiredFound, "expired memories must stay excluded even when chunked")
}

// A memory whose chunk rows are unreadable must not fail the whole recall — the
// pre-chunking path skipped corrupted embeddings silently and that contract
// still holds.
func TestVectorSearchDB_CorruptChunkIsSkippedNotFatal(t *testing.T) {
	repo, chunkRepo, wsID := setupVectorSearchDBTest(t)
	ctx := context.Background()

	good := newMemory(t, repo, wsID, "good")
	withChunks(t, chunkRepo, good.ID, vecStrong)

	corrupt := newMemory(t, repo, wsID, "corrupt")
	withChunks(t, chunkRepo, corrupt.ID, vecExact)
	_, err := repo.db.ExecContext(ctx,
		`UPDATE memory_chunks SET embedding = 'not-valid-base64!!' WHERE memory_id = $1`, corrupt.ID)
	require.NoError(t, err)

	results, err := repo.VectorSearch(ctx, queryVec, wsID, nil, domain.MemorySearchFilter{}, 10)
	require.NoError(t, err, "an unreadable chunk must not fail the recall")

	require.Len(t, results, 1)
	assert.Equal(t, good.ID, results[0].ID)
}

// The legacy branch has to survive the two malformed shapes actually present in
// this column's history: rows written before the encoding was settled, and rows
// whose embedding was blanked rather than nulled. Neither may fail a recall.
//
// It also pins the case where candidates exist but nothing decodes: the result
// is an empty recall, not an error and not a nil-dereference on the way to
// hydration.
func TestVectorSearchDB_UndecodableLegacyVectorsAreSkipped(t *testing.T) {
	repo, _, wsID := setupVectorSearchDBTest(t)
	ctx := context.Background()

	malformed := newMemory(t, repo, wsID, "legacy-malformed")
	_, err := repo.db.ExecContext(ctx,
		`UPDATE memories SET embedding = 'not-json-at-all' WHERE id = $1`, malformed.ID)
	require.NoError(t, err)

	blank := newMemory(t, repo, wsID, "legacy-blank")
	_, err = repo.db.ExecContext(ctx, `UPDATE memories SET embedding = '' WHERE id = $1`, blank.ID)
	require.NoError(t, err)

	results, err := repo.VectorSearch(ctx, queryVec, wsID, nil, domain.MemorySearchFilter{}, 10)
	require.NoError(t, err, "undecodable legacy vectors must not fail the recall")
	assert.Empty(t, results, "candidates that decode to nothing yield an empty result, not an error")

	// A healthy memory alongside them still ranks — the bad rows are skipped
	// individually, they do not poison the batch.
	good := newMemory(t, repo, wsID, "legacy-good")
	require.NoError(t, repo.UpdateEmbedding(ctx, good.ID, vecExact, "test-model", 3))

	results = search(t, repo, wsID, 10)
	require.Len(t, results, 1)
	assert.Equal(t, good.ID, results[0].ID)
}

// An empty candidate set is not an error, and must not reach the chunk query.
func TestVectorSearchDB_EmptyWorkspaceReturnsNoResults(t *testing.T) {
	repo, _, wsID := setupVectorSearchDBTest(t)

	results, err := repo.VectorSearch(context.Background(), queryVec, wsID, nil, domain.MemorySearchFilter{}, 10)
	require.NoError(t, err)
	assert.Empty(t, results)
}
