package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// documentCommentFixture is one document, inside one project, inside one
// workspace, with the service under test wired to fresh mocks.
type documentCommentFixture struct {
	svc        *documentCommentService
	repo       *MockDocumentCommentRepository
	docRepo    *MockDocumentRepository
	documentID uuid.UUID
	projectID  uuid.UUID
	wsID       uuid.UUID
	authorID   uuid.UUID
}

func setupDocumentCommentService(t *testing.T) *documentCommentFixture {
	t.Helper()

	projectID, wsID, documentID := uuid.New(), uuid.New(), uuid.New()

	docRepo := NewMockDocumentRepository().WithProjectWorkspace(projectID, wsID)
	docRepo.Seed(&domain.Document{
		ID:        documentID,
		ProjectID: projectID,
		Slug:      "runbook",
		Title:     "Runbook",
	})
	repo := NewMockDocumentCommentRepository().WithDocumentWorkspace(documentID, wsID)

	return &documentCommentFixture{
		svc:        NewDocumentCommentService(repo, docRepo).(*documentCommentService),
		repo:       repo,
		docRepo:    docRepo,
		documentID: documentID,
		projectID:  projectID,
		wsID:       wsID,
		authorID:   uuid.New(),
	}
}

func sampleAnchor() *domain.DocumentAnchor {
	return &domain.DocumentAnchor{
		Start:  120,
		End:    168,
		Exact:  "the migration is applied before the image swap",
		Prefix: "Deploy discipline. ",
		Suffix: ", never after.",
	}
}

