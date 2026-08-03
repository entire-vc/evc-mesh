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
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func setupTaskStatusTest(mockSvc *MockTaskStatusService) (*TaskStatusHandler, *echo.Echo) {
	e := echo.New()
	h := NewTaskStatusHandler(mockSvc)
	return h, e
}

// --- TestTaskStatusHandler_Create ---

func TestTaskStatusHandler_Create_Success(t *testing.T) {
	projID := uuid.New()
	mockSvc := &MockTaskStatusService{
		CreateFunc: func(ctx context.Context, status *domain.TaskStatus) error {
			assert.Equal(t, projID, status.ProjectID)
			assert.Equal(t, "In Review", status.Name)
			assert.Equal(t, "#ff9900", status.Color)
			assert.Equal(t, domain.StatusCategoryReview, status.Category)
			return nil
		},
	}

	h, e := setupTaskStatusTest(mockSvc)

	body := `{"name":"In Review","color":"#ff9900","category":"review"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/statuses")
	c.SetParamNames("proj_id")
	c.SetParamValues(projID.String())

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, rec.Code)

	var result domain.TaskStatus
	err = json.Unmarshal(rec.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.Equal(t, "In Review", result.Name)
	assert.Equal(t, "#ff9900", result.Color)
}

func TestTaskStatusHandler_Create_MissingName(t *testing.T) {
	projID := uuid.New()
	mockSvc := &MockTaskStatusService{}
	h, e := setupTaskStatusTest(mockSvc)

	body := `{"color":"#ff0000"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/statuses")
	c.SetParamNames("proj_id")
	c.SetParamValues(projID.String())

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var apiErr apierror.Error
	err = json.Unmarshal(rec.Body.Bytes(), &apiErr)
	require.NoError(t, err)
	assert.Equal(t, "name is required", apiErr.Validation["name"])
}

func TestTaskStatusHandler_Create_InvalidProjectID(t *testing.T) {
	mockSvc := &MockTaskStatusService{}
	h, e := setupTaskStatusTest(mockSvc)

	body := `{"name":"Test"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/statuses")
	c.SetParamNames("proj_id")
	c.SetParamValues("bad")

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- TestTaskStatusHandler_List ---

func TestTaskStatusHandler_List_Success(t *testing.T) {
	projID := uuid.New()
	statuses := []domain.TaskStatus{
		{ID: uuid.New(), ProjectID: projID, Name: "Todo", Category: domain.StatusCategoryTodo, Position: 0},
		{ID: uuid.New(), ProjectID: projID, Name: "Done", Category: domain.StatusCategoryDone, Position: 1},
	}

	mockSvc := &MockTaskStatusService{
		ListByProjectFunc: func(ctx context.Context, pid uuid.UUID) ([]domain.TaskStatus, error) {
			assert.Equal(t, projID, pid)
			return statuses, nil
		},
	}

	h, e := setupTaskStatusTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/statuses")
	c.SetParamNames("proj_id")
	c.SetParamValues(projID.String())

	err := h.List(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var result []domain.TaskStatus
	err = json.Unmarshal(rec.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.Len(t, result, 2)
	assert.Equal(t, "Todo", result[0].Name)
}

func TestTaskStatusHandler_List_ServiceError(t *testing.T) {
	projID := uuid.New()
	mockSvc := &MockTaskStatusService{
		ListByProjectFunc: func(ctx context.Context, pid uuid.UUID) ([]domain.TaskStatus, error) {
			return nil, apierror.InternalError("db error")
		},
	}

	h, e := setupTaskStatusTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/statuses")
	c.SetParamNames("proj_id")
	c.SetParamValues(projID.String())

	err := h.List(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- TestTaskStatusHandler_Reorder ---

func TestTaskStatusHandler_Reorder_Success(t *testing.T) {
	projID := uuid.New()
	id1 := uuid.New()
	id2 := uuid.New()

	mockSvc := &MockTaskStatusService{
		ReorderFunc: func(ctx context.Context, pid uuid.UUID, statusIDs []uuid.UUID) error {
			assert.Equal(t, projID, pid)
			assert.Len(t, statusIDs, 2)
			assert.Equal(t, id1, statusIDs[0])
			assert.Equal(t, id2, statusIDs[1])
			return nil
		},
	}

	h, e := setupTaskStatusTest(mockSvc)

	body := `{"status_ids":["` + id1.String() + `","` + id2.String() + `"]}`
	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/statuses/reorder")
	c.SetParamNames("proj_id")
	c.SetParamValues(projID.String())

	err := h.Reorder(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestTaskStatusHandler_Reorder_EmptyIDs(t *testing.T) {
	projID := uuid.New()
	mockSvc := &MockTaskStatusService{}
	h, e := setupTaskStatusTest(mockSvc)

	body := `{"status_ids":[]}`
	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/statuses/reorder")
	c.SetParamNames("proj_id")
	c.SetParamValues(projID.String())

	err := h.Reorder(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTaskStatusHandler_Reorder_ServiceError(t *testing.T) {
	projID := uuid.New()
	mockSvc := &MockTaskStatusService{
		ReorderFunc: func(ctx context.Context, pid uuid.UUID, statusIDs []uuid.UUID) error {
			return apierror.BadRequest("status ID does not belong to the specified project")
		},
	}

	h, e := setupTaskStatusTest(mockSvc)

	body := `{"status_ids":["` + uuid.New().String() + `"]}`
	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/statuses/reorder")
	c.SetParamNames("proj_id")
	c.SetParamValues(projID.String())

	err := h.Reorder(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- TestTaskStatusHandler_ListByTask ---

func TestTaskStatusHandler_ListByTask_Success(t *testing.T) {
	taskID := uuid.New()
	projID := uuid.New()
	statuses := []domain.TaskStatus{
		{ID: uuid.New(), ProjectID: projID, Name: "Todo", Slug: "todo", Category: domain.StatusCategoryTodo, Position: 0},
		{ID: uuid.New(), ProjectID: projID, Name: "Done", Slug: "done", Category: domain.StatusCategoryDone, Position: 1},
	}

	var gotTaskID uuid.UUID
	mockSvc := &MockTaskStatusService{
		ListByTaskFunc: func(ctx context.Context, id uuid.UUID) ([]domain.TaskStatus, error) {
			gotTaskID = id
			return statuses, nil
		},
	}
	h, e := setupTaskStatusTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/statuses")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.ListByTask(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, taskID, gotTaskID, "handler must pass the task_id through, not a project id")

	var result []domain.TaskStatus
	err = json.Unmarshal(rec.Body.Bytes(), &result)
	require.NoError(t, err)
	require.Len(t, result, 2)
	// The MCP slug resolver reads exactly these fields; the shape must match
	// GET /projects/:proj_id/statuses so it is a drop-in replacement.
	assert.Equal(t, "todo", result[0].Slug)
	assert.Equal(t, domain.StatusCategoryTodo, result[0].Category)
	assert.NotEqual(t, uuid.Nil, result[0].ID)
}

func TestTaskStatusHandler_ListByTask_InvalidTaskID(t *testing.T) {
	called := false
	mockSvc := &MockTaskStatusService{
		ListByTaskFunc: func(ctx context.Context, id uuid.UUID) ([]domain.TaskStatus, error) {
			called = true
			return nil, nil
		},
	}
	h, e := setupTaskStatusTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/statuses")
	c.SetParamNames("task_id")
	c.SetParamValues("not-a-uuid")

	err := h.ListByTask(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.False(t, called, "service must not be reached with an unparseable task_id")
}

func TestTaskStatusHandler_ListByTask_TaskNotFound(t *testing.T) {
	mockSvc := &MockTaskStatusService{
		ListByTaskFunc: func(ctx context.Context, id uuid.UUID) ([]domain.TaskStatus, error) {
			return nil, apierror.NotFound("Task")
		},
	}
	h, e := setupTaskStatusTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/statuses")
	c.SetParamNames("task_id")
	c.SetParamValues(uuid.New().String())

	err := h.ListByTask(c)
	require.NoError(t, err)
	// A missing task must read as 404, never as a permission refusal — mislabelling
	// this as 403 is the exact failure mode this endpoint exists to remove.
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
