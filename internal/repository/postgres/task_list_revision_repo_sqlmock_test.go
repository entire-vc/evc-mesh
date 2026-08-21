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

// The real-DB tests (task_list_revision_repo_db_test.go, build tag
// `integration`) prove the trigger mechanism against a live Postgres, but
// that file is excluded from `go test ./...` with no build tag — which is
// exactly what the "Go coverage" CI job runs. Without a test in THIS file,
// GetRevision showed 0% coverage on the lines this change introduces, even
// though it is exercised repeatedly under -tags=integration. These sqlmock
// tests pin GetRevision's two branches with no database required.

func newTaskListRevisionRepoMock(t *testing.T) (*TaskListRevisionRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewTaskListRevisionRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

func TestTaskListRevisionRepo_GetRevision_RowExists(t *testing.T) {
	repo, mock := newTaskListRevisionRepoMock(t)
	projectID := uuid.New()

	mock.ExpectQuery("SELECT revision FROM task_list_revisions").
		WithArgs(projectID).
		WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(int64(7)))

	got, err := repo.GetRevision(context.Background(), projectID)
	require.NoError(t, err)
	assert.Equal(t, int64(7), got)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestTaskListRevisionRepo_GetRevision_NoRowDefaultsToZero pins the
// self-healing default from the doc comment: a project with no
// task_list_revisions row yet (never mutated, or the migration just landed)
// reports revision 0 rather than an error — a cursor validator (subtask #6)
// can compare against it unconditionally with no existence check first.
func TestTaskListRevisionRepo_GetRevision_NoRowDefaultsToZero(t *testing.T) {
	repo, mock := newTaskListRevisionRepoMock(t)
	projectID := uuid.New()

	mock.ExpectQuery("SELECT revision FROM task_list_revisions").
		WithArgs(projectID).
		WillReturnRows(sqlmock.NewRows([]string{"revision"}))

	got, err := repo.GetRevision(context.Background(), projectID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), got)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTaskListRevisionRepo_GetRevision_QueryErrorPropagates(t *testing.T) {
	repo, mock := newTaskListRevisionRepoMock(t)
	projectID := uuid.New()
	dbErr := errors.New("connection reset")

	mock.ExpectQuery("SELECT revision FROM task_list_revisions").
		WithArgs(projectID).
		WillReturnError(dbErr)

	_, err := repo.GetRevision(context.Background(), projectID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection reset")
	assert.NoError(t, mock.ExpectationsWereMet())
}
