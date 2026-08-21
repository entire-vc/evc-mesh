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
	require.NoError(t, memRepo.Upsert(context.Background(), mem, domain.MemoryWriteIntent{}))

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

// TestMemoryRepo_ListNotYetChunked_IndependentOfEmbeddingModel proves the backfill
// selection query (subtask 7) picks a memory purely on missing chunk rows — even one
// already carrying the CURRENT model's embedding_model watermark from the pre-chunking
// single-vector path, which is the real state of every existing row before this feature
// ships. ListNeedingEmbedding's own filter would never select such a row.
func TestMemoryRepo_ListNotYetChunked_IndependentOfEmbeddingModel(t *testing.T) {
	chunkRepo, memRepo, chunkedID := setupMemoryChunkTest(t)
	ctx := context.Background()
	db := testDB(t)

	// chunkedID already has chunks (from setupMemoryChunkTest's caller pattern) —
	// give it one so it's excluded.
	require.NoError(t, chunkRepo.ReplaceChunks(ctx, chunkedID, []domain.MemoryChunk{
		{ChunkIdx: 0, ChunkStart: 0, ChunkEnd: 10, Embedding: "x", EmbeddingModel: "m", EmbeddingDim: 4},
	}))
	chunkedMem, err := memRepo.GetByID(ctx, chunkedID)
	require.NoError(t, err)
	wsID := chunkedMem.WorkspaceID

	// A second memory in the SAME workspace, with no chunks, but already watermarked
	// with the "current" model — the exact scenario that defeats ListNeedingEmbedding.
	unchunked := &domain.Memory{
		ID:             uuid.New(),
		WorkspaceID:    wsID,
		Key:            "not-yet-chunked-" + uuid.New().String()[:8],
		Content:        "already has embedding_model set from the legacy single-vector path",
		Scope:          domain.ScopeWorkspace,
		SourceType:     domain.SourceAgent,
		EmbeddingModel: "multilingual-e5-small",
	}
	require.NoError(t, memRepo.Upsert(ctx, unchunked, domain.MemoryWriteIntent{}))
	// Upsert doesn't necessarily write embedding_model on insert depending on its column
	// list — set it explicitly via the same path production code uses, so this test
	// reflects the real pre-chunking row shape regardless of Upsert's exact column set.
	_, err = db.ExecContext(ctx, `UPDATE memories SET embedding_model = $1 WHERE id = $2`, "multilingual-e5-small", unchunked.ID)
	require.NoError(t, err)

	got, err := memRepo.ListNotYetChunked(ctx, wsID, 100)
	require.NoError(t, err)

	var gotIDs []uuid.UUID
	for _, m := range got {
		gotIDs = append(gotIDs, m.ID)
	}
	assert.Contains(t, gotIDs, unchunked.ID, "an unchunked memory must be selected even though embedding_model already matches the current model")
	assert.NotContains(t, gotIDs, chunkedID, "a memory that already has chunk rows must not be reselected")
}
