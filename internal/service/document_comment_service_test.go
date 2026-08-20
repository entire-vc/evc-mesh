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
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

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

	// Where "the API" actually sits in documentCommentBody, in the units the
	// anchor columns are documented in — bytes, half-open. Located rather than
	// invented: the service checks that an anchor's offsets contain its own
	// quote, so a made-up offset would fail for a reason no test here is about.
	quoteStart, quoteEnd int
}

func setupDocumentCommentService(t *testing.T) *documentCommentFixture {
	t.Helper()

	projectID, wsID, documentID := uuid.New(), uuid.New(), uuid.New()

	docs := NewMockDocumentRepository().WithProjectWorkspace(projectID, wsID)
	docs.Seed(&domain.Document{
		ID: documentID, ProjectID: projectID, Title: "Runbook", Body: documentCommentBody,
	})

	comments := NewMockDocumentCommentRepository().WithDocumentWorkspace(documentID, wsID)

	timeNow = func() time.Time { return frozenTime }

	at := strings.Index(documentCommentBody, documentCommentQuote)
	require.GreaterOrEqual(t, at, 0, "fixture body does not contain the quote it anchors on")

	return &documentCommentFixture{
		svc:         NewDocumentCommentService(comments, docs, docs),
		comments:    comments,
		docs:        docs,
		documentID:  documentID,
		projectID:   projectID,
		wsID:        wsID,
		author:      uuid.New(),
		otherPerson: uuid.New(),
		quoteStart:  at,
		quoteEnd:    at + len(documentCommentQuote),
	}
}

// documentCommentBody is the markdown the fixture document holds. The anchors in
// these tests are located in it rather than made up: since the service checks
// that an anchor's offsets point at its own quote, an invented offset would fail
// for a reason the test is not about.
const documentCommentBody = "Send the token to authenticate the API with a token header.\n"

// documentCommentQuote is the fixture's anchored text.
const documentCommentQuote = "the API"

func (f *documentCommentFixture) createInput() CreateDocumentCommentInput {
	return CreateDocumentCommentInput{
		DocumentID:  f.documentID,
		WorkspaceID: f.wsID,
		Body:        "this contradicts the paragraph above",
		Anchor: &domain.DocumentCommentAnchor{
			Exact: documentCommentQuote, Prefix: "authenticate ", Suffix: " with a token",
			Start: intPointer(f.quoteStart), End: intPointer(f.quoteEnd),
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
	assert.Equal(t, f.quoteStart, *c.Anchor.Start)
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
		documentBody: f.docs,
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
