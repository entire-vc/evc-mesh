package service

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Searching documents by CONTENT.
//
// The acceptance criterion is one sentence — search finds documents by their
// content, not only by their title — and the thing that would quietly fail it is
// a service that indexes nothing and searches titles. So the assertions are
// about the text that reached the index, not about which methods were called.

type documentSearchFixture struct {
	svc       *documentService
	repo      *MockDocumentRepository
	storage   *MockStorageClient
	projectID uuid.UUID
	wsID      uuid.UUID
	actorID   uuid.UUID
}

func setupDocumentSearch(t *testing.T) *documentSearchFixture {
	t.Helper()
	projectID, wsID := uuid.New(), uuid.New()

	repo := NewMockDocumentRepository().WithProjectWorkspace(projectID, wsID)
	projects := NewMockProjectRepository()
	require.NoError(t, projects.Create(context.Background(), &domain.Project{ID: projectID, WorkspaceID: wsID}))
	storage := NewMockStorageClient()

	return &documentSearchFixture{
		svc:       NewDocumentService(repo, storage, projects, NewMockDocumentCommentRepository()).(*documentService),
		repo:      repo,
		storage:   storage,
		projectID: projectID,
		wsID:      wsID,
		actorID:   uuid.New(),
	}
}

func (f *documentSearchFixture) create(t *testing.T, title, body string) *domain.Document {
	t.Helper()
	doc, err := f.svc.Create(context.Background(), CreateDocumentInput{
		ProjectID:     f.projectID,
		Title:         title,
		Body:          body,
		CreatedBy:     f.actorID,
		CreatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)
	return doc
}

// ---------------------------------------------------------------------------
// Indexing
// ---------------------------------------------------------------------------

func TestDocumentSearch_CreateIndexesTheBody(t *testing.T) {
	f := setupDocumentSearch(t)

	doc := f.create(t, "Deploy runbook", "The migration is applied before the image swap.")

	indexed, ok := f.repo.SearchText(doc.ID)
	require.True(t, ok, "a created document must reach the index")
	assert.Equal(t, "The migration is applied before the image swap.", indexed)
}

func TestDocumentSearch_UpdateReindexesTheNewBody(t *testing.T) {
	f := setupDocumentSearch(t)
	doc := f.create(t, "Runbook", "first version")

	body := "second version, entirely rewritten"
	_, err := f.svc.Update(context.Background(), doc.ID, f.wsID, UpdateDocumentInput{
		Body:          &body,
		UpdatedBy:     f.actorID,
		UpdatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)

	indexed, _ := f.repo.SearchText(doc.ID)
	assert.Equal(t, body, indexed, "the index must follow the body, not lag one edit behind")
}

func TestDocumentSearch_RenameDoesNotRewriteTheIndexedBody(t *testing.T) {
	f := setupDocumentSearch(t)
	doc := f.create(t, "Runbook", "a megabyte of unchanged prose")

	title := "Runbook, renamed"
	_, err := f.svc.Update(context.Background(), doc.ID, f.wsID, UpdateDocumentInput{
		Title:         &title,
		UpdatedBy:     f.actorID,
		UpdatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)

	// Still there, and still the same text: a rename must not push the whole body
	// back through the write path to store identical bytes.
	indexed, ok := f.repo.SearchText(doc.ID)
	require.True(t, ok)
	assert.Equal(t, "a megabyte of unchanged prose", indexed)
}

func TestDocumentSearch_IndexFailureDoesNotFailTheSave(t *testing.T) {
	// The body is in object storage by the time indexing runs. Failing the save
	// because the INDEX could not be written would trade a working document for a
	// working search box.
	f := setupDocumentSearch(t)
	f.repo.failSearchTextOnly = true

	doc, err := f.svc.Create(context.Background(), CreateDocumentInput{
		ProjectID: f.projectID, Title: "Runbook", Body: "text",
		CreatedBy: f.actorID, CreatedByType: domain.ActorTypeUser,
	})

	require.NoError(t, err, "the document must be saved even when indexing fails")
	require.NotNil(t, doc)
	// And it is still findable by the half that does not depend on the index.
	hits, searchErr := f.svc.Search(context.Background(), f.projectID, f.wsID, "Runbook", 0)
	require.NoError(t, searchErr)
	assert.Len(t, hits, 1)
}

// ---------------------------------------------------------------------------
// Searching
// ---------------------------------------------------------------------------

func TestDocumentSearch_FindsByContentNotOnlyTitle(t *testing.T) {
	f := setupDocumentSearch(t)
	f.create(t, "Onboarding", "New joiners should read the rollback procedure.")
	f.create(t, "Unrelated", "Nothing to see here.")

	hits, err := f.svc.Search(context.Background(), f.projectID, f.wsID, "rollback", 0)

	require.NoError(t, err)
	require.Len(t, hits, 1, "the word appears only in the body — a title search finds nothing")
	assert.Equal(t, "Onboarding", hits[0].Title)
}

func TestDocumentSearch_RefusesAnEmptyQuery(t *testing.T) {
	f := setupDocumentSearch(t)
	f.create(t, "Runbook", "text")

	for _, q := range []string{"", "   ", "\n\t"} {
		_, err := f.svc.Search(context.Background(), f.projectID, f.wsID, q, 0)
		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr, "query %q", q)
		assert.Equal(t, 400, apiErr.StatusCode())
	}
}

func TestDocumentSearch_RefusesAnotherTenant(t *testing.T) {
	// The negative control named in the acceptance criteria: a project id is a
	// caller-supplied value, and knowing one must not be enough to read it.
	f := setupDocumentSearch(t)
	f.create(t, "Runbook", "rollback procedure")

	hits, err := f.svc.Search(context.Background(), f.projectID, uuid.New(), "rollback", 0)

	require.NoError(t, err)
	assert.Empty(t, hits)
}

func TestDocumentSearch_CapsTheLimit(t *testing.T) {
	f := setupDocumentSearch(t)
	for i := 0; i < 60; i++ {
		f.create(t, "Doc", "rollback")
	}

	hits, err := f.svc.Search(context.Background(), f.projectID, f.wsID, "rollback", 1000)

	require.NoError(t, err)
	assert.LessOrEqual(t, len(hits), maxSearchResults)
}

func TestDocumentSearch_DefaultsTheLimit(t *testing.T) {
	f := setupDocumentSearch(t)
	for i := 0; i < 40; i++ {
		f.create(t, "Doc", "rollback")
	}

	hits, err := f.svc.Search(context.Background(), f.projectID, f.wsID, "rollback", 0)

	require.NoError(t, err)
	assert.Len(t, hits, defaultSearchResults)
}

func TestDocumentSearch_LongBodyIsIndexedWhole(t *testing.T) {
	// The service stores the whole body; the truncation that keeps a tsvector
	// legal lives in the trigger, where it belongs — the column is the record of
	// what the document says, not of what fits in an index.
	f := setupDocumentSearch(t)
	body := strings.Repeat("word ", 100000)

	doc := f.create(t, "Huge", body)

	indexed, _ := f.repo.SearchText(doc.ID)
	assert.Len(t, indexed, len(body))
}
