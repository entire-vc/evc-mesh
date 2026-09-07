package postgres

import (
	"context"
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TouchUpdatedAt is what RepeatOpenInstance uses to bump a recurring instance's
// updated_at without touching any other column (task #recurring-no-dup). Three
// outcomes matter: a real row updated, a driver failure surfaced (not
// swallowed), and a missing/soft-deleted task reported as NotFound rather than
// a silent success.

func TestTaskRepo_TouchUpdatedAt_Success(t *testing.T) {
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer rawDB.Close()
	repo := NewTaskRepo(sqlx.NewDb(rawDB, "postgres"))

	taskID := uuid.New()
	mock.ExpectExec("UPDATE tasks SET updated_at").
		WithArgs(taskID).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.TouchUpdatedAt(context.Background(), taskID))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTaskRepo_TouchUpdatedAt_MissingTaskIsNotFound(t *testing.T) {
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer rawDB.Close()
	repo := NewTaskRepo(sqlx.NewDb(rawDB, "postgres"))

	taskID := uuid.New()
	mock.ExpectExec("UPDATE tasks SET updated_at").
		WithArgs(taskID).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err = repo.TouchUpdatedAt(context.Background(), taskID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Task",
		"zero rows affected (deleted or never existed) must surface as NotFound, not a silent success")
}

func TestTaskRepo_TouchUpdatedAt_PropagatesDriverError(t *testing.T) {
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer rawDB.Close()
	repo := NewTaskRepo(sqlx.NewDb(rawDB, "postgres"))

	taskID := uuid.New()
	mock.ExpectExec("UPDATE tasks SET updated_at").
		WithArgs(taskID).
		WillReturnError(errors.New("connection reset"))

	err = repo.TouchUpdatedAt(context.Background(), taskID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection reset")
}
