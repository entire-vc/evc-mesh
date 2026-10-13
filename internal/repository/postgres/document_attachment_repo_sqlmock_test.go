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
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// These pin the parts of DocumentAttachmentRepo that need no database to prove:
// that the reads exclude soft-deleted attachments AND soft-deleted documents,
// that the workspace-scoped read really joins through to projects, and that a
// delete which matched nothing is a 404 rather than a silent success.

func newDocumentAttachmentRepoMock(t *testing.T) (*DocumentAttachmentRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewDocumentAttachmentRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

// documentAttachmentRows builds a result set with every column
// documentAttachmentRow scans, so a column added to the query without a matching
// field fails here rather than in production (see artifactSelectCols for why
// nothing uses SELECT *).
func documentAttachmentRows(atts ...domain.DocumentAttachment) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{
		"id", "document_id", "name", "mime_type", "size_bytes", "storage_key",
		"uploaded_by", "uploaded_by_type", "created_at", "deleted_at",
		"source_path", "source_sha256",
	})
	for _, a := range atts {
		rows.AddRow(a.ID, a.DocumentID, a.Name, a.MimeType, a.SizeBytes, a.StorageKey,
			a.UploadedBy, a.UploadedByType, a.CreatedAt, a.DeletedAt, a.SourcePath, a.SourceSHA256)
	}
	return rows
}

func sampleDocumentAttachment() domain.DocumentAttachment {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	return domain.DocumentAttachment{
		ID:             uuid.New(),
		DocumentID:     uuid.New(),
		Name:           "screenshot.png",
		MimeType:       "image/png",
		SizeBytes:      2048,
		StorageKey:     "documents/p/d/attachments/a.png",
		UploadedBy:     uuid.New(),
		UploadedByType: domain.ActorTypeUser,
		CreatedAt:      now,
	}
}

