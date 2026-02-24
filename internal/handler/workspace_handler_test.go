package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func setupWorkspaceTest(mockSvc *MockWorkspaceService) (*WorkspaceHandler, *echo.Echo) {
	e := echo.New()
	h := NewWorkspaceHandler(mockSvc)
	return h, e
}

// --- TestWorkspaceHandler_Create ---

func TestWorkspaceHandler_Create_Success(t *testing.T) {
	mockSvc := &MockWorkspaceService{
		CreateFunc: func(ctx context.Context, ws *domain.Workspace) error {
			assert.Equal(t, "Test Workspace", ws.Name)
			assert.Equal(t, "test-ws", ws.Slug)
			return nil
		},
	}

	h, e := setupWorkspaceTest(mockSvc)

	body := `{"name":"Test Workspace","slug":"test-ws"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces")
	c.Set("user_id", uuid.New())

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, rec.Code)

	var result domain.Workspace
	err = json.Unmarshal(rec.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.Equal(t, "Test Workspace", result.Name)
	assert.Equal(t, "test-ws", result.Slug)
}

func TestWorkspaceHandler_Create_MissingName(t *testing.T) {
	mockSvc := &MockWorkspaceService{}
	h, e := setupWorkspaceTest(mockSvc)

	body := `{"slug":"my-ws"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces")

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var apiErr apierror.Error
	err = json.Unmarshal(rec.Body.Bytes(), &apiErr)
	require.NoError(t, err)
	assert.Equal(t, "Validation failed", apiErr.Message)
	assert.Equal(t, "name is required", apiErr.Validation["name"])
}

func TestWorkspaceHandler_Create_ServiceError(t *testing.T) {
	mockSvc := &MockWorkspaceService{
		CreateFunc: func(ctx context.Context, ws *domain.Workspace) error {
			return apierror.Conflict("slug already exists")
		},
	}

	h, e := setupWorkspaceTest(mockSvc)

	body := `{"name":"Dup Workspace","slug":"dup-slug"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces")

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, rec.Code)
}

// --- TestWorkspaceHandler_GetByID ---

func TestWorkspaceHandler_GetByID_Found(t *testing.T) {
	wsID := uuid.New()
	now := time.Now()
	expected := &domain.Workspace{
		ID:        wsID,
		Name:      "Found WS",
		Slug:      "found-ws",
		OwnerID:   uuid.New(),
		CreatedAt: now,
		UpdatedAt: now,
	}

	mockSvc := &MockWorkspaceService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Workspace, error) {
			assert.Equal(t, wsID, id)
			return expected, nil
		},
	}

	h, e := setupWorkspaceTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id")
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())

	err := h.GetByID(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var result domain.Workspace
	err = json.Unmarshal(rec.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.Equal(t, wsID, result.ID)
	assert.Equal(t, "Found WS", result.Name)
}

func TestWorkspaceHandler_GetByID_NotFound(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockWorkspaceService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Workspace, error) {
			return nil, apierror.NotFound("Workspace")
		},
	}

	h, e := setupWorkspaceTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id")
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())

	err := h.GetByID(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestWorkspaceHandler_GetByID_InvalidUUID(t *testing.T) {
	mockSvc := &MockWorkspaceService{}
	h, e := setupWorkspaceTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id")
	c.SetParamNames("ws_id")
	c.SetParamValues("not-a-uuid")

	err := h.GetByID(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- TestWorkspaceHandler_List ---

func TestWorkspaceHandler_List_Success(t *testing.T) {
	userID := uuid.New()
	now := time.Now()
	workspaces := []domain.Workspace{
		{ID: uuid.New(), Name: "WS1", OwnerID: userID, CreatedAt: now, UpdatedAt: now},
		{ID: uuid.New(), Name: "WS2", OwnerID: userID, CreatedAt: now, UpdatedAt: now},
	}

	mockSvc := &MockWorkspaceService{
		ListByOwnerFunc: func(ctx context.Context, ownerID uuid.UUID) ([]domain.Workspace, error) {
			assert.Equal(t, userID, ownerID)
			return workspaces, nil
		},
	}

	h, e := setupWorkspaceTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces")
	c.Set("user_id", userID)

	err := h.List(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var result []domain.Workspace
	err = json.Unmarshal(rec.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.Len(t, result, 2)
}

// --- TestWorkspaceHandler_Update ---

func TestWorkspaceHandler_Update_Success(t *testing.T) {
	wsID := uuid.New()
	now := time.Now()
	existing := &domain.Workspace{
		ID:        wsID,
		Name:      "Old Name",
		Slug:      "old-slug",
		OwnerID:   uuid.New(),
		CreatedAt: now,
		UpdatedAt: now,
	}

	mockSvc := &MockWorkspaceService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Workspace, error) {
			return existing, nil
		},
		UpdateFunc: func(ctx context.Context, ws *domain.Workspace) error {
			assert.Equal(t, "New Name", ws.Name)
			assert.Equal(t, "old-slug", ws.Slug) // Slug not changed.
			return nil
		},
	}

	h, e := setupWorkspaceTest(mockSvc)

	body := `{"name":"New Name"}`
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id")
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())

	err := h.Update(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// --- TestWorkspaceHandler_Delete ---

func TestWorkspaceHandler_Delete_Success(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockWorkspaceService{
		DeleteFunc: func(ctx context.Context, id uuid.UUID) error {
			assert.Equal(t, wsID, id)
			return nil
		},
	}

	h, e := setupWorkspaceTest(mockSvc)

	req := httptest.NewRequest(http.MethodDelete, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id")
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())

	err := h.Delete(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestWorkspaceHandler_Delete_NotFound(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockWorkspaceService{
		DeleteFunc: func(ctx context.Context, id uuid.UUID) error {
			return apierror.NotFound("Workspace")
		},
	}

	h, e := setupWorkspaceTest(mockSvc)

	req := httptest.NewRequest(http.MethodDelete, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id")
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())

	err := h.Delete(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
