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

// #ddd219f4: DELETE used to call Archive and answer 204, so "Delete" in the UI
// left the project alive. It must reach the real delete and never the archive.
func TestProjectHandler_Delete_DeletesNotArchives(t *testing.T) {
	projID := uuid.New()
	var deleted bool
	mockSvc := &MockProjectService{
		DeleteFunc: func(ctx context.Context, id uuid.UUID) error {
			assert.Equal(t, projID, id)
			deleted = true
			return nil
		},
		ArchiveFunc: func(ctx context.Context, id uuid.UUID) error {
			t.Fatal("DELETE must not archive the project")
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
	assert.True(t, deleted, "DELETE must call ProjectService.Delete")
}

func TestProjectHandler_Delete_NotFound(t *testing.T) {
	mockSvc := &MockProjectService{
		DeleteFunc: func(ctx context.Context, id uuid.UUID) error {
			return apierror.NotFound("Project")
		},
	}
	h, e := setupProjectTest(mockSvc)

	req := httptest.NewRequest(http.MethodDelete, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id")
	c.SetParamNames("proj_id")
	c.SetParamValues(uuid.New().String())

	_ = h.Delete(c)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- archive / unarchive ---

func TestProjectHandler_ArchiveAndUnarchive(t *testing.T) {
	for _, tc := range []struct {
		name     string
		archived bool
	}{{"archive", true}, {"unarchive", false}} {
		t.Run(tc.name, func(t *testing.T) {
			projID := uuid.New()
			state := !tc.archived
			mockSvc := &MockProjectService{
				ArchiveFunc: func(ctx context.Context, id uuid.UUID) error {
					assert.Equal(t, projID, id)
					state = true
					return nil
				},
				UnarchiveFunc: func(ctx context.Context, id uuid.UUID) error {
					assert.Equal(t, projID, id)
					state = false
					return nil
				},
				GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Project, error) {
					return &domain.Project{ID: id, IsArchived: state}, nil
				},
			}
			h, e := setupProjectTest(mockSvc)

			req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetPath("/projects/:proj_id/" + tc.name)
			c.SetParamNames("proj_id")
			c.SetParamValues(projID.String())

			var err error
			if tc.archived {
				err = h.Archive(c)
			} else {
				err = h.Unarchive(c)
			}
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, rec.Code)

			var got domain.Project
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			assert.Equal(t, tc.archived, got.IsArchived, "response must carry the flag as stored")
		})
	}
}

// #ddd219f4: PATCH {is_archived:true} was answered 200 with nothing changed,
// because the field wasn't in the request contract. It must be refused loudly.
func TestProjectHandler_Update_IsArchivedRefused(t *testing.T) {
	mockSvc := &MockProjectService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Project, error) {
			return &domain.Project{ID: id, Name: "p"}, nil
		},
		UpdateFunc: func(ctx context.Context, project *domain.Project) error {
			t.Fatal("PATCH with is_archived must not persist anything")
			return nil
		},
	}
	h, e := setupProjectTest(mockSvc)

	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{"is_archived":true}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id")
	c.SetParamNames("proj_id")
	c.SetParamValues(uuid.New().String())

	require.NoError(t, h.Update(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "/archive")
}

func TestProjectHandler_ArchiveErrors(t *testing.T) {
	newCtx := func(e *echo.Echo, id string) (echo.Context, *httptest.ResponseRecorder) {
		req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetPath("/projects/:proj_id/archive")
		c.SetParamNames("proj_id")
		c.SetParamValues(id)
		return c, rec
	}

	t.Run("invalid id", func(t *testing.T) {
		h, e := setupProjectTest(&MockProjectService{})
		c, rec := newCtx(e, "not-a-uuid")
		require.NoError(t, h.Archive(c))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("service error is surfaced, not answered 200", func(t *testing.T) {
		h, e := setupProjectTest(&MockProjectService{
			UnarchiveFunc: func(ctx context.Context, id uuid.UUID) error { return apierror.NotFound("Project") },
		})
		c, rec := newCtx(e, uuid.New().String())
		_ = h.Unarchive(c)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("re-read error is surfaced", func(t *testing.T) {
		h, e := setupProjectTest(&MockProjectService{
			GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Project, error) {
				return nil, apierror.NotFound("Project")
			},
		})
		c, rec := newCtx(e, uuid.New().String())
		_ = h.Archive(c)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
}
