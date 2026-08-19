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
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// docCommentsRequest builds a request against /documents/:doc_id/comments.
func docCommentsRequest(e *echo.Echo, method, docID string, wsID *uuid.UUID, body string) (echo.Context, *httptest.ResponseRecorder) {
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
	c.SetPath("/documents/:doc_id/comments")
	c.SetParamNames("doc_id")
	c.SetParamValues(docID)
	return c, rec
}

// commentRequest builds a request against /document-comments/:dc_id.
func commentRequest(e *echo.Echo, method, commentID string, wsID *uuid.UUID, body string) (echo.Context, *httptest.ResponseRecorder) {
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
	c.SetPath("/document-comments/:dc_id")
	c.SetParamNames("dc_id")
	c.SetParamValues(commentID)
	return c, rec
}

func sampleHandlerComment(docID uuid.UUID) *domain.DocumentComment {
	return &domain.DocumentComment{
		ID:         uuid.New(),
		DocumentID: docID,
		Body:       "This contradicts the runbook.",
		Anchor: &domain.DocumentAnchor{
			Start: 120, End: 168,
			Exact:  "the migration is applied before the image swap",
			Prefix: "Deploy discipline. ",
			Suffix: ", never after.",
		},
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeUser,
	}
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func TestDocumentCommentHandler_List_ReturnsItems(t *testing.T) {
	docID, wsID := uuid.New(), uuid.New()
	comment := sampleHandlerComment(docID)
	h := NewDocumentCommentHandler(&MockDocumentCommentService{
		ListByDocumentFunc: func(_ context.Context, gotDoc, gotWS uuid.UUID) ([]domain.DocumentComment, error) {
			// The route's id and the caller's workspace, both — the workspace is
			// what makes the document id checkable at all.
			assert.Equal(t, docID, gotDoc)
			assert.Equal(t, wsID, gotWS)
			return []domain.DocumentComment{*comment}, nil
		},
	})
	e := echo.New()

	c, rec := docCommentsRequest(e, http.MethodGet, docID.String(), &wsID, "")
	require.NoError(t, h.List(c))

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Items []domain.DocumentComment `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Items, 1)
	// The anchor has to survive serialisation: it is the whole point of the row,
	// and a `db:"-"` field is exactly the kind that gets dropped by a struct tag
	// nobody re-reads.
	require.NotNil(t, body.Items[0].Anchor)
	assert.Equal(t, comment.Anchor.Exact, body.Items[0].Anchor.Exact)
	assert.Equal(t, comment.Anchor.Start, body.Items[0].Anchor.Start)
}

func TestDocumentCommentHandler_List_RefusesWithoutAWorkspace(t *testing.T) {
	h := NewDocumentCommentHandler(&MockDocumentCommentService{})
	e := echo.New()

	c, rec := docCommentsRequest(e, http.MethodGet, uuid.New().String(), nil, "")
	require.NoError(t, h.List(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestDocumentCommentHandler_List_RejectsAMalformedDocID(t *testing.T) {
	wsID := uuid.New()
	h := NewDocumentCommentHandler(&MockDocumentCommentService{})
	e := echo.New()

	c, rec := docCommentsRequest(e, http.MethodGet, "not-a-uuid", &wsID, "")
	require.NoError(t, h.List(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func TestDocumentCommentHandler_Create_PassesTheAnchorThrough(t *testing.T) {
	docID, wsID := uuid.New(), uuid.New()
	var got service.CreateDocumentCommentInput
	h := NewDocumentCommentHandler(&MockDocumentCommentService{
		CreateFunc: func(_ context.Context, in service.CreateDocumentCommentInput) (*domain.DocumentComment, error) {
			got = in
			return sampleHandlerComment(docID), nil
		},
	})
	e := echo.New()

	c, rec := docCommentsRequest(e, http.MethodPost, docID.String(), &wsID, `{
		"body": "This contradicts the runbook.",
		"anchor": {"start": 120, "end": 168, "exact": "the migration is applied before the image swap",
		           "prefix": "Deploy discipline. ", "suffix": ", never after."}
	}`)
	require.NoError(t, h.Create(c))

	require.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, got.Anchor)
	// Field by field: an anchor that arrived with a zeroed offset or a dropped
	// context would still be non-nil, and would resolve to the wrong place or to
	// nothing at all.
	assert.Equal(t, 120, got.Anchor.Start)
	assert.Equal(t, 168, got.Anchor.End)
	assert.Equal(t, "the migration is applied before the image swap", got.Anchor.Exact)
	assert.Equal(t, "Deploy discipline. ", got.Anchor.Prefix)
	assert.Equal(t, ", never after.", got.Anchor.Suffix)
	// The document comes from the route, never the body.
	assert.Equal(t, docID, got.DocumentID)
	assert.Equal(t, wsID, got.WorkspaceID)
}

func TestDocumentCommentHandler_Create_RejectsAMalformedBody(t *testing.T) {
	wsID := uuid.New()
	h := NewDocumentCommentHandler(&MockDocumentCommentService{})
	e := echo.New()

	c, rec := docCommentsRequest(e, http.MethodPost, uuid.New().String(), &wsID, `{"body": `)
	require.NoError(t, h.Create(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDocumentCommentHandler_Create_SurfacesTheServiceRefusal(t *testing.T) {
	wsID := uuid.New()
	h := NewDocumentCommentHandler(&MockDocumentCommentService{
		CreateFunc: func(context.Context, service.CreateDocumentCommentInput) (*domain.DocumentComment, error) {
			return nil, apierror.ValidationError(map[string]string{"anchor": "a thread root must carry an anchor"})
		},
	})
	e := echo.New()

	c, rec := docCommentsRequest(e, http.MethodPost, uuid.New().String(), &wsID, `{"body": "x"}`)
	require.NoError(t, h.Create(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "must carry an anchor")
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func TestDocumentCommentHandler_Update_EditsTheBody(t *testing.T) {
	commentID, wsID := uuid.New(), uuid.New()
	called := false
	h := NewDocumentCommentHandler(&MockDocumentCommentService{
		UpdateBodyFunc: func(_ context.Context, id, ws uuid.UUID, body string, _ uuid.UUID) (*domain.DocumentComment, error) {
			called = true
			assert.Equal(t, commentID, id)
			assert.Equal(t, wsID, ws)
			assert.Equal(t, "rewritten", body)
			return sampleHandlerComment(uuid.New()), nil
		},
		SetResolvedFunc: func(context.Context, uuid.UUID, uuid.UUID, bool, uuid.UUID, domain.ActorType) (*domain.DocumentComment, error) {
			t.Fatal("resolve must not be called when only body was sent")
			return nil, nil
		},
	})
	e := echo.New()

	c, rec := commentRequest(e, http.MethodPatch, commentID.String(), &wsID, `{"body": "rewritten"}`)
	require.NoError(t, h.Update(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, called)
}

func TestDocumentCommentHandler_Update_ResolvesAndUnresolves(t *testing.T) {
	commentID, wsID := uuid.New(), uuid.New()
	e := echo.New()

	for name, payload := range map[string]bool{"resolve": true, "unresolve": false} {
		t.Run(name, func(t *testing.T) {
			var got bool
			h := NewDocumentCommentHandler(&MockDocumentCommentService{
				SetResolvedFunc: func(_ context.Context, _, _ uuid.UUID, resolved bool, _ uuid.UUID, _ domain.ActorType) (*domain.DocumentComment, error) {
					got = resolved
					return sampleHandlerComment(uuid.New()), nil
				},
			})
			body := `{"resolved": false}`
			if payload {
				body = `{"resolved": true}`
			}
			c, rec := commentRequest(e, http.MethodPatch, commentID.String(), &wsID, body)
			require.NoError(t, h.Update(c))

			assert.Equal(t, http.StatusOK, rec.Code)
			// `resolved: false` must reach the service as false, not be swallowed
			// as "absent" — a nullable bool read with the zero value is how an
			// unresolve silently becomes a no-op.
			assert.Equal(t, payload, got)
		})
	}
}

func TestDocumentCommentHandler_Update_RefusesAnEmptyPatch(t *testing.T) {
	wsID := uuid.New()
	h := NewDocumentCommentHandler(&MockDocumentCommentService{
		UpdateBodyFunc: func(context.Context, uuid.UUID, uuid.UUID, string, uuid.UUID) (*domain.DocumentComment, error) {
			t.Fatal("nothing should be called for an empty patch")
			return nil, nil
		},
	})
	e := echo.New()

	c, rec := commentRequest(e, http.MethodPatch, uuid.New().String(), &wsID, `{}`)
	require.NoError(t, h.Update(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDocumentCommentHandler_Update_StopsAtTheFirstRefusal(t *testing.T) {
	wsID := uuid.New()
	h := NewDocumentCommentHandler(&MockDocumentCommentService{
		UpdateBodyFunc: func(context.Context, uuid.UUID, uuid.UUID, string, uuid.UUID) (*domain.DocumentComment, error) {
			return nil, apierror.Forbidden("only the author can edit a comment")
		},
		SetResolvedFunc: func(context.Context, uuid.UUID, uuid.UUID, bool, uuid.UUID, domain.ActorType) (*domain.DocumentComment, error) {
			// A request carrying both fields must be refused whole. Resolving after
			// a refused edit would half-apply a request the caller was told failed.
			t.Fatal("resolve must not run after the edit was refused")
			return nil, nil
		},
	})
	e := echo.New()

	c, rec := commentRequest(e, http.MethodPatch, uuid.New().String(), &wsID, `{"body": "x", "resolved": true}`)
	require.NoError(t, h.Update(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestDocumentCommentHandler_Update_RejectsAMalformedID(t *testing.T) {
	wsID := uuid.New()
	h := NewDocumentCommentHandler(&MockDocumentCommentService{})
	e := echo.New()

	c, rec := commentRequest(e, http.MethodPatch, "nope", &wsID, `{"resolved": true}`)
	require.NoError(t, h.Update(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestDocumentCommentHandler_Delete_NoContent(t *testing.T) {
	commentID, wsID := uuid.New(), uuid.New()
	called := false
	h := NewDocumentCommentHandler(&MockDocumentCommentService{
		DeleteFunc: func(_ context.Context, id, ws uuid.UUID, _ uuid.UUID) error {
			called = true
			assert.Equal(t, commentID, id)
			assert.Equal(t, wsID, ws)
			return nil
		},
	})
	e := echo.New()

	c, rec := commentRequest(e, http.MethodDelete, commentID.String(), &wsID, "")
	require.NoError(t, h.Delete(c))

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.True(t, called)
}

func TestDocumentCommentHandler_Delete_SurfacesARefusal(t *testing.T) {
	wsID := uuid.New()
	h := NewDocumentCommentHandler(&MockDocumentCommentService{
		DeleteFunc: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
			return apierror.Forbidden("only the author can delete a comment")
		},
	})
	e := echo.New()

	c, rec := commentRequest(e, http.MethodDelete, uuid.New().String(), &wsID, "")
	require.NoError(t, h.Delete(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestDocumentCommentHandler_Delete_RefusesWithoutAWorkspace(t *testing.T) {
	h := NewDocumentCommentHandler(&MockDocumentCommentService{})
	e := echo.New()

	c, rec := commentRequest(e, http.MethodDelete, uuid.New().String(), nil, "")
	require.NoError(t, h.Delete(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
}
