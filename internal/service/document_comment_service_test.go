package service

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// runbookBody is the markdown the fixture document holds.
//
// It is Russian because that is where a byte offset and a character offset are
// different numbers: an ASCII fixture would let a resolver using either one pass.
// It repeats "токен" on purpose, so the ambiguity path has something to be
// ambiguous about, and carries one bold run for the markup-tolerant path.
const runbookBody = `# Рунбук отката

Сначала верните образ, потом применяйте миграцию. Токен берётся из 1Password.

Порядок отката: **сначала верните образ**, потом миграцию. Токен обязателен.
`

// documentCommentFixture is one document inside one workspace, with the service
// under test wired to fresh mocks.
type documentCommentFixture struct {
	svc         DocumentCommentService
	comments    *MockDocumentCommentRepository
	docs        *MockDocumentRepository
	documentID  uuid.UUID
	projectID   uuid.UUID
	wsID        uuid.UUID
	author      uuid.UUID
	otherPerson uuid.UUID
}

func setupDocumentCommentService(t *testing.T) *documentCommentFixture {
	t.Helper()

	projectID, wsID, documentID := uuid.New(), uuid.New(), uuid.New()

	docs := NewMockDocumentRepository().WithProjectWorkspace(projectID, wsID)
	docs.Seed(&domain.Document{ID: documentID, ProjectID: projectID, Title: "Runbook", Body: runbookBody})

	comments := NewMockDocumentCommentRepository().WithDocumentWorkspace(documentID, wsID)

	timeNow = func() time.Time { return frozenTime }

	return &documentCommentFixture{
		// The document repository doubles as the body reader here: in production
		// those are two different objects (the reader fetches from object storage),
		// but the port is one method with one signature and the fake satisfies it.
		svc:         NewDocumentCommentService(comments, docs, docs),
		comments:    comments,
		docs:        docs,
		documentID:  documentID,
		projectID:   projectID,
		wsID:        wsID,
		author:      uuid.New(),
		otherPerson: uuid.New(),
	}
}

func (f *documentCommentFixture) createInput() CreateDocumentCommentInput {
	return CreateDocumentCommentInput{
		DocumentID:  f.documentID,
		WorkspaceID: f.wsID,
		Body:        "this contradicts the paragraph above",
		Anchor: &domain.DocumentCommentAnchor{
			Exact: "the API", Prefix: "authenticate ", Suffix: " with a token",
			Start: intPointer(10), End: intPointer(17),
		},
		AuthorID:   f.author,
		AuthorType: domain.ActorTypeUser,
	}
}

// create is the happy-path create most tests start from.
func (f *documentCommentFixture) create(t *testing.T) *domain.DocumentComment {
	t.Helper()
	c, err := f.svc.Create(context.Background(), f.createInput())
	require.NoError(t, err)
	require.NotNil(t, c)
	return c
}

func intPointer(v int) *int { return &v }

// --- Create ---

func TestDocumentCommentService_Create(t *testing.T) {
	f := setupDocumentCommentService(t)

	c := f.create(t)

	assert.NotEqual(t, uuid.Nil, c.ID)
	assert.Equal(t, f.documentID, c.DocumentID)
	assert.Nil(t, c.ParentCommentID)
	assert.Equal(t, f.author, c.AuthorID)
	assert.Equal(t, domain.ActorTypeUser, c.AuthorType)
	assert.Equal(t, frozenTime, c.CreatedAt)
	assert.Equal(t, frozenTime, c.UpdatedAt)
	assert.False(t, c.IsResolved(), "a comment cannot be born resolved")

	require.NotNil(t, c.Anchor)
	assert.Equal(t, "the API", c.Anchor.Exact)
	require.NotNil(t, c.Anchor.Start)
	assert.Equal(t, 10, *c.Anchor.Start)
	assert.False(t, c.Anchor.IsOrphaned())
}

func TestDocumentCommentService_Create_TrimsTheBody(t *testing.T) {
	f := setupDocumentCommentService(t)
	in := f.createInput()
	in.Body = "  needs a comma  "

	c, err := f.svc.Create(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, "needs a comma", c.Body)
}

func TestDocumentCommentService_Create_EmptyBodyIsRefused(t *testing.T) {
	f := setupDocumentCommentService(t)
	in := f.createInput()
	in.Body = "   "

	_, err := f.svc.Create(context.Background(), in)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
}

func TestDocumentCommentService_Create_OversizedBodyIsRefused(t *testing.T) {
	f := setupDocumentCommentService(t)
	in := f.createInput()
	in.Body = strings.Repeat("x", maxDocumentCommentBodyBytes+1)

	_, err := f.svc.Create(context.Background(), in)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
}

