//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// setupMemoryChunkTest creates a workspace + one memory row for the chunk
// repo tests to attach chunks to, and returns the repos under test.
func setupMemoryChunkTest(t *testing.T) (*MemoryChunkRepo, *MemoryRepo, uuid.UUID) {
	t.Helper()
	db := testDB(t)

	wsRepo := NewWorkspaceRepo(db)
	ws := &domain.Workspace{
		ID:      uuid.New(),
		Name:    "chunk-test-ws",
		Slug:    "chunk-test-" + uuid.New().String()[:8],
		OwnerID: uuid.New(),
	}
	require.NoError(t, wsRepo.Create(context.Background(), ws))

	memRepo := NewMemoryRepo(db)
	mem := &domain.Memory{
		ID:          uuid.New(),
		WorkspaceID: ws.ID,
		Key:         "chunk-test-" + uuid.New().String()[:8],
		Content:     "a memory long enough to plausibly get chunked in production",
		Scope:       domain.ScopeWorkspace,
		SourceType:  domain.SourceAgent,
	}
	require.NoError(t, memRepo.Upsert(context.Background(), mem))

	return NewMemoryChunkRepo(db), memRepo, mem.ID
}

func TestMemoryChunkRepo_ReplaceChunks_InsertsAndReturns(t *testing.T) {
	repo, _, memID := setupMemoryChunkTest(t)
	ctx := context.Background()

	chunks := []domain.MemoryChunk{
		{ChunkIdx: 0, ChunkStart: 0, ChunkEnd: 100, Embedding: "b64-chunk-0", EmbeddingModel: "multilingual-e5-small", EmbeddingDim: 384},
		{ChunkIdx: 1, ChunkStart: 80, ChunkEnd: 180, Embedding: "b64-chunk-1", EmbeddingModel: "multilingual-e5-small", EmbeddingDim: 384},
	}
	require.NoError(t, repo.ReplaceChunks(ctx, memID, chunks))

	got, err := repo.ListByMemoryIDs(ctx, []uuid.UUID{memID})
	require.NoError(t, err)
	require.Len(t, got, 2)

	byIdx := map[int]domain.MemoryChunk{}
	for _, c := range got {
		byIdx[c.ChunkIdx] = c
	}
	assert.Equal(t, "b64-chunk-0", byIdx[0].Embedding)
	assert.Equal(t, 0, byIdx[0].ChunkStart)
	assert.Equal(t, 100, byIdx[0].ChunkEnd)
	assert.Equal(t, 384, byIdx[0].EmbeddingDim)
	assert.Equal(t, "b64-chunk-1", byIdx[1].Embedding)
	assert.Equal(t, memID, byIdx[1].MemoryID)
	assert.WithinDuration(t, time.Now(), byIdx[0].CreatedAt, time.Minute)
}

func TestMemoryChunkRepo_ReplaceChunks_IsIdempotent(t *testing.T) {
	repo, _, memID := setupMemoryChunkTest(t)
	ctx := context.Background()

	first := []domain.MemoryChunk{
		{ChunkIdx: 0, ChunkStart: 0, ChunkEnd: 100, Embedding: "v1-chunk-0", EmbeddingModel: "m", EmbeddingDim: 4},
		{ChunkIdx: 1, ChunkStart: 80, ChunkEnd: 180, Embedding: "v1-chunk-1", EmbeddingModel: "m", EmbeddingDim: 4},
		{ChunkIdx: 2, ChunkStart: 160, ChunkEnd: 260, Embedding: "v1-chunk-2", EmbeddingModel: "m", EmbeddingDim: 4},
	}
	require.NoError(t, repo.ReplaceChunks(ctx, memID, first))

	// Re-run with a DIFFERENT chunk count and different content — simulates
	// re-embedding after the memory's content changed. The old rows must not
	// linger; this is the entire idempotency contract.
	second := []domain.MemoryChunk{
		{ChunkIdx: 0, ChunkStart: 0, ChunkEnd: 150, Embedding: "v2-chunk-0", EmbeddingModel: "m", EmbeddingDim: 4},
	}
	require.NoError(t, repo.ReplaceChunks(ctx, memID, second))

	got, err := repo.ListByMemoryIDs(ctx, []uuid.UUID{memID})
	require.NoError(t, err)
	require.Len(t, got, 1, "old chunks must be fully replaced, not accumulated")
	assert.Equal(t, "v2-chunk-0", got[0].Embedding)
}

func TestMemoryChunkRepo_ReplaceChunks_EmptySliceClearsRows(t *testing.T) {
	repo, _, memID := setupMemoryChunkTest(t)
	ctx := context.Background()

	require.NoError(t, repo.ReplaceChunks(ctx, memID, []domain.MemoryChunk{
		{ChunkIdx: 0, ChunkStart: 0, ChunkEnd: 50, Embedding: "x", EmbeddingModel: "m", EmbeddingDim: 4},
	}))
	require.NoError(t, repo.ReplaceChunks(ctx, memID, nil))

	got, err := repo.ListByMemoryIDs(ctx, []uuid.UUID{memID})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestMemoryChunkRepo_DeletingMemoryCascadesChunks(t *testing.T) {
	repo, memRepo, memID := setupMemoryChunkTest(t)
	ctx := context.Background()

	require.NoError(t, repo.ReplaceChunks(ctx, memID, []domain.MemoryChunk{
		{ChunkIdx: 0, ChunkStart: 0, ChunkEnd: 50, Embedding: "x", EmbeddingModel: "m", EmbeddingDim: 4},
	}))

	require.NoError(t, memRepo.Delete(ctx, memID))

	got, err := repo.ListByMemoryIDs(ctx, []uuid.UUID{memID})
	require.NoError(t, err)
	assert.Empty(t, got, "ON DELETE CASCADE must remove chunk rows when the parent memory is deleted")
}

func TestMemoryChunkRepo_MemoryIDsWithChunks(t *testing.T) {
	repoA, _, memWithChunks := setupMemoryChunkTest(t)
	_, _, memWithoutChunks := setupMemoryChunkTest(t)
	ctx := context.Background()

	require.NoError(t, repoA.ReplaceChunks(ctx, memWithChunks, []domain.MemoryChunk{
		{ChunkIdx: 0, ChunkStart: 0, ChunkEnd: 50, Embedding: "x", EmbeddingModel: "m", EmbeddingDim: 4},
	}))

	got, err := repoA.MemoryIDsWithChunks(ctx, []uuid.UUID{memWithChunks, memWithoutChunks})
	require.NoError(t, err)
	assert.True(t, got[memWithChunks])
	assert.False(t, got[memWithoutChunks], "a memory with no chunk rows must not appear as true")
}

func TestMemoryChunkRepo_ListByMemoryIDs_EmptyInput(t *testing.T) {
	repo, _, _ := setupMemoryChunkTest(t)
	got, err := repo.ListByMemoryIDs(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}
