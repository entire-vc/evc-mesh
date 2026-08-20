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
	parentTitle := "Parent task"
	outgoing := []domain.EnrichedTaskDependency{
		{TaskDependency: domain.TaskDependency{ID: uuid.New(), TaskID: taskID, DependsOnTaskID: uuid.New(), DependencyType: domain.DependencyTypeIsChildOf, CreatedAt: now}, RelatedTaskTitle: &parentTitle},
	}
	childTitle := "Child task"
	incoming := []domain.EnrichedTaskDependency{
		{TaskDependency: domain.TaskDependency{ID: uuid.New(), TaskID: uuid.New(), DependsOnTaskID: taskID, DependencyType: domain.DependencyTypeIsChildOf, CreatedAt: now}, RelatedTaskTitle: &childTitle},
	}

	mockSvc := &MockTaskDependencyService{
		ListByTaskBothDirectionsFunc: func(ctx context.Context, tid uuid.UUID) ([]domain.EnrichedTaskDependency, []domain.EnrichedTaskDependency, error) {
			assert.Equal(t, taskID, tid)
			return outgoing, incoming, nil
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

	var result dependencyListResponse
	err = json.Unmarshal(rec.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.Len(t, result.Outgoing, 1)
	assert.Len(t, result.Incoming, 1)
	assert.Equal(t, "Parent task", *result.Outgoing[0].RelatedTaskTitle)
	assert.Equal(t, "Child task", *result.Incoming[0].RelatedTaskTitle)
}

func TestDependencyHandler_List_Empty(t *testing.T) {
	taskID := uuid.New()
	mockSvc := &MockTaskDependencyService{
		ListByTaskBothDirectionsFunc: func(ctx context.Context, tid uuid.UUID) ([]domain.EnrichedTaskDependency, []domain.EnrichedTaskDependency, error) {
			return []domain.EnrichedTaskDependency{}, []domain.EnrichedTaskDependency{}, nil
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
		ListByTaskFunc: func(ctx context.Context, id uuid.UUID) ([]domain.TaskDependency, error) {
			assert.Equal(t, taskID, id)
			return []domain.TaskDependency{{ID: depID, TaskID: taskID}}, nil
		},
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

// TestDependencyHandler_Delete_ForeignEdgeIsRefused pins the cross-tenant repro at
// the handler. DELETE /tasks/:task_id/dependencies/:dep_id used to read only
// :dep_id and delete by it, so pairing one of your own task ids with a stranger's
// :dep_id cut an edge out of their dependency graph and answered 204. The edge now
// has to hang off the task in the path, and nothing is deleted when it does not.
func TestDependencyHandler_Delete_ForeignEdgeIsRefused(t *testing.T) {
	ownTask := uuid.New()
	foreignDep := uuid.New()

	deleted := false
	mockSvc := &MockTaskDependencyService{
		ListByTaskFunc: func(ctx context.Context, id uuid.UUID) ([]domain.TaskDependency, error) {
			assert.Equal(t, ownTask, id)
			// The caller's own task has an edge, just not this one.
			return []domain.TaskDependency{{ID: uuid.New(), TaskID: ownTask}}, nil
		},
		DeleteFunc: func(ctx context.Context, id uuid.UUID) error {
			deleted = true
			return nil
		},
	}

	h, e := setupDependencyTest(mockSvc)

	req := httptest.NewRequest(http.MethodDelete, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/dependencies/:dep_id")
	c.SetParamNames("task_id", "dep_id")
	c.SetParamValues(ownTask.String(), foreignDep.String())

	require.NoError(t, h.Delete(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.False(t, deleted, "a dependency that does not hang off :task_id was deleted anyway")
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
