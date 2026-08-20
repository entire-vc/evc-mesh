package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// The document-mention inbox is written against the same three queries as the
// task one, which is the point — but it joins one table further (mention ->
// comment -> document), and each extra hop is a place a deleted_at filter can be
// left out. CommentMentionRepo shipped exactly that omission: its List and
// CountUnseen already joined tasks and still did not filter t.deleted_at, so a
// mention on a deleted task stayed in the inbox and in the badge forever. These
// tests read the SQL back rather than trusting that the same thing was not done
// twice.

// errDBUnavailable stands in for any database failure the repository must
// surface rather than swallow.
var errDBUnavailable = errors.New("database unavailable")

func newDocumentCommentMentionRepoMock(t *testing.T) (*DocumentCommentMentionRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewDocumentCommentMentionRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

var documentMentionViewColumns = []string{
	"comment_id", "mentioned_id", "mentioned_kind", "mentioned_slug", "extracted_at", "seen_at",
	"document_id", "document_title", "document_slug", "project_id",
	"comment_body", "author_id", "author_name",
}

// captureSQL runs fn against a mock that records the statement instead of
// matching it, and returns what the repository actually sent.
func captureSQL(t *testing.T, isQuery bool, fn func(*DocumentCommentMentionRepo)) string {
	t.Helper()

	var captured string
	rawDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(
		sqlmock.QueryMatcherFunc(func(_, actualSQL string) error {
			captured = actualSQL
			return nil
		})))
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })

	if isQuery {
		mock.ExpectQuery(".*").WillReturnRows(sqlmock.NewRows(documentMentionViewColumns))
	} else {
		mock.ExpectExec(".*").WillReturnResult(sqlmock.NewResult(0, 1))
	}

	fn(NewDocumentCommentMentionRepo(sqlx.NewDb(rawDB, "postgres")))
	return captured
}

func TestDocumentCommentMentionRepo_InsertBatch_IsANoOpWhenEmpty(t *testing.T) {
	repo, mock := newDocumentCommentMentionRepoMock(t)

	require.NoError(t, repo.InsertBatch(context.Background(), nil))

	assert.NoError(t, mock.ExpectationsWereMet(), "an empty batch must not touch the database")
}

