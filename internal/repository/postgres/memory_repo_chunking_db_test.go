package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// No //go:build integration tag on purpose — see userRepoTestDB's doc comment
// in user_repo_test.go. MarkEmbeddingModel is already exercised under the
// integration tag (memory_chunk_repo_test.go), which the "Go coverage" gate's
// untagged `go test $pkg` never runs.

func setupMemoryRepoChunkingDBTest(t *testing.T) (*MemoryRepo, uuid.UUID) {
	t.Helper()
	db := userRepoTestDB(t)

	wsRepo := NewWorkspaceRepo(db)
	ws := &domain.Workspace{
		ID:      uuid.New(),
		Name:    "chunking-db-test-ws",
		Slug:    "chunking-db-test-" + uuid.New().String()[:8],
		OwnerID: uuid.New(),
	}
	require.NoError(t, wsRepo.Create(context.Background(), ws))

	return NewMemoryRepo(db), ws.ID
}

func TestMemoryRepoDB_MarkEmbeddingModel_SetsModelLeavesVectorUntouched(t *testing.T) {
	repo, wsID := setupMemoryRepoChunkingDBTest(t)
	ctx := context.Background()

	mem := &domain.Memory{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		Key:         "mark-model-" + uuid.New().String()[:8],
		Content:     "content",
		Scope:       domain.ScopeWorkspace,
		SourceType:  domain.SourceAgent,
	}
	require.NoError(t, repo.Upsert(ctx, mem, domain.MemoryWriteIntent{}))
	require.NoError(t, repo.UpdateEmbedding(ctx, mem.ID, []float32{0.1, 0.2}, "old-model", 2))

	require.NoError(t, repo.MarkEmbeddingModel(ctx, mem.ID, "multilingual-e5-small"))

	// GetByID's column list never includes embedding_model/embedding_dim (they're
	// read only by the vector-search path), so query them directly to observe
	// what MarkEmbeddingModel actually wrote.
	var row struct {
		EmbeddingModel string `db:"embedding_model"`
		EmbeddingDim   int    `db:"embedding_dim"`
	}
	require.NoError(t, repo.db.GetContext(ctx, &row,
		`SELECT embedding_model, embedding_dim FROM memories WHERE id = $1`, mem.ID))
	assert.Equal(t, "multilingual-e5-small", row.EmbeddingModel)
	assert.Equal(t, 2, row.EmbeddingDim, "MarkEmbeddingModel must not touch embedding_dim")
}

func TestMemoryRepoDB_MarkEmbeddingModel_UnknownIDIsNotAnError(t *testing.T) {
	repo, _ := setupMemoryRepoChunkingDBTest(t)
	// An UPDATE matching zero rows is not a driver error — same contract as
	// UpdateEmbedding, which this mirrors.
	err := repo.MarkEmbeddingModel(context.Background(), uuid.New(), "m")
	require.NoError(t, err)
}

// newUnembeddedMemory inserts a memory with no embedding and no embedding_model — the
// row shape ListNeedingEmbedding must select by default.
func newUnembeddedMemory(t *testing.T, ctx context.Context, repo *MemoryRepo, wsID uuid.UUID) *domain.Memory {
	t.Helper()
	mem := &domain.Memory{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		Key:         "needs-embedding-" + uuid.New().String()[:8],
		Content:     "content",
		Scope:       domain.ScopeWorkspace,
		SourceType:  domain.SourceAgent,
	}
	require.NoError(t, repo.Upsert(ctx, mem, domain.MemoryWriteIntent{}))
	return mem
}

func TestMemoryRepoDB_ListNeedingEmbedding_SelectsMemoryWithNoEmbeddingAndNoChunks(t *testing.T) {
	repo, wsID := setupMemoryRepoChunkingDBTest(t)
	ctx := context.Background()
	mem := newUnembeddedMemory(t, ctx, repo, wsID)

	got, err := repo.ListNeedingEmbedding(ctx, wsID, "current-model", 100)
	require.NoError(t, err)
	assert.Contains(t, memoryIDs(got), mem.ID)
}

func TestMemoryRepoDB_ListNeedingEmbedding_ExcludesMemoryWithMatchingEmbedding(t *testing.T) {
	repo, wsID := setupMemoryRepoChunkingDBTest(t)
	ctx := context.Background()
	mem := newUnembeddedMemory(t, ctx, repo, wsID)
	require.NoError(t, repo.UpdateEmbedding(ctx, mem.ID, []float32{0.1, 0.2}, "current-model", 2))

	got, err := repo.ListNeedingEmbedding(ctx, wsID, "current-model", 100)
	require.NoError(t, err)
	assert.NotContains(t, memoryIDs(got), mem.ID)
}