func TestDocumentAttachmentRepo_Create(t *testing.T) {
	repo, mock := newDocumentAttachmentRepoMock(t)
	att := sampleDocumentAttachment()

	mock.ExpectExec("INSERT INTO document_attachments").
		WithArgs(att.ID, att.DocumentID, att.Name, att.MimeType, att.SizeBytes, att.StorageKey,
			att.UploadedBy, att.UploadedByType, att.CreatedAt, att.SourcePath, att.SourceSHA256).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.Create(context.Background(), &att))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentAttachmentRepo_GetByIDInWorkspace(t *testing.T) {
	repo, mock := newDocumentAttachmentRepoMock(t)
	att := sampleDocumentAttachment()
	wsID := uuid.New()

	mock.ExpectQuery("FROM document_attachments a").
		WithArgs(att.ID, wsID).
		WillReturnRows(documentAttachmentRows(att))

	got, err := repo.GetByIDInWorkspace(context.Background(), att.ID, wsID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, att.ID, got.ID)
	assert.Equal(t, att.StorageKey, got.StorageKey)
	assert.Equal(t, att.SizeBytes, got.SizeBytes)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// An id belonging to another tenant produces no rows, and that is nil-and-no-error
// rather than an error: the caller answers 404, so a stranger's id and a
// nonexistent one are indistinguishable.
func TestDocumentAttachmentRepo_GetByIDInWorkspace_ForeignTenantIsNilNotError(t *testing.T) {
	repo, mock := newDocumentAttachmentRepoMock(t)
	id, wsID := uuid.New(), uuid.New()

	mock.ExpectQuery("FROM document_attachments a").
		WithArgs(id, wsID).
		WillReturnRows(documentAttachmentRows())

	got, err := repo.GetByIDInWorkspace(context.Background(), id, wsID)
	require.NoError(t, err)
	assert.Nil(t, got)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// The read joins all the way to projects and filters BOTH deleted_at columns.
// Asserting on the SQL is unusual, but each of these clauses is a security or
// correctness property that a refactor could drop without any test noticing: the
// mock returns whatever rows it is told to, so the query text is the only place
// the scoping is observable here.
func TestDocumentAttachmentRepo_GetByIDInWorkspace_QueryShape(t *testing.T) {
	repo, mock := newDocumentAttachmentRepoMock(t)
	id, wsID := uuid.New(), uuid.New()

	for _, clause := range []string{
		"JOIN documents d ON a.document_id = d.id",
		"JOIN projects p ON d.project_id = p.id",
		"a.deleted_at IS NULL",
		"d.deleted_at IS NULL",
		"p.workspace_id = ",
	} {
		mock.ExpectQuery(clause).
			WithArgs(id, wsID).
			WillReturnRows(documentAttachmentRows())
		_, err := repo.GetByIDInWorkspace(context.Background(), id, wsID)
		require.NoError(t, err, "query is missing %q", clause)
	}
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentAttachmentRepo_ListByDocument(t *testing.T) {
	repo, mock := newDocumentAttachmentRepoMock(t)
	att := sampleDocumentAttachment()

	mock.ExpectQuery("SELECT COUNT").
		WithArgs(att.DocumentID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("FROM document_attachments").
		WithArgs(att.DocumentID).
		WillReturnRows(documentAttachmentRows(att))

	page, err := repo.ListByDocument(context.Background(), att.DocumentID, pagination.Params{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, att.Name, page.Items[0].Name)
	assert.Equal(t, 1, page.TotalCount)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// The tiebreak on id is what keeps a page boundary from repeating or skipping a
// row when two uploads share a created_at — a property that is invisible until a
// user pages through a document that was populated by a script.
func TestDocumentAttachmentRepo_ListByDocument_OrdersWithATiebreak(t *testing.T) {
	repo, mock := newDocumentAttachmentRepoMock(t)
	docID := uuid.New()

	mock.ExpectQuery("SELECT COUNT").
		WithArgs(docID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("ORDER BY created_at ASC, id ASC").
		WithArgs(docID).
		WillReturnRows(documentAttachmentRows())

	_, err := repo.ListByDocument(context.Background(), docID, pagination.Params{})
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentAttachmentRepo_ListByDocument_ExcludesSoftDeleted(t *testing.T) {
	repo, mock := newDocumentAttachmentRepoMock(t)
	docID := uuid.New()

	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM document_attachments WHERE document_id = \\$1 AND deleted_at IS NULL").
		WithArgs(docID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("WHERE document_id = \\$1 AND deleted_at IS NULL").
		WithArgs(docID).
		WillReturnRows(documentAttachmentRows())

	_, err := repo.ListByDocument(context.Background(), docID, pagination.Params{})
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentAttachmentRepo_SoftDelete(t *testing.T) {
	repo, mock := newDocumentAttachmentRepoMock(t)
	id := uuid.New()
	at := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	mock.ExpectExec("UPDATE document_attachments SET deleted_at").
		WithArgs(id, at).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.SoftDelete(context.Background(), id, at))
	assert.NoError(t, mock.ExpectationsWereMet())
}

// Zero rows affected means the id was already deleted or never existed. Returning
// nil there would report a successful delete of something that is not there — the
// caller would tell the user it worked.
func TestDocumentAttachmentRepo_SoftDelete_NoRowsIs404(t *testing.T) {
	repo, mock := newDocumentAttachmentRepoMock(t)
	id := uuid.New()
	at := time.Now()

	mock.ExpectExec("UPDATE document_attachments SET deleted_at").
		WithArgs(id, at).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.SoftDelete(context.Background(), id, at)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
	assert.NoError(t, mock.ExpectationsWereMet())
}

// The delete is idempotent-safe by construction: the WHERE names deleted_at IS
// NULL, so a second delete matches nothing and cannot overwrite the original
// timestamp with a later one.
func TestDocumentAttachmentRepo_SoftDelete_DoesNotRestampAnAlreadyDeletedRow(t *testing.T) {
	repo, mock := newDocumentAttachmentRepoMock(t)
	id := uuid.New()
	at := time.Now()

	mock.ExpectExec("WHERE id = \\$1 AND deleted_at IS NULL").
		WithArgs(id, at).
		WillReturnResult(sqlmock.NewResult(0, 0))

	require.Error(t, repo.SoftDelete(context.Background(), id, at))
	assert.NoError(t, mock.ExpectationsWereMet())
}

// The embed importer's read side: hit, miss, and the soft-deleted row reading
// as a miss — the partial unique index keeps (document, source path) unique
// among LIVE rows only, so a replaced attachment must not surface here.
func TestDocumentAttachmentRepo_GetLiveBySourcePath(t *testing.T) {
	src := "Notes/pic.png"
	att := sampleDocumentAttachment()
	att.SourcePath = &src

	t.Run("hit", func(t *testing.T) {
		repo, mock := newDocumentAttachmentRepoMock(t)
		mock.ExpectQuery("WHERE document_id = \\$1 AND source_path = \\$2 AND deleted_at IS NULL").
			WithArgs(att.DocumentID, src).
			WillReturnRows(documentAttachmentRows(att))

		got, err := repo.GetLiveBySourcePath(context.Background(), att.DocumentID, src)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, src, *got.SourcePath)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("miss is nil not error", func(t *testing.T) {
		repo, mock := newDocumentAttachmentRepoMock(t)
		mock.ExpectQuery("source_path = \\$2").
			WithArgs(att.DocumentID, "nothing/references-me.png").
			WillReturnRows(documentAttachmentRows())

		got, err := repo.GetLiveBySourcePath(context.Background(), att.DocumentID, "nothing/references-me.png")
		require.NoError(t, err)
		assert.Nil(t, got)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestDocumentAttachmentRepo_ListLiveByDocument(t *testing.T) {
	repo, mock := newDocumentAttachmentRepoMock(t)
	a, b := sampleDocumentAttachment(), sampleDocumentAttachment()
	b.CreatedAt = a.CreatedAt.Add(time.Minute)

	mock.ExpectQuery("WHERE document_id = \\$1 AND deleted_at IS NULL ORDER BY created_at").
		WithArgs(a.DocumentID).
		WillReturnRows(documentAttachmentRows(a, b))

	got, err := repo.ListLiveByDocument(context.Background(), a.DocumentID)
	require.NoError(t, err)
	assert.Len(t, got, 2)
	assert.Equal(t, a.ID, got[0].ID, "oldest first, like ListByDocument")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentAttachmentRepo_Replace_CommitsRetireAndInsertTogether(t *testing.T) {
	repo, mock := newDocumentAttachmentRepoMock(t)
	att := sampleDocumentAttachment()
	oldID := uuid.New()
	at := time.Date(2026, 8, 19, 13, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE document_attachments SET deleted_at").
		WithArgs(oldID, at).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO document_attachments").
		WithArgs(att.ID, att.DocumentID, att.Name, att.MimeType, att.SizeBytes, att.StorageKey,
			att.UploadedBy, att.UploadedByType, att.CreatedAt, att.SourcePath, att.SourceSHA256).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.Replace(context.Background(), oldID, &att, at))
	assert.NoError(t, mock.ExpectationsWereMet())
}

// A failed insert must roll the retire back: this is the property the service
// relies on to never leave a document without a live attachment.
func TestDocumentAttachmentRepo_Replace_FailedInsertRollsBack(t *testing.T) {
	repo, mock := newDocumentAttachmentRepoMock(t)
	att := sampleDocumentAttachment()
	oldID := uuid.New()
	at := time.Now()

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE document_attachments SET deleted_at").
		WithArgs(oldID, at).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO document_attachments").
		WillReturnError(assert.AnError)
	mock.ExpectRollback()

	require.Error(t, repo.Replace(context.Background(), oldID, &att, at))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentAttachmentRepo_Replace_MissingOldRowIsNotFound(t *testing.T) {
	repo, mock := newDocumentAttachmentRepoMock(t)
	att := sampleDocumentAttachment()
	oldID := uuid.New()
	at := time.Now()

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE document_attachments SET deleted_at").
		WithArgs(oldID, at).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	err := repo.Replace(context.Background(), oldID, &att, at)
	require.Error(t, err)
	var ae *apierror.Error
	assert.ErrorAs(t, err, &ae)
	assert.NoError(t, mock.ExpectationsWereMet())
}
