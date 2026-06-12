package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

func setupTaskTest(mockSvc *MockTaskService) (*TaskHandler, *echo.Echo) {
	e := echo.New()
	h := NewTaskHandler(mockSvc)
	return h, e
}

// --- TestTaskHandler_Create ---

func TestTaskHandler_Create_Success(t *testing.T) {
	projectID := uuid.New()
	mockSvc := &MockTaskService{
		CreateFunc: func(ctx context.Context, task *domain.Task) error {
			assert.Equal(t, projectID, task.ProjectID)
			assert.Equal(t, "My Task", task.Title)
			assert.Equal(t, "A description", task.Description)
			assert.Equal(t, domain.PriorityHigh, task.Priority)
			return nil
		},
	}

	h, e := setupTaskTest(mockSvc)

	body := `{"title":"My Task","description":"A description","priority":"high","labels":["bug","urgent"]}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tasks")
	c.SetParamNames("proj_id")
	c.SetParamValues(projectID.String())

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, rec.Code)

	var result domain.Task
	err = json.Unmarshal(rec.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.Equal(t, "My Task", result.Title)
	assert.Equal(t, "A description", result.Description)
	assert.Equal(t, domain.PriorityHigh, result.Priority)
	assert.Equal(t, projectID, result.ProjectID)
	assert.Contains(t, []string(result.Labels), "bug")
	assert.Contains(t, []string(result.Labels), "urgent")
}

func TestTaskHandler_Create_MissingTitle(t *testing.T) {
	mockSvc := &MockTaskService{}
	h, e := setupTaskTest(mockSvc)

	projectID := uuid.New()
	body := `{"description":"No title here"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tasks")
	c.SetParamNames("proj_id")
	c.SetParamValues(projectID.String())

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var apiErr apierror.Error
	err = json.Unmarshal(rec.Body.Bytes(), &apiErr)
	require.NoError(t, err)
	assert.Equal(t, "Validation failed", apiErr.Message)
	assert.Equal(t, "title is required", apiErr.Validation["title"])
}

func TestTaskHandler_Create_InvalidJSON(t *testing.T) {
	mockSvc := &MockTaskService{}
	h, e := setupTaskTest(mockSvc)

	projectID := uuid.New()
	body := `{not valid json}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tasks")
	c.SetParamNames("proj_id")
	c.SetParamValues(projectID.String())

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTaskHandler_Create_InvalidProjectID(t *testing.T) {
	mockSvc := &MockTaskService{}
	h, e := setupTaskTest(mockSvc)

	body := `{"title":"Test"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tasks")
	c.SetParamNames("proj_id")
	c.SetParamValues("not-a-uuid")

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTaskHandler_Create_ServiceError(t *testing.T) {
	projectID := uuid.New()
	mockSvc := &MockTaskService{
		CreateFunc: func(ctx context.Context, task *domain.Task) error {
			return apierror.Conflict("task already exists")
		},
	}

	h, e := setupTaskTest(mockSvc)

	body := `{"title":"Duplicate Task"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tasks")
	c.SetParamNames("proj_id")
	c.SetParamValues(projectID.String())

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestTaskHandler_Create_WithAssignee(t *testing.T) {
	projectID := uuid.New()
	assigneeID := uuid.New()

	mockSvc := &MockTaskService{
		CreateFunc: func(ctx context.Context, task *domain.Task) error {
			assert.NotNil(t, task.AssigneeID)
			assert.Equal(t, assigneeID, *task.AssigneeID)
			assert.Equal(t, domain.AssigneeTypeAgent, task.AssigneeType)
			return nil
		},
	}

	h, e := setupTaskTest(mockSvc)

	body := `{"title":"Agent Task","assignee_id":"` + assigneeID.String() + `","assignee_type":"agent"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tasks")
	c.SetParamNames("proj_id")
	c.SetParamValues(projectID.String())

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, rec.Code)
}

func TestTaskHandler_Create_WithCustomFields(t *testing.T) {
	projectID := uuid.New()

	mockSvc := &MockTaskService{
		CreateFunc: func(ctx context.Context, task *domain.Task) error {
			assert.NotNil(t, task.CustomFields)
			var cf map[string]any
			err := json.Unmarshal(task.CustomFields, &cf)
			require.NoError(t, err)
			assert.Equal(t, "bar", cf["foo"])
			return nil
		},
	}

	h, e := setupTaskTest(mockSvc)

	body := `{"title":"Custom Task","custom_fields":{"foo":"bar"}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tasks")
	c.SetParamNames("proj_id")
	c.SetParamValues(projectID.String())

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, rec.Code)
}

func TestTaskHandler_Create_RejectsReviewStatus(t *testing.T) {
	projectID := uuid.New()
	reviewStatusID := uuid.New()

	mockSvc := &MockTaskService{
		GetStatusByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.TaskStatus, error) {
			assert.Equal(t, reviewStatusID, id)
			return &domain.TaskStatus{ID: id, Name: "Review", Category: domain.StatusCategoryReview}, nil
		},
	}

	h, e := setupTaskTest(mockSvc)

	body := `{"title":"Oops","status_id":"` + reviewStatusID.String() + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tasks")
	c.SetParamNames("proj_id")
	c.SetParamValues(projectID.String())

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Contains(t, fmt.Sprint(resp["message"]), "review status")
}

func TestTaskHandler_Create_AllowsInProgressStatus(t *testing.T) {
	projectID := uuid.New()
	inProgressStatusID := uuid.New()

	mockSvc := &MockTaskService{
		GetStatusByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.TaskStatus, error) {
			return &domain.TaskStatus{ID: id, Name: "In Progress", Category: domain.StatusCategoryInProgress}, nil
		},
		CreateFunc: func(_ context.Context, _ *domain.Task) error { return nil },
	}

	h, e := setupTaskTest(mockSvc)

	body := `{"title":"Active Work","status_id":"` + inProgressStatusID.String() + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tasks")
	c.SetParamNames("proj_id")
	c.SetParamValues(projectID.String())

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, rec.Code)
}

func TestTaskHandler_MoveTask_ToReviewAllowed(t *testing.T) {
	taskID := uuid.New()
	reviewStatusID := uuid.New()
	moved := false

	mockSvc := &MockTaskService{
		MoveTaskFunc: func(_ context.Context, _ uuid.UUID, _ service.MoveTaskInput) error {
			moved = true
			return nil
		},
	}

	h, e := setupTaskTest(mockSvc)

	body := `{"status_id":"` + reviewStatusID.String() + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/move")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.MoveTask(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, moved, "MoveTask should succeed even when moving to review")
}

// --- TestTaskHandler_GetByID ---

func TestTaskHandler_GetByID_Found(t *testing.T) {
	taskID := uuid.New()
	now := time.Now()
	expectedTask := &domain.Task{
		ID:        taskID,
		ProjectID: uuid.New(),
		Title:     "Found Task",
		Priority:  domain.PriorityMedium,
		Labels:    pq.StringArray{"feature"},
		CreatedAt: now,
		UpdatedAt: now,
	}

	mockSvc := &MockTaskService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
			assert.Equal(t, taskID, id)
			return expectedTask, nil
		},
	}

	h, e := setupTaskTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.GetByID(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var result domain.Task
	err = json.Unmarshal(rec.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.Equal(t, taskID, result.ID)
	assert.Equal(t, "Found Task", result.Title)
}

func TestTaskHandler_GetByID_NotFound(t *testing.T) {
	taskID := uuid.New()
	mockSvc := &MockTaskService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
			return nil, apierror.NotFound("Task")
		},
	}

	h, e := setupTaskTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.GetByID(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	var apiErr apierror.Error
	err = json.Unmarshal(rec.Body.Bytes(), &apiErr)
	require.NoError(t, err)
	assert.Equal(t, "Task not found", apiErr.Message)
}

