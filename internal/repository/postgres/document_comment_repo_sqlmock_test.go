package postgres

import (
	"context"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// These pin the parts of DocumentCommentRepo that need no database to prove: that
// the reads exclude soft-deleted comments AND soft-deleted documents, that the
// anchor survives the row/domain trip whole or not at all, that resolve and
// unresolve move all three actor columns together, and that a write which matched
// nothing is a 404 rather than a silent success.

func newDocumentCommentRepoMock(t *testing.T) (*DocumentCommentRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewDocumentCommentRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

// documentCommentRows builds a result set with every column documentCommentRow
// scans, so a column added to the query without a matching field fails here
// rather than in production.
func documentCommentRows(comments ...domain.DocumentComment) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{
		"id", "document_id", "parent_comment_id", "body",
		"anchor_start", "anchor_end", "anchor_exact", "anchor_prefix", "anchor_suffix",
		"resolved_at", "resolved_by", "resolved_by_type",
		"author_id", "author_type", "created_at", "updated_at", "deleted_at",
	})
	for _, c := range comments {
		var (
			start, end            any
			exact, prefix, suffix any
		)
		if c.Anchor != nil {
			start, end = c.Anchor.Start, c.Anchor.End
			exact, prefix, suffix = c.Anchor.Exact, c.Anchor.Prefix, c.Anchor.Suffix
		}
		rows.AddRow(c.ID, c.DocumentID, c.ParentID, c.Body,
			start, end, exact, prefix, suffix,
			c.ResolvedAt, c.ResolvedBy, c.ResolvedByType,
			c.AuthorID, c.AuthorType, c.CreatedAt, c.UpdatedAt, c.DeletedAt)
	}
	return rows
}

