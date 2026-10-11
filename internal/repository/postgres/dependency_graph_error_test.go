package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// A failure of the workspace-scoped edge read must surface as an error: returning a
// graph without its edges would silently show a task as unblocked.
func TestReadGraph_EdgeReadFailureIsReturned(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = raw.Close() }()
	ws, project, actor := uuid.New(), uuid.New(), uuid.New()
	boom := errors.New("edge read failed")

	mock.ExpectBegin()
	mock.ExpectQuery(`graph_read`).WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(ws))
	mock.ExpectQuery(`graph_read`).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`graph_read`).WillReturnRows(sqlmock.NewRows([]string{"id", "task_id", "depends_on_task_id", "dependency_type", "created_at"}))
	mock.ExpectQuery(`graph_read`).WillReturnError(boom)
	mock.ExpectRollback()

	repo := postgres.NewTaskDependencyRepo(sqlx.NewDb(raw, "sqlmock"))
	_, err = repo.ReadGraph(context.Background(), project, uuid.Nil, domain.GraphScope{WorkspaceID: ws, ActorID: actor})
	require.ErrorIs(t, err, boom)
	require.NoError(t, mock.ExpectationsWereMet())
}