func TestTaskHandler_GetByID_InvalidUUID(t *testing.T) {
	mockSvc := &MockTaskService{}
	h, e := setupTaskTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues("not-a-uuid")

	err := h.GetByID(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTaskHandler_GetByID_InternalError(t *testing.T) {
	taskID := uuid.New()
	mockSvc := &MockTaskService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
			return nil, assert.AnError
		},
	}

	h, e := setupTaskTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.GetByID(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- TestTaskHandler_Update ---

func TestTaskHandler_Update_Success(t *testing.T) {
	taskID := uuid.New()
	now := time.Now()
	existingTask := &domain.Task{
		ID:          taskID,
		ProjectID:   uuid.New(),
		Title:       "Old Title",
		Description: "Old Desc",
		Priority:    domain.PriorityLow,
		Labels:      pq.StringArray{"old"},
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	mockSvc := &MockTaskService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
			return existingTask, nil
		},
		UpdateFunc: func(ctx context.Context, task *domain.Task) error {
			assert.Equal(t, "New Title", task.Title)
			assert.Equal(t, domain.PriorityUrgent, task.Priority)
			// Description should remain unchanged since we didn't send it
			assert.Equal(t, "Old Desc", task.Description)
			return nil
		},
	}

	h, e := setupTaskTest(mockSvc)

	body := `{"title":"New Title","priority":"urgent"}`
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.Update(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var result domain.Task
	err = json.Unmarshal(rec.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.Equal(t, "New Title", result.Title)
}

func TestTaskHandler_Update_InvalidUUID(t *testing.T) {
	mockSvc := &MockTaskService{}
	h, e := setupTaskTest(mockSvc)

	body := `{"title":"X"}`
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues("bad-id")

	err := h.Update(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTaskHandler_Update_NotFound(t *testing.T) {
	taskID := uuid.New()
	mockSvc := &MockTaskService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
			return nil, apierror.NotFound("Task")
		},
	}

	h, e := setupTaskTest(mockSvc)

	body := `{"title":"X"}`
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.Update(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestTaskHandler_Update_ServiceError(t *testing.T) {
	taskID := uuid.New()
	now := time.Now()
	existingTask := &domain.Task{
		ID:        taskID,
		Title:     "Old",
		CreatedAt: now,
		UpdatedAt: now,
	}

	mockSvc := &MockTaskService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
			return existingTask, nil
		},
		UpdateFunc: func(ctx context.Context, task *domain.Task) error {
			return apierror.Conflict("concurrent update")
		},
	}

	h, e := setupTaskTest(mockSvc)

	body := `{"title":"Conflict"}`
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.Update(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestTaskHandler_Update_PartialLabels(t *testing.T) {
	taskID := uuid.New()
	now := time.Now()
	existingTask := &domain.Task{
		ID:        taskID,
		Title:     "Existing",
		Labels:    pq.StringArray{"old-label"},
		CreatedAt: now,
		UpdatedAt: now,
	}

	mockSvc := &MockTaskService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
			return existingTask, nil
		},
		UpdateFunc: func(ctx context.Context, task *domain.Task) error {
			assert.Equal(t, pq.StringArray{"new-label-1", "new-label-2"}, task.Labels)
			return nil
		},
	}

	h, e := setupTaskTest(mockSvc)

	body := `{"labels":["new-label-1","new-label-2"]}`
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.Update(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// --- TestTaskHandler_Delete ---

func TestTaskHandler_Delete_Success(t *testing.T) {
	taskID := uuid.New()
	mockSvc := &MockTaskService{
		DeleteFunc: func(ctx context.Context, id uuid.UUID) error {
			assert.Equal(t, taskID, id)
			return nil
		},
	}

	h, e := setupTaskTest(mockSvc)

	req := httptest.NewRequest(http.MethodDelete, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.Delete(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestTaskHandler_Delete_NotFound(t *testing.T) {
	taskID := uuid.New()
	mockSvc := &MockTaskService{
		DeleteFunc: func(ctx context.Context, id uuid.UUID) error {
			return apierror.NotFound("Task")
		},
	}

	h, e := setupTaskTest(mockSvc)

	req := httptest.NewRequest(http.MethodDelete, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.Delete(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestTaskHandler_Delete_InvalidUUID(t *testing.T) {
	mockSvc := &MockTaskService{}
	h, e := setupTaskTest(mockSvc)

	req := httptest.NewRequest(http.MethodDelete, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues("garbage")

	err := h.Delete(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- TestTaskHandler_List ---

func TestTaskHandler_List_Success(t *testing.T) {
	projectID := uuid.New()
	now := time.Now()
	tasks := []domain.Task{
		{ID: uuid.New(), ProjectID: projectID, Title: "Task 1", CreatedAt: now, UpdatedAt: now},
		{ID: uuid.New(), ProjectID: projectID, Title: "Task 2", CreatedAt: now, UpdatedAt: now},
	}

	mockSvc := &MockTaskService{
		ListFunc: func(ctx context.Context, pid uuid.UUID, filter repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error) {
			assert.Equal(t, projectID, pid)
			return pagination.NewPage(tasks, 2, pg), nil
		},
	}

	h, e := setupTaskTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/?page=1&page_size=10", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tasks")
	c.SetParamNames("proj_id")
	c.SetParamValues(projectID.String())

	err := h.List(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var page pagination.Page[domain.Task]
	err = json.Unmarshal(rec.Body.Bytes(), &page)
	require.NoError(t, err)
	assert.Equal(t, 2, page.TotalCount)
	assert.Len(t, page.Items, 2)
	assert.Equal(t, "Task 1", page.Items[0].Title)
	assert.Equal(t, "Task 2", page.Items[1].Title)
}

func TestTaskHandler_List_EmptyResult(t *testing.T) {
	projectID := uuid.New()
	mockSvc := &MockTaskService{
		ListFunc: func(ctx context.Context, pid uuid.UUID, filter repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error) {
			return pagination.NewPage([]domain.Task{}, 0, pg), nil
		},
	}

	h, e := setupTaskTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tasks")
	c.SetParamNames("proj_id")
	c.SetParamValues(projectID.String())

	err := h.List(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var page pagination.Page[domain.Task]
	err = json.Unmarshal(rec.Body.Bytes(), &page)
	require.NoError(t, err)
	assert.Equal(t, 0, page.TotalCount)
	assert.Empty(t, page.Items)
}

func TestTaskHandler_List_WithFilters(t *testing.T) {
	projectID := uuid.New()
	mockSvc := &MockTaskService{
		ListFunc: func(ctx context.Context, pid uuid.UUID, filter repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error) {
			assert.NotNil(t, filter.Priority)
			assert.Equal(t, domain.PriorityHigh, *filter.Priority)
			assert.NotNil(t, filter.AssigneeType)
			assert.Equal(t, domain.AssigneeTypeAgent, *filter.AssigneeType)
			assert.Equal(t, "search term", filter.Search)
			return pagination.NewPage([]domain.Task{}, 0, pg), nil
		},
	}

	h, e := setupTaskTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/?priority=high&assignee_type=agent&search=search+term", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tasks")
	c.SetParamNames("proj_id")
	c.SetParamValues(projectID.String())

	err := h.List(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestTaskHandler_List_StatusCategoryFilter verifies that status_category is
// forwarded to the service layer as TaskFilter.StatusCategory.
func TestTaskHandler_List_StatusCategoryFilter(t *testing.T) {
	projectID := uuid.New()
	mockSvc := &MockTaskService{
		ListFunc: func(_ context.Context, _ uuid.UUID, filter repository.TaskFilter, _ pagination.Params) (*pagination.Page[domain.Task], error) {
			require.NotNil(t, filter.StatusCategory)
			assert.Equal(t, domain.StatusCategoryReview, *filter.StatusCategory)
			assert.Empty(t, filter.StatusIDs)
			return pagination.NewPage([]domain.Task{}, 0, pagination.Params{Page: 1, PageSize: 50}), nil
		},
	}
	h, e := setupTaskTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/?status_category=review", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tasks")
	c.SetParamNames("proj_id")
	c.SetParamValues(projectID.String())

	require.NoError(t, h.List(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestTaskHandler_List_StatusIDFilter verifies that status_id= is forwarded as StatusIDs.
func TestTaskHandler_List_StatusIDFilter(t *testing.T) {
	projectID := uuid.New()
	statusID := uuid.New()
	mockSvc := &MockTaskService{
		ListFunc: func(_ context.Context, _ uuid.UUID, filter repository.TaskFilter, _ pagination.Params) (*pagination.Page[domain.Task], error) {
			require.Len(t, filter.StatusIDs, 1)
			assert.Equal(t, statusID, filter.StatusIDs[0])
			assert.Nil(t, filter.StatusCategory)
			return pagination.NewPage([]domain.Task{}, 0, pagination.Params{Page: 1, PageSize: 50}), nil
		},
	}
	h, e := setupTaskTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/?status_id="+statusID.String(), http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tasks")
	c.SetParamNames("proj_id")
	c.SetParamValues(projectID.String())

	require.NoError(t, h.List(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestTaskHandler_List_OffsetPagination verifies that offset=50 advances to page 2.
func TestTaskHandler_List_OffsetPagination(t *testing.T) {
	projectID := uuid.New()
	mockSvc := &MockTaskService{
		ListFunc: func(_ context.Context, _ uuid.UUID, _ repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error) {
			assert.Equal(t, 2, pg.Page)
			assert.Equal(t, 50, pg.PageSize)
			return pagination.NewPage([]domain.Task{}, 0, pg), nil
		},
	}
	h, e := setupTaskTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/?offset=50", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tasks")
	c.SetParamNames("proj_id")
	c.SetParamValues(projectID.String())

	require.NoError(t, h.List(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestTaskHandler_List_LimitPagination verifies that limit= sets page_size.
func TestTaskHandler_List_LimitPagination(t *testing.T) {
	projectID := uuid.New()
	mockSvc := &MockTaskService{
		ListFunc: func(_ context.Context, _ uuid.UUID, _ repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error) {
			assert.Equal(t, 200, pg.PageSize)
			return pagination.NewPage([]domain.Task{}, 0, pg), nil
		},
	}
	h, e := setupTaskTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/?limit=200", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tasks")
	c.SetParamNames("proj_id")
	c.SetParamValues(projectID.String())

	require.NoError(t, h.List(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestTaskHandler_List_InvalidProjectID(t *testing.T) {
	mockSvc := &MockTaskService{}
	h, e := setupTaskTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tasks")
	c.SetParamNames("proj_id")
	c.SetParamValues("bad-id")

	err := h.List(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTaskHandler_List_ServiceError(t *testing.T) {
	projectID := uuid.New()
	mockSvc := &MockTaskService{
		ListFunc: func(ctx context.Context, pid uuid.UUID, filter repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error) {
			return nil, apierror.InternalError("database error")
		},
	}

	h, e := setupTaskTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tasks")
	c.SetParamNames("proj_id")
	c.SetParamValues(projectID.String())

	err := h.List(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- TestTaskHandler_MoveTask ---

func TestTaskHandler_MoveTask_Success(t *testing.T) {
	taskID := uuid.New()
	statusID := uuid.New()
	position := 2.5

	mockSvc := &MockTaskService{
		MoveTaskFunc: func(ctx context.Context, tid uuid.UUID, input service.MoveTaskInput) error {
			assert.Equal(t, taskID, tid)
			assert.Equal(t, statusID, *input.StatusID)
			assert.Equal(t, position, *input.Position)
			return nil
		},
	}

	h, e := setupTaskTest(mockSvc)

	body := `{"status_id":"` + statusID.String() + `","position":2.5}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/move")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.MoveTask(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestTaskHandler_MoveTask_StatusOnly(t *testing.T) {
	taskID := uuid.New()
	statusID := uuid.New()

	mockSvc := &MockTaskService{
		MoveTaskFunc: func(ctx context.Context, tid uuid.UUID, input service.MoveTaskInput) error {
			assert.NotNil(t, input.StatusID)
			assert.Nil(t, input.Position)
			return nil
		},
	}

	h, e := setupTaskTest(mockSvc)

	body := `{"status_id":"` + statusID.String() + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/move")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.MoveTask(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestTaskHandler_MoveTask_EmptyBody(t *testing.T) {
	taskID := uuid.New()
	mockSvc := &MockTaskService{}
	h, e := setupTaskTest(mockSvc)

	body := `{}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/move")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.MoveTask(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTaskHandler_MoveTask_InvalidTaskID(t *testing.T) {
	mockSvc := &MockTaskService{}
	h, e := setupTaskTest(mockSvc)

	body := `{"status_id":"` + uuid.New().String() + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/move")
	c.SetParamNames("task_id")
	c.SetParamValues("invalid")

	err := h.MoveTask(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTaskHandler_MoveTask_ServiceError(t *testing.T) {
	taskID := uuid.New()
	statusID := uuid.New()
	mockSvc := &MockTaskService{
		MoveTaskFunc: func(ctx context.Context, tid uuid.UUID, input service.MoveTaskInput) error {
			return apierror.NotFound("Task")
		},
	}

	h, e := setupTaskTest(mockSvc)

	body := `{"status_id":"` + statusID.String() + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/move")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.MoveTask(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- TestTaskHandler_ListSubtasks ---

func TestTaskHandler_ListSubtasks_Success(t *testing.T) {
	parentID := uuid.New()
	now := time.Now()
	subtasks := []domain.Task{
		{ID: uuid.New(), ParentTaskID: &parentID, Title: "Sub 1", CreatedAt: now, UpdatedAt: now},
		{ID: uuid.New(), ParentTaskID: &parentID, Title: "Sub 2", CreatedAt: now, UpdatedAt: now},
	}

	mockSvc := &MockTaskService{
		ListSubtasksFunc: func(ctx context.Context, pid uuid.UUID) ([]domain.Task, error) {
			assert.Equal(t, parentID, pid)
			return subtasks, nil
		},
	}

	h, e := setupTaskTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/subtasks")
	c.SetParamNames("task_id")
	c.SetParamValues(parentID.String())

	err := h.ListSubtasks(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var wrapper struct {
		Items []domain.Task `json:"items"`
	}
	err = json.Unmarshal(rec.Body.Bytes(), &wrapper)
	require.NoError(t, err)
	assert.Len(t, wrapper.Items, 2)
}

func TestTaskHandler_ListSubtasks_Empty(t *testing.T) {
	parentID := uuid.New()

	mockSvc := &MockTaskService{
		ListSubtasksFunc: func(ctx context.Context, pid uuid.UUID) ([]domain.Task, error) {
			return []domain.Task{}, nil
		},
	}

	h, e := setupTaskTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/subtasks")
	c.SetParamNames("task_id")
	c.SetParamValues(parentID.String())

	err := h.ListSubtasks(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var wrapper struct {
		Items []domain.Task `json:"items"`
	}
	err = json.Unmarshal(rec.Body.Bytes(), &wrapper)
	require.NoError(t, err)
	assert.Empty(t, wrapper.Items)
}

func TestTaskHandler_ListSubtasks_InvalidUUID(t *testing.T) {
	mockSvc := &MockTaskService{}
	h, e := setupTaskTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/subtasks")
	c.SetParamNames("task_id")
	c.SetParamValues("nope")

	err := h.ListSubtasks(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTaskHandler_ListSubtasks_ServiceError(t *testing.T) {
	parentID := uuid.New()

	mockSvc := &MockTaskService{
		ListSubtasksFunc: func(ctx context.Context, pid uuid.UUID) ([]domain.Task, error) {
			return nil, apierror.NotFound("Task")
		},
	}

	h, e := setupTaskTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/subtasks")
	c.SetParamNames("task_id")
	c.SetParamValues(parentID.String())

	err := h.ListSubtasks(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- TestTaskHandler_Checkout 409 body ---

func TestTaskHandler_Checkout_409_AllFieldsPresent(t *testing.T) {
	taskID := uuid.New()
	holderID := uuid.New()
	acquiredAt := time.Now().Add(-10 * time.Minute)
	expiresAt := time.Now().Add(50 * time.Minute)

	mockSvc := &MockTaskService{
		CheckoutTaskFunc: func(_ context.Context, _ uuid.UUID, _ int, _ map[string]interface{}) (*service.CheckoutResult, error) {
			return nil, &service.CheckoutConflictError{
				CheckedOutBy:     holderID,
				CheckedOutByName: "Linus",
				CheckedOutByKind: "agent",
				ExpiresAt:        expiresAt,
				AcquiredAt:       &acquiredAt,
			}
		},
	}

	h, e := setupTaskTest(mockSvc)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"ttl_minutes":30}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/checkout")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.Checkout(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, rec.Code)

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	details, ok := body["details"].(map[string]interface{})
	require.True(t, ok, "response must have a 'details' object")
	assert.Equal(t, "Linus", details["checked_out_by_name"], "checked_out_by_name must always be present")
	assert.NotNil(t, details["expires_in_seconds"], "expires_in_seconds must always be present")
	assert.NotNil(t, details["acquired_at"], "acquired_at must be present when AcquiredAt is set")
}

func TestTaskHandler_Checkout_409_FallbackNameAndZeroExpiry(t *testing.T) {
	taskID := uuid.New()
	holderID := uuid.New()
	// Lock is already expired (in the past).
	expiresAt := time.Now().Add(-5 * time.Minute)

	mockSvc := &MockTaskService{
		CheckoutTaskFunc: func(_ context.Context, _ uuid.UUID, _ int, _ map[string]interface{}) (*service.CheckoutResult, error) {
			return nil, &service.CheckoutConflictError{
				CheckedOutBy:     holderID,
				CheckedOutByName: "", // name unavailable
				CheckedOutByKind: "agent",
				ExpiresAt:        expiresAt,
				AcquiredAt:       nil, // no acquired_at in older lock
			}
		},
	}

	h, e := setupTaskTest(mockSvc)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"ttl_minutes":30}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/checkout")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.Checkout(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, rec.Code)

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	details, ok := body["details"].(map[string]interface{})
	require.True(t, ok, "response must have a 'details' object")
	assert.Equal(t, holderID.String(), details["checked_out_by_name"],
		"checked_out_by_name must fall back to UUID string when name is empty")
	expiresIn, ok := details["expires_in_seconds"].(float64)
	require.True(t, ok, "expires_in_seconds must always be present")
	assert.Equal(t, float64(0), expiresIn, "expires_in_seconds must be 0 for expired locks (not negative)")
	assert.Nil(t, details["acquired_at"], "acquired_at must be absent when AcquiredAt is nil")
}

func TestTaskHandler_MoveTask_ShortID(t *testing.T) {
	taskID := uuid.New()
	shortID := taskID.String()[:8] // first 8 hex chars
	statusID := uuid.New()
	moved := false

	mockSvc := &MockTaskService{
		GetByShortIDFunc: func(ctx context.Context, prefix string) (*domain.Task, error) {
			assert.Equal(t, shortID, prefix)
			return &domain.Task{ID: taskID}, nil
		},
		MoveTaskFunc: func(_ context.Context, id uuid.UUID, _ service.MoveTaskInput) error {
			assert.Equal(t, taskID, id)
			moved = true
			return nil
		},
	}

	h, e := setupTaskTest(mockSvc)
	body := `{"status_id":"` + statusID.String() + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("task_id")
	c.SetParamValues(shortID)

	require.NoError(t, h.MoveTask(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, moved)
}

func TestTaskHandler_AssignTask_ShortID(t *testing.T) {
	taskID := uuid.New()
	shortID := taskID.String()[:8]
	agentID := uuid.New()
	assigned := false

	mockSvc := &MockTaskService{
		GetByShortIDFunc: func(ctx context.Context, prefix string) (*domain.Task, error) {
			assert.Equal(t, shortID, prefix)
			return &domain.Task{ID: taskID}, nil
		},
		AssignTaskFunc: func(_ context.Context, id uuid.UUID, _ service.AssignTaskInput) error {
			assert.Equal(t, taskID, id)
			assigned = true
			return nil
		},
		GetByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.Task, error) {
			return &domain.Task{ID: taskID}, nil
		},
	}

	h, e := setupTaskTest(mockSvc)
	body := `{"assignee_id":"` + agentID.String() + `","assignee_type":"agent"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("task_id")
	c.SetParamValues(shortID)

	require.NoError(t, h.AssignTask(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, assigned)
}

func TestTaskHandler_MoveTask_FullUUID_StillWorks(t *testing.T) {
	taskID := uuid.New()
	statusID := uuid.New()
	moved := false

	mockSvc := &MockTaskService{
		MoveTaskFunc: func(_ context.Context, id uuid.UUID, _ service.MoveTaskInput) error {
			assert.Equal(t, taskID, id)
			moved = true
			return nil
		},
	}

	h, e := setupTaskTest(mockSvc)
	body := `{"status_id":"` + statusID.String() + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	require.NoError(t, h.MoveTask(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, moved)
}

func TestTaskHandler_MoveTask_InvalidID_Returns400(t *testing.T) {
	mockSvc := &MockTaskService{}
	h, e := setupTaskTest(mockSvc)

	body := `{"status_id":"` + uuid.New().String() + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("task_id")
	c.SetParamValues("not-a-uuid-or-hex")

	require.NoError(t, h.MoveTask(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