// A comment on another tenant's document is a 404, not an empty success: the
// workspace is what makes the document id checkable at all.
func TestDocumentCommentService_Create_OtherWorkspaceIsNotFound(t *testing.T) {
	f := setupDocumentCommentService(t)
	in := f.createInput()
	in.WorkspaceID = uuid.New()

	_, err := f.svc.Create(context.Background(), in)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

func TestDocumentCommentService_Create_UnknownDocumentIsNotFound(t *testing.T) {
	f := setupDocumentCommentService(t)
	in := f.createInput()
	in.DocumentID = uuid.New()

	_, err := f.svc.Create(context.Background(), in)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

// A comment with no anchor is a comment on the whole page. It must not come back
// carrying an empty anchor, which the frontend would render as an orphan.
func TestDocumentCommentService_Create_WithoutAnAnchor(t *testing.T) {
	f := setupDocumentCommentService(t)
	in := f.createInput()
	in.Anchor = nil

	c, err := f.svc.Create(context.Background(), in)
	require.NoError(t, err)
	assert.Nil(t, c.Anchor)
}

// A client that re-read the body and could not find the range says so, rather
// than keeping offsets it knows are wrong.
func TestDocumentCommentService_Create_OrphanedAnchorIsAccepted(t *testing.T) {
	f := setupDocumentCommentService(t)
	in := f.createInput()
	in.Anchor = &domain.DocumentCommentAnchor{Exact: "the API"}

	c, err := f.svc.Create(context.Background(), in)
	require.NoError(t, err)
	require.NotNil(t, c.Anchor)
	assert.True(t, c.Anchor.IsOrphaned())
	assert.Equal(t, "the API", c.Anchor.Exact)
}

func TestDocumentCommentService_Create_AnchorValidation(t *testing.T) {
	tests := []struct {
		name   string
		anchor *domain.DocumentCommentAnchor
		field  string
	}{
		{
			"offsets with no quote",
			&domain.DocumentCommentAnchor{Start: intPointer(1), End: intPointer(2)},
			"anchor.exact",
		},
		{
			"a neighbourhood with no quote",
			&domain.DocumentCommentAnchor{Prefix: "before "},
			"anchor.exact",
		},
		{
			"start without end",
			&domain.DocumentCommentAnchor{Exact: "q", Start: intPointer(1)},
			"anchor.start",
		},
		{
			"end without start",
			&domain.DocumentCommentAnchor{Exact: "q", End: intPointer(2)},
			"anchor.start",
		},
		{
			"negative start",
			&domain.DocumentCommentAnchor{Exact: "q", Start: intPointer(-1), End: intPointer(2)},
			"anchor.start",
		},
		{
			"empty range",
			&domain.DocumentCommentAnchor{Exact: "q", Start: intPointer(5), End: intPointer(5)},
			"anchor.end",
		},
		{
			"inverted range",
			&domain.DocumentCommentAnchor{Exact: "q", Start: intPointer(9), End: intPointer(2)},
			"anchor.end",
		},
		{
			"oversized quote",
			&domain.DocumentCommentAnchor{Exact: strings.Repeat("x", maxAnchorTextBytes+1)},
			"anchor.exact",
		},
		{
			"oversized prefix",
			&domain.DocumentCommentAnchor{Exact: "q", Prefix: strings.Repeat("x", maxAnchorTextBytes+1)},
			"anchor.prefix",
		},
		{
			"oversized suffix",
			&domain.DocumentCommentAnchor{Exact: "q", Suffix: strings.Repeat("x", maxAnchorTextBytes+1)},
			"anchor.suffix",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := setupDocumentCommentService(t)
			in := f.createInput()
			in.Anchor = tt.anchor

			_, err := f.svc.Create(context.Background(), in)

			var apiErr *apierror.Error
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, 400, apiErr.StatusCode())
			assert.Contains(t, apiErr.Validation, tt.field,
				"the refusal has to name the field the caller got wrong")
		})
	}
}

// An entirely empty anchor object is not an error — it is the same thing as
// sending none, and refusing it would punish a client that always sends the key.
func TestDocumentCommentService_Create_EmptyAnchorObjectIsNoAnchor(t *testing.T) {
	f := setupDocumentCommentService(t)
	in := f.createInput()
	in.Anchor = &domain.DocumentCommentAnchor{}

	c, err := f.svc.Create(context.Background(), in)
	require.NoError(t, err)
	assert.Nil(t, c.Anchor)
}

func TestDocumentCommentService_Create_Reply(t *testing.T) {
	f := setupDocumentCommentService(t)
	root := f.create(t)

	in := f.createInput()
	in.ParentCommentID = &root.ID
	in.Anchor = nil
	in.Body = "agreed, fixing"

	reply, err := f.svc.Create(context.Background(), in)
	require.NoError(t, err)

	require.NotNil(t, reply.ParentCommentID)
	assert.Equal(t, root.ID, *reply.ParentCommentID)
	assert.Nil(t, reply.Anchor, "a reply inherits its parent's anchor rather than copying it")
}