func TestDocumentCommentMentionRepo_InsertBatch_WritesOneRowPerRecipient(t *testing.T) {
	repo, mock := newDocumentCommentMentionRepoMock(t)
	commentID, agentID, userID := uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()

	for _, id := range []uuid.UUID{agentID, userID} {
		mock.ExpectExec("INSERT INTO document_comment_mentions").
			WithArgs(commentID, id, sqlmock.AnyArg(), sqlmock.AnyArg(), now).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}

	err := repo.InsertBatch(context.Background(), []domain.DocumentCommentMention{
		{CommentID: commentID, MentionedID: agentID, MentionedKind: "agent", MentionedSlug: "daedalus", ExtractedAt: now},
		{CommentID: commentID, MentionedID: userID, MentionedKind: "user", MentionedSlug: "pavel", ExtractedAt: now},
	})

	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestDocumentCommentMentionRepo_InsertBatch_DoesNotResetSeenAt: re-running
// extraction over an edited body must not raise on the primary key, and must not
// mark a mention the recipient already read as unread again.
func TestDocumentCommentMentionRepo_InsertBatch_DoesNotResetSeenAt(t *testing.T) {
	sql := captureSQL(t, false, func(repo *DocumentCommentMentionRepo) {
		_ = repo.InsertBatch(context.Background(), []domain.DocumentCommentMention{{
			CommentID: uuid.New(), MentionedID: uuid.New(), MentionedKind: "user", MentionedSlug: "pavel",
		}})
	})

	assert.Contains(t, sql, "ON CONFLICT (comment_id, mentioned_id) DO NOTHING")
	assert.NotContains(t, sql, "seen_at",
		"seen_at must not be written on insert — DO UPDATE here would un-read a read mention")
}

func TestDocumentCommentMentionRepo_InsertBatch_SurfacesAWriteFailure(t *testing.T) {
	repo, mock := newDocumentCommentMentionRepoMock(t)
	mock.ExpectExec("INSERT INTO document_comment_mentions").
		WillReturnError(errDBUnavailable)

	err := repo.InsertBatch(context.Background(), []domain.DocumentCommentMention{{
		CommentID: uuid.New(), MentionedID: uuid.New(), MentionedKind: "user", MentionedSlug: "pavel",
	}})

	assert.ErrorIs(t, err, errDBUnavailable)
}

func TestDocumentCommentMentionRepo_List_DefaultsToFiftyForTheCallersOwnRows(t *testing.T) {
	repo, mock := newDocumentCommentMentionRepoMock(t)
	mentionedID := uuid.New()

	mock.ExpectQuery("FROM document_comment_mentions dcm").
		WithArgs(mentionedID, "user", 50).
		WillReturnRows(sqlmock.NewRows(documentMentionViewColumns))

	rows, err := repo.List(context.Background(), mentionedID, "user", repository.MentionFilter{})

	require.NoError(t, err)
	assert.Empty(t, rows)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentCommentMentionRepo_List_ReturnsTheEnrichedRow(t *testing.T) {
	repo, mock := newDocumentCommentMentionRepoMock(t)
	mentionedID, commentID, documentID, projectID, authorID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()

	mock.ExpectQuery("FROM document_comment_mentions dcm").
		WillReturnRows(sqlmock.NewRows(documentMentionViewColumns).AddRow(
			commentID, mentionedID, "user", "pavel", time.Now().UTC(), nil,
			documentID, "Runbook", "runbook", projectID,
			"@pavel does this still hold?", authorID, "Ann",
		))

	rows, err := repo.List(context.Background(), mentionedID, "user", repository.MentionFilter{})

	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, documentID, rows[0].DocumentID)
	assert.Equal(t, "Runbook", rows[0].DocumentTitle, "the inbox has to be able to name the page")
	assert.Equal(t, "Ann", rows[0].AuthorName)
	assert.Nil(t, rows[0].SeenAt)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentCommentMentionRepo_List_SurfacesAQueryFailure(t *testing.T) {
	repo, mock := newDocumentCommentMentionRepoMock(t)
	mock.ExpectQuery("FROM document_comment_mentions dcm").WillReturnError(errDBUnavailable)

	_, err := repo.List(context.Background(), uuid.New(), "user", repository.MentionFilter{})

	assert.ErrorIs(t, err, errDBUnavailable)
}

// TestDocumentCommentMentionRepo_List_HidesRowsWhoseTargetIsGone is the
// regression the task-side repository needed a fix for: an inbox must not offer
// to open a comment or a page that has been deleted.
func TestDocumentCommentMentionRepo_List_HidesRowsWhoseTargetIsGone(t *testing.T) {
	sql := captureSQL(t, true, func(repo *DocumentCommentMentionRepo) {
		_, _ = repo.List(context.Background(), uuid.New(), "user", repository.MentionFilter{})
	})

	assert.Contains(t, sql, "dc.deleted_at IS NULL", "a mention on a deleted comment must not be listed")
	assert.Contains(t, sql, "d.deleted_at IS NULL", "a mention on a deleted document must not be listed")
	assert.Contains(t, sql, "ORDER BY dcm.extracted_at DESC")
}

func TestDocumentCommentMentionRepo_List_AppliesEveryFilter(t *testing.T) {
	since := time.Now().UTC().Add(-time.Hour)
	projectID := uuid.New()
	seen := true

	sql := captureSQL(t, true, func(repo *DocumentCommentMentionRepo) {
		_, _ = repo.List(context.Background(), uuid.New(), "agent", repository.MentionFilter{
			Seen: &seen, Since: &since, ProjectID: &projectID, Limit: 7,
		})
	})

	assert.Contains(t, sql, "dcm.seen_at IS NOT NULL")
	assert.Contains(t, sql, "dcm.extracted_at > $3")
	assert.Contains(t, sql, "d.project_id = $4")
	assert.Contains(t, sql, "LIMIT $5", "the limit is always the last placeholder, whatever precedes it")
}

func TestDocumentCommentMentionRepo_List_UnseenFilterIsTheOtherDirection(t *testing.T) {
	unseen := false

	sql := captureSQL(t, true, func(repo *DocumentCommentMentionRepo) {
		_, _ = repo.List(context.Background(), uuid.New(), "user", repository.MentionFilter{Seen: &unseen})
	})

	assert.Contains(t, sql, "dcm.seen_at IS NULL")
	assert.NotContains(t, sql, "dcm.seen_at IS NOT NULL")
}

// TestDocumentCommentMentionRepo_MarkSeen_OnlyTheFirstMarkWins: without the
// `seen_at IS NULL` guard a replayed request would move the timestamp, and the
// row would report having been read at whatever moment the retry landed.
func TestDocumentCommentMentionRepo_MarkSeen_OnlyTheFirstMarkWins(t *testing.T) {
	repo, mock := newDocumentCommentMentionRepoMock(t)
	commentID, mentionedID := uuid.New(), uuid.New()

	mock.ExpectExec("UPDATE document_comment_mentions").
		WithArgs(sqlmock.AnyArg(), commentID, mentionedID).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.MarkSeen(context.Background(), commentID, mentionedID))
	assert.NoError(t, mock.ExpectationsWereMet())

	sql := captureSQL(t, false, func(r *DocumentCommentMentionRepo) {
		_ = r.MarkSeen(context.Background(), commentID, mentionedID)
	})
	assert.Contains(t, sql, "seen_at IS NULL")
	assert.Contains(t, sql, "mentioned_id = $3",
		"the recipient is part of the key, so a caller cannot mark somebody else's mention")
}

func TestDocumentCommentMentionRepo_MarkSeen_SurfacesAWriteFailure(t *testing.T) {
	repo, mock := newDocumentCommentMentionRepoMock(t)
	mock.ExpectExec("UPDATE document_comment_mentions").WillReturnError(errDBUnavailable)

	err := repo.MarkSeen(context.Background(), uuid.New(), uuid.New())

	assert.ErrorIs(t, err, errDBUnavailable)
}

func TestDocumentCommentMentionRepo_CountUnseen(t *testing.T) {
	repo, mock := newDocumentCommentMentionRepoMock(t)
	mentionedID := uuid.New()

	mock.ExpectQuery("SELECT COUNT").
		WithArgs(mentionedID, "agent").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))

	count, err := repo.CountUnseen(context.Background(), mentionedID, "agent")

	require.NoError(t, err)
	assert.Equal(t, int64(3), count)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentCommentMentionRepo_CountUnseen_SurfacesAQueryFailure(t *testing.T) {
	repo, mock := newDocumentCommentMentionRepoMock(t)
	mock.ExpectQuery("SELECT COUNT").WillReturnError(errDBUnavailable)

	_, err := repo.CountUnseen(context.Background(), uuid.New(), "user")

	assert.ErrorIs(t, err, errDBUnavailable)
}

// TestDocumentCommentMentionRepo_CountUnseen_AgreesWithTheList: a badge counting
// rows the list then hides is a badge that can never be cleared.
func TestDocumentCommentMentionRepo_CountUnseen_AgreesWithTheList(t *testing.T) {
	sql := captureSQL(t, true, func(repo *DocumentCommentMentionRepo) {
		_, _ = repo.CountUnseen(context.Background(), uuid.New(), "user")
	})

	assert.Contains(t, sql, "dcm.seen_at IS NULL")
	assert.Contains(t, sql, "dc.deleted_at IS NULL")
	assert.Contains(t, sql, "d.deleted_at IS NULL")
}
