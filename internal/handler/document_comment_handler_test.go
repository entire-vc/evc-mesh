package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

func setupDocumentCommentTest(mockSvc *MockDocumentCommentService) (*DocumentCommentHandler, *echo.Echo) {
	return NewDocumentCommentHandler(mockSvc), echo.New()
}

// docCommentListRequest builds a request against /documents/:doc_id/comments.
func docCommentListRequest(e *echo.Echo, method, docID string, wsID *uuid.UUID, target, body string) (echo.Context, *httptest.ResponseRecorder) {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, http.NoBody)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if wsID != nil {
		c.Set("workspace_id", *wsID)
	}
	c.SetPath("/documents/:doc_id/comments")
	c.SetParamNames("doc_id")
	c.SetParamValues(docID)
	return c, rec
}

// docCommentRequest builds a request against one of the
// /document-comments/:dcom_id routes.
func docCommentRequest(e *echo.Echo, method, commentID string, wsID *uuid.UUID, body string) (echo.Context, *httptest.ResponseRecorder) {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, "/", http.NoBody)
	} else {
		req = httptest.NewRequest(method, "/", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if wsID != nil {
		c.Set("workspace_id", *wsID)
	}
	c.SetPath("/document-comments/:dcom_id")
	c.SetParamNames("dcom_id")
	c.SetParamValues(commentID)
	return c, rec
}

// --- List ---

func TestDocumentCommentHandler_List(t *testing.T) {
	docID, wsID := uuid.New(), uuid.New()
	var gotDoc, gotWS uuid.UUID
	var gotFilter repository.DocumentCommentFilter
	var gotPageSize int

	mockSvc := &MockDocumentCommentService{
		ListByDocumentFunc: func(_ context.Context, d, ws uuid.UUID, f repository.DocumentCommentFilter, pg pagination.Params) (*pagination.Page[domain.DocumentComment], error) {
			gotDoc, gotWS, gotFilter, gotPageSize = d, ws, f, pg.PageSize
			return pagination.NewPage([]domain.DocumentComment{{ID: uuid.New(), Body: "needs a comma"}}, 1, pg), nil
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	c, rec := docCommentListRequest(e, http.MethodGet, docID.String(), &wsID, "/?page_size=2", "")
	require.NoError(t, h.List(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, docID, gotDoc)
	assert.Equal(t, wsID, gotWS, "the listing is narrowed to the caller's workspace")
	assert.Equal(t, 2, gotPageSize)
	assert.False(t, gotFilter.IncludeResolved, "resolved threads are hidden unless asked for")
	assert.Contains(t, rec.Body.String(), "needs a comma")
}

func TestDocumentCommentHandler_List_IncludeResolved(t *testing.T) {
	wsID := uuid.New()
	var gotFilter repository.DocumentCommentFilter

	mockSvc := &MockDocumentCommentService{
		ListByDocumentFunc: func(_ context.Context, _, _ uuid.UUID, f repository.DocumentCommentFilter, pg pagination.Params) (*pagination.Page[domain.DocumentComment], error) {
			gotFilter = f
			return pagination.NewPage([]domain.DocumentComment{}, 0, pg), nil
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	c, rec := docCommentListRequest(e, http.MethodGet, uuid.New().String(), &wsID, "/?include_resolved=true", "")
	require.NoError(t, h.List(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, gotFilter.IncludeResolved)
}

// Anything other than the literal "true" leaves resolved threads hidden: a
// mistyped flag must not quietly widen what comes back.
func TestDocumentCommentHandler_List_IncludeResolvedIsStrict(t *testing.T) {
	wsID := uuid.New()
	var gotFilter repository.DocumentCommentFilter

	mockSvc := &MockDocumentCommentService{
		ListByDocumentFunc: func(_ context.Context, _, _ uuid.UUID, f repository.DocumentCommentFilter, pg pagination.Params) (*pagination.Page[domain.DocumentComment], error) {
			gotFilter = f
			return pagination.NewPage([]domain.DocumentComment{}, 0, pg), nil
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	c, _ := docCommentListRequest(e, http.MethodGet, uuid.New().String(), &wsID, "/?include_resolved=yes", "")
	require.NoError(t, h.List(c))

	assert.False(t, gotFilter.IncludeResolved)
}

// Malformed pagination is the caller's mistake, and it has to be named as one:
// binding it silently would serve page 1 of 50 to somebody who asked for
// something else and looked like it worked.
func TestDocumentCommentHandler_List_MalformedPagination(t *testing.T) {
	wsID := uuid.New()
	called := false
	mockSvc := &MockDocumentCommentService{
		ListByDocumentFunc: func(context.Context, uuid.UUID, uuid.UUID, repository.DocumentCommentFilter, pagination.Params) (*pagination.Page[domain.DocumentComment], error) {
			called = true
			return nil, nil
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	c, rec := docCommentListRequest(e, http.MethodGet, uuid.New().String(), &wsID, "/?page_size=not-a-number", "")
	require.NoError(t, h.List(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.False(t, called)
}

func TestDocumentCommentHandler_List_InvalidDocumentID(t *testing.T) {
	wsID := uuid.New()
	h, e := setupDocumentCommentTest(&MockDocumentCommentService{})

	c, rec := docCommentListRequest(e, http.MethodGet, "not-a-uuid", &wsID, "/", "")
	require.NoError(t, h.List(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// No workspace in context means the guards upstream did not resolve one. A
// listing with no tenant would be a listing across all of them.
func TestDocumentCommentHandler_List_NoWorkspaceInContext(t *testing.T) {
	called := false
	mockSvc := &MockDocumentCommentService{
		ListByDocumentFunc: func(context.Context, uuid.UUID, uuid.UUID, repository.DocumentCommentFilter, pagination.Params) (*pagination.Page[domain.DocumentComment], error) {
			called = true
			return nil, nil
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	c, rec := docCommentListRequest(e, http.MethodGet, uuid.New().String(), nil, "/", "")
	require.NoError(t, h.List(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, called, "the service was reached without a workspace to scope the read to")
}

func TestDocumentCommentHandler_List_ServiceError(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockDocumentCommentService{
		ListByDocumentFunc: func(context.Context, uuid.UUID, uuid.UUID, repository.DocumentCommentFilter, pagination.Params) (*pagination.Page[domain.DocumentComment], error) {
			return nil, apierror.NotFound("Document")
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	c, rec := docCommentListRequest(e, http.MethodGet, uuid.New().String(), &wsID, "/", "")
	require.NoError(t, h.List(c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- Create ---

func TestDocumentCommentHandler_Create(t *testing.T) {
	docID, wsID, userID := uuid.New(), uuid.New(), uuid.New()
	var got service.CreateDocumentCommentInput

	mockSvc := &MockDocumentCommentService{
		CreateFunc: func(_ context.Context, in service.CreateDocumentCommentInput) (*domain.DocumentComment, error) {
			got = in
			return &domain.DocumentComment{ID: uuid.New(), DocumentID: in.DocumentID, Body: in.Body}, nil
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	body := `{"body":"needs a comma","anchor":{"exact":"the API","prefix":"authenticate ","suffix":" with a token","start":10,"end":17}}`
	c, rec := docCommentListRequest(e, http.MethodPost, docID.String(), &wsID, "/", body)
	c.Set("user_id", userID)

	require.NoError(t, h.Create(c))

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, docID, got.DocumentID, "the document comes from the path, not the body")
	assert.Equal(t, wsID, got.WorkspaceID)
	assert.Equal(t, "needs a comma", got.Body)
	assert.Equal(t, userID, got.AuthorID)
	assert.Equal(t, domain.ActorTypeUser, got.AuthorType)
	assert.Nil(t, got.ParentCommentID)

	require.NotNil(t, got.Anchor)
	assert.Equal(t, "the API", got.Anchor.Exact)
	assert.Equal(t, "authenticate ", got.Anchor.Prefix)
	assert.Equal(t, " with a token", got.Anchor.Suffix)
	require.NotNil(t, got.Anchor.Start)
	assert.Equal(t, 10, *got.Anchor.Start)
	require.NotNil(t, got.Anchor.End)
	assert.Equal(t, 17, *got.Anchor.End)
}

// An absent anchor stays absent all the way to the service: bound as a zero value
// it would arrive as an anchor with an empty quote, which is a different request.
func TestDocumentCommentHandler_Create_WithoutAnAnchor(t *testing.T) {
	wsID := uuid.New()
	var got service.CreateDocumentCommentInput

	mockSvc := &MockDocumentCommentService{
		CreateFunc: func(_ context.Context, in service.CreateDocumentCommentInput) (*domain.DocumentComment, error) {
			got = in
			return &domain.DocumentComment{ID: uuid.New()}, nil
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	c, rec := docCommentListRequest(e, http.MethodPost, uuid.New().String(), &wsID, "/", `{"body":"page-level note"}`)
	require.NoError(t, h.Create(c))

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Nil(t, got.Anchor)
}

// An anchor with no offsets is how a client reports an orphan, so the nil has to
// survive binding rather than arriving as 0.
func TestDocumentCommentHandler_Create_OrphanedAnchorKeepsNilOffsets(t *testing.T) {
	wsID := uuid.New()
	var got service.CreateDocumentCommentInput

	mockSvc := &MockDocumentCommentService{
		CreateFunc: func(_ context.Context, in service.CreateDocumentCommentInput) (*domain.DocumentComment, error) {
			got = in
			return &domain.DocumentComment{ID: uuid.New()}, nil
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	c, rec := docCommentListRequest(e, http.MethodPost, uuid.New().String(), &wsID, "/",
		`{"body":"was about this","anchor":{"exact":"the API"}}`)
	require.NoError(t, h.Create(c))

	assert.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, got.Anchor)
	assert.Nil(t, got.Anchor.Start, "0 is a position; nil is 'we no longer know'")
	assert.Nil(t, got.Anchor.End)
}

func TestDocumentCommentHandler_Create_Reply(t *testing.T) {
	wsID, parentID := uuid.New(), uuid.New()
	var got service.CreateDocumentCommentInput

	mockSvc := &MockDocumentCommentService{
		CreateFunc: func(_ context.Context, in service.CreateDocumentCommentInput) (*domain.DocumentComment, error) {
			got = in
			return &domain.DocumentComment{ID: uuid.New()}, nil
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	body := `{"body":"agreed","parent_comment_id":"` + parentID.String() + `"}`
	c, rec := docCommentListRequest(e, http.MethodPost, uuid.New().String(), &wsID, "/", body)
	require.NoError(t, h.Create(c))

	assert.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, got.ParentCommentID)
	assert.Equal(t, parentID, *got.ParentCommentID)
}

func TestDocumentCommentHandler_Create_AgentCallerIsRecordedAsAnAgent(t *testing.T) {
	wsID, agentID := uuid.New(), uuid.New()
	var got service.CreateDocumentCommentInput

	mockSvc := &MockDocumentCommentService{
		CreateFunc: func(_ context.Context, in service.CreateDocumentCommentInput) (*domain.DocumentComment, error) {
			got = in
			return &domain.DocumentComment{ID: uuid.New()}, nil
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	c, _ := docCommentListRequest(e, http.MethodPost, uuid.New().String(), &wsID, "/", `{"body":"from a review agent"}`)
	c.Set("agent_id", agentID)

	require.NoError(t, h.Create(c))
	assert.Equal(t, agentID, got.AuthorID)
	assert.Equal(t, domain.ActorTypeAgent, got.AuthorType)
}

func TestDocumentCommentHandler_Create_InvalidDocumentID(t *testing.T) {
	wsID := uuid.New()
	h, e := setupDocumentCommentTest(&MockDocumentCommentService{})

	c, rec := docCommentListRequest(e, http.MethodPost, "nope", &wsID, "/", `{"body":"x"}`)
	require.NoError(t, h.Create(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDocumentCommentHandler_Create_MalformedBody(t *testing.T) {
	wsID := uuid.New()
	h, e := setupDocumentCommentTest(&MockDocumentCommentService{})

	c, rec := docCommentListRequest(e, http.MethodPost, uuid.New().String(), &wsID, "/", `{"body":`)
	require.NoError(t, h.Create(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDocumentCommentHandler_Create_NoWorkspaceInContext(t *testing.T) {
	h, e := setupDocumentCommentTest(&MockDocumentCommentService{})

	c, rec := docCommentListRequest(e, http.MethodPost, uuid.New().String(), nil, "/", `{"body":"x"}`)
	require.NoError(t, h.Create(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// The service's field-level verdict has to reach the caller as a 400 naming the
// field, not be swallowed into a 500.
func TestDocumentCommentHandler_Create_ValidationIsRelayed(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockDocumentCommentService{
		CreateFunc: func(context.Context, service.CreateDocumentCommentInput) (*domain.DocumentComment, error) {
			return nil, apierror.ValidationError(map[string]string{"body": "body is required"})
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	c, rec := docCommentListRequest(e, http.MethodPost, uuid.New().String(), &wsID, "/", `{"body":"  "}`)
	require.NoError(t, h.Create(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "body")
}

// --- Update ---

func TestDocumentCommentHandler_Update(t *testing.T) {
	commentID, wsID, userID := uuid.New(), uuid.New(), uuid.New()
	var gotID, gotWS uuid.UUID
	var gotInput service.UpdateDocumentCommentInput

	mockSvc := &MockDocumentCommentService{
		UpdateFunc: func(_ context.Context, id, ws uuid.UUID, in service.UpdateDocumentCommentInput) (*domain.DocumentComment, error) {
			gotID, gotWS, gotInput = id, ws, in
			return &domain.DocumentComment{ID: id, Body: in.Body}, nil
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	c, rec := docCommentRequest(e, http.MethodPatch, commentID.String(), &wsID, `{"body":"on reflection"}`)
	c.Set("user_id", userID)

	require.NoError(t, h.Update(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, commentID, gotID)
	assert.Equal(t, wsID, gotWS)
	assert.Equal(t, "on reflection", gotInput.Body)
	assert.Equal(t, userID, gotInput.EditorID, "the editor is read from the request, never from the body")
	assert.Equal(t, domain.ActorTypeUser, gotInput.EditorType)
}

func TestDocumentCommentHandler_Update_InvalidCommentID(t *testing.T) {
	wsID := uuid.New()
	h, e := setupDocumentCommentTest(&MockDocumentCommentService{})

	c, rec := docCommentRequest(e, http.MethodPatch, "nope", &wsID, `{"body":"x"}`)
	require.NoError(t, h.Update(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "dcom_id")
}

func TestDocumentCommentHandler_Update_MalformedBody(t *testing.T) {
	wsID := uuid.New()
	h, e := setupDocumentCommentTest(&MockDocumentCommentService{})

	c, rec := docCommentRequest(e, http.MethodPatch, uuid.New().String(), &wsID, `{"body":`)
	require.NoError(t, h.Update(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDocumentCommentHandler_Update_NoWorkspaceInContext(t *testing.T) {
	h, e := setupDocumentCommentTest(&MockDocumentCommentService{})

	c, rec := docCommentRequest(e, http.MethodPatch, uuid.New().String(), nil, `{"body":"x"}`)
	require.NoError(t, h.Update(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestDocumentCommentHandler_Update_ForbiddenIsRelayed(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockDocumentCommentService{
		UpdateFunc: func(context.Context, uuid.UUID, uuid.UUID, service.UpdateDocumentCommentInput) (*domain.DocumentComment, error) {
			return nil, apierror.Forbidden("you can only edit your own comments")
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	c, rec := docCommentRequest(e, http.MethodPatch, uuid.New().String(), &wsID, `{"body":"x"}`)
	require.NoError(t, h.Update(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// --- Resolve / Unresolve ---

func TestDocumentCommentHandler_Resolve(t *testing.T) {
	commentID, wsID, userID := uuid.New(), uuid.New(), uuid.New()
	var gotID, gotWS uuid.UUID
	var gotInput service.ResolveDocumentCommentInput

	mockSvc := &MockDocumentCommentService{
		SetResolvedFunc: func(_ context.Context, id, ws uuid.UUID, in service.ResolveDocumentCommentInput) (*domain.DocumentComment, error) {
			gotID, gotWS, gotInput = id, ws, in
			return &domain.DocumentComment{ID: id}, nil
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	c, rec := docCommentRequest(e, http.MethodPost, commentID.String(), &wsID, "")
	c.Set("user_id", userID)

	require.NoError(t, h.Resolve(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, commentID, gotID)
	assert.Equal(t, wsID, gotWS)
	assert.True(t, gotInput.Resolved)
	assert.Equal(t, userID, gotInput.ActorID)
	assert.Equal(t, domain.ActorTypeUser, gotInput.ActorType)
}

// The desired state is in the URL, not in a body field, so the two endpoints
// cannot both be reached by one request that forgot to set it.
func TestDocumentCommentHandler_Unresolve(t *testing.T) {
	wsID := uuid.New()
	var gotInput service.ResolveDocumentCommentInput

	mockSvc := &MockDocumentCommentService{
		SetResolvedFunc: func(_ context.Context, id, _ uuid.UUID, in service.ResolveDocumentCommentInput) (*domain.DocumentComment, error) {
			gotInput = in
			return &domain.DocumentComment{ID: id}, nil
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	c, rec := docCommentRequest(e, http.MethodPost, uuid.New().String(), &wsID, "")
	require.NoError(t, h.Unresolve(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.False(t, gotInput.Resolved)
}

func TestDocumentCommentHandler_Resolve_InvalidCommentID(t *testing.T) {
	wsID := uuid.New()
	h, e := setupDocumentCommentTest(&MockDocumentCommentService{})

	c, rec := docCommentRequest(e, http.MethodPost, "nope", &wsID, "")
	require.NoError(t, h.Resolve(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDocumentCommentHandler_Resolve_NoWorkspaceInContext(t *testing.T) {
	called := false
	mockSvc := &MockDocumentCommentService{
		SetResolvedFunc: func(context.Context, uuid.UUID, uuid.UUID, service.ResolveDocumentCommentInput) (*domain.DocumentComment, error) {
			called = true
			return nil, nil
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	c, rec := docCommentRequest(e, http.MethodPost, uuid.New().String(), nil, "")
	require.NoError(t, h.Resolve(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, called, "a resolve ran with no workspace to scope it to")
}

func TestDocumentCommentHandler_Resolve_ServiceErrorIsRelayed(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockDocumentCommentService{
		SetResolvedFunc: func(context.Context, uuid.UUID, uuid.UUID, service.ResolveDocumentCommentInput) (*domain.DocumentComment, error) {
			return nil, apierror.ValidationError(map[string]string{
				"comment_id": "resolve the thread's first comment, not a reply to it",
			})
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	c, rec := docCommentRequest(e, http.MethodPost, uuid.New().String(), &wsID, "")
	require.NoError(t, h.Resolve(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "reply")
}

// The resolved thread comes back with who resolved it resolved to a name — that
// byline is the whole point of recording the actor.
func TestDocumentCommentHandler_Resolve_ReturnsTheResolvedByline(t *testing.T) {
	wsID := uuid.New()
	name := "Grace"
	mockSvc := &MockDocumentCommentService{
		SetResolvedFunc: func(_ context.Context, id, _ uuid.UUID, _ service.ResolveDocumentCommentInput) (*domain.DocumentComment, error) {
			return &domain.DocumentComment{ID: id, ResolvedByName: &name}, nil
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	c, rec := docCommentRequest(e, http.MethodPost, uuid.New().String(), &wsID, "")
	require.NoError(t, h.Resolve(c))

	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "Grace", got["resolved_by_name"])
}

// --- Delete ---

func TestDocumentCommentHandler_Delete(t *testing.T) {
	commentID, wsID, userID := uuid.New(), uuid.New(), uuid.New()
	var gotID, gotWS, gotActor uuid.UUID
	var gotActorType domain.ActorType

	mockSvc := &MockDocumentCommentService{
		DeleteFunc: func(_ context.Context, id, ws, actor uuid.UUID, actorType domain.ActorType) error {
			gotID, gotWS, gotActor, gotActorType = id, ws, actor, actorType
			return nil
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	c, rec := docCommentRequest(e, http.MethodDelete, commentID.String(), &wsID, "")
	c.Set("user_id", userID)

	require.NoError(t, h.Delete(c))

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, commentID, gotID)
	assert.Equal(t, wsID, gotWS)
	assert.Equal(t, userID, gotActor)
	assert.Equal(t, domain.ActorTypeUser, gotActorType)
}

func TestDocumentCommentHandler_Delete_InvalidCommentID(t *testing.T) {
	wsID := uuid.New()
	h, e := setupDocumentCommentTest(&MockDocumentCommentService{})

	c, rec := docCommentRequest(e, http.MethodDelete, "nope", &wsID, "")
	require.NoError(t, h.Delete(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDocumentCommentHandler_Delete_NoWorkspaceInContext(t *testing.T) {
	called := false
	mockSvc := &MockDocumentCommentService{
		DeleteFunc: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, domain.ActorType) error {
			called = true
			return nil
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	c, rec := docCommentRequest(e, http.MethodDelete, uuid.New().String(), nil, "")
	require.NoError(t, h.Delete(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, called, "a delete ran with no workspace to scope it to")
}

func TestDocumentCommentHandler_Delete_NotFound(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockDocumentCommentService{
		DeleteFunc: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, domain.ActorType) error {
			return apierror.NotFound("Comment")
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	c, rec := docCommentRequest(e, http.MethodDelete, uuid.New().String(), &wsID, "")
	require.NoError(t, h.Delete(c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- anchor binding ---

// toDomain on a nil receiver is the "no anchor sent" path, and it has to answer
// rather than panic: it runs on every create that did not select any text.
func TestDocumentCommentAnchorBody_ToDomain_NilIsNil(t *testing.T) {
	var a *documentCommentAnchorBody
	assert.Nil(t, a.toDomain())
}

// It must NOT drop a quoteless anchor to nil the way the domain constructor does:
// the service has to be able to tell "no anchor was sent" from "an anchor was
// sent with fields missing" in order to answer 400 on the second.
func TestDocumentCommentAnchorBody_ToDomain_KeepsAQuotelessAnchor(t *testing.T) {
	a := &documentCommentAnchorBody{Start: intPtr(1), End: intPtr(2)}

	got := a.toDomain()
	require.NotNil(t, got, "a quoteless anchor must reach the service so it can be refused by name")
	assert.Empty(t, got.Exact)
	require.NotNil(t, got.Start)
	assert.Equal(t, 1, *got.Start)
}

func intPtr(v int) *int { return &v }