func TestDocumentCommentService_Create_ReplyWithItsOwnAnchorIsRefused(t *testing.T) {
	f := setupDocumentCommentService(t)
	root := f.create(t)

	in := f.createInput()
	in.ParentCommentID = &root.ID

	_, err := f.svc.Create(context.Background(), in)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Contains(t, apiErr.Validation, "anchor")
}

func TestDocumentCommentService_Create_UnknownParentIsRefused(t *testing.T) {
	f := setupDocumentCommentService(t)
	missing := uuid.New()

	in := f.createInput()
	in.ParentCommentID = &missing
	in.Anchor = nil

	_, err := f.svc.Create(context.Background(), in)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Contains(t, apiErr.Validation, "parent_comment_id")
}

// Two documents in one tenant are two conversations. A reply whose parent is on
// another page would appear in one thread and be listed under the other.
func TestDocumentCommentService_Create_ParentOnAnotherDocumentIsRefused(t *testing.T) {
	f := setupDocumentCommentService(t)

	otherDoc := uuid.New()
	f.docs.Seed(&domain.Document{ID: otherDoc, ProjectID: f.projectID, Title: "Other"})
	f.comments.WithDocumentWorkspace(otherDoc, f.wsID)

	elsewhere, err := f.svc.Create(context.Background(), CreateDocumentCommentInput{
		DocumentID: otherDoc, WorkspaceID: f.wsID, Body: "over here",
		AuthorID: f.author, AuthorType: domain.ActorTypeUser,
	})
	require.NoError(t, err)

	in := f.createInput()
	in.ParentCommentID = &elsewhere.ID
	in.Anchor = nil

	_, err = f.svc.Create(context.Background(), in)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
}

// Threads are one level deep, and the refusal is explicit: silently flattening
// would answer 201 to a request the caller can only discover was reinterpreted by
// reading the response.
func TestDocumentCommentService_Create_ReplyToAReplyIsRefused(t *testing.T) {
	f := setupDocumentCommentService(t)
	root := f.create(t)

	replyIn := f.createInput()
	replyIn.ParentCommentID = &root.ID
	replyIn.Anchor = nil
	reply, err := f.svc.Create(context.Background(), replyIn)
	require.NoError(t, err)

	nestedIn := f.createInput()
	nestedIn.ParentCommentID = &reply.ID
	nestedIn.Anchor = nil

	_, err = f.svc.Create(context.Background(), nestedIn)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Contains(t, apiErr.Validation["parent_comment_id"], "one level deep")
}

func TestDocumentCommentService_Create_RepositoryErrorIsReturned(t *testing.T) {
	f := setupDocumentCommentService(t)
	f.comments.FailCreateWith(assert.AnError)

	_, err := f.svc.Create(context.Background(), f.createInput())
	assert.ErrorIs(t, err, assert.AnError)
}

func TestDocumentCommentService_Create_DocumentLookupErrorIsReturned(t *testing.T) {
	f := setupDocumentCommentService(t)
	f.docs.errToReturn = assert.AnError

	_, err := f.svc.Create(context.Background(), f.createInput())
	assert.ErrorIs(t, err, assert.AnError)
}

func TestDocumentCommentService_Create_ParentLookupErrorIsReturned(t *testing.T) {
	f := setupDocumentCommentService(t)
	parentID := uuid.New()
	f.comments.FailWith(assert.AnError)

	in := f.createInput()
	in.ParentCommentID = &parentID
	in.Anchor = nil

	_, err := f.svc.Create(context.Background(), in)
	assert.ErrorIs(t, err, assert.AnError)
}

// --- ListByDocument ---

func TestDocumentCommentService_ListByDocument(t *testing.T) {
	f := setupDocumentCommentService(t)
	first := f.create(t)

	second := f.createInput()
	second.Anchor = nil
	second.Body = "and here"
	_, err := f.svc.Create(context.Background(), second)
	require.NoError(t, err)

	page, err := f.svc.ListByDocument(context.Background(), f.documentID, f.wsID,
		repository.DocumentCommentFilter{}, pagination.Params{})
	require.NoError(t, err)

	assert.Len(t, page.Items, 2)
	assert.Equal(t, f.documentID, first.DocumentID)
}

