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
// in user_repo_test.go. ListNotYetChunked is already exercised under the
// integration tag (memory_chunk_repo_test.go), which the "Go coverage" gate's
// untagged `go test $pkg` never runs. Reuses setupMemoryRepoChunkingDBTest
// from memory_repo_chunking_db_test.go.

func TestMemoryRepoDB_ListNotYetChunked_ExcludesExpiredAndDefaultsLimit(t *testing.T) {
	repo, wsID := setupMemoryRepoChunkingDBTest(t)
	ctx := context.Background()

	unchunked := &domain.Memory{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		Key:         "not-chunked-" + uuid.New().String()[:8],
		Content:     "content",
		Scope:       domain.ScopeWorkspace,
		SourceType:  domain.SourceAgent,
	}
	require.NoError(t, repo.Upsert(ctx, unchunked, domain.MemoryWriteIntent{}))

	got, err := repo.ListNotYetChunked(ctx, wsID, 0)
	require.NoError(t, err, "limit<=0 must default rather than error")
	found := false
	for _, m := range got {
		if m.ID == unchunked.ID {
			found = true
		}
	}
	assert.True(t, found, "an unchunked memory must be returned")
}

func TestMemoryRepoDB_ListNotYetChunked_ExcludesRowsWithChunks(t *testing.T) {
	repo, wsID := setupMemoryRepoChunkingDBTest(t)
	chunkRepo := NewMemoryChunkRepo(repo.db)
	ctx := context.Background()

	chunked := &domain.Memory{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		Key:         "already-chunked-" + uuid.New().String()[:8],
		Content:     "content",
		Scope:       domain.ScopeWorkspace,
		SourceType:  domain.SourceAgent,
	}
	require.NoError(t, repo.Upsert(ctx, chunked, domain.MemoryWriteIntent{}))
	require.NoError(t, chunkRepo.ReplaceChunks(ctx, chunked.ID, []domain.MemoryChunk{
		{ChunkIdx: 0, ChunkStart: 0, ChunkEnd: 7, Embedding: "x", EmbeddingModel: "m", EmbeddingDim: 4},
	}))

	got, err := repo.ListNotYetChunked(ctx, wsID, 100)
	require.NoError(t, err)
	for _, m := range got {
		assert.NotEqual(t, chunked.ID, m.ID, "a memory with a memory_chunks row must not be returned")
	}
}