func sampleRootComment() domain.DocumentComment {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	return domain.DocumentComment{
		ID:         uuid.New(),
		DocumentID: uuid.New(),
		Body:       "This paragraph contradicts the runbook.",
		Anchor: &domain.DocumentAnchor{
			Start:  120,
			End:    168,
			Exact:  "the migration is applied before the image swap",
			Prefix: "Deploy discipline. ",
			Suffix: ", never after.",
		},
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeUser,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

func TestDocumentCommentRepo_Create_WritesTheAnchor(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	c := sampleRootComment()

	mock.ExpectExec("INSERT INTO document_comments").
		WithArgs(c.ID, c.DocumentID, c.ParentID, c.Body,
			c.Anchor.Start, c.Anchor.End, c.Anchor.Exact, c.Anchor.Prefix, c.Anchor.Suffix,
			c.AuthorID, c.AuthorType, c.CreatedAt, c.UpdatedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.Create(context.Background(), &c))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentCommentRepo_Create_ReplyWritesNoAnchor(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	root := uuid.New()
	c := sampleRootComment()
	c.Anchor = nil
	c.ParentID = &root

	// All five columns nil, not zero. A reply written with anchor_start = 0 would
	// satisfy ck_document_comments_root_has_anchor's other half and become a
	// second root pointing at the top of the document.
	mock.ExpectExec("INSERT INTO document_comments").
		WithArgs(c.ID, c.DocumentID, c.ParentID, c.Body,
			nil, nil, nil, nil, nil,
			c.AuthorID, c.AuthorType, c.CreatedAt, c.UpdatedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.Create(context.Background(), &c))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentCommentRepo_GetByIDInWorkspace_ExcludesDeletedRowsAndDocuments(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	c := sampleRootComment()
	wsID := uuid.New()

	mock.ExpectQuery(`FROM document_comments c\s+JOIN documents d ON c\.document_id = d\.id\s+JOIN projects p ON d\.project_id = p\.id\s+WHERE c\.id = \$1\s+AND c\.deleted_at IS NULL\s+AND d\.deleted_at IS NULL\s+AND p\.workspace_id = \$2`).
		WithArgs(c.ID, wsID).
		WillReturnRows(documentCommentRows(c))

	got, err := repo.GetByIDInWorkspace(context.Background(), c.ID, wsID)
	require.NoError(t, err)
	require.NotNil(t, got)
	// The anchor is the point of the row, so it is asserted field by field: a
	// toDomain that dropped one of the five would still return a non-nil anchor.
	require.NotNil(t, got.Anchor)
	assert.Equal(t, *c.Anchor, *got.Anchor)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentCommentRepo_GetByIDInWorkspace_UnknownIsNilNotError(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	mock.ExpectQuery("FROM document_comments c").
		WillReturnRows(documentCommentRows())

	got, err := repo.GetByIDInWorkspace(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	// nil, not an error: the caller turns it into the same 404 an id from another
	// tenant gets, so the two stay indistinguishable.
	assert.Nil(t, got)
}

func TestDocumentCommentRepo_ListByDocument_OrdersOldestFirstAndHidesDeleted(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	root := sampleRootComment()
	reply := sampleRootComment()
	reply.DocumentID = root.DocumentID
	reply.Anchor = nil
	reply.ParentID = &root.ID

	mock.ExpectQuery(`FROM document_comments\s+WHERE document_id = \$1 AND deleted_at IS NULL\s+ORDER BY created_at ASC, id ASC`).
		WithArgs(root.DocumentID).
		WillReturnRows(documentCommentRows(root, reply))

	got, err := repo.ListByDocument(context.Background(), root.DocumentID)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.NotNil(t, got[0].Anchor)
	// A reply must come back anchorless: an inherited-looking anchor here would
	// have the client draw a second highlight for every reply in the thread.
	assert.Nil(t, got[1].Anchor)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentCommentRepo_UpdateBody_LeavesTheAnchorAlone(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	id := uuid.New()
	at := time.Date(2026, 8, 19, 13, 0, 0, 0, time.UTC)

	// Named columns, not a wildcard: the assertion is that the statement touches
	// body and updated_at and nothing else. Editing the words of a note must not
	// move where it points.
	mock.ExpectExec(`UPDATE document_comments SET body = \$2, updated_at = \$3\s+WHERE id = \$1 AND deleted_at IS NULL`).
		WithArgs(id, "rewritten", at).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.UpdateBody(context.Background(), id, "rewritten", at))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentCommentRepo_UpdateBody_NoRowIs404(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	mock.ExpectExec("UPDATE document_comments SET body").
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.UpdateBody(context.Background(), uuid.New(), "x", time.Now())
	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

func TestDocumentCommentRepo_SetResolved_StampsAllThreeColumns(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	id, by := uuid.New(), uuid.New()
	byType := domain.ActorTypeUser
	at := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)

	mock.ExpectExec(`UPDATE document_comments\s+SET resolved_at = \$2, resolved_by = \$3, resolved_by_type = \$4, updated_at = \$5\s+WHERE id = \$1 AND deleted_at IS NULL AND parent_comment_id IS NULL`).
		WithArgs(id, at, by, byType, at).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.SetResolved(context.Background(), id, &by, &byType, at))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentCommentRepo_SetResolved_UnresolveClearsTheActorToo(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	id := uuid.New()
	byType := domain.ActorTypeUser
	at := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)

	// All three nil. Clearing only the timestamp would leave the previous
	// resolver's name on an open thread, and the next reader would attribute a
	// resolution nobody made. The caller passing a stale byType must not be able
	// to write it back either — hence byType is nilled inside the repo, and this
	// call deliberately hands one in.
	mock.ExpectExec("UPDATE document_comments").
		WithArgs(id, nil, nil, nil, at).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.SetResolved(context.Background(), id, nil, &byType, at))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentCommentRepo_SetResolved_ReplyIs404(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	// The WHERE carries `parent_comment_id IS NULL`, so a reply matches nothing.
	// A 404 is the honest answer — there is no such thread — where letting the
	// CHECK constraint catch it would be a 500.
	mock.ExpectExec("UPDATE document_comments").
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.SetResolved(context.Background(), uuid.New(), nil, nil, time.Now())
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

func TestDocumentCommentRepo_SoftDelete_TakesTheWholeSubtree(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	id := uuid.New()
	at := time.Date(2026, 8, 19, 15, 0, 0, 0, time.UTC)

	// The recursive CTE is the assertion: deleting a root without its replies
	// would leave them live, anchorless, and displayable nowhere.
	mock.ExpectExec(`WITH RECURSIVE subtree AS \(.*JOIN subtree s ON c\.parent_comment_id = s\.id.*\)\s+UPDATE document_comments\s+SET deleted_at = \$2\s+WHERE id IN \(SELECT id FROM subtree\)`).
		WithArgs(id, at).
		WillReturnResult(sqlmock.NewResult(0, 3))

	require.NoError(t, repo.SoftDelete(context.Background(), id, at))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentCommentRepo_SoftDelete_NoRowIs404(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	mock.ExpectExec("WITH RECURSIVE subtree").
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.SoftDelete(context.Background(), uuid.New(), time.Now())
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

// A row whose anchor columns are partly NULL cannot exist while
// ck_document_comments_anchor_complete holds. This pins what happens if it ever
// does: an anchorless comment, not one whose missing context silently reads as
// "no context" — which is exactly how a match gets accepted that should have been
// refused.
func TestDocumentCommentRepo_PartialAnchorBecomesNoAnchor(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	c := sampleRootComment()

	rows := sqlmock.NewRows([]string{
		"id", "document_id", "parent_comment_id", "body",
		"anchor_start", "anchor_end", "anchor_exact", "anchor_prefix", "anchor_suffix",
		"resolved_at", "resolved_by", "resolved_by_type",
		"author_id", "author_type", "created_at", "updated_at", "deleted_at",
	}).AddRow(c.ID, c.DocumentID, nil, c.Body,
		c.Anchor.Start, c.Anchor.End, c.Anchor.Exact, nil, nil,
		nil, nil, nil,
		c.AuthorID, c.AuthorType, c.CreatedAt, c.UpdatedAt, nil)

	mock.ExpectQuery("FROM document_comments c").WillReturnRows(rows)

	got, err := repo.GetByIDInWorkspace(context.Background(), c.ID, uuid.New())
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Nil(t, got.Anchor)
}
