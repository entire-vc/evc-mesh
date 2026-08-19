package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// These pin the parts of DocumentRepo that need no database to prove: that the
// reads exclude soft-deleted rows, that a write which matched nothing is a 404
// rather than a silent success, and that the partial unique index on sibling
// slugs surfaces as a 409 instead of a 500.

func newDocumentRepoMock(t *testing.T) (*DocumentRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewDocumentRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

// documentRows builds a result set with every column documentRow scans — the
// stored ones and the two names documentEnrichedSelect computes — so a column
// added to the query without a matching field fails here rather than in
// production (see artifactSelectCols for why nothing uses SELECT *).
func documentRows(docs ...domain.Document) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{
		"id", "project_id", "parent_id", "slug", "title", "storage_key",
		"position", "created_by", "created_by_type", "updated_by", "updated_by_type",
		"created_at", "updated_at", "deleted_at", "created_by_name", "updated_by_name",
	})
	for _, d := range docs {
		rows.AddRow(d.ID, d.ProjectID, d.ParentID, d.Slug, d.Title, d.StorageKey,
			d.Position, d.CreatedBy, d.CreatedByType, d.UpdatedBy, d.UpdatedByType,
			d.CreatedAt, d.UpdatedAt, d.DeletedAt, d.CreatedByName, d.UpdatedByName)
	}
	return rows
}

func sampleDocument() domain.Document {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	parent := uuid.New()
	editor := uuid.New()
	editorType := domain.ActorTypeAgent
	creatorName, editorName := "Ada", "howard"
	return domain.Document{
		ID:            uuid.New(),
		ProjectID:     uuid.New(),
		ParentID:      &parent,
		Slug:          "runbook",
		Title:         "Runbook",
		StorageKey:    "documents/p/d.md",
		Position:      3,
		CreatedBy:     uuid.New(),
		CreatedByType: domain.ActorTypeUser,
		UpdatedBy:     &editor,
		UpdatedByType: &editorType,
		CreatedAt:     now,
		UpdatedAt:     now,
		CreatedByName: &creatorName,
		UpdatedByName: &editorName,
	}
}

func TestDocumentRepo_Create(t *testing.T) {
	repo, mock := newDocumentRepoMock(t)
	doc := sampleDocument()

	mock.ExpectExec("INSERT INTO documents").
		WithArgs(doc.ID, doc.ProjectID, doc.ParentID, doc.Slug, doc.Title, doc.StorageKey,
			doc.Position, doc.CreatedBy, doc.CreatedByType, doc.UpdatedBy, doc.UpdatedByType,
			doc.CreatedAt, doc.UpdatedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.Create(context.Background(), &doc))
	assert.NoError(t, mock.ExpectationsWereMet())
}

// The partial unique index is a caller mistake — "that name is taken here" —
// not a server fault, so it must not reach the client as a 500.
func TestDocumentRepo_Create_DuplicateSiblingSlugIsAConflict(t *testing.T) {
	for _, constraint := range []string{"uq_documents_sibling_slug", "uq_documents_root_slug"} {
		t.Run(constraint, func(t *testing.T) {
			repo, mock := newDocumentRepoMock(t)
			doc := sampleDocument()

			mock.ExpectExec("INSERT INTO documents").
				WillReturnError(&pq.Error{Code: "23505", Constraint: constraint})

			err := repo.Create(context.Background(), &doc)

			var apiErr *apierror.Error
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, 409, apiErr.StatusCode())
		})
	}
}

// Any other unique violation belongs to whatever future index raised it, and
// dressing it up as a slug conflict would send the caller to fix the wrong thing.
func TestDocumentRepo_Create_ForeignConstraintIsNotDressedUpAsASlugConflict(t *testing.T) {
	repo, mock := newDocumentRepoMock(t)
	doc := sampleDocument()

	mock.ExpectExec("INSERT INTO documents").
		WillReturnError(&pq.Error{Code: "23505", Constraint: "uq_something_else"})

	err := repo.Create(context.Background(), &doc)

	require.Error(t, err)
	var apiErr *apierror.Error
	assert.False(t, errors.As(err, &apiErr),
		"an unrelated constraint was reported as a slug conflict instead of being passed through")
}

