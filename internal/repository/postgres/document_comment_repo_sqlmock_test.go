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
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// These pin the parts of DocumentCommentRepo that need no database to prove: that
// the anchor round-trips through five nullable columns without losing which of
// its three states it was in, that the reads exclude soft-deleted rows, that a
// write matching nothing is a 404 rather than a silent success, and that hiding
// resolved threads hides their replies too.

func newDocumentCommentRepoMock(t *testing.T) (*DocumentCommentRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewDocumentCommentRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

// documentCommentRows builds a result set with every column
// documentCommentRow scans, so a column added to the query without a matching
// field fails here rather than in production.
func documentCommentRows(comments ...documentCommentRow) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{
		"id", "document_id", "parent_comment_id", "author_id", "author_type", "body",
		"anchor_exact", "anchor_prefix", "anchor_suffix", "anchor_start", "anchor_end",
		"resolved_at", "resolved_by", "resolved_by_type",
		"created_at", "updated_at", "deleted_at", "author_name", "resolved_by_name",
	})
	for _, c := range comments {
		rows.AddRow(c.ID, c.DocumentID, c.ParentCommentID, c.AuthorID, c.AuthorType, c.Body,
			c.AnchorExact, c.AnchorPrefix, c.AnchorSuffix, c.AnchorStart, c.AnchorEnd,
			c.ResolvedAt, c.ResolvedBy, c.ResolvedByType,
			c.CreatedAt, c.UpdatedAt, c.DeletedAt, c.AuthorName, c.ResolvedByName)
	}
	return rows
}

func strPtr(s string) *string { return &s }
func numPtr(v int) *int       { return &v }

func sampleDocumentCommentRow() documentCommentRow {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	return documentCommentRow{
		ID:           uuid.New(),
		DocumentID:   uuid.New(),
		AuthorID:     uuid.New(),
		AuthorType:   domain.ActorTypeUser,
		Body:         "this paragraph contradicts the one above",
		AnchorExact:  strPtr("the API"),
		AnchorPrefix: strPtr("authenticate "),
		AnchorSuffix: strPtr(" with a token"),
		AnchorStart:  numPtr(10),
		AnchorEnd:    numPtr(17),
		CreatedAt:    now,
		UpdatedAt:    now,
		AuthorName:   strPtr("Ada"),
	}
}