// TestMemoryRepoDB_ListNeedingEmbedding_ExcludesMemoryWithMatchingModelChunksAlone is the
// regression test for #84b0694d/#7cf0f3be. It writes ONLY chunk rows — no
// memories.embedding, no embedding_model watermark on the memories row — the exact shape
// the original chunked-embed design intended once the read path (ADR-0002 subtask 6/8)
// lands and the memories.embedding stopgap write is removed. Before the NOT EXISTS clause
// was added to ListNeedingEmbedding's query, this memory had embedding IS NULL forever and
// was selected on every call — reindex re-embedded it every run without converging. This
// test is RED against the query prior to that fix (only the embedding-column OR clause,
// no chunk-freshness check) and green after.
func TestMemoryRepoDB_ListNeedingEmbedding_ExcludesMemoryWithMatchingModelChunksAlone(t *testing.T) {
	repo, wsID := setupMemoryRepoChunkingDBTest(t)
	chunkRepo := NewMemoryChunkRepo(repo.db)
	ctx := context.Background()
	mem := newUnembeddedMemory(t, ctx, repo, wsID)

	require.NoError(t, chunkRepo.ReplaceChunks(ctx, mem.ID, []domain.MemoryChunk{
		{ChunkIdx: 0, ChunkStart: 0, ChunkEnd: 7, Embedding: "x", EmbeddingModel: "current-model", EmbeddingDim: 4},
	}))

	got, err := repo.ListNeedingEmbedding(ctx, wsID, "current-model", 100)
	require.NoError(t, err)
	assert.NotContains(t, memoryIDs(got), mem.ID, "a memory with current-model chunks must not be re-selected even though memories.embedding is still NULL")

	// Convergence check, mirroring the acceptance criterion directly: calling
	// ListNeedingEmbedding a second time with nothing changed must not re-select it either
	// — this is what "reindex converges" means at the query level.
	got2, err := repo.ListNeedingEmbedding(ctx, wsID, "current-model", 100)
	require.NoError(t, err)
	assert.NotContains(t, memoryIDs(got2), mem.ID)
}

func TestMemoryRepoDB_ListNeedingEmbedding_SelectsMemoryWhoseChunksAreFromDifferentModel(t *testing.T) {
	repo, wsID := setupMemoryRepoChunkingDBTest(t)
	chunkRepo := NewMemoryChunkRepo(repo.db)
	ctx := context.Background()
	mem := newUnembeddedMemory(t, ctx, repo, wsID)

	require.NoError(t, chunkRepo.ReplaceChunks(ctx, mem.ID, []domain.MemoryChunk{
		{ChunkIdx: 0, ChunkStart: 0, ChunkEnd: 7, Embedding: "x", EmbeddingModel: "old-model", EmbeddingDim: 4},
	}))

	got, err := repo.ListNeedingEmbedding(ctx, wsID, "current-model", 100)
	require.NoError(t, err)
	assert.Contains(t, memoryIDs(got), mem.ID, "chunks from a stale model must not shield a memory from re-embedding")
}

// TestMemoryRepoDB_ListNeedingEmbedding_ExcludesMemoryAfterStopgapEmbedChunkedFlow mirrors
// embedChunked's actual current write sequence (ReplaceChunks then UpdateEmbedding with the
// first chunk's vector, see memory_service.go) end to end through the real repos, without
// going through the service layer's embedder — the two DB writes are what matter here.
func TestMemoryRepoDB_ListNeedingEmbedding_ExcludesMemoryAfterStopgapEmbedChunkedFlow(t *testing.T) {
	repo, wsID := setupMemoryRepoChunkingDBTest(t)
	chunkRepo := NewMemoryChunkRepo(repo.db)
	ctx := context.Background()
	mem := newUnembeddedMemory(t, ctx, repo, wsID)

	require.NoError(t, chunkRepo.ReplaceChunks(ctx, mem.ID, []domain.MemoryChunk{
		{ChunkIdx: 0, ChunkStart: 0, ChunkEnd: 7, Embedding: "x", EmbeddingModel: "current-model", EmbeddingDim: 4},
	}))
	require.NoError(t, repo.UpdateEmbedding(ctx, mem.ID, []float32{0.1, 0.2, 0.3, 0.4}, "current-model", 4))

	got, err := repo.ListNeedingEmbedding(ctx, wsID, "current-model", 100)
	require.NoError(t, err)
	assert.NotContains(t, memoryIDs(got), mem.ID)
}

func memoryIDs(mems []domain.Memory) []uuid.UUID {
	ids := make([]uuid.UUID, len(mems))
	for i, m := range mems {
		ids[i] = m.ID
	}
	return ids
}

