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
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

func setupProjectTest(mockSvc *MockProjectService) (*ProjectHandler, *echo.Echo) {
	e := echo.New()
	h := NewProjectHandler(mockSvc)
	return h, e
}

// --- TestProjectHandler_Create ---

func TestProjectHandler_Create_Success(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockProjectService{
		CreateFunc: func(ctx context.Context, project *domain.Project) error {
			assert.Equal(t, wsID, project.WorkspaceID)
			assert.Equal(t, "My Project", project.Name)
			assert.Equal(t, "my-proj", project.Slug)
			assert.Equal(t, "A cool project", project.Description)
			return nil
		},
	}

	h, e := setupProjectTest(mockSvc)

	body := `{"name":"My Project","slug":"my-proj","description":"A cool project","icon":"rocket"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id/projects")
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, rec.Code)

	var result domain.Project
	err = json.Unmarshal(rec.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.Equal(t, "My Project", result.Name)
	assert.Equal(t, wsID, result.WorkspaceID)
}

func TestProjectHandler_Create_MissingName(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockProjectService{}
	h, e := setupProjectTest(mockSvc)

	body := `{"slug":"no-name"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id/projects")
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var apiErr apierror.Error
	err = json.Unmarshal(rec.Body.Bytes(), &apiErr)
	require.NoError(t, err)
	assert.Equal(t, "name is required", apiErr.Validation["name"])
}

func TestProjectHandler_Create_InvalidWorkspaceID(t *testing.T) {
	mockSvc := &MockProjectService{}
	h, e := setupProjectTest(mockSvc)

	body := `{"name":"Test"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id/projects")
	c.SetParamNames("ws_id")
	c.SetParamValues("bad-uuid")

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- TestProjectHandler_GetByID ---

func TestProjectHandler_GetByID_Found(t *testing.T) {
	projID := uuid.New()
	now := time.Now()
	expected := &domain.Project{
		ID:          projID,
		WorkspaceID: uuid.New(),
		Name:        "Found Project",
		Slug:        "found-proj",
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	mockSvc := &MockProjectService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Project, error) {
			assert.Equal(t, projID, id)
			return expected, nil
		},
	}

	h, e := setupProjectTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id")
	c.SetParamNames("proj_id")
	c.SetParamValues(projID.String())

	err := h.GetByID(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var result domain.Project
	err = json.Unmarshal(rec.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.Equal(t, projID, result.ID)
	assert.Equal(t, "Found Project", result.Name)
}

func TestProjectHandler_GetByID_NotFound(t *testing.T) {
	projID := uuid.New()
	mockSvc := &MockProjectService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Project, error) {
			return nil, apierror.NotFound("Project")
		},
	}

	h, e := setupProjectTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id")
	c.SetParamNames("proj_id")
	c.SetParamValues(projID.String())

	err := h.GetByID(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- TestProjectHandler_List ---

func TestProjectHandler_List_Success(t *testing.T) {
	wsID := uuid.New()
	now := time.Now()
	projects := []domain.Project{
		{ID: uuid.New(), WorkspaceID: wsID, Name: "Proj 1", CreatedAt: now, UpdatedAt: now},
		{ID: uuid.New(), WorkspaceID: wsID, Name: "Proj 2", CreatedAt: now, UpdatedAt: now},
	}

	mockSvc := &MockProjectService{
		ListFunc: func(ctx context.Context, wid uuid.UUID, filter repository.ProjectFilter, pg pagination.Params) (*pagination.Page[domain.Project], error) {
			assert.Equal(t, wsID, wid)
			return pagination.NewPage(projects, 2, pg), nil
		},
	}

	h, e := setupProjectTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/?page=1&page_size=10", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id/projects")
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())

	err := h.List(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var page pagination.Page[domain.Project]
	err = json.Unmarshal(rec.Body.Bytes(), &page)
	require.NoError(t, err)
	assert.Equal(t, 2, page.TotalCount)
	assert.Len(t, page.Items, 2)
}

func TestProjectHandler_List_WithFilters(t *testing.T) {
	wsID := uuid.New()

	mockSvc := &MockProjectService{
		ListFunc: func(ctx context.Context, wid uuid.UUID, filter repository.ProjectFilter, pg pagination.Params) (*pagination.Page[domain.Project], error) {
			assert.NotNil(t, filter.IsArchived)
			assert.Equal(t, false, *filter.IsArchived)
			assert.Equal(t, "test", filter.Search)
			return pagination.NewPage([]domain.Project{}, 0, pg), nil
		},
	}

	h, e := setupProjectTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/?is_archived=false&search=test", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id/projects")
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())

	err := h.List(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// --- TestProjectHandler_Delete ---

func TestProjectHandler_Delete_Success(t *testing.T) {
	projID := uuid.New()
	mockSvc := &MockProjectService{
		ArchiveFunc: func(ctx context.Context, id uuid.UUID) error {
			assert.Equal(t, projID, id)
			return nil
		},
	}

	h, e := setupProjectTest(mockSvc)

	req := httptest.NewRequest(http.MethodDelete, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id")
	c.SetParamNames("proj_id")
	c.SetParamValues(projID.String())

	err := h.Delete(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}
