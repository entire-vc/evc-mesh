package postgres

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// GetByID exists so Delete can read an is_child_of edge before removing it and
// undo the parent_task_id that edge set. Its "no row" contract is the load-bearing
// part: Delete treats (nil, nil) as "nothing to undo" and carries on, so a
// sql.ErrNoRows leaking out as an error would turn an ordinary delete into a 500.
// These pin that contract without needing a database.

func newTaskDependencyRepoMock(t *testing.T) (*TaskDependencyRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewTaskDependencyRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

func TestTaskDependencyRepo_GetByID(t *testing.T) {
	ctx := context.Background()
	const q = `SELECT ` + taskDependencySelectCols + ` FROM task_dependencies WHERE id = $1`

	t.Run("returns the edge with its type and endpoints", func(t *testing.T) {
		repo, mock := newTaskDependencyRepoMock(t)
		id, taskID, parentID := uuid.New(), uuid.New(), uuid.New()
		now := time.Now().UTC()

		mock.ExpectQuery(regexp.QuoteMeta(q)).
			WithArgs(id).
			WillReturnRows(sqlmock.NewRows(
				[]string{"id", "task_id", "depends_on_task_id", "dependency_type", "created_at"},
			).AddRow(id, taskID, parentID, string(domain.DependencyTypeIsChildOf), now))

		dep, err := repo.GetByID(ctx, id)

		require.NoError(t, err)
		require.NotNil(t, dep)
		assert.Equal(t, id, dep.ID)
		// Delete dispatches on all three of these, so a mis-scan would silently
		// un-parent the wrong task or skip the undo entirely.
		assert.Equal(t, taskID, dep.TaskID)
		assert.Equal(t, parentID, dep.DependsOnTaskID)
		assert.Equal(t, domain.DependencyTypeIsChildOf, dep.DependencyType)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("missing row is (nil, nil), not an error", func(t *testing.T) {
		repo, mock := newTaskDependencyRepoMock(t)
		id := uuid.New()

		// An empty result set, so the driver raises sql.ErrNoRows itself rather
		// than the test asserting against a hand-made error.
		mock.ExpectQuery(regexp.QuoteMeta(q)).WithArgs(id).
			WillReturnRows(sqlmock.NewRows(
				[]string{"id", "task_id", "depends_on_task_id", "dependency_type", "created_at"},
			))

		dep, err := repo.GetByID(ctx, id)

		require.NoError(t, err,
			"sql.ErrNoRows must not escape — Delete reads (nil, nil) as 'nothing to undo'")
		assert.Nil(t, dep)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("a real driver error propagates", func(t *testing.T) {
		repo, mock := newTaskDependencyRepoMock(t)
		id := uuid.New()
		boom := errors.New("connection reset")

		mock.ExpectQuery(regexp.QuoteMeta(q)).WithArgs(id).WillReturnError(boom)

		dep, err := repo.GetByID(ctx, id)

		require.Error(t, err, "a genuine failure must not be flattened into 'no such edge'")
		assert.Nil(t, dep)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}
