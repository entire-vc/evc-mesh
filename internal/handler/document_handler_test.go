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
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

func setupDocumentTest(mockSvc *MockDocumentService) (*DocumentHandler, *echo.Echo) {
	return NewDocumentHandler(mockSvc), echo.New()
}

// docRequest builds a request against one of the /documents/:doc_id routes.
func docRequest(e *echo.Echo, method, docID string, wsID *uuid.UUID, body string) (echo.Context, *httptest.ResponseRecorder) {
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
	c.SetPath("/documents/:doc_id")
	c.SetParamNames("doc_id")
	c.SetParamValues(docID)
	return c, rec
}

// projectDocRequest builds a request against the project-scoped pair.
func projectDocRequest(e *echo.Echo, method, projID, target, body string) (echo.Context, *httptest.ResponseRecorder) {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, http.NoBody)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/documents")
	c.SetParamNames("proj_id")
	c.SetParamValues(projID)
	return c, rec
}

// --- List ---

func TestDocumentHandler_List(t *testing.T) {
	projID := uuid.New()
	var gotProject uuid.UUID
	var gotPageSize int

	mockSvc := &MockDocumentService{
		ListByProjectFunc: func(_ context.Context, id uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Document], error) {
			gotProject, gotPageSize = id, pg.PageSize
			return pagination.NewPage([]domain.Document{{ID: uuid.New(), Title: "Runbook"}}, 1, pg), nil
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := projectDocRequest(e, http.MethodGet, projID.String(), "/?page_size=2", "")
	require.NoError(t, h.List(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, projID, gotProject)
	assert.Equal(t, 2, gotPageSize, "the pagination parameters reached the service")
	assert.Contains(t, rec.Body.String(), "Runbook")
}

func TestDocumentHandler_List_InvalidProjectID(t *testing.T) {
	h, e := setupDocumentTest(&MockDocumentService{})

	c, rec := projectDocRequest(e, http.MethodGet, "not-a-uuid", "/", "")
	require.NoError(t, h.List(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDocumentHandler_List_ServiceError(t *testing.T) {
	mockSvc := &MockDocumentService{
		ListByProjectFunc: func(context.Context, uuid.UUID, pagination.Params) (*pagination.Page[domain.Document], error) {
			return nil, apierror.NotFound("Project")
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := projectDocRequest(e, http.MethodGet, uuid.New().String(), "/", "")
	require.NoError(t, h.List(c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- Create ---

func TestDocumentHandler_Create(t *testing.T) {
	projID := uuid.New()
	userID := uuid.New()
	parentID := uuid.New()

	var got service.CreateDocumentInput
	mockSvc := &MockDocumentService{}
	mockSvc.CreateFunc = func(_ context.Context, input service.CreateDocumentInput) (*domain.Document, error) {
		got = input
		return &domain.Document{ID: uuid.New(), ProjectID: input.ProjectID, Title: input.Title}, nil
	}
	h, e := setupDocumentTest(mockSvc)

	body := `{"title":"Runbook","slug":"runbook","parent_id":"` + parentID.String() + `","position":4,"body":"# hi"}`
	c, rec := projectDocRequest(e, http.MethodPost, projID.String(), "/", body)
	c.Set("user_id", userID)

	require.NoError(t, h.Create(c))

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, projID, got.ProjectID, "the project comes from the path, not the body")
	assert.Equal(t, "Runbook", got.Title)
	assert.Equal(t, "runbook", got.Slug)
	require.NotNil(t, got.ParentID)
	assert.Equal(t, parentID, *got.ParentID)
	assert.Equal(t, 4, got.Position)
	assert.Equal(t, "# hi", got.Body)
	assert.Equal(t, userID, got.CreatedBy)
	assert.Equal(t, domain.ActorTypeUser, got.CreatedByType)
}

func TestDocumentHandler_Create_InvalidProjectID(t *testing.T) {
	h, e := setupDocumentTest(&MockDocumentService{})

	c, rec := projectDocRequest(e, http.MethodPost, "nope", "/", `{"title":"x"}`)
	require.NoError(t, h.Create(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDocumentHandler_Create_MalformedBody(t *testing.T) {
	h, e := setupDocumentTest(&MockDocumentService{})

	c, rec := projectDocRequest(e, http.MethodPost, uuid.New().String(), "/", `{"title":`)
	require.NoError(t, h.Create(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// A missing title is the service's validation to make, and the handler has to
// pass its verdict through as a 400 rather than swallowing it into a 500.
func TestDocumentHandler_Create_MissingTitleIsRelayed(t *testing.T) {
	mockSvc := &MockDocumentService{}
	mockSvc.CreateFunc = func(context.Context, service.CreateDocumentInput) (*domain.Document, error) {
		return nil, apierror.ValidationError(map[string]string{"title": "title is required"})
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := projectDocRequest(e, http.MethodPost, uuid.New().String(), "/", `{"body":"no title"}`)
	require.NoError(t, h.Create(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "title")
}

// The branch that only fires when object storage is down — which is exactly when
// nobody is watching it — must answer 503 rather than 500, so an operator reading
// the log can tell "storage is missing" from "the API is broken".
func TestDocumentHandler_Create_StorageUnavailable(t *testing.T) {
	mockSvc := &MockDocumentService{}
	mockSvc.CreateFunc = func(context.Context, service.CreateDocumentInput) (*domain.Document, error) {
		return nil, apierror.ServiceUnavailable("storage backend not configured")
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := projectDocRequest(e, http.MethodPost, uuid.New().String(), "/", `{"title":"x"}`)
	require.NoError(t, h.Create(c))

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "storage")
}

func TestDocumentHandler_Create_AgentCallerIsRecordedAsAnAgent(t *testing.T) {
	agentID := uuid.New()
	mockSvc := &MockDocumentService{}
	var gotType domain.ActorType
	var gotID uuid.UUID
	mockSvc.CreateFunc = func(_ context.Context, input service.CreateDocumentInput) (*domain.Document, error) {
		gotID, gotType = input.CreatedBy, input.CreatedByType
		return &domain.Document{ID: uuid.New()}, nil
	}
	h, e := setupDocumentTest(mockSvc)

	c, _ := projectDocRequest(e, http.MethodPost, uuid.New().String(), "/", `{"title":"x"}`)
	c.Set("agent_id", agentID)

	require.NoError(t, h.Create(c))
	assert.Equal(t, agentID, gotID)
	assert.Equal(t, domain.ActorTypeAgent, gotType)
}

// --- GetByID ---

func TestDocumentHandler_GetByID(t *testing.T) {
	docID := uuid.New()
	wsID := uuid.New()
	var gotWS uuid.UUID

	mockSvc := &MockDocumentService{
		GetByIDInWorkspaceFunc: func(_ context.Context, id, ws uuid.UUID) (*domain.Document, error) {
			gotWS = ws
			return &domain.Document{ID: id, Title: "Runbook", Body: "# hi"}, nil
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docRequest(e, http.MethodGet, docID.String(), &wsID, "")
	require.NoError(t, h.GetByID(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, wsID, gotWS, "the lookup is narrowed to the caller's workspace")

	var doc map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.Equal(t, "# hi", doc["body"], "the markdown body comes back with the metadata")
}

func TestDocumentHandler_GetByID_InvalidUUID(t *testing.T) {
	wsID := uuid.New()
	h, e := setupDocumentTest(&MockDocumentService{})

	c, rec := docRequest(e, http.MethodGet, "not-a-uuid", &wsID, "")
	require.NoError(t, h.GetByID(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// No workspace in context means the guards upstream did not resolve one. Refusing
// is the only safe reading: a lookup with no tenant would be a lookup across all
// of them.
func TestDocumentHandler_GetByID_NoWorkspaceInContext(t *testing.T) {
	called := false
	mockSvc := &MockDocumentService{
		GetByIDInWorkspaceFunc: func(context.Context, uuid.UUID, uuid.UUID) (*domain.Document, error) {
			called = true
			return &domain.Document{}, nil
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docRequest(e, http.MethodGet, uuid.New().String(), nil, "")
	require.NoError(t, h.GetByID(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, called, "the service was reached without a workspace to scope the read to")
}

func TestDocumentHandler_GetByID_NotFound(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockDocumentService{
		GetByIDInWorkspaceFunc: func(context.Context, uuid.UUID, uuid.UUID) (*domain.Document, error) {
			return nil, apierror.NotFound("Document")
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docRequest(e, http.MethodGet, uuid.New().String(), &wsID, "")
	require.NoError(t, h.GetByID(c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- Update ---

func TestDocumentHandler_Update(t *testing.T) {
	docID := uuid.New()
	wsID := uuid.New()
	parentID := uuid.New()
	var gotInput service.UpdateDocumentInput
	var gotWS uuid.UUID

	mockSvc := &MockDocumentService{
		UpdateFunc: func(_ context.Context, id, ws uuid.UUID, input service.UpdateDocumentInput) (*domain.Document, error) {
			gotInput, gotWS = input, ws
			return &domain.Document{ID: id, Title: "Final"}, nil
		},
	}
	h, e := setupDocumentTest(mockSvc)

	body := `{"title":"Final","parent_id":"` + parentID.String() + `","position":2,"body":"new"}`
	c, rec := docRequest(e, http.MethodPatch, docID.String(), &wsID, body)
	require.NoError(t, h.Update(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, wsID, gotWS)
	require.NotNil(t, gotInput.Title)
	assert.Equal(t, "Final", *gotInput.Title)
	require.NotNil(t, gotInput.ParentID)
	assert.Equal(t, parentID, *gotInput.ParentID)
	require.NotNil(t, gotInput.Position)
	assert.Equal(t, 2, *gotInput.Position)
	require.NotNil(t, gotInput.Body)
	assert.Equal(t, "new", *gotInput.Body)
	assert.False(t, gotInput.ClearParent)
}

// The editor is read from the request context, never bound from the body — an
// updated_by a caller could choose is a byline anyone can forge.
func TestDocumentHandler_Update_RecordsTheCallerAsTheEditor(t *testing.T) {
	wsID, agentID := uuid.New(), uuid.New()
	var gotInput service.UpdateDocumentInput

	mockSvc := &MockDocumentService{
		UpdateFunc: func(_ context.Context, id, _ uuid.UUID, input service.UpdateDocumentInput) (*domain.Document, error) {
			gotInput = input
			return &domain.Document{ID: id}, nil
		},
	}
	h, e := setupDocumentTest(mockSvc)

	// A body that names somebody else as the editor: the field is not bound, so
	// it must have no effect at all.
	body := `{"title":"Final","updated_by":"` + uuid.New().String() + `"}`
	c, rec := docRequest(e, http.MethodPatch, uuid.New().String(), &wsID, body)
	c.Set("agent_id", agentID)

	require.NoError(t, h.Update(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, agentID, gotInput.UpdatedBy)
	assert.Equal(t, domain.ActorTypeAgent, gotInput.UpdatedByType)
}

// The byline this feature exists for has to survive serialization: bare ids would
// leave the client fanning out to resolve them.
func TestDocumentHandler_GetByID_ReturnsTheBylineNames(t *testing.T) {
	wsID := uuid.New()
	editor := uuid.New()
	editorType := domain.ActorTypeAgent
	creatorName, editorName := "Ada", "howard"

	mockSvc := &MockDocumentService{
		GetByIDInWorkspaceFunc: func(_ context.Context, id, _ uuid.UUID) (*domain.Document, error) {
			return &domain.Document{
				ID: id, Title: "Runbook",
				CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
				UpdatedBy: &editor, UpdatedByType: &editorType,
				CreatedByName: &creatorName, UpdatedByName: &editorName,
			}, nil
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docRequest(e, http.MethodGet, uuid.New().String(), &wsID, "")
	require.NoError(t, h.GetByID(c))

	var doc map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.Equal(t, "Ada", doc["created_by_name"])
	assert.Equal(t, "howard", doc["updated_by_name"])
	assert.Equal(t, editor.String(), doc["updated_by"])
	assert.Equal(t, "agent", doc["updated_by_type"])
}

// A document written before the column existed has no last editor, and the
// response must say so rather than repeat the creator.
func TestDocumentHandler_GetByID_LegacyDocumentHasNoEditor(t *testing.T) {
	wsID := uuid.New()
	creatorName := "Ada"

	mockSvc := &MockDocumentService{
		GetByIDInWorkspaceFunc: func(_ context.Context, id, _ uuid.UUID) (*domain.Document, error) {
			return &domain.Document{ID: id, CreatedByName: &creatorName}, nil
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docRequest(e, http.MethodGet, uuid.New().String(), &wsID, "")
	require.NoError(t, h.GetByID(c))

	var doc map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.Nil(t, doc["updated_by"])
	assert.Nil(t, doc["updated_by_type"])
	_, present := doc["updated_by_name"]
	assert.False(t, present, "an unresolvable name is omitted, not sent as an empty string")
}

// A field nobody sent must stay nil all the way to the service: bound as a zero
// value it would blank the title of every document patched for its position.
func TestDocumentHandler_Update_AbsentFieldsStayNil(t *testing.T) {
	wsID := uuid.New()
	var gotInput service.UpdateDocumentInput

	mockSvc := &MockDocumentService{
		UpdateFunc: func(_ context.Context, id, _ uuid.UUID, input service.UpdateDocumentInput) (*domain.Document, error) {
			gotInput = input
			return &domain.Document{ID: id}, nil
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docRequest(e, http.MethodPatch, uuid.New().String(), &wsID, `{"position":9}`)
	require.NoError(t, h.Update(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Nil(t, gotInput.Title)
	assert.Nil(t, gotInput.Body)
	assert.Nil(t, gotInput.ParentID)
	require.NotNil(t, gotInput.Position)
	assert.Equal(t, 9, *gotInput.Position)
}

func TestDocumentHandler_Update_ClearParent(t *testing.T) {
	wsID := uuid.New()
	var gotInput service.UpdateDocumentInput

	mockSvc := &MockDocumentService{
		UpdateFunc: func(_ context.Context, id, _ uuid.UUID, input service.UpdateDocumentInput) (*domain.Document, error) {
			gotInput = input
			return &domain.Document{ID: id}, nil
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docRequest(e, http.MethodPatch, uuid.New().String(), &wsID, `{"clear_parent":true}`)
	require.NoError(t, h.Update(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, gotInput.ClearParent)
	assert.Nil(t, gotInput.ParentID)
}

func TestDocumentHandler_Update_InvalidUUID(t *testing.T) {
	wsID := uuid.New()
	h, e := setupDocumentTest(&MockDocumentService{})

	c, rec := docRequest(e, http.MethodPatch, "nope", &wsID, `{"title":"x"}`)
	require.NoError(t, h.Update(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDocumentHandler_Update_MalformedBody(t *testing.T) {
	wsID := uuid.New()
	h, e := setupDocumentTest(&MockDocumentService{})

	c, rec := docRequest(e, http.MethodPatch, uuid.New().String(), &wsID, `{"title":`)
	require.NoError(t, h.Update(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDocumentHandler_Update_NoWorkspaceInContext(t *testing.T) {
	h, e := setupDocumentTest(&MockDocumentService{})

	c, rec := docRequest(e, http.MethodPatch, uuid.New().String(), nil, `{"title":"x"}`)
	require.NoError(t, h.Update(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestDocumentHandler_Update_NotFound(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockDocumentService{
		UpdateFunc: func(context.Context, uuid.UUID, uuid.UUID, service.UpdateDocumentInput) (*domain.Document, error) {
			return nil, apierror.NotFound("Document")
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docRequest(e, http.MethodPatch, uuid.New().String(), &wsID, `{"title":"x"}`)
	require.NoError(t, h.Update(c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- Delete ---

func TestDocumentHandler_Delete(t *testing.T) {
	docID := uuid.New()
	wsID := uuid.New()
	userID := uuid.New()
	var gotID, gotWS, gotBy uuid.UUID
	var gotByType domain.ActorType

	mockSvc := &MockDocumentService{
		DeleteFunc: func(_ context.Context, id, ws, by uuid.UUID, byType domain.ActorType) error {
			gotID, gotWS, gotBy, gotByType = id, ws, by, byType
			return nil
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docRequest(e, http.MethodDelete, docID.String(), &wsID, "")
	c.Set("user_id", userID)
	require.NoError(t, h.Delete(c))

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, docID, gotID)
	assert.Equal(t, wsID, gotWS)
	assert.Equal(t, userID, gotBy, "the deleter is recorded as the row's last editor")
	assert.Equal(t, domain.ActorTypeUser, gotByType)
}

func TestDocumentHandler_Delete_InvalidUUID(t *testing.T) {
	wsID := uuid.New()
	h, e := setupDocumentTest(&MockDocumentService{})

	c, rec := docRequest(e, http.MethodDelete, "nope", &wsID, "")
	require.NoError(t, h.Delete(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDocumentHandler_Delete_NoWorkspaceInContext(t *testing.T) {
	called := false
	mockSvc := &MockDocumentService{
		DeleteFunc: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, domain.ActorType) error {
			called = true
			return nil
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docRequest(e, http.MethodDelete, uuid.New().String(), nil, "")
	require.NoError(t, h.Delete(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, called, "a delete ran with no workspace to scope it to")
}

func TestDocumentHandler_Delete_NotFound(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockDocumentService{
		DeleteFunc: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, domain.ActorType) error {
			return apierror.NotFound("Document")
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docRequest(e, http.MethodDelete, uuid.New().String(), &wsID, "")
	require.NoError(t, h.Delete(c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- callerActor ---

func TestCallerActor(t *testing.T) {
	e := echo.New()
	userID, agentID := uuid.New(), uuid.New()

	newCtx := func() echo.Context {
		return e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), httptest.NewRecorder())
	}

	t.Run("agent wins over user", func(t *testing.T) {
		c := newCtx()
		c.Set("agent_id", agentID)
		c.Set("user_id", userID)
		id, at := callerActor(c)
		assert.Equal(t, agentID, id)
		assert.Equal(t, domain.ActorTypeAgent, at)
	})

	t.Run("user", func(t *testing.T) {
		c := newCtx()
		c.Set("user_id", userID)
		id, at := callerActor(c)
		assert.Equal(t, userID, id)
		assert.Equal(t, domain.ActorTypeUser, at)
	})

	t.Run("falls back to the actor context", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		req = req.WithContext(actorctx.WithActor(req.Context(), agentID, domain.ActorTypeAgent))
		c := e.NewContext(req, httptest.NewRecorder())
		id, at := callerActor(c)
		assert.Equal(t, agentID, id)
		assert.Equal(t, domain.ActorTypeAgent, at)
	})

	// Nobody identifiable is "system", not a zero-value user: created_by_type is
	// an enum column and an empty string would not insert at all.
	t.Run("nobody", func(t *testing.T) {
		id, at := callerActor(newCtx())
		assert.Equal(t, uuid.Nil, id)
		assert.Equal(t, domain.ActorTypeSystem, at)
	})
}