func TestDocumentRepo_GetByID(t *testing.T) {
	repo, mock := newDocumentRepoMock(t)
	doc := sampleDocument()

	mock.ExpectQuery("FROM documents d WHERE d.id = \\$1 AND d.deleted_at IS NULL").
		WithArgs(doc.ID).
		WillReturnRows(documentRows(doc))

	got, err := repo.GetByID(context.Background(), doc.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, doc.ID, got.ID)
	assert.Equal(t, doc.ProjectID, got.ProjectID)
	require.NotNil(t, got.ParentID)
	assert.Equal(t, *doc.ParentID, *got.ParentID)
	assert.Equal(t, "runbook", got.Slug)
	assert.Equal(t, 3, got.Position)
	assert.Equal(t, domain.ActorTypeUser, got.CreatedByType)
	require.NotNil(t, got.UpdatedBy)
	assert.Equal(t, *doc.UpdatedBy, *got.UpdatedBy)
	require.NotNil(t, got.UpdatedByType)
	assert.Equal(t, domain.ActorTypeAgent, *got.UpdatedByType)
	assert.Empty(t, got.Body, "the body is not a column; the service fills it from storage")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// The two display names are what make the byline renderable — a caller handed
// bare uuids would have to fan out to resolve them — so the scan has to carry
// them off the enriched SELECT and onto the domain object.
func TestDocumentRepo_GetByID_CarriesTheResolvedActorNames(t *testing.T) {
	repo, mock := newDocumentRepoMock(t)
	doc := sampleDocument()

	mock.ExpectQuery("created_by_name").WillReturnRows(documentRows(doc))

	got, err := repo.GetByID(context.Background(), doc.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	require.NotNil(t, got.CreatedByName)
	assert.Equal(t, "Ada", *got.CreatedByName)
	require.NotNil(t, got.UpdatedByName)
	assert.Equal(t, "howard", *got.UpdatedByName)
}

// A row written before updated_by existed has no last editor, and nothing may
// invent one for it — that is the whole reason the column was not back-filled.
func TestDocumentRepo_GetByID_LegacyRowHasNoLastEditor(t *testing.T) {
	repo, mock := newDocumentRepoMock(t)
	doc := sampleDocument()
	doc.UpdatedBy, doc.UpdatedByType, doc.UpdatedByName = nil, nil, nil

	mock.ExpectQuery("FROM documents d").WillReturnRows(documentRows(doc))

	got, err := repo.GetByID(context.Background(), doc.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Nil(t, got.UpdatedBy)
	assert.Nil(t, got.UpdatedByType)
	assert.Nil(t, got.UpdatedByName, "an unresolvable name is absent, not an empty string")
	assert.NotNil(t, got.CreatedByName, "the creator is still known")
}

// A missing (or soft-deleted) row is nil and no error, so the service can answer
// 404 rather than pass sql.ErrNoRows up as a 500.
func TestDocumentRepo_GetByID_MissingIsNilAndNoError(t *testing.T) {
	repo, mock := newDocumentRepoMock(t)

	mock.ExpectQuery("FROM documents").WillReturnRows(documentRows())

	got, err := repo.GetByID(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestDocumentRepo_GetByIDInWorkspace(t *testing.T) {
	repo, mock := newDocumentRepoMock(t)
	doc := sampleDocument()
	wsID := uuid.New()

	mock.ExpectQuery("JOIN projects p ON d.project_id = p.id").
		WithArgs(doc.ID, wsID).
		WillReturnRows(documentRows(doc))

	got, err := repo.GetByIDInWorkspace(context.Background(), doc.ID, wsID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, doc.ID, got.ID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// The workspace predicate is in the query, not in a caller's filter, so a
// document of another tenant comes back as "no such document".
func TestDocumentRepo_GetByIDInWorkspace_OtherTenantIsNil(t *testing.T) {
	repo, mock := newDocumentRepoMock(t)

	mock.ExpectQuery("FROM documents d").WillReturnRows(documentRows())

	got, err := repo.GetByIDInWorkspace(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestDocumentRepo_Update(t *testing.T) {
	repo, mock := newDocumentRepoMock(t)
	doc := sampleDocument()

	mock.ExpectExec("UPDATE documents").
		WithArgs(doc.ID, doc.Title, doc.ParentID, doc.Position, doc.UpdatedAt,
			doc.UpdatedBy, doc.UpdatedByType).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.Update(context.Background(), &doc))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentRepo_Update_NoRowsIsNotFound(t *testing.T) {
	repo, mock := newDocumentRepoMock(t)
	doc := sampleDocument()

	mock.ExpectExec("UPDATE documents").WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.Update(context.Background(), &doc)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

// Re-parenting changes which siblings a document is unique against, so the slug
// index can fire on an update that never touched the slug.
func TestDocumentRepo_Update_SlugConflictOnReparent(t *testing.T) {
	repo, mock := newDocumentRepoMock(t)
	doc := sampleDocument()

	mock.ExpectExec("UPDATE documents").
		WillReturnError(&pq.Error{Code: "23505", Constraint: "uq_documents_sibling_slug"})

	err := repo.Update(context.Background(), &doc)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 409, apiErr.StatusCode())
}

func TestDocumentRepo_SoftDelete(t *testing.T) {
	repo, mock := newDocumentRepoMock(t)
	id, by := uuid.New(), uuid.New()
	at := time.Now().UTC()

	// Two rows: the document and one descendant the recursive statement caught.
	// The deleter is stamped on both — a delete is a change to every row it
	// touches, and a restored child must not claim its last editor was whoever
	// happened to touch it last week.
	mock.ExpectExec("WITH RECURSIVE subtree").
		WithArgs(id, at, by, domain.ActorTypeUser).
		WillReturnResult(sqlmock.NewResult(0, 2))

	require.NoError(t, repo.SoftDelete(context.Background(), id, at, by, domain.ActorTypeUser))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentRepo_SoftDelete_NoRowsIsNotFound(t *testing.T) {
	repo, mock := newDocumentRepoMock(t)

	mock.ExpectExec("WITH RECURSIVE subtree").WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.SoftDelete(context.Background(), uuid.New(), time.Now(), uuid.New(), domain.ActorTypeAgent)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

func TestDocumentRepo_ListByProject(t *testing.T) {
	repo, mock := newDocumentRepoMock(t)
	projID := uuid.New()

	first, second := sampleDocument(), sampleDocument()
	first.ProjectID, second.ProjectID = projID, projID

	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM documents").
		WithArgs(projID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery("ORDER BY d.position ASC, d.created_at ASC, d.id ASC").
		WithArgs(projID).
		WillReturnRows(documentRows(first, second))

	page, err := repo.ListByProject(context.Background(), projID, pagination.Params{})
	require.NoError(t, err)

	require.Len(t, page.Items, 2)
	assert.Equal(t, first.ID, page.Items[0].ID)
	assert.Equal(t, 2, page.TotalCount)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentRepo_HasAncestor(t *testing.T) {
	docID, ancestorID := uuid.New(), uuid.New()

	for _, tc := range []struct {
		name string
		want bool
	}{{"is an ancestor", true}, {"is not", false}} {
		t.Run(tc.name, func(t *testing.T) {
			repo, mock := newDocumentRepoMock(t)
			mock.ExpectQuery("WITH RECURSIVE ancestors").
				WithArgs(docID, ancestorID).
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(tc.want))

			got, err := repo.HasAncestor(context.Background(), docID, ancestorID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestIsDocumentSlugConflict(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"sibling slug index", &pq.Error{Code: "23505", Constraint: "uq_documents_sibling_slug"}, true},
		{"root slug index", &pq.Error{Code: "23505", Constraint: "uq_documents_root_slug"}, true},
		{"another unique index", &pq.Error{Code: "23505", Constraint: "uq_tasks_project_number"}, false},
		{"a foreign key violation on the same index name", &pq.Error{Code: "23503", Constraint: "uq_documents_root_slug"}, false},
		{"not a pq error", assert.AnError, false},
		{"no error", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isDocumentSlugConflict(tt.err))
		})
	}
}
