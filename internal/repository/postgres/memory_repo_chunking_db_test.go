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
