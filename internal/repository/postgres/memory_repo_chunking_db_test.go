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
	require.NoError(t, repo.Upsert(ctx, mem))
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
	require.NoError(t, repo.Upsert(ctx, mem))
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