// Resolving a root takes its replies out of the default listing with it —
// otherwise the reader sees answers to a question that is no longer shown.
func TestDocumentCommentService_ListByDocument_HidesResolvedThreads(t *testing.T) {
	f := setupDocumentCommentService(t)
	root := f.create(t)

	replyIn := f.createInput()
	replyIn.ParentCommentID = &root.ID
	replyIn.Anchor = nil
	_, err := f.svc.Create(context.Background(), replyIn)
	require.NoError(t, err)

	_, err = f.svc.SetResolved(context.Background(), root.ID, f.wsID,
		ResolveDocumentCommentInput{Resolved: true, ActorID: f.otherPerson, ActorType: domain.ActorTypeUser})
	require.NoError(t, err)

	page, err := f.svc.ListByDocument(context.Background(), f.documentID, f.wsID,
		repository.DocumentCommentFilter{}, pagination.Params{})
	require.NoError(t, err)
	assert.Empty(t, page.Items, "the resolved root's reply stayed in the default listing")

	page, err = f.svc.ListByDocument(context.Background(), f.documentID, f.wsID,
		repository.DocumentCommentFilter{IncludeResolved: true}, pagination.Params{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 2)
}

// A listing for another tenant's document is a 404 rather than an empty page: an
// empty page is an answer, and answering at all confirms which ids exist.
func TestDocumentCommentService_ListByDocument_OtherWorkspaceIsNotFound(t *testing.T) {
	f := setupDocumentCommentService(t)
	f.create(t)

	_, err := f.svc.ListByDocument(context.Background(), f.documentID, uuid.New(),
		repository.DocumentCommentFilter{}, pagination.Params{})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

func TestDocumentCommentService_ListByDocument_DocumentLookupErrorIsReturned(t *testing.T) {
	f := setupDocumentCommentService(t)
	f.docs.errToReturn = assert.AnError

	_, err := f.svc.ListByDocument(context.Background(), f.documentID, f.wsID,
		repository.DocumentCommentFilter{}, pagination.Params{})
	assert.ErrorIs(t, err, assert.AnError)
}

// --- Update ---

func TestDocumentCommentService_Update(t *testing.T) {
	f := setupDocumentCommentService(t)
	c := f.create(t)

	updated, err := f.svc.Update(context.Background(), c.ID, f.wsID, UpdateDocumentCommentInput{
		Body: "  actually it is fine  ", EditorID: f.author, EditorType: domain.ActorTypeUser,
	})
	require.NoError(t, err)

	assert.Equal(t, "actually it is fine", updated.Body, "the body is trimmed")
	require.NotNil(t, updated.Anchor)
	assert.Equal(t, "the API", updated.Anchor.Exact, "an edit must not move what the comment was written about")
}

func TestDocumentCommentService_Update_OnlyTheAuthor(t *testing.T) {
	f := setupDocumentCommentService(t)
	c := f.create(t)

	_, err := f.svc.Update(context.Background(), c.ID, f.wsID, UpdateDocumentCommentInput{
		Body: "hijacked", EditorID: f.otherPerson, EditorType: domain.ActorTypeUser,
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 403, apiErr.StatusCode())

	stored, err := f.comments.GetByID(context.Background(), c.ID)
	require.NoError(t, err)
	assert.Equal(t, c.Body, stored.Body, "the refused edit reached the row")
}

// An agent holding the author's uuid is not the author. Comparing only the id
// would make that an assumption instead of a check.
func TestDocumentCommentService_Update_ActorTypeMustMatchToo(t *testing.T) {
	f := setupDocumentCommentService(t)
	c := f.create(t)

	_, err := f.svc.Update(context.Background(), c.ID, f.wsID, UpdateDocumentCommentInput{
		Body: "hijacked", EditorID: f.author, EditorType: domain.ActorTypeAgent,
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 403, apiErr.StatusCode())
}

func TestDocumentCommentService_Update_OtherWorkspaceIsNotFound(t *testing.T) {
	f := setupDocumentCommentService(t)
	c := f.create(t)

	_, err := f.svc.Update(context.Background(), c.ID, uuid.New(), UpdateDocumentCommentInput{
		Body: "hijacked", EditorID: f.author, EditorType: domain.ActorTypeUser,
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

func TestDocumentCommentService_Update_BodyValidation(t *testing.T) {
	f := setupDocumentCommentService(t)
	c := f.create(t)

	for _, body := range []string{"  ", strings.Repeat("x", maxDocumentCommentBodyBytes+1)} {
		_, err := f.svc.Update(context.Background(), c.ID, f.wsID, UpdateDocumentCommentInput{
			Body: body, EditorID: f.author, EditorType: domain.ActorTypeUser,
		})
		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, 400, apiErr.StatusCode())
	}
}

func TestDocumentCommentService_Update_LookupErrorIsReturned(t *testing.T) {
	f := setupDocumentCommentService(t)
	c := f.create(t)
	f.comments.FailWith(assert.AnError)

	_, err := f.svc.Update(context.Background(), c.ID, f.wsID, UpdateDocumentCommentInput{
		Body: "x", EditorID: f.author, EditorType: domain.ActorTypeUser,
	})
	assert.ErrorIs(t, err, assert.AnError)
}

// A failed write is the one error that must NOT be swallowed the way the
// enrichment re-read is: the caller's edit did not land, and answering 200 would
// show them their new text over a row that still holds the old.
func TestDocumentCommentService_Update_WriteErrorIsReturned(t *testing.T) {
	f := setupDocumentCommentService(t)
	c := f.create(t)

	svc := &documentCommentService{
		commentRepo:  &writeFailsRepo{MockDocumentCommentRepository: f.comments},
		documentRepo: f.docs,
	}

	_, err := svc.Update(context.Background(), c.ID, f.wsID, UpdateDocumentCommentInput{
		Body: "x", EditorID: f.author, EditorType: domain.ActorTypeUser,
	})
	assert.ErrorIs(t, err, assert.AnError)
}

// --- SetResolved ---

func TestDocumentCommentService_SetResolved(t *testing.T) {
	f := setupDocumentCommentService(t)
	c := f.create(t)

	// Not the author: resolving is not an authorship power, it is what the person
	// who addressed the feedback does.
	resolved, err := f.svc.SetResolved(context.Background(), c.ID, f.wsID,
		ResolveDocumentCommentInput{Resolved: true, ActorID: f.otherPerson, ActorType: domain.ActorTypeAgent})
	require.NoError(t, err)

	assert.True(t, resolved.IsResolved())
	require.NotNil(t, resolved.ResolvedAt)
	assert.Equal(t, frozenTime, *resolved.ResolvedAt)
	require.NotNil(t, resolved.ResolvedBy)
	assert.Equal(t, f.otherPerson, *resolved.ResolvedBy)
	require.NotNil(t, resolved.ResolvedByType)
	assert.Equal(t, domain.ActorTypeAgent, *resolved.ResolvedByType)
}

func TestDocumentCommentService_SetResolved_Unresolve(t *testing.T) {
	f := setupDocumentCommentService(t)
	c := f.create(t)

	_, err := f.svc.SetResolved(context.Background(), c.ID, f.wsID,
		ResolveDocumentCommentInput{Resolved: true, ActorID: f.otherPerson, ActorType: domain.ActorTypeUser})
	require.NoError(t, err)

	reopened, err := f.svc.SetResolved(context.Background(), c.ID, f.wsID,
		ResolveDocumentCommentInput{Resolved: false, ActorID: f.author, ActorType: domain.ActorTypeUser})
	require.NoError(t, err)

	assert.False(t, reopened.IsResolved())
	assert.Nil(t, reopened.ResolvedAt)
	assert.Nil(t, reopened.ResolvedBy, "a half-cleared resolution would leave 'unresolved, by Ann' in the row")
	assert.Nil(t, reopened.ResolvedByType)
}

// Re-resolving must not rewrite resolved_by, or the last person to press a button
// already in that state takes credit for somebody else's work.
func TestDocumentCommentService_SetResolved_IsIdempotent(t *testing.T) {
	f := setupDocumentCommentService(t)
	c := f.create(t)

	first, err := f.svc.SetResolved(context.Background(), c.ID, f.wsID,
		ResolveDocumentCommentInput{Resolved: true, ActorID: f.otherPerson, ActorType: domain.ActorTypeUser})
	require.NoError(t, err)

	again, err := f.svc.SetResolved(context.Background(), c.ID, f.wsID,
		ResolveDocumentCommentInput{Resolved: true, ActorID: f.author, ActorType: domain.ActorTypeUser})
	require.NoError(t, err)

	require.NotNil(t, again.ResolvedBy)
	assert.Equal(t, *first.ResolvedBy, *again.ResolvedBy)
}

func TestDocumentCommentService_SetResolved_UnresolveAnUnresolvedThreadIsANoOp(t *testing.T) {
	f := setupDocumentCommentService(t)
	c := f.create(t)

	got, err := f.svc.SetResolved(context.Background(), c.ID, f.wsID,
		ResolveDocumentCommentInput{Resolved: false, ActorID: f.author, ActorType: domain.ActorTypeUser})
	require.NoError(t, err)
	assert.False(t, got.IsResolved())
}

// Resolution belongs to the conversation. A half-resolved thread is something the
// listing filter — which hides a thread by its root — could not represent.
func TestDocumentCommentService_SetResolved_ARepliesCannotBeResolved(t *testing.T) {
	f := setupDocumentCommentService(t)
	root := f.create(t)

	replyIn := f.createInput()
	replyIn.ParentCommentID = &root.ID
	replyIn.Anchor = nil
	reply, err := f.svc.Create(context.Background(), replyIn)
	require.NoError(t, err)

	_, err = f.svc.SetResolved(context.Background(), reply.ID, f.wsID,
		ResolveDocumentCommentInput{Resolved: true, ActorID: f.author, ActorType: domain.ActorTypeUser})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
}

func TestDocumentCommentService_SetResolved_OtherWorkspaceIsNotFound(t *testing.T) {
	f := setupDocumentCommentService(t)
	c := f.create(t)

	_, err := f.svc.SetResolved(context.Background(), c.ID, uuid.New(),
		ResolveDocumentCommentInput{Resolved: true, ActorID: f.otherPerson, ActorType: domain.ActorTypeUser})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

func TestDocumentCommentService_SetResolved_LookupErrorIsReturned(t *testing.T) {
	f := setupDocumentCommentService(t)
	c := f.create(t)
	f.comments.FailWith(assert.AnError)

	_, err := f.svc.SetResolved(context.Background(), c.ID, f.wsID,
		ResolveDocumentCommentInput{Resolved: true, ActorID: f.author, ActorType: domain.ActorTypeUser})
	assert.ErrorIs(t, err, assert.AnError)
}

// A resolve that failed to write must not answer 200 with resolved_at set —
// the thread would disappear from the reader's listing and come back on refresh.
func TestDocumentCommentService_SetResolved_WriteErrorIsReturned(t *testing.T) {
	f := setupDocumentCommentService(t)
	c := f.create(t)

	svc := &documentCommentService{
		commentRepo:  &writeFailsRepo{MockDocumentCommentRepository: f.comments},
		documentRepo: f.docs,
	}

	_, err := svc.SetResolved(context.Background(), c.ID, f.wsID,
		ResolveDocumentCommentInput{Resolved: true, ActorID: f.author, ActorType: domain.ActorTypeUser})
	assert.ErrorIs(t, err, assert.AnError)
}

// --- Delete ---

func TestDocumentCommentService_Delete(t *testing.T) {
	f := setupDocumentCommentService(t)
	c := f.create(t)

	require.NoError(t, f.svc.Delete(context.Background(), c.ID, f.wsID, f.author, domain.ActorTypeUser))

	gone, err := f.comments.GetByID(context.Background(), c.ID)
	require.NoError(t, err)
	assert.Nil(t, gone)
}

func TestDocumentCommentService_Delete_TakesTheRepliesWithIt(t *testing.T) {
	f := setupDocumentCommentService(t)
	root := f.create(t)

	replyIn := f.createInput()
	replyIn.ParentCommentID = &root.ID
	replyIn.Anchor = nil
	reply, err := f.svc.Create(context.Background(), replyIn)
	require.NoError(t, err)

	require.NoError(t, f.svc.Delete(context.Background(), root.ID, f.wsID, f.author, domain.ActorTypeUser))

	orphan, err := f.comments.GetByID(context.Background(), reply.ID)
	require.NoError(t, err)
	assert.Nil(t, orphan, "the reply outlived the comment it answers")
}

func TestDocumentCommentService_Delete_OnlyTheAuthor(t *testing.T) {
	f := setupDocumentCommentService(t)
	c := f.create(t)

	err := f.svc.Delete(context.Background(), c.ID, f.wsID, f.otherPerson, domain.ActorTypeUser)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 403, apiErr.StatusCode())

	stored, getErr := f.comments.GetByID(context.Background(), c.ID)
	require.NoError(t, getErr)
	assert.NotNil(t, stored, "the refused delete reached the row")
}

func TestDocumentCommentService_Delete_OtherWorkspaceIsNotFound(t *testing.T) {
	f := setupDocumentCommentService(t)
	c := f.create(t)

	err := f.svc.Delete(context.Background(), c.ID, uuid.New(), f.author, domain.ActorTypeUser)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

// --- enrichment fallback ---

// The write already succeeded when the re-read runs, so a failure there must not
// be reported as one: answering 500 would tell the caller their comment was lost
// when it is in the table.
func TestDocumentCommentService_Create_FallsBackWhenTheReReadFails(t *testing.T) {
	f := setupDocumentCommentService(t)

	svc := &documentCommentService{
		commentRepo:  &readFailsAfterWriteRepo{MockDocumentCommentRepository: f.comments},
		documentRepo: f.docs,
	}

	c, err := svc.Create(context.Background(), f.createInput())
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, "this contradicts the paragraph above", c.Body)
}

// readFailsAfterWriteRepo is the mock with GetByID broken, so only the enrichment
// re-read fails and nothing else does.
type readFailsAfterWriteRepo struct {
	*MockDocumentCommentRepository
}

func (r *readFailsAfterWriteRepo) GetByID(context.Context, uuid.UUID) (*domain.DocumentComment, error) {
	return nil, assert.AnError
}

// writeFailsRepo is the mock with Update broken and every read intact, so a test
// can reach the write and fail only there.
type writeFailsRepo struct {
	*MockDocumentCommentRepository
}

func (r *writeFailsRepo) Update(context.Context, *domain.DocumentComment) error {
	return assert.AnError
}

// --- Create by quote: the caller has no selection, the server measures ---
//
// These exercise the path an agent takes over MCP. The unit under test is the
// service plus the real resolver: nothing is stubbed between "here is a quote"
// and "here are the offsets", because a stub there would be a second answer to
// the question this whole unit exists to answer once.

// quoteInput is a create that names its target by quote rather than by offsets.
func (f *documentCommentFixture) quoteInput(quote string) CreateDocumentCommentInput {
	in := f.createInput()
	in.Anchor = nil
	in.Quote = quote
	return in
}

func TestDocumentCommentService_Create_ByQuote_BuildsTheAnchorFromTheBody(t *testing.T) {
	f := setupDocumentCommentService(t)
	quote := "Сначала верните образ, потом применяйте миграцию."

	c, err := f.svc.Create(context.Background(), f.quoteInput(quote))
	require.NoError(t, err)

	require.NotNil(t, c.Anchor)
	require.NotNil(t, c.Anchor.Start)
	require.NotNil(t, c.Anchor.End)
	assert.Equal(t, quote, c.Anchor.Exact)
	assert.False(t, c.Anchor.IsOrphaned())

	// The acceptance criterion: slice the body bytes with what came back.
	assert.Equal(t, quote, runbookBody[*c.Anchor.Start:*c.Anchor.End],
		"the stored offsets do not slice back to the quote")

	// And the offsets are bytes, not characters — on this body those differ.
	assert.NotEqual(t, utf8.RuneCountInString(runbookBody[:*c.Anchor.Start]), *c.Anchor.Start,
		"character offsets and byte offsets coincide here, so this assertion proves nothing")
}

func TestDocumentCommentService_Create_ByQuote_FillsTheNeighbours(t *testing.T) {
	f := setupDocumentCommentService(t)

	c, err := f.svc.Create(context.Background(), f.quoteInput("Токен берётся из 1Password."))
	require.NoError(t, err)

	require.NotNil(t, c.Anchor)
	assert.True(t, strings.HasSuffix(runbookBody[:*c.Anchor.Start], c.Anchor.Prefix))
	assert.True(t, strings.HasPrefix(runbookBody[*c.Anchor.End:], c.Anchor.Suffix))
	assert.NotEmpty(t, c.Anchor.Prefix, "there is text before this quote; the anchor should carry some of it")
}

func TestDocumentCommentService_Create_ByQuote_AmbiguousSaysHowManyTimes(t *testing.T) {
	f := setupDocumentCommentService(t)

	// "Токен" appears twice in the body and nothing says which is meant.
	_, err := f.svc.Create(context.Background(), f.quoteInput("Токен"))

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Contains(t, apiErr.Validation["quote"], "2 times",
		"the caller needs the count to act on: %q", apiErr.Validation["quote"])
	assert.Contains(t, apiErr.Validation["quote"], "quote_context",
		"say what to send next, not just that it failed")
}

func TestDocumentCommentService_Create_ByQuote_AmbiguousWritesNothing(t *testing.T) {
	f := setupDocumentCommentService(t)

	_, err := f.svc.Create(context.Background(), f.quoteInput("Токен"))
	require.Error(t, err)

	page, listErr := f.svc.ListByDocument(context.Background(), f.documentID, f.wsID,
		repository.DocumentCommentFilter{}, pagination.Params{})
	require.NoError(t, listErr)
	assert.Empty(t, page.Items,
		"an ambiguous quote must not land on the first occurrence — or anywhere else")
}

func TestDocumentCommentService_Create_ByQuote_ContextNarrowsIt(t *testing.T) {
	f := setupDocumentCommentService(t)
	in := f.quoteInput("Токен")
	in.QuoteContext = "миграцию. Токен обязателен."

	c, err := f.svc.Create(context.Background(), in)
	require.NoError(t, err)

	require.NotNil(t, c.Anchor)
	assert.Equal(t, "Токен", runbookBody[*c.Anchor.Start:*c.Anchor.End])
	assert.Equal(t, strings.LastIndex(runbookBody, "Токен"), *c.Anchor.Start,
		"the context named the second occurrence")
}

func TestDocumentCommentService_Create_ByQuote_SuffixNarrowsIt(t *testing.T) {
	f := setupDocumentCommentService(t)
	in := f.quoteInput("Токен")
	in.QuoteSuffix = " обязателен."

	c, err := f.svc.Create(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, strings.LastIndex(runbookBody, "Токен"), *c.Anchor.Start)
}

func TestDocumentCommentService_Create_ByQuote_ContextThatFitsBothIsStillRefused(t *testing.T) {
	f := setupDocumentCommentService(t)
	in := f.quoteInput("Токен")
	// True of BOTH occurrences — every sentence in this body ends that way. A
	// context that narrows nothing must not be treated as having narrowed
	// something, or the caller gets an arbitrary pick reported as a decision.
	in.QuotePrefix = "миграцию. "

	_, err := f.svc.Create(context.Background(), in)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Contains(t, apiErr.Validation["quote"], "2 times")
}

func TestDocumentCommentService_Create_ByQuote_MissingQuoteIsNamed(t *testing.T) {
	f := setupDocumentCommentService(t)

	_, err := f.svc.Create(context.Background(), f.quoteInput("этой фразы в документе нет"))

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Contains(t, apiErr.Validation["quote"], "no such quote in the document")
}

func TestDocumentCommentService_Create_ByQuote_SpanningMarkupResolves(t *testing.T) {
	f := setupDocumentCommentService(t)
	// What a reader sees. The source has "**сначала верните образ**".
	quote := "сначала верните образ, потом миграцию."

	c, err := f.svc.Create(context.Background(), f.quoteInput(quote))
	require.NoError(t, err)

	require.NotNil(t, c.Anchor)
	slice := runbookBody[*c.Anchor.Start:*c.Anchor.End]
	assert.Contains(t, slice, "**", "the raw slice carries the markup the quote does not")
	assert.Equal(t, quote, c.Anchor.Exact)
}

func TestDocumentCommentService_Create_ByQuote_AndAnchorTogetherIsRefused(t *testing.T) {
	f := setupDocumentCommentService(t)
	in := f.createInput() // carries an anchor
	in.Quote = "Токен берётся из 1Password."

	_, err := f.svc.Create(context.Background(), in)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Contains(t, apiErr.Validation["quote"], "not both")
}

func TestDocumentCommentService_Create_ByQuote_OnAReplyIsRefused(t *testing.T) {
	f := setupDocumentCommentService(t)
	root := f.create(t)

	in := f.quoteInput("Токен берётся из 1Password.")
	in.ParentCommentID = &root.ID

	_, err := f.svc.Create(context.Background(), in)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Contains(t, apiErr.Validation["quote"], "inherits its parent's anchor",
		"the reply rule is reported against the field the caller actually sent")
}

func TestDocumentCommentService_Create_ContextWithoutAQuoteIsRefused(t *testing.T) {
	f := setupDocumentCommentService(t)
	in := f.createInput()
	in.Anchor = nil
	in.QuoteContext = "миграцию. Токен обязателен."

	_, err := f.svc.Create(context.Background(), in)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Contains(t, apiErr.Validation["quote"], "required")
}

func TestDocumentCommentService_Create_ByQuote_BothContextFormsAtOnceIsRefused(t *testing.T) {
	f := setupDocumentCommentService(t)
	in := f.quoteInput("Токен")
	in.QuoteContext = "миграцию. Токен обязателен."
	in.QuotePrefix = "миграцию. "

	_, err := f.svc.Create(context.Background(), in)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Contains(t, apiErr.Validation["quote_context"], "not both")
}

func TestDocumentCommentService_Create_ByQuote_OversizedQuoteIsRefused(t *testing.T) {
	f := setupDocumentCommentService(t)

	_, err := f.svc.Create(context.Background(), f.quoteInput(strings.Repeat("я", maxAnchorTextBytes)))

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Contains(t, apiErr.Validation["quote"], "at most")
}

func TestDocumentCommentService_Create_ByQuote_WithoutABodyReaderIs503(t *testing.T) {
	f := setupDocumentCommentService(t)
	// A deployment with no object storage: everything else about comments keeps
	// working, and only the path that genuinely needs the markdown says it cannot.
	svc := NewDocumentCommentService(f.comments, f.docs, nil)

	_, err := svc.Create(context.Background(), f.quoteInput("Токен берётся из 1Password."))

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 503, apiErr.StatusCode())

	// The control: the same service still takes an ordinary comment.
	_, err = svc.Create(context.Background(), f.createInput())
	assert.NoError(t, err)
}

func TestDocumentCommentService_Create_ByQuote_EmptyBodySaysSo(t *testing.T) {
	f := setupDocumentCommentService(t)
	f.docs.Seed(&domain.Document{ID: f.documentID, ProjectID: f.projectID, Title: "Runbook", Body: ""})

	_, err := f.svc.Create(context.Background(), f.quoteInput("Токен"))

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Contains(t, apiErr.Validation["quote"], "no body",
		"an unfetched body must not read as an absent quote")
}
