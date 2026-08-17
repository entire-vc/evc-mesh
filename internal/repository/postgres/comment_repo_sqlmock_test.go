package postgres

import (
	"context"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository"
)

// The real-DB tests (comment_repo_test.go, build tag `integration`) cover
// ordering/pagination against a live Postgres. These sqlmock tests pin the
// one thing that needs no database to prove: that ListByAuthor's query text
// actually excludes a deleted task/project, since /me/comments is reached
// with no ws_id path parameter — nothing upstream of this query can catch a
// row from a workspace that got deleted (whose projects/tasks are cascaded
// to deleted_at by WorkspaceRepo.Delete).

func newCommentRepoMock(t *testing.T) (*CommentRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewCommentRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

func TestListByAuthor_ExcludesCommentsOnADeletedTaskOrProject(t *testing.T) {
	repo, mock := newCommentRepoMock(t)
	authorID := uuid.New()

	mock.ExpectQuery("FROM comments c").
		WithArgs(authorID, 50).
		WillReturnRows(sqlmock.NewRows([]string{
			"comment_id", "task_id", "task_title", "project_id", "project_name",
			"comment_body", "author_id", "author_kind", "author_name", "created_at", "updated_at",
		}))

	rows, cursor, err := repo.ListByAuthor(context.Background(), authorID, repository.CommentViewFilter{})
	require.NoError(t, err)
	assert.Empty(t, rows)
	assert.Nil(t, cursor)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestListByAuthor_SQL(t *testing.T) {
	var captured string
	rawDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(
		sqlmock.QueryMatcherFunc(func(_, actualSQL string) error {
			captured = actualSQL
			return nil
		})))
	require.NoError(t, err)
	defer func() { _ = rawDB.Close() }()

	repo := NewCommentRepo(sqlx.NewDb(rawDB, "postgres"))
	mock.ExpectQuery(".*").WillReturnRows(sqlmock.NewRows([]string{
		"comment_id", "task_id", "task_title", "project_id", "project_name",
		"comment_body", "author_id", "author_kind", "author_name", "created_at", "updated_at",
	}))

	_, _, err = repo.ListByAuthor(context.Background(), uuid.New(), repository.CommentViewFilter{})
	require.NoError(t, err)

	assert.Contains(t, captured, "JOIN tasks t ON t.id = c.task_id AND t.deleted_at IS NULL")
	assert.Contains(t, captured, "JOIN projects p ON p.id = t.project_id AND p.deleted_at IS NULL")
}

func TestListRecentByWorkspace_SQL(t *testing.T) {
	// commentViewSelect is shared by both callers — pin the same guarantee
	// for the workspace activity feed, not just /me/comments.
	var captured string
	rawDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(
		sqlmock.QueryMatcherFunc(func(_, actualSQL string) error {
			captured = actualSQL
			return nil
		})))
	require.NoError(t, err)
	defer func() { _ = rawDB.Close() }()

	repo := NewCommentRepo(sqlx.NewDb(rawDB, "postgres"))
	mock.ExpectQuery(".*").WillReturnRows(sqlmock.NewRows([]string{
		"comment_id", "task_id", "task_title", "project_id", "project_name",
		"comment_body", "author_id", "author_kind", "author_name", "created_at", "updated_at",
	}))

	_, _, err = repo.ListRecentByWorkspace(context.Background(), uuid.New(), repository.CommentViewFilter{})
	require.NoError(t, err)

	assert.Contains(t, captured, "JOIN tasks t ON t.id = c.task_id AND t.deleted_at IS NULL")
	assert.Contains(t, captured, "JOIN projects p ON p.id = t.project_id AND p.deleted_at IS NULL")
}