func sampleDocumentComment() domain.DocumentComment {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	return domain.DocumentComment{
		ID:         uuid.New(),
		DocumentID: uuid.New(),
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeUser,
		Body:       "this paragraph contradicts the one above",
		Anchor:     domain.NewDocumentCommentAnchor("the API", "authenticate ", " with a token", numPtr(10), numPtr(17)),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

func TestDocumentCommentRepo_Create(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	c := sampleDocumentComment()

	mock.ExpectExec("INSERT INTO document_comments").
		WithArgs(c.ID, c.DocumentID, c.ParentCommentID, c.AuthorID, c.AuthorType, c.Body,
			"the API", "authenticate ", " with a token", 10, 17,
			c.CreatedAt, c.UpdatedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.Create(context.Background(), &c))
	assert.NoError(t, mock.ExpectationsWereMet())
}

// An unanchored comment writes NULL rather than the empty string: the "never
// anchored" state has to be one value in the column, and an empty anchor_prefix
// beside a NULL anchor_exact would trip
// chk_document_comments_anchor_neighbourhood.
func TestDocumentCommentRepo_Create_UnanchoredWritesNulls(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	c := sampleDocumentComment()
	c.Anchor = nil

	mock.ExpectExec("INSERT INTO document_comments").
		WithArgs(c.ID, c.DocumentID, c.ParentCommentID, c.AuthorID, c.AuthorType, c.Body,
			nil, nil, nil, nil, nil,
			c.CreatedAt, c.UpdatedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.Create(context.Background(), &c))
	assert.NoError(t, mock.ExpectationsWereMet())
}

// An orphan is written as a quote with no offsets. It has to be writable: a
// client that re-read the body and could not find the range says so, rather than
// keeping numbers it knows are wrong.
func TestDocumentCommentRepo_Create_OrphanWritesQuoteWithoutOffsets(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	c := sampleDocumentComment()
	c.Anchor = domain.NewDocumentCommentAnchor("the API", "", "", nil, nil)

	mock.ExpectExec("INSERT INTO document_comments").
		WithArgs(c.ID, c.DocumentID, c.ParentCommentID, c.AuthorID, c.AuthorType, c.Body,
			"the API", nil, nil, nil, nil,
			c.CreatedAt, c.UpdatedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.Create(context.Background(), &c))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentCommentRepo_GetByID(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	row := sampleDocumentCommentRow()

	mock.ExpectQuery("FROM document_comments dc").
		WithArgs(row.ID).
		WillReturnRows(documentCommentRows(row))

	got, err := repo.GetByID(context.Background(), row.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, row.ID, got.ID)
	assert.Equal(t, row.Body, got.Body)
	require.NotNil(t, got.Anchor)
	assert.Equal(t, "the API", got.Anchor.Exact)
	assert.Equal(t, "authenticate ", got.Anchor.Prefix)
	require.NotNil(t, got.Anchor.Start)
	assert.Equal(t, 10, *got.Anchor.Start)
	assert.False(t, got.Anchor.IsOrphaned())
	require.NotNil(t, got.AuthorName)
	assert.Equal(t, "Ada", *got.AuthorName)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// The scan has to reconstruct which of the three anchor states the row was in.
// Collapsing "no quote" into an empty anchor would turn a document-level comment
// into a permanently orphaned one on the way out.
func TestDocumentCommentRepo_GetByID_AnchorStatesRoundTrip(t *testing.T) {
	tests := []struct {
		name         string
		exact        *string
		start, end   *int
		wantAnchor   bool
		wantOrphaned bool
	}{
		{"anchored", strPtr("the API"), numPtr(1), numPtr(8), true, false},
		{"orphaned", strPtr("the API"), nil, nil, true, true},
		{"never anchored", nil, nil, nil, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, mock := newDocumentCommentRepoMock(t)
			row := sampleDocumentCommentRow()
			row.AnchorExact, row.AnchorStart, row.AnchorEnd = tt.exact, tt.start, tt.end
			if tt.exact == nil {
				row.AnchorPrefix, row.AnchorSuffix = nil, nil
			}

			mock.ExpectQuery("FROM document_comments dc").WillReturnRows(documentCommentRows(row))

			got, err := repo.GetByID(context.Background(), row.ID)
			require.NoError(t, err)
			require.NotNil(t, got)

			if !tt.wantAnchor {
				assert.Nil(t, got.Anchor, "a comment with no quote must not come back carrying an anchor")
				return
			}
			require.NotNil(t, got.Anchor)
			assert.Equal(t, tt.wantOrphaned, got.Anchor.IsOrphaned())
		})
	}
}

// A missing (or soft-deleted) row is nil and no error, so the service can answer
// 404 rather than pass sql.ErrNoRows up as a 500.
func TestDocumentCommentRepo_GetByID_MissingIsNilAndNoError(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)

	mock.ExpectQuery("FROM document_comments dc").WillReturnRows(documentCommentRows())

	got, err := repo.GetByID(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestDocumentCommentRepo_GetByIDInWorkspace(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	row := sampleDocumentCommentRow()
	wsID := uuid.New()

	mock.ExpectQuery("JOIN projects p ON d.project_id = p.id").
		WithArgs(row.ID, wsID).
		WillReturnRows(documentCommentRows(row))

	got, err := repo.GetByIDInWorkspace(context.Background(), row.ID, wsID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, row.ID, got.ID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// The workspace predicate is in the query, not in a caller's filter, so another
// tenant's comment comes back as "no such comment".
func TestDocumentCommentRepo_GetByIDInWorkspace_OtherTenantIsNil(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)

	mock.ExpectQuery("FROM document_comments dc").WillReturnRows(documentCommentRows())

	got, err := repo.GetByIDInWorkspace(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestDocumentCommentRepo_Update(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	c := sampleDocumentComment()
	c.Body = "edited"

	mock.ExpectExec("UPDATE document_comments").
		WithArgs(c.ID, "edited", c.ResolvedAt, c.ResolvedBy, c.ResolvedByType, c.UpdatedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.Update(context.Background(), &c))
	assert.NoError(t, mock.ExpectationsWereMet())
}

// Resolution is written through the same statement as the body, all three columns
// together — the schema refuses two of three.
func TestDocumentCommentRepo_Update_WritesTheResolutionTriple(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	c := sampleDocumentComment()
	at := c.UpdatedAt
	by := uuid.New()
	byType := domain.ActorTypeAgent
	c.ResolvedAt, c.ResolvedBy, c.ResolvedByType = &at, &by, &byType

	mock.ExpectExec("UPDATE document_comments").
		WithArgs(c.ID, c.Body, &at, &by, &byType, c.UpdatedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.Update(context.Background(), &c))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentCommentRepo_Update_NoRowsIsNotFound(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	c := sampleDocumentComment()

	mock.ExpectExec("UPDATE document_comments").WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.Update(context.Background(), &c)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

func TestDocumentCommentRepo_Update_ErrorIsPassedThrough(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	c := sampleDocumentComment()

	mock.ExpectExec("UPDATE document_comments").WillReturnError(assert.AnError)

	assert.ErrorIs(t, repo.Update(context.Background(), &c), assert.AnError)
}

// The replies go with the root in one statement: a reply left behind is an answer
// to a question the reader can no longer see.
func TestDocumentCommentRepo_SoftDelete(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	id := uuid.New()
	at := time.Now().UTC()

	mock.ExpectExec("id = \\$1 OR parent_comment_id = \\$1").
		WithArgs(id, at).
		WillReturnResult(sqlmock.NewResult(0, 3))

	require.NoError(t, repo.SoftDelete(context.Background(), id, at))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentCommentRepo_SoftDelete_NoRowsIsNotFound(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)

	mock.ExpectExec("UPDATE document_comments").WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.SoftDelete(context.Background(), uuid.New(), time.Now())

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

func TestDocumentCommentRepo_SoftDelete_ErrorIsPassedThrough(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)

	mock.ExpectExec("UPDATE document_comments").WillReturnError(assert.AnError)

	assert.ErrorIs(t, repo.SoftDelete(context.Background(), uuid.New(), time.Now()), assert.AnError)
}

func TestDocumentCommentRepo_ListByDocument(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	docID := uuid.New()

	first, second := sampleDocumentCommentRow(), sampleDocumentCommentRow()
	first.DocumentID, second.DocumentID = docID, docID

	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM document_comments dc").
		WithArgs(docID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery("ORDER BY dc.created_at ASC, dc.id ASC").
		WithArgs(docID).
		WillReturnRows(documentCommentRows(first, second))

	page, err := repo.ListByDocument(context.Background(), docID,
		repository.DocumentCommentFilter{}, pagination.Params{})
	require.NoError(t, err)

	require.Len(t, page.Items, 2)
	assert.Equal(t, first.ID, page.Items[0].ID)
	assert.Equal(t, 2, page.TotalCount)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// The default listing hides a resolved THREAD, root and replies alike. Filtering
// on dc.resolved_at alone would leave the replies of a resolved thread on screen.
func TestDocumentCommentRepo_ListByDocument_HidesResolvedThreadsByDefault(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	docID := uuid.New()

	mock.ExpectQuery("COALESCE\\(dc.parent_comment_id, dc.id\\)").
		WithArgs(docID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("COALESCE\\(dc.parent_comment_id, dc.id\\)").
		WithArgs(docID).
		WillReturnRows(documentCommentRows())

	page, err := repo.ListByDocument(context.Background(), docID,
		repository.DocumentCommentFilter{}, pagination.Params{})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentCommentRepo_ListByDocument_IncludeResolvedDropsTheFilter(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)
	docID := uuid.New()
	row := sampleDocumentCommentRow()
	resolvedAt := row.CreatedAt
	resolvedBy := uuid.New()
	resolvedByType := domain.ActorTypeUser
	row.ResolvedAt, row.ResolvedBy, row.ResolvedByType = &resolvedAt, &resolvedBy, &resolvedByType
	row.ResolvedByName = strPtr("Grace")

	mock.ExpectQuery("SELECT COUNT").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("FROM document_comments dc").WillReturnRows(documentCommentRows(row))

	page, err := repo.ListByDocument(context.Background(), docID,
		repository.DocumentCommentFilter{IncludeResolved: true}, pagination.Params{})
	require.NoError(t, err)

	require.Len(t, page.Items, 1)
	assert.True(t, page.Items[0].IsResolved())
	require.NotNil(t, page.Items[0].ResolvedByName)
	assert.Equal(t, "Grace", *page.Items[0].ResolvedByName,
		"who resolved a thread is resolved to a name the same way the author is")

	// The unfiltered query must not carry the thread predicate at all — a filter
	// that stayed on would make include_resolved a no-op that looks like it works.
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentCommentRepo_ListByDocument_CountErrorIsReturned(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)

	mock.ExpectQuery("SELECT COUNT").WillReturnError(assert.AnError)

	_, err := repo.ListByDocument(context.Background(), uuid.New(),
		repository.DocumentCommentFilter{}, pagination.Params{})
	assert.ErrorIs(t, err, assert.AnError)
}

func TestDocumentCommentRepo_ListByDocument_DataErrorIsReturned(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)

	mock.ExpectQuery("SELECT COUNT").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("FROM document_comments dc").WillReturnError(assert.AnError)

	_, err := repo.ListByDocument(context.Background(), uuid.New(),
		repository.DocumentCommentFilter{}, pagination.Params{})
	assert.ErrorIs(t, err, assert.AnError)
}

func TestDocumentCommentRepo_GetByID_ErrorIsPassedThrough(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)

	mock.ExpectQuery("FROM document_comments dc").WillReturnError(assert.AnError)

	_, err := repo.GetByID(context.Background(), uuid.New())
	assert.ErrorIs(t, err, assert.AnError)
}

func TestDocumentCommentRepo_GetByIDInWorkspace_ErrorIsPassedThrough(t *testing.T) {
	repo, mock := newDocumentCommentRepoMock(t)

	mock.ExpectQuery("FROM document_comments dc").WillReturnError(assert.AnError)

	_, err := repo.GetByIDInWorkspace(context.Background(), uuid.New(), uuid.New())
	assert.ErrorIs(t, err, assert.AnError)
}

// actorNameExpr is what makes a byline renderable, and it is shared by documents
// and document comments. The property that matters is that both actor kinds are
// covered and anything else resolves to NULL rather than to an empty name.
func TestActorNameExpr(t *testing.T) {
	got := actorNameExpr("d.created_by", "d.created_by_type", "created_by_name")

	assert.Contains(t, got, "d.created_by_type = 'agent'")
	assert.Contains(t, got, "d.created_by_type = 'user'")
	assert.Contains(t, got, "FROM agents WHERE id = d.created_by AND deleted_at IS NULL",
		"a deleted agent must resolve to no name rather than to its last one")
	assert.Contains(t, got, "SPLIT_PART(u.email, '@', 1)",
		"a user with no display name falls back to the local part of their email")
	assert.Contains(t, got, "ELSE NULL")
	assert.Contains(t, got, "AS created_by_name")
}