// TestMemoryRepoDB_ListNeedingEmbedding_StopgapRemovedDoesNotSelectWholeChunkedCorpus is the
// acceptance criterion for #dd0fda98 stated directly: simulate the future state where the
// stopgap write is gone (chunks written, memories.embedding left NULL — embedChunked's
// ORIGINAL shape) across a whole corpus, and assert reindex's selector does not sweep all of
// it back up every run.
//
// Deliberately a corpus, not a single row: with the NOT EXISTS branch reverted the predicate
// selects EVERY one of these rows, so the assertion moves from 0 to corpusSize. A one-row
// NotContains would also fail on that mutation, but it cannot distinguish "the guard is gone"
// from "this particular row leaked" — and the cost being guarded against is proportional to
// corpus size (every chunk of every memory re-embedded, every run, forever).
func TestMemoryRepoDB_ListNeedingEmbedding_StopgapRemovedDoesNotSelectWholeChunkedCorpus(t *testing.T) {
	repo, wsID := setupMemoryRepoChunkingDBTest(t)
	chunkRepo := NewMemoryChunkRepo(repo.db)
	ctx := context.Background()

	const corpusSize = 5
	chunked := make(map[uuid.UUID]bool, corpusSize)
	for i := 0; i < corpusSize; i++ {
		mem := newUnembeddedMemory(t, ctx, repo, wsID)
		// Chunks only — no UpdateEmbedding. This is exactly what embedChunked did before the
		// stopgap, and what it would do again if someone removes the now-redundant-for-ranking
		// memories.embedding write after the chunk read-path (#396) landed.
		require.NoError(t, chunkRepo.ReplaceChunks(ctx, mem.ID, []domain.MemoryChunk{
			{ChunkIdx: 0, ChunkStart: 0, ChunkEnd: 7, Embedding: "x", EmbeddingModel: "current-model", EmbeddingDim: 4},
		}))
		chunked[mem.ID] = true
	}

	// A row genuinely needing work must still be found, so the assertion below cannot pass
	// trivially by the query returning nothing at all.
	needsWork := newUnembeddedMemory(t, ctx, repo, wsID)

	got, err := repo.ListNeedingEmbedding(ctx, wsID, "current-model", 100)
	require.NoError(t, err)

	var reselected int
	for _, id := range memoryIDs(got) {
		if chunked[id] {
			reselected++
		}
	}
	assert.Zero(t, reselected,
		"stopgap-removed corpus: %d/%d chunked memories were re-selected; with the chunk-freshness branch gone this is the whole corpus, and reindex re-embeds every chunk of every memory on every run without ever converging",
		reselected, corpusSize)
	assert.Contains(t, memoryIDs(got), needsWork.ID,
		"a memory with neither embedding nor chunks must still be selected — otherwise the guard is over-broad and nothing ever gets embedded")
}

// TestMemoryRepoDB_StopgapWrite_PopulatesEmbeddingColumnNotJustWatermark pins the stopgap
// contract itself (#7cf0f3be): embedChunked must write memories.embedding, not only a
// watermark. It asserts the COLUMN, deliberately, instead of asserting through
// ListNeedingEmbedding.
//
// Why not through the selector: since the chunk-freshness guard (#dd0fda98) landed, the
// selector excludes a chunked memory on the chunk branch alone, so reverting
// UpdateEmbedding -> MarkEmbeddingModel leaves every selector-based test GREEN. Verified by
// mutation, not assumed. That masking is the guard working as intended — defence in depth —
// but it means the selector can no longer witness the stopgap regression, and a criterion
// phrased "red when UpdateEmbedding is reverted" cannot be met by a selector test while the
// guard is in main.
//
// So this test witnesses the write directly. It goes red the moment the stopgap write is
// removed, which is exactly when a human should be made to think about whether dense-arm
// visibility still holds — the guard protects reindex convergence, NOT visibility.
func TestMemoryRepoDB_StopgapWrite_PopulatesEmbeddingColumnNotJustWatermark(t *testing.T) {
	repo, wsID := setupMemoryRepoChunkingDBTest(t)
	chunkRepo := NewMemoryChunkRepo(repo.db)
	ctx := context.Background()
	mem := newUnembeddedMemory(t, ctx, repo, wsID)

	// embedChunked's real write sequence (memory_service.go): chunks, then the first chunk's
	// vector into memories.embedding via UpdateEmbedding.
	require.NoError(t, chunkRepo.ReplaceChunks(ctx, mem.ID, []domain.MemoryChunk{
		{ChunkIdx: 0, ChunkStart: 0, ChunkEnd: 7, Embedding: "x", EmbeddingModel: "current-model", EmbeddingDim: 4},
	}))
	require.NoError(t, repo.UpdateEmbedding(ctx, mem.ID, []float32{0.1, 0.2, 0.3, 0.4}, "current-model", 4))

	var embedding *string
	var model *string
	var dim *int
	require.NoError(t, repo.db.QueryRowContext(ctx,
		`SELECT embedding, embedding_model, embedding_dim FROM memories WHERE id = $1`, mem.ID,
	).Scan(&embedding, &model, &dim))

	require.NotNil(t, embedding, "memories.embedding must be populated by the chunked write path — a watermark-only write leaves this NULL and VectorSearch requires embedding IS NOT NULL (live regression #7cf0f3be)")
	assert.NotEmpty(t, *embedding)
	require.NotNil(t, model)
	assert.Equal(t, "current-model", *model)
	require.NotNil(t, dim)
	assert.Equal(t, 4, *dim, "embedding_dim must come from the actual vector length, not from config (the env-sourced value is why the column was 0 on all prod rows)")
}
