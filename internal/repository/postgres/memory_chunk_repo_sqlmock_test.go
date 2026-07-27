package postgres

import (
	"context"
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Real-DB tests (memory_chunk_repo_db_test.go) cover the happy paths and the
// FK-violation insert error, which Postgres itself is the natural way to
// trigger. The DELETE and Commit error branches have no such natural trigger
// against a healthy database, so they're covered here with sqlmock instead.

func newChunkRepoMock(t *testing.T) (*MemoryChunkRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewMemoryChunkRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

func TestMemoryChunkRepo_ReplaceChunks_DeleteFails(t *testing.T) {
	repo, mock := newChunkRepoMock(t)
	memID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM memory_chunks").
		WithArgs(memID).
		WillReturnError(errors.New("connection reset"))
	mock.ExpectRollback()

	err := repo.ReplaceChunks(context.Background(), memID, []domain.MemoryChunk{
		{ChunkIdx: 0, ChunkStart: 0, ChunkEnd: 10, Embedding: "x", EmbeddingModel: "m", EmbeddingDim: 4},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "replace chunks: delete")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMemoryChunkRepo_ReplaceChunks_CommitFails(t *testing.T) {
	repo, mock := newChunkRepoMock(t)
	memID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM memory_chunks").
		WithArgs(memID).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit().WillReturnError(errors.New("connection reset"))
	// No ExpectRollback: database/sql marks a Tx done as soon as Commit is
	// attempted, so the deferred Rollback() after a failed Commit returns
	// sql.ErrTxDone without a driver round trip — nothing for sqlmock to see.

	err := repo.ReplaceChunks(context.Background(), memID, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "replace chunks: commit")
	require.NoError(t, mock.ExpectationsWereMet())
}
