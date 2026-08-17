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

// CommentMentionRepo.List/CountUnseen had NO deleted_at filtering at any
// level before this fix — not even the task's own, despite already joining
// tasks — so a mention on an individually deleted task (a live, existing
// feature) or one cascaded away by WorkspaceRepo.Delete stayed visible in
// /me/mentions and its unseen badge forever.

func newCommentMentionRepoMock(t *testing.T) (*CommentMentionRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewCommentMentionRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

func TestCommentMentionRepo_List_ExcludesMentionsOnADeletedTask(t *testing.T) {
	repo, mock := newCommentMentionRepoMock(t)
	mentionedID := uuid.New()

	mock.ExpectQuery("FROM comment_mentions cm").
		WithArgs(mentionedID, "user", 50).
		WillReturnRows(sqlmock.NewRows([]string{
			"comment_id", "mentioned_id", "mentioned_kind", "mentioned_slug", "extracted_at", "seen_at",
			"task_id", "task_title", "project_id", "comment_body", "author_id", "author_name",
		}))

	rows, err := repo.List(context.Background(), mentionedID, "user", repository.MentionFilter{})
	require.NoError(t, err)
	assert.Empty(t, rows)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestCommentMentionRepo_List_SQL(t *testing.T) {
	var captured string
	rawDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(
		sqlmock.QueryMatcherFunc(func(_, actualSQL string) error {
			captured = actualSQL
			return nil
		})))
	require.NoError(t, err)
	defer func() { _ = rawDB.Close() }()

	repo := NewCommentMentionRepo(sqlx.NewDb(rawDB, "postgres"))
	mock.ExpectQuery(".*").WillReturnRows(sqlmock.NewRows([]string{
		"comment_id", "mentioned_id", "mentioned_kind", "mentioned_slug", "extracted_at", "seen_at",
		"task_id", "task_title", "project_id", "comment_body", "author_id", "author_name",
	}))

	_, err = repo.List(context.Background(), uuid.New(), "user", repository.MentionFilter{})
	require.NoError(t, err)

	assert.Contains(t, captured, "t.deleted_at IS NULL")
}

func TestCommentMentionRepo_CountUnseen_ExcludesMentionsOnADeletedTask(t *testing.T) {
	repo, mock := newCommentMentionRepoMock(t)
	mentionedID := uuid.New()

	mock.ExpectQuery("FROM comment_mentions cm").
		WithArgs(mentionedID, "user").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	count, err := repo.CountUnseen(context.Background(), mentionedID, "user")
	require.NoError(t, err)
	assert.Zero(t, count)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestCommentMentionRepo_CountUnseen_SQL(t *testing.T) {
	var captured string
	rawDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(
		sqlmock.QueryMatcherFunc(func(_, actualSQL string) error {
			captured = actualSQL
			return nil
		})))
	require.NoError(t, err)
	defer func() { _ = rawDB.Close() }()

	repo := NewCommentMentionRepo(sqlx.NewDb(rawDB, "postgres"))
	mock.ExpectQuery(".*").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	_, err = repo.CountUnseen(context.Background(), uuid.New(), "user")
	require.NoError(t, err)

	assert.Contains(t, captured, "JOIN tasks t ON t.id = c.task_id")
	assert.Contains(t, captured, "t.deleted_at IS NULL")
}
