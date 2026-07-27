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

// No //go:build integration tag on purpose — MemoryChunkRepo's own coverage was
// otherwise 0% measured (the existing integration-tagged suite in
// memory_chunk_repo_test.go is excluded from the "Go coverage" gate, which runs
// `go test $pkg` with no -tags). Uses the same DATABASE_URL / graceful-skip
// convention as userRepoTestDB in user_repo_test.go, so it runs for real in CI
// (which sets DATABASE_URL) and skips cleanly with no DB reachable locally.

func setupMemoryChunkDBTest(t *testing.T) (*MemoryChunkRepo, *MemoryRepo, uuid.UUID) {
	t.Helper()
	db := userRepoTestDB(t)

	wsRepo := NewWorkspaceRepo(db)
	ws := &domain.Workspace{
		ID:      uuid.New(),
		Name:    "chunk-db-test-ws",
		Slug:    "chunk-db-test-" + uuid.New().String()[:8],
		OwnerID: uuid.New(),
	}
	require.NoError(t, wsRepo.Create(context.Background(), ws))

	memRepo := NewMemoryRepo(db)
	mem := &domain.Memory{
		ID:          uuid.New(),
		WorkspaceID: ws.ID,
		Key:         "chunk-db-test-" + uuid.New().String()[:8],
		Content:     "a memory long enough to plausibly get chunked in production",
		Scope:       domain.ScopeWorkspace,
		SourceType:  domain.SourceAgent,
	}
	require.NoError(t, memRepo.Upsert(context.Background(), mem))

	return NewMemoryChunkRepo(db), memRepo, mem.ID
}

func TestMemoryChunkRepoDB_ReplaceChunks_InsertsAndLists(t *testing.T) {
	repo, _, memID := setupMemoryChunkDBTest(t)
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
	assert.Equal(t, memID, byIdx[1].MemoryID)
	assert.WithinDuration(t, time.Now(), byIdx[0].CreatedAt, time.Minute)
}

func TestMemoryChunkRepoDB_ReplaceChunks_IsIdempotent(t *testing.T) {
	repo, _, memID := setupMemoryChunkDBTest(t)
	ctx := context.Background()

	first := []domain.MemoryChunk{
		{ChunkIdx: 0, ChunkStart: 0, ChunkEnd: 100, Embedding: "v1-chunk-0", EmbeddingModel: "m", EmbeddingDim: 4},
		{ChunkIdx: 1, ChunkStart: 80, ChunkEnd: 180, Embedding: "v1-chunk-1", EmbeddingModel: "m", EmbeddingDim: 4},
	}
	require.NoError(t, repo.ReplaceChunks(ctx, memID, first))

	second := []domain.MemoryChunk{
		{ChunkIdx: 0, ChunkStart: 0, ChunkEnd: 150, Embedding: "v2-chunk-0", EmbeddingModel: "m", EmbeddingDim: 4},
	}
	require.NoError(t, repo.ReplaceChunks(ctx, memID, second))

	got, err := repo.ListByMemoryIDs(ctx, []uuid.UUID{memID})
	require.NoError(t, err)
	require.Len(t, got, 1, "old chunks must be fully replaced, not accumulated")
	assert.Equal(t, "v2-chunk-0", got[0].Embedding)
}

func TestMemoryChunkRepoDB_ReplaceChunks_EmptySliceClearsRows(t *testing.T) {
	repo, _, memID := setupMemoryChunkDBTest(t)
	ctx := context.Background()

	require.NoError(t, repo.ReplaceChunks(ctx, memID, []domain.MemoryChunk{
		{ChunkIdx: 0, ChunkStart: 0, ChunkEnd: 50, Embedding: "x", EmbeddingModel: "m", EmbeddingDim: 4},
	}))
	require.NoError(t, repo.ReplaceChunks(ctx, memID, nil))

	got, err := repo.ListByMemoryIDs(ctx, []uuid.UUID{memID})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestMemoryChunkRepoDB_MemoryIDsWithChunks(t *testing.T) {
	repoA, _, memWithChunks := setupMemoryChunkDBTest(t)
	_, _, memWithoutChunks := setupMemoryChunkDBTest(t)
	ctx := context.Background()

	require.NoError(t, repoA.ReplaceChunks(ctx, memWithChunks, []domain.MemoryChunk{
		{ChunkIdx: 0, ChunkStart: 0, ChunkEnd: 50, Embedding: "x", EmbeddingModel: "m", EmbeddingDim: 4},
	}))

	got, err := repoA.MemoryIDsWithChunks(ctx, []uuid.UUID{memWithChunks, memWithoutChunks})
	require.NoError(t, err)
	assert.True(t, got[memWithChunks])
	assert.False(t, got[memWithoutChunks], "a memory with no chunk rows must not appear as true")
}

func TestMemoryChunkRepoDB_ListByMemoryIDs_EmptyInput(t *testing.T) {
	repo, _, _ := setupMemoryChunkDBTest(t)
	got, err := repo.ListByMemoryIDs(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestMemoryChunkRepoDB_MemoryIDsWithChunks_EmptyInput(t *testing.T) {
	repo, _, _ := setupMemoryChunkDBTest(t)
	got, err := repo.MemoryIDsWithChunks(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestMemoryChunkRepoDB_ReplaceChunks_UnknownMemoryIDFailsInsert(t *testing.T) {
	repo, _, _ := setupMemoryChunkDBTest(t)

	err := repo.ReplaceChunks(context.Background(), uuid.New(), []domain.MemoryChunk{
		{ChunkIdx: 0, ChunkStart: 0, ChunkEnd: 10, Embedding: "x", EmbeddingModel: "m", EmbeddingDim: 4},
	})
	require.Error(t, err, "inserting a chunk for a memory_id with no matching memories row must fail the FK constraint")
}

func TestMemoryChunkRepoDB_ReplaceChunks_CanceledContext(t *testing.T) {
	repo, _, memID := setupMemoryChunkDBTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := repo.ReplaceChunks(ctx, memID, []domain.MemoryChunk{
		{ChunkIdx: 0, ChunkStart: 0, ChunkEnd: 10, Embedding: "x", EmbeddingModel: "m", EmbeddingDim: 4},
	})
	require.Error(t, err)
}

func TestMemoryChunkRepoDB_ListByMemoryIDs_CanceledContext(t *testing.T) {
	repo, _, memID := setupMemoryChunkDBTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := repo.ListByMemoryIDs(ctx, []uuid.UUID{memID})
	require.Error(t, err)
}

func TestMemoryChunkRepoDB_MemoryIDsWithChunks_CanceledContext(t *testing.T) {
	repo, _, memID := setupMemoryChunkDBTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := repo.MemoryIDsWithChunks(ctx, []uuid.UUID{memID})
	require.Error(t, err)
}
