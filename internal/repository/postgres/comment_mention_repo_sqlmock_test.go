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

// TestCommentMentionRepo_List_FiltersByWorkspace is the regression test for the
// dashboard defect: /me/mentions returned every workspace's rows because
// nothing here joined as far as workspace_id. Without WorkspaceID set, the
// join is present but the WHERE clause is not — see
// TestCommentMentionRepo_List_OmitsTheWorkspaceClauseWhenUnset for the other
// half of that contract.
func TestCommentMentionRepo_List_FiltersByWorkspace(t *testing.T) {
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

	workspaceID := uuid.New()
	mentionedID := uuid.New()
	_, err = repo.List(context.Background(), mentionedID, "user", repository.MentionFilter{WorkspaceID: &workspaceID})
	require.NoError(t, err)

	assert.Contains(t, captured, "JOIN projects p ON p.id = t.project_id")
	assert.Contains(t, captured, "p.workspace_id = $3",
		"workspace_id is the third arg on an otherwise-empty filter: mentionedID, mentionedKind, then this")
}

// TestCommentMentionRepo_List_OmitsTheWorkspaceClauseWhenUnset documents the
// repo-level contract: WorkspaceID is an optional *uuid.UUID here, same as
// ProjectID, and the required-ness lives one layer up in
// parseMentionFilter. A caller that reaches the repo without setting it — as
// every pre-existing test in this file does — gets the old unscoped query,
// not a panic or a zero-UUID filter that matches nothing.
func TestCommentMentionRepo_List_OmitsTheWorkspaceClauseWhenUnset(t *testing.T) {
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

	assert.NotContains(t, captured, "p.workspace_id")
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