func (f *documentCommentFixture) createRoot(t *testing.T) *domain.DocumentComment {
	t.Helper()
	c, err := f.svc.Create(context.Background(), CreateDocumentCommentInput{
		DocumentID:  f.documentID,
		WorkspaceID: f.wsID,
		Body:        "This contradicts the runbook.",
		Anchor:      sampleAnchor(),
		AuthorID:    f.authorID,
		AuthorType:  domain.ActorTypeUser,
	})
	require.NoError(t, err)
	return c
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func TestDocumentCommentService_Create_RootKeepsItsAnchor(t *testing.T) {
	f := setupDocumentCommentService(t)

	c := f.createRoot(t)

	require.NotNil(t, c.Anchor)
	assert.Equal(t, *sampleAnchor(), *c.Anchor)
	assert.True(t, c.IsRoot())
	assert.False(t, c.IsResolved())
}

func TestDocumentCommentService_Create_RequiresAnchorOnARoot(t *testing.T) {
	f := setupDocumentCommentService(t)

	_, err := f.svc.Create(context.Background(), CreateDocumentCommentInput{
		DocumentID:  f.documentID,
		WorkspaceID: f.wsID,
		Body:        "floating comment",
		AuthorID:    f.authorID,
		AuthorType:  domain.ActorTypeUser,
	})

	// A field-level validation error, not the 500 a violated CHECK constraint
	// would give.
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
}

func TestDocumentCommentService_Create_ReplyMustNotCarryAnAnchor(t *testing.T) {
	f := setupDocumentCommentService(t)
	root := f.createRoot(t)

	_, err := f.svc.Create(context.Background(), CreateDocumentCommentInput{
		DocumentID:  f.documentID,
		WorkspaceID: f.wsID,
		Body:        "agreed",
		ParentID:    &root.ID,
		Anchor:      sampleAnchor(),
		AuthorID:    f.authorID,
		AuthorType:  domain.ActorTypeUser,
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
}

func TestDocumentCommentService_Create_ReplyJoinsTheThread(t *testing.T) {
	f := setupDocumentCommentService(t)
	root := f.createRoot(t)

	reply, err := f.svc.Create(context.Background(), CreateDocumentCommentInput{
		DocumentID:  f.documentID,
		WorkspaceID: f.wsID,
		Body:        "agreed, fixing",
		ParentID:    &root.ID,
		AuthorID:    uuid.New(),
		AuthorType:  domain.ActorTypeAgent,
	})

	require.NoError(t, err)
	assert.Nil(t, reply.Anchor)
	require.NotNil(t, reply.ParentID)
	assert.Equal(t, root.ID, *reply.ParentID)
}

func TestDocumentCommentService_Create_RefusesAReplyFromAnotherDocument(t *testing.T) {
	f := setupDocumentCommentService(t)
	root := f.createRoot(t)

	// A second document in the same workspace: the tenant check passes, so the
	// only thing that can refuse this is the document check itself. Without it a
	// reply could be grafted onto a page whose root never said what it answers.
	otherDoc := uuid.New()
	f.docRepo.Seed(&domain.Document{ID: otherDoc, ProjectID: f.projectID, Slug: "other", Title: "Other"})
	f.repo.WithDocumentWorkspace(otherDoc, f.wsID)

	_, err := f.svc.Create(context.Background(), CreateDocumentCommentInput{
		DocumentID:  otherDoc,
		WorkspaceID: f.wsID,
		Body:        "grafted",
		ParentID:    &root.ID,
		AuthorID:    f.authorID,
		AuthorType:  domain.ActorTypeUser,
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
}

func TestDocumentCommentService_Create_RefusesAnotherTenantsDocument(t *testing.T) {
	f := setupDocumentCommentService(t)

	_, err := f.svc.Create(context.Background(), CreateDocumentCommentInput{
		DocumentID:  f.documentID,
		WorkspaceID: uuid.New(), // a different tenant
		Body:        "not mine",
		Anchor:      sampleAnchor(),
		AuthorID:    f.authorID,
		AuthorType:  domain.ActorTypeUser,
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

func TestDocumentCommentService_Create_RefusesEmptyAndOversizedBodies(t *testing.T) {
	f := setupDocumentCommentService(t)

	for name, body := range map[string]string{
		"blank":     "   \n\t ",
		"oversized": strings.Repeat("x", maxCommentBodyBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.Create(context.Background(), CreateDocumentCommentInput{
				DocumentID:  f.documentID,
				WorkspaceID: f.wsID,
				Body:        body,
				Anchor:      sampleAnchor(),
				AuthorID:    f.authorID,
				AuthorType:  domain.ActorTypeUser,
			})
			var apiErr *apierror.Error
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, 400, apiErr.StatusCode())
		})
	}
}

// The anchor validations, each of which is a way for a comment to end up unable
// to find its place — or able to find every place.
func TestDocumentCommentService_Create_RefusesUnresolvableAnchors(t *testing.T) {
	f := setupDocumentCommentService(t)

	cases := map[string]domain.DocumentAnchor{
		// Would match at every position in the document.
		"empty range":    {Start: 10, End: 10, Exact: "x", Prefix: "", Suffix: ""},
		"inverted range": {Start: 20, End: 10, Exact: "x", Prefix: "", Suffix: ""},
		"negative start": {Start: -1, End: 10, Exact: "x", Prefix: "", Suffix: ""},
		// No quote means nothing survives an edit above it: the anchor would fall
		// straight through to the context match on every single read.
		"blank quote": {Start: 10, End: 20, Exact: "   ", Prefix: "", Suffix: ""},
		"oversized quote": {
			Start: 10, End: 20,
			Exact: strings.Repeat("x", maxAnchorFieldLen+1),
		},
	}

	for name, anchor := range cases {
		t.Run(name, func(t *testing.T) {
			a := anchor
			_, err := f.svc.Create(context.Background(), CreateDocumentCommentInput{
				DocumentID:  f.documentID,
				WorkspaceID: f.wsID,
				Body:        "note",
				Anchor:      &a,
				AuthorID:    f.authorID,
				AuthorType:  domain.ActorTypeUser,
			})
			var apiErr *apierror.Error
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, 400, apiErr.StatusCode())
		})
	}
}

// The positive control for the table above: the same shape, valid, is accepted.
// Without it a validator that refused everything would pass every case.
func TestDocumentCommentService_Create_AcceptsAMinimalValidAnchor(t *testing.T) {
	f := setupDocumentCommentService(t)

	_, err := f.svc.Create(context.Background(), CreateDocumentCommentInput{
		DocumentID:  f.documentID,
		WorkspaceID: f.wsID,
		Body:        "note",
		// Context may legitimately be empty — a range at the very start or end of
		// a document has nothing on one side.
		Anchor:     &domain.DocumentAnchor{Start: 0, End: 1, Exact: "x", Prefix: "", Suffix: ""},
		AuthorID:   f.authorID,
		AuthorType: domain.ActorTypeUser,
	})

	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func TestDocumentCommentService_ListByDocument_ReturnsTheWholeThread(t *testing.T) {
	f := setupDocumentCommentService(t)
	root := f.createRoot(t)
	_, err := f.svc.Create(context.Background(), CreateDocumentCommentInput{
		DocumentID: f.documentID, WorkspaceID: f.wsID, Body: "reply",
		ParentID: &root.ID, AuthorID: f.authorID, AuthorType: domain.ActorTypeUser,
	})
	require.NoError(t, err)

	got, err := f.svc.ListByDocument(context.Background(), f.documentID, f.wsID)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestDocumentCommentService_ListByDocument_RefusesAnotherTenant(t *testing.T) {
	f := setupDocumentCommentService(t)
	f.createRoot(t)

	_, err := f.svc.ListByDocument(context.Background(), f.documentID, uuid.New())

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

// ---------------------------------------------------------------------------
// UpdateBody
// ---------------------------------------------------------------------------

func TestDocumentCommentService_UpdateBody_AuthorMayRewriteTheirOwn(t *testing.T) {
	f := setupDocumentCommentService(t)
	root := f.createRoot(t)

	got, err := f.svc.UpdateBody(context.Background(), root.ID, f.wsID, "  rewritten  ", f.authorID)

	require.NoError(t, err)
	assert.Equal(t, "rewritten", got.Body)
	// The anchor is not a field an edit can reach: a caller who could rewrite both
	// at once could move a comment onto text its author never saw.
	require.NotNil(t, got.Anchor)
	assert.Equal(t, *sampleAnchor(), *got.Anchor)
}

func TestDocumentCommentService_UpdateBody_RefusesAnyoneElse(t *testing.T) {
	f := setupDocumentCommentService(t)
	root := f.createRoot(t)

	_, err := f.svc.UpdateBody(context.Background(), root.ID, f.wsID, "words in your mouth", uuid.New())

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 403, apiErr.StatusCode())

	// And it really did not write: a 403 that still mutated would be worse than no
	// check at all.
	after, err := f.svc.ListByDocument(context.Background(), f.documentID, f.wsID)
	require.NoError(t, err)
	assert.Equal(t, "This contradicts the runbook.", after[0].Body)
}

// ---------------------------------------------------------------------------
// Resolve
// ---------------------------------------------------------------------------

func TestDocumentCommentService_SetResolved_RoundTrips(t *testing.T) {
	f := setupDocumentCommentService(t)
	root := f.createRoot(t)
	other := uuid.New()

	// Anyone with write access may resolve — a note only its author could close is
	// a note that outlives them.
	resolved, err := f.svc.SetResolved(context.Background(), root.ID, f.wsID, true, other, domain.ActorTypeAgent)
	require.NoError(t, err)
	require.True(t, resolved.IsResolved())
	require.NotNil(t, resolved.ResolvedBy)
	assert.Equal(t, other, *resolved.ResolvedBy)
	require.NotNil(t, resolved.ResolvedByType)
	assert.Equal(t, domain.ActorTypeAgent, *resolved.ResolvedByType)

	reopened, err := f.svc.SetResolved(context.Background(), root.ID, f.wsID, false, other, domain.ActorTypeAgent)
	require.NoError(t, err)
	assert.False(t, reopened.IsResolved())
	// All three cleared together. A stale actor left behind would have the next
	// reader attribute a resolution nobody made.
	assert.Nil(t, reopened.ResolvedBy)
	assert.Nil(t, reopened.ResolvedByType)

	// And the round trip is visible to a fresh read, not just in the returned
	// struct — the difference between "the service updated its copy" and "the
	// service wrote".
	after, err := f.svc.ListByDocument(context.Background(), f.documentID, f.wsID)
	require.NoError(t, err)
	assert.False(t, after[0].IsResolved())
	assert.Nil(t, after[0].ResolvedBy)
}

func TestDocumentCommentService_SetResolved_RefusesAReply(t *testing.T) {
	f := setupDocumentCommentService(t)
	root := f.createRoot(t)
	reply, err := f.svc.Create(context.Background(), CreateDocumentCommentInput{
		DocumentID: f.documentID, WorkspaceID: f.wsID, Body: "reply",
		ParentID: &root.ID, AuthorID: f.authorID, AuthorType: domain.ActorTypeUser,
	})
	require.NoError(t, err)

	_, err = f.svc.SetResolved(context.Background(), reply.ID, f.wsID, true, f.authorID, domain.ActorTypeUser)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestDocumentCommentService_Delete_TakesTheRepliesWithIt(t *testing.T) {
	f := setupDocumentCommentService(t)
	root := f.createRoot(t)
	reply, err := f.svc.Create(context.Background(), CreateDocumentCommentInput{
		DocumentID: f.documentID, WorkspaceID: f.wsID, Body: "reply",
		ParentID: &root.ID, AuthorID: f.authorID, AuthorType: domain.ActorTypeUser,
	})
	require.NoError(t, err)
	nested, err := f.svc.Create(context.Background(), CreateDocumentCommentInput{
		DocumentID: f.documentID, WorkspaceID: f.wsID, Body: "nested reply",
		ParentID: &reply.ID, AuthorID: f.authorID, AuthorType: domain.ActorTypeUser,
	})
	require.NoError(t, err)
	_ = nested

	require.NoError(t, f.svc.Delete(context.Background(), root.ID, f.wsID, f.authorID))

	// Two levels down, not just one: a reply that outlived its root would be a
	// comment on nothing, displayable nowhere.
	after, err := f.svc.ListByDocument(context.Background(), f.documentID, f.wsID)
	require.NoError(t, err)
	assert.Empty(t, after)
}

func TestDocumentCommentService_Delete_RefusesAnyoneElse(t *testing.T) {
	f := setupDocumentCommentService(t)
	root := f.createRoot(t)

	err := f.svc.Delete(context.Background(), root.ID, f.wsID, uuid.New())

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 403, apiErr.StatusCode())

	after, err := f.svc.ListByDocument(context.Background(), f.documentID, f.wsID)
	require.NoError(t, err)
	assert.Len(t, after, 1)
}

func TestDocumentCommentService_Delete_RefusesAnotherTenant(t *testing.T) {
	f := setupDocumentCommentService(t)
	root := f.createRoot(t)

	err := f.svc.Delete(context.Background(), root.ID, uuid.New(), f.authorID)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

// A comment on a document that has since been soft-deleted is unreachable, not
// merely hidden: the page is gone as far as every read is concerned.
func TestDocumentCommentService_DeletedDocumentHidesItsComments(t *testing.T) {
	f := setupDocumentCommentService(t)
	f.createRoot(t)

	deletedAt := time.Now().UTC()
	f.docRepo.Seed(&domain.Document{
		ID: f.documentID, ProjectID: f.projectID, Slug: "runbook", Title: "Runbook",
		DeletedAt: &deletedAt,
	})

	_, err := f.svc.ListByDocument(context.Background(), f.documentID, f.wsID)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}
