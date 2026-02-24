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

func setupDependencyTest(mockSvc *MockTaskDependencyService) (*DependencyHandler, *echo.Echo) {
	e := echo.New()
	h := NewDependencyHandler(mockSvc, nil)
	return h, e
}

// --- TestDependencyHandler_Create ---

func TestDependencyHandler_Create_Success(t *testing.T) {
	taskID := uuid.New()
	depOnID := uuid.New()

	mockSvc := &MockTaskDependencyService{
		CreateFunc: func(ctx context.Context, dep *domain.TaskDependency) error {
			assert.Equal(t, taskID, dep.TaskID)
			assert.Equal(t, depOnID, dep.DependsOnTaskID)
			assert.Equal(t, domain.DependencyTypeBlocks, dep.DependencyType)
			return nil
		},
	}

	h, e := setupDependencyTest(mockSvc)

	body := `{"depends_on_task_id":"` + depOnID.String() + `","dependency_type":"blocks"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/dependencies")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, rec.Code)

	var result domain.TaskDependency
	err = json.Unmarshal(rec.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.Equal(t, taskID, result.TaskID)
	assert.Equal(t, depOnID, result.DependsOnTaskID)
}

func TestDependencyHandler_Create_MissingDependsOn(t *testing.T) {
	taskID := uuid.New()
	mockSvc := &MockTaskDependencyService{}
	h, e := setupDependencyTest(mockSvc)

	body := `{"dependency_type":"blocks"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/dependencies")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var apiErr apierror.Error
	err = json.Unmarshal(rec.Body.Bytes(), &apiErr)
	require.NoError(t, err)
	assert.Equal(t, "depends_on_task_id is required", apiErr.Validation["depends_on_task_id"])
}

func TestDependencyHandler_Create_InvalidTaskID(t *testing.T) {
	mockSvc := &MockTaskDependencyService{}
	h, e := setupDependencyTest(mockSvc)

	body := `{"depends_on_task_id":"` + uuid.New().String() + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/dependencies")
	c.SetParamNames("task_id")
	c.SetParamValues("bad-id")

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDependencyHandler_Create_ServiceError(t *testing.T) {
	taskID := uuid.New()
	depOnID := uuid.New()

	mockSvc := &MockTaskDependencyService{
		CreateFunc: func(ctx context.Context, dep *domain.TaskDependency) error {
			return apierror.Conflict("circular dependency detected")
		},
	}

	h, e := setupDependencyTest(mockSvc)

	body := `{"depends_on_task_id":"` + depOnID.String() + `","dependency_type":"blocks"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/dependencies")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, rec.Code)
}

// --- TestDependencyHandler_List ---

func TestDependencyHandler_List_Success(t *testing.T) {
	taskID := uuid.New()
	now := time.Now()
	deps := []domain.TaskDependency{
		{ID: uuid.New(), TaskID: taskID, DependsOnTaskID: uuid.New(), DependencyType: domain.DependencyTypeBlocks, CreatedAt: now},
		{ID: uuid.New(), TaskID: taskID, DependsOnTaskID: uuid.New(), DependencyType: domain.DependencyTypeRelatesTo, CreatedAt: now},
	}

	mockSvc := &MockTaskDependencyService{
		ListByTaskFunc: func(ctx context.Context, tid uuid.UUID) ([]domain.TaskDependency, error) {
			assert.Equal(t, taskID, tid)
			return deps, nil
		},
	}

	h, e := setupDependencyTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/dependencies")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.List(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var result []domain.TaskDependency
	err = json.Unmarshal(rec.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.Len(t, result, 2)
}

func TestDependencyHandler_List_Empty(t *testing.T) {
	taskID := uuid.New()
	mockSvc := &MockTaskDependencyService{
		ListByTaskFunc: func(ctx context.Context, tid uuid.UUID) ([]domain.TaskDependency, error) {
			return []domain.TaskDependency{}, nil
		},
	}

	h, e := setupDependencyTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/dependencies")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.List(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestDependencyHandler_List_InvalidTaskID(t *testing.T) {
	mockSvc := &MockTaskDependencyService{}
	h, e := setupDependencyTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/dependencies")
	c.SetParamNames("task_id")
	c.SetParamValues("nope")

	err := h.List(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- TestDependencyHandler_Delete ---

func TestDependencyHandler_Delete_Success(t *testing.T) {
	depID := uuid.New()
	taskID := uuid.New()
	mockSvc := &MockTaskDependencyService{
		DeleteFunc: func(ctx context.Context, id uuid.UUID) error {
			assert.Equal(t, depID, id)
			return nil
		},
	}

	h, e := setupDependencyTest(mockSvc)

	req := httptest.NewRequest(http.MethodDelete, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/dependencies/:dep_id")
	c.SetParamNames("task_id", "dep_id")
	c.SetParamValues(taskID.String(), depID.String())

	err := h.Delete(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestDependencyHandler_Delete_InvalidDepID(t *testing.T) {
	taskID := uuid.New()
	mockSvc := &MockTaskDependencyService{}
	h, e := setupDependencyTest(mockSvc)

	req := httptest.NewRequest(http.MethodDelete, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/dependencies/:dep_id")
	c.SetParamNames("task_id", "dep_id")
	c.SetParamValues(taskID.String(), "bad")

	err := h.Delete(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDependencyHandler_Delete_NotFound(t *testing.T) {
	depID := uuid.New()
	taskID := uuid.New()
	mockSvc := &MockTaskDependencyService{
		DeleteFunc: func(ctx context.Context, id uuid.UUID) error {
			return apierror.NotFound("TaskDependency")
		},
	}

	h, e := setupDependencyTest(mockSvc)

	req := httptest.NewRequest(http.MethodDelete, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/dependencies/:dep_id")
	c.SetParamNames("task_id", "dep_id")
	c.SetParamValues(taskID.String(), depID.String())

	err := h.Delete(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
