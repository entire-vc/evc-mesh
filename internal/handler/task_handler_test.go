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
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
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

// TestTaskHandler_GetByID_GatedTask_AttachesHumanGateInfo is task #040cddcf:
// a gated task's GET response must carry human_gate_info populated from
// commentService.GetHumanGateOwner, not just the bare human_gate boolean.
func TestTaskHandler_GetByID_GatedTask_AttachesHumanGateInfo(t *testing.T) {
	taskID := uuid.New()
	ownerID := uuid.New()
	markerID := uuid.New()
	markerAt := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	gatedTask := &domain.Task{ID: taskID, HumanGate: true, Title: "Gated"}

	mockSvc := &MockTaskService{
		GetByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.Task, error) {
			assert.Equal(t, taskID, id)
			return gatedTask, nil
		},
	}
	mockComment := &MockCommentService{
		GetHumanGateOwnerFunc: func(_ context.Context, gotTaskID uuid.UUID) (*domain.HumanGateInfo, error) {
			assert.Equal(t, taskID, gotTaskID)
			return &domain.HumanGateInfo{
				Gated:            true,
				OwnerAgentID:     &ownerID,
				OwnerName:        "Howard",
				MarkerCommentID:  &markerID,
				MarkerCreatedAt:  &markerAt,
				ClearableByOwner: true,
			}, nil
		},
	}

	e := echo.New()
	h := NewTaskHandler(mockSvc).WithCommentService(mockComment)

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
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	require.NotNil(t, result.HumanGateInfo)
	assert.True(t, result.HumanGateInfo.Gated)
	require.NotNil(t, result.HumanGateInfo.OwnerAgentID)
	assert.Equal(t, ownerID, *result.HumanGateInfo.OwnerAgentID)
	assert.Equal(t, "Howard", result.HumanGateInfo.OwnerName)
	assert.True(t, result.HumanGateInfo.ClearableByOwner)
}

// TestTaskHandler_GetByID_UngatedTask_NoHumanGateInfoLookup ensures the
// lookup is skipped entirely (no wasted comment-thread scan) when the task
// isn't gated — the common case, and the one #040cddcf's live measurement
// says applies to nearly every task in the workspace.
func TestTaskHandler_GetByID_UngatedTask_NoHumanGateInfoLookup(t *testing.T) {
	taskID := uuid.New()
	ungatedTask := &domain.Task{ID: taskID, HumanGate: false, Title: "Ungated"}

	mockSvc := &MockTaskService{
		GetByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.Task, error) {
			return ungatedTask, nil
		},
	}
	mockComment := &MockCommentService{
		GetHumanGateOwnerFunc: func(_ context.Context, _ uuid.UUID) (*domain.HumanGateInfo, error) {
			t.Fatal("GetHumanGateOwner must not be called for an ungated task")
			return nil, nil
		},
	}

	e := echo.New()
	h := NewTaskHandler(mockSvc).WithCommentService(mockComment)

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
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.Nil(t, result.HumanGateInfo)
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

// --- TestTaskHandler_Update_HumanGate ---

func TestUpdate_HumanGate_AgentForbidden(t *testing.T) {
	taskID := uuid.New()
	existingTask := &domain.Task{ID: taskID, HumanGate: true}

	mockSvc := &MockTaskService{
		GetByIDFunc: func(_ context.Context, _ uuid.UUID) (*domain.Task, error) {
			return existingTask, nil
		},
	}
	mockComment := &MockCommentService{}

	e := echo.New()
	h := NewTaskHandler(mockSvc).WithCommentService(mockComment)

	falseVal := false
	body, _ := json.Marshal(map[string]interface{}{"human_gate": falseVal})
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(string(body)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	// Inject agent actor context
	agentID := uuid.New()
	ctx := actorctx.WithActor(req.Context(), agentID, domain.ActorTypeAgent)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.Update(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	var apiErr apierror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &apiErr))
	assert.Contains(t, apiErr.Message, "human_gate")
}

func TestUpdate_HumanGate_UserAllowed(t *testing.T) {
	taskID := uuid.New()
	existingTask := &domain.Task{ID: taskID, HumanGate: true}

	updateCalled := false
	mockSvc := &MockTaskService{
		GetByIDFunc: func(_ context.Context, _ uuid.UUID) (*domain.Task, error) {
			return existingTask, nil
		},
		UpdateFunc: func(_ context.Context, task *domain.Task) error {
			updateCalled = true
			assert.False(t, task.HumanGate)
			return nil
		},
	}
	commentCaptured := false
	mockComment := &MockCommentService{
		CreateFunc: func(_ context.Context, c *domain.Comment) error {
			commentCaptured = true
			assert.Equal(t, taskID, c.TaskID)
			assert.True(t, c.IsInternal)
			assert.Contains(t, c.Body, "human_gate")
			return nil
		},
	}

	e := echo.New()
	h := NewTaskHandler(mockSvc).WithCommentService(mockComment)

	falseVal := false
	body, _ := json.Marshal(map[string]interface{}{"human_gate": falseVal})
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(string(body)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	userID := uuid.New()
	ctx := actorctx.WithActor(req.Context(), userID, domain.ActorTypeUser)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.Update(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, updateCalled)
	assert.True(t, commentCaptured)
}

func TestUpdate_HumanGate_AgentSetTrue(t *testing.T) {
	taskID := uuid.New()
	existingTask := &domain.Task{ID: taskID, HumanGate: false}

	updateCalled := false
	mockSvc := &MockTaskService{
		GetByIDFunc: func(_ context.Context, _ uuid.UUID) (*domain.Task, error) {
			return existingTask, nil
		},
		UpdateFunc: func(_ context.Context, task *domain.Task) error {
			updateCalled = true
			assert.True(t, task.HumanGate)
			return nil
		},
	}
	mockComment := &MockCommentService{}

	e := echo.New()
	h := NewTaskHandler(mockSvc).WithCommentService(mockComment)

	trueVal := true
	body, _ := json.Marshal(map[string]interface{}{"human_gate": trueVal})
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(string(body)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	agentID := uuid.New()
	ctx := actorctx.WithActor(req.Context(), agentID, domain.ActorTypeAgent)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.Update(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, updateCalled)
}

// TestUpdate_HumanGate_AgentSetTrue_PostsRawArmComment is task #a2e2ac72: a raw
// false→true PATCH (agent or user, either way — arming itself stays
// unrestricted) must leave a trace, or releaseHumanGateOnWithdrawal's
// soleMarkerAuthor fast path (comment_service.go, task #9959f201) cannot tell
// a genuine first marker apart from one fabricated onto an already-armed
// gate. See hasRawArmMarker for the read side this comment feeds.
func TestUpdate_HumanGate_AgentSetTrue_PostsRawArmComment(t *testing.T) {
	taskID := uuid.New()
	existingTask := &domain.Task{ID: taskID, HumanGate: false}

	mockSvc := &MockTaskService{
		GetByIDFunc: func(_ context.Context, _ uuid.UUID) (*domain.Task, error) {
			return existingTask, nil
		},
		UpdateFunc: func(_ context.Context, task *domain.Task) error {
			return nil
		},
	}
	commentCaptured := false
	mockComment := &MockCommentService{
		CreateFunc: func(_ context.Context, c *domain.Comment) error {
			commentCaptured = true
			assert.Equal(t, taskID, c.TaskID)
			assert.True(t, c.IsInternal)
			assert.Contains(t, c.Body, "human_gate взведён напрямую")
			return nil
		},
	}

	e := echo.New()
	h := NewTaskHandler(mockSvc).WithCommentService(mockComment)

	trueVal := true
	body, _ := json.Marshal(map[string]interface{}{"human_gate": trueVal})
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(string(body)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	agentID := uuid.New()
	ctx := actorctx.WithActor(req.Context(), agentID, domain.ActorTypeAgent)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.Update(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, commentCaptured, "a raw arm must post the system comment releaseHumanGateOnWithdrawal looks for")
}

// TestUpdate_HumanGate_AlreadyTrue_NoDuplicateArmComment ensures the raw-arm
// comment only fires on a genuine false→true transition, not on every PATCH
// that merely re-asserts an already-true gate (which would let a bystander
// pad the thread with fresh "arm" markers indefinitely).
func TestUpdate_HumanGate_AlreadyTrue_NoDuplicateArmComment(t *testing.T) {
	taskID := uuid.New()
	existingTask := &domain.Task{ID: taskID, HumanGate: true}

	mockSvc := &MockTaskService{
		GetByIDFunc: func(_ context.Context, _ uuid.UUID) (*domain.Task, error) {
			return existingTask, nil
		},
		UpdateFunc: func(_ context.Context, task *domain.Task) error {
			return nil
		},
	}
	mockComment := &MockCommentService{
		CreateFunc: func(_ context.Context, c *domain.Comment) error {
			t.Fatalf("no comment should be created when human_gate was already true")
			return nil
		},
	}

	e := echo.New()
	h := NewTaskHandler(mockSvc).WithCommentService(mockComment)

	trueVal := true
	body, _ := json.Marshal(map[string]interface{}{"human_gate": trueVal})
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(string(body)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	agentID := uuid.New()
	ctx := actorctx.WithActor(req.Context(), agentID, domain.ActorTypeAgent)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.Update(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUpdate_CompletionSignal_AgentSetsTrue(t *testing.T) {
	taskID := uuid.New()
	existingTask := &domain.Task{ID: taskID, CompletionSignal: false}

	var captured domain.Task
	mockSvc := &MockTaskService{
		GetByIDFunc: func(_ context.Context, _ uuid.UUID) (*domain.Task, error) {
			return existingTask, nil
		},
		UpdateFunc: func(_ context.Context, task *domain.Task) error {
			captured = *task
			return nil
		},
	}

	e := echo.New()
	h := NewTaskHandler(mockSvc)

	trueVal := true
	body, _ := json.Marshal(map[string]interface{}{"completion_signal": trueVal})
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(string(body)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	agentID := uuid.New()
	ctx := actorctx.WithActor(req.Context(), agentID, domain.ActorTypeAgent)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.Update(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, captured.CompletionSignal, "completion_signal should be set to true")
}

func TestUpdate_CompletionSignal_DoesNotChangeStatus(t *testing.T) {
	taskID := uuid.New()
	statusID := uuid.New()
	existingTask := &domain.Task{ID: taskID, StatusID: statusID, CompletionSignal: false}

	var captured domain.Task
	mockSvc := &MockTaskService{
		GetByIDFunc: func(_ context.Context, _ uuid.UUID) (*domain.Task, error) {
			return existingTask, nil
		},
		UpdateFunc: func(_ context.Context, task *domain.Task) error {
			captured = *task
			return nil
		},
	}

	e := echo.New()
	h := NewTaskHandler(mockSvc)

	trueVal := true
	body, _ := json.Marshal(map[string]interface{}{"completion_signal": trueVal})
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(string(body)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	agentID := uuid.New()
	ctx := actorctx.WithActor(req.Context(), agentID, domain.ActorTypeAgent)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.Update(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	// Status ID must be unchanged — completion_signal is signal-only, no status move.
	assert.Equal(t, statusID, captured.StatusID)
	assert.True(t, captured.CompletionSignal)
}

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

// --- CreateSubtask field parity with Create (regression for #d13fe920) ---

// TestTaskHandler_CreateSubtask_WithAssignee mirrors
// TestTaskHandler_Create_WithAssignee: assignee_id/assignee_type must reach
// service.CreateSubtaskInput, and when assignee_type is omitted it must be
// inferred as "agent" (not left empty/unassigned) so an explicit assignee_id
// is never silently clobbered by applyAutoAssign.
func TestTaskHandler_CreateSubtask_WithAssignee(t *testing.T) {
	parentID := uuid.New()
	assigneeID := uuid.New()

	mockSvc := &MockTaskService{
		CreateSubtaskFunc: func(ctx context.Context, pid uuid.UUID, input service.CreateSubtaskInput) (*domain.Task, error) {
			assert.Equal(t, parentID, pid)
			require.NotNil(t, input.AssigneeID)
			assert.Equal(t, assigneeID, *input.AssigneeID)
			assert.Equal(t, domain.AssigneeTypeAgent, input.AssigneeType, "assignee_type must be inferred as agent when assignee_id is set but assignee_type is omitted")
			return &domain.Task{ID: uuid.New(), Title: input.Title, ParentTaskID: &pid, AssigneeID: input.AssigneeID, AssigneeType: input.AssigneeType}, nil
		},
	}

	h, e := setupTaskTest(mockSvc)

	body := `{"title":"Delegated subtask","assignee_id":"` + assigneeID.String() + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/subtasks")
	c.SetParamNames("task_id")
	c.SetParamValues(parentID.String())

	err := h.CreateSubtask(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, rec.Code)
}

// TestTaskHandler_CreateSubtask_WithLabelsAndCustomFields confirms the
// remaining previously-dropped fields (labels, custom_fields, due_date,
// estimated_hours) now reach service.CreateSubtaskInput.
func TestTaskHandler_CreateSubtask_WithLabelsAndCustomFields(t *testing.T) {
	parentID := uuid.New()

	mockSvc := &MockTaskService{
		CreateSubtaskFunc: func(ctx context.Context, pid uuid.UUID, input service.CreateSubtaskInput) (*domain.Task, error) {
			assert.Equal(t, []string{"a", "b"}, input.Labels)
			require.NotNil(t, input.CustomFields)
			var cf map[string]any
			require.NoError(t, json.Unmarshal(input.CustomFields, &cf))
			assert.Equal(t, "bar", cf["foo"])
			require.NotNil(t, input.DueDate)
			require.NotNil(t, input.EstimatedHours)
			assert.Equal(t, 3.5, *input.EstimatedHours)
			return &domain.Task{ID: uuid.New(), Title: input.Title, ParentTaskID: &pid}, nil
		},
	}

	h, e := setupTaskTest(mockSvc)

	body := `{"title":"Labelled subtask","labels":["a","b"],"custom_fields":{"foo":"bar"},"due_date":"2026-08-01T00:00:00Z","estimated_hours":3.5}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/subtasks")
	c.SetParamNames("task_id")
	c.SetParamValues(parentID.String())

	err := h.CreateSubtask(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, rec.Code)
}

// TestTaskHandler_CreateSubtask_NoAssignee_RegressionUnchanged confirms
// omitting assignee_id still leaves AssigneeType empty on the input (the
// service layer defaults it to "unassigned" and may auto-assign) — the fix
// must not force every subtask to carry an assignee.
func TestTaskHandler_CreateSubtask_NoAssignee_RegressionUnchanged(t *testing.T) {
	parentID := uuid.New()

	mockSvc := &MockTaskService{
		CreateSubtaskFunc: func(ctx context.Context, pid uuid.UUID, input service.CreateSubtaskInput) (*domain.Task, error) {
			assert.Nil(t, input.AssigneeID)
			assert.Equal(t, domain.AssigneeType(""), input.AssigneeType, "must not infer an assignee_type when no assignee_id was given")
			return &domain.Task{ID: uuid.New(), Title: input.Title, ParentTaskID: &pid}, nil
		},
	}

	h, e := setupTaskTest(mockSvc)

	body := `{"title":"Undelegated subtask"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/subtasks")
	c.SetParamNames("task_id")
	c.SetParamValues(parentID.String())

	err := h.CreateSubtask(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, rec.Code)
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

// --- delegation_level default regression tests (fix #0a46e636 / #a9dc1a42) ---
// The default must be "auto" so agents close tasks as done, not pile them
// into review (which 72h auto-moves to triage → lands on Pavel).

func TestTaskHandler_Create_DelegationLevel_DefaultsToAuto(t *testing.T) {
	projectID := uuid.New()
	var capturedDelegation domain.DelegationLevel
	mockSvc := &MockTaskService{
		CreateFunc: func(ctx context.Context, task *domain.Task) error {
			capturedDelegation = task.DelegationLevel
			return nil
		},
	}
	h, e := setupTaskTest(mockSvc)

	body := `{"title":"No delegation field"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("proj_id")
	c.SetParamValues(projectID.String())

	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, domain.DelegationLevelAuto, capturedDelegation, "default must be auto, not review")
}

func TestTaskHandler_Create_DelegationLevel_ExplicitReviewKept(t *testing.T) {
	projectID := uuid.New()
	var capturedDelegation domain.DelegationLevel
	mockSvc := &MockTaskService{
		CreateFunc: func(ctx context.Context, task *domain.Task) error {
			capturedDelegation = task.DelegationLevel
			return nil
		},
	}
	h, e := setupTaskTest(mockSvc)

	body := `{"title":"Captain task","delegation_level":"review"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("proj_id")
	c.SetParamValues(projectID.String())

	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, domain.DelegationLevelReview, capturedDelegation, "explicit review must be preserved")
}

func TestTaskHandler_Create_DelegationLevel_ExplicitSupervisedKept(t *testing.T) {
	projectID := uuid.New()
	var capturedDelegation domain.DelegationLevel
	mockSvc := &MockTaskService{
		CreateFunc: func(ctx context.Context, task *domain.Task) error {
			capturedDelegation = task.DelegationLevel
			return nil
		},
	}
	h, e := setupTaskTest(mockSvc)

	body := `{"title":"Money task","delegation_level":"supervised"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("proj_id")
	c.SetParamValues(projectID.String())

	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, domain.DelegationLevelSupervised, capturedDelegation, "explicit supervised must be preserved")
}

func TestTaskHandler_MoveTask_CASConflict_Returns409(t *testing.T) {
	taskID := uuid.New()
	statusID := uuid.New()
	currentStatusID := uuid.New()
	currentUpdatedAt := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)

	mockSvc := &MockTaskService{
		MoveTaskFunc: func(ctx context.Context, tid uuid.UUID, input service.MoveTaskInput) error {
			return &service.CASConflictError{
				CurrentStatusID:  currentStatusID,
				CurrentUpdatedAt: currentUpdatedAt,
			}
		},
	}

	h, e := setupTaskTest(mockSvc)

	body := `{"status_id":"` + statusID.String() + `","expected_status_id":"` + uuid.New().String() + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/move")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.MoveTask(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, rec.Code)

	var body409 map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body409))
	assert.Equal(t, "cas_conflict", body409["code"])
	assert.NotEmpty(t, body409["current_status_id"])
	assert.NotEmpty(t, body409["current_updated_at"])
}

func TestTaskHandler_MoveTask_DefaultSource_IsAPI(t *testing.T) {
	taskID := uuid.New()
	statusID := uuid.New()
	var capturedSource string

	mockSvc := &MockTaskService{
		MoveTaskFunc: func(ctx context.Context, tid uuid.UUID, input service.MoveTaskInput) error {
			capturedSource = input.Source
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

	require.NoError(t, h.MoveTask(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "api", capturedSource)
}

// --- Reviewer binding on PATCH /tasks/:task_id ---
//
// reviewer_id and clear_reviewer are two fields rather than one nullable field on
// purpose, and the asymmetry is the part worth pinning: a PATCH that omits
// reviewer_id must leave an existing reviewer alone (otherwise every unrelated
// edit — retitling a card — silently unassigns the reviewer), so "absent" cannot
// also mean "remove". clear_reviewer carries the removal intent explicitly.

// updateTaskWithBody runs h.Update against a task whose current state is `existing`
// and returns whatever the handler handed to the service.
func updateTaskWithBody(t *testing.T, existing *domain.Task, body string) *domain.Task {
	t.Helper()
	var got *domain.Task
	mockSvc := &MockTaskService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
			return existing, nil
		},
		UpdateFunc: func(ctx context.Context, task *domain.Task) error {
			got = task
			return nil
		},
	}
	h, e := setupTaskTest(mockSvc)

	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues(existing.ID.String())

	require.NoError(t, h.Update(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got, "service Update was never called")
	return got
}

func newReviewerTask(reviewerID *uuid.UUID, reviewerType *domain.AssigneeType) *domain.Task {
	return &domain.Task{
		ID: uuid.New(), ProjectID: uuid.New(), Title: "Old Title",
		Priority: domain.PriorityLow, ReviewerID: reviewerID, ReviewerType: reviewerType,
	}
}

func TestTaskHandler_Update_SetsReviewer(t *testing.T) {
	reviewerID := uuid.New()
	got := updateTaskWithBody(t, newReviewerTask(nil, nil),
		fmt.Sprintf(`{"reviewer_id":%q,"reviewer_type":"user"}`, reviewerID))

	require.NotNil(t, got.ReviewerID)
	assert.Equal(t, reviewerID, *got.ReviewerID)
	require.NotNil(t, got.ReviewerType)
	assert.Equal(t, domain.AssigneeTypeUser, *got.ReviewerType)
}

func TestTaskHandler_Update_ClearReviewerRemovesBothFields(t *testing.T) {
	existingID := uuid.New()
	existingType := domain.AssigneeTypeUser
	got := updateTaskWithBody(t, newReviewerTask(&existingID, &existingType),
		`{"clear_reviewer":true}`)

	assert.Nil(t, got.ReviewerID, "clear_reviewer must drop the reviewer id")
	assert.Nil(t, got.ReviewerType,
		"clear_reviewer must drop the type too — a dangling reviewer_type is the "+
			"inconsistent-row shape the assignee columns had to be repaired for")
}

// clear_reviewer wins over a reviewer_id sent in the same body. Pinning a
// deterministic winner matters because the two fields are independent on the wire
// and a client can send both.
func TestTaskHandler_Update_ClearReviewerBeatsReviewerID(t *testing.T) {
	got := updateTaskWithBody(t, newReviewerTask(nil, nil),
		fmt.Sprintf(`{"clear_reviewer":true,"reviewer_id":%q,"reviewer_type":"user"}`, uuid.New()))

	assert.Nil(t, got.ReviewerID, "clear_reviewer must win over a reviewer_id in the same body")
	assert.Nil(t, got.ReviewerType)
}

// The regression this guards: an omitted reviewer_id must not be read as "clear".
func TestTaskHandler_Update_OmittedReviewerLeavesItUntouched(t *testing.T) {
	existingID := uuid.New()
	existingType := domain.AssigneeTypeAgent
	got := updateTaskWithBody(t, newReviewerTask(&existingID, &existingType),
		`{"title":"New Title"}`)

	require.NotNil(t, got.ReviewerID, "an unrelated edit must not unassign the reviewer")
	assert.Equal(t, existingID, *got.ReviewerID)
	require.NotNil(t, got.ReviewerType)
	assert.Equal(t, domain.AssigneeTypeAgent, *got.ReviewerType)
}

// --- include_description projection (#32f4c087) ------------------------------
//
// The board's task list was 77% description bytes for cards that never render a
// description. These tests pin the three things that made the obvious fix unsafe:
// the default must stay INCLUSIVE (the fleet dispatcher reads descriptions out of
// this list to decide whether a task is prose-gated), the opt-out must drop the
// KEY and not just blank it, and has_description must describe the task rather
// than the projection — otherwise the card loses its "has a description" glyph.

func listTasksForDescriptionTest(t *testing.T, query string) map[string]any {
	t.Helper()
	projectID := uuid.New()
	now := time.Now()
	tasks := []domain.Task{
		{ID: uuid.New(), ProjectID: projectID, Title: "with", Description: "a long spec", CreatedAt: now, UpdatedAt: now},
		{ID: uuid.New(), ProjectID: projectID, Title: "without", Description: "", CreatedAt: now, UpdatedAt: now},
		{ID: uuid.New(), ProjectID: projectID, Title: "whitespace", Description: "   \n\t ", CreatedAt: now, UpdatedAt: now},
	}
	mockSvc := &MockTaskService{
		ListFunc: func(ctx context.Context, pid uuid.UUID, filter repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error) {
			return pagination.NewPage(tasks, len(tasks), pg), nil
		},
	}
	h, e := setupTaskTest(mockSvc)
	req := httptest.NewRequest(http.MethodGet, "/?page=1&page_size=10"+query, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tasks")
	c.SetParamNames("proj_id")
	c.SetParamValues(projectID.String())

	require.NoError(t, h.List(c))
	require.Equal(t, http.StatusOK, rec.Code)

	// Decode into map[string]any, NOT domain.Task: unmarshalling into the struct
	// turns an absent key and an empty string into the same zero value, which is
	// exactly the distinction under test.
	var raw map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	return raw
}

func rawItems(t *testing.T, raw map[string]any) []map[string]any {
	t.Helper()
	items, ok := raw["items"].([]any)
	require.True(t, ok, "response has no items array")
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		require.True(t, ok)
		out = append(out, m)
	}
	return out
}

// The default must not change. The fleet dispatcher's dependency-park gate greps
// the description of every task in a list feed; serving it a description-less
// list does not fail loudly, it silently stops matching.
func TestTaskHandler_List_IncludesDescriptionByDefault(t *testing.T) {
	items := rawItems(t, listTasksForDescriptionTest(t, ""))
	require.Len(t, items, 3)
	assert.Equal(t, "a long spec", items[0]["description"],
		"description must be present by default — opting OUT is the caller's choice")
	assert.Equal(t, true, items[0]["has_description"])
}

func TestTaskHandler_List_ExcludeDescriptionDropsTheKey(t *testing.T) {
	items := rawItems(t, listTasksForDescriptionTest(t, "&include_description=false"))
	require.Len(t, items, 3)
	for _, it := range items {
		_, present := it["description"]
		assert.False(t, present,
			"include_description=false must omit the key, not send an empty string: %v", it["title"])
	}
}

// The whole point of the projection: the card keeps its glyph. has_description
// reflects the task, not what the caller asked to be sent — and whitespace-only
// text is not a description.
func TestTaskHandler_List_HasDescriptionSurvivesExclusion(t *testing.T) {
	for _, q := range []string{"", "&include_description=false"} {
		items := rawItems(t, listTasksForDescriptionTest(t, q))
		require.Len(t, items, 3)
		assert.Equal(t, true, items[0]["has_description"], "q=%q: real description", q)
		assert.Equal(t, false, items[1]["has_description"], "q=%q: empty description", q)
		assert.Equal(t, false, items[2]["has_description"], "q=%q: whitespace-only description", q)
	}
}

// Only an explicit negative opts out. A typo, an empty value, or an unrelated
// word must fall back to the safe side, because the failure mode of guessing
// wrong here is silent.
func TestTaskHandler_List_ExclusionRequiresAnExplicitNegative(t *testing.T) {
	for _, q := range []string{
		"&include_description=true",
		"&include_description=",
		"&include_description=maybe",
		"&include_description=FALSEY",
	} {
		items := rawItems(t, listTasksForDescriptionTest(t, q))
		_, present := items[0]["description"]
		assert.True(t, present, "%q must NOT be read as an opt-out", q)
	}
	// ...and the spellings that must work, including case and the numeric form.
	for _, q := range []string{
		"&include_description=false",
		"&include_description=FALSE",
		"&include_description=0",
		"&include_description=no",
	} {
		items := rawItems(t, listTasksForDescriptionTest(t, q))
		_, present := items[0]["description"]
		assert.False(t, present, "%q must be read as an opt-out", q)
	}
}

// SearchGlobal shares the list decorator with List. It is here because the first
// version of this change patched only one of the two call sites — they contain a
// byte-identical block — and left global search reporting has_description:false
// for every task that had one. A silently wrong flag is worse than an absent one.
func TestTaskHandler_SearchGlobal_DecoratesLikeList(t *testing.T) {
	now := time.Now()
	tasks := []domain.Task{
		{ID: uuid.New(), Title: "with", Description: "a long spec", CreatedAt: now, UpdatedAt: now},
		{ID: uuid.New(), Title: "without", Description: "", CreatedAt: now, UpdatedAt: now},
	}
	mockSvc := &MockTaskService{
		SearchFunc: func(ctx context.Context, wsID uuid.UUID, filter repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error) {
			return pagination.NewPage(tasks, len(tasks), pg), nil
		},
	}
	h, e := setupTaskTest(mockSvc)

	call := func(query string) []map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/?search=spec"+query, http.NoBody)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetPath("/workspaces/:ws_id/tasks")
		c.SetParamNames("ws_id")
		c.SetParamValues(uuid.New().String())
		require.NoError(t, h.SearchGlobal(c))
		require.Equal(t, http.StatusOK, rec.Code)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
		return rawItems(t, raw)
	}

	items := call("")
	require.Len(t, items, 2)
	assert.Equal(t, true, items[0]["has_description"], "search must set the flag too")
	assert.Equal(t, false, items[1]["has_description"])
	assert.Equal(t, "a long spec", items[0]["description"], "search still includes bodies by default")

	slim := call("&include_description=false")
	require.Len(t, slim, 2)
	_, present := slim[0]["description"]
	assert.False(t, present)
	assert.Equal(t, true, slim[0]["has_description"], "flag survives exclusion in search too")
}

// ListSubtasks is a third list-shaped endpoint (list-view.tsx's inline subtask
// expansion renders it through the same EnhancedTitleCell as the board/list, which
// trusts has_description over a live text check). Before decorateTaskList was
// wired in here too, HasDescription was left at its Go zero value for every
// subtask row — a subtask with real description text reported
// has_description:false and the card silently lost its glyph, even though this
// endpoint never truncates the text itself. Caught auditing consumers of the
// #32f4c087 projection.
func TestTaskHandler_ListSubtasks_HasDescriptionAndURLAreDecorated(t *testing.T) {
	parentID := uuid.New()
	now := time.Now()
	subtasks := []domain.Task{
		{ID: uuid.New(), ParentTaskID: &parentID, Title: "with", Description: "real text", CreatedAt: now, UpdatedAt: now},
		{ID: uuid.New(), ParentTaskID: &parentID, Title: "without", Description: "", CreatedAt: now, UpdatedAt: now},
	}
	mockSvc := &MockTaskService{
		ListSubtasksFunc: func(ctx context.Context, pid uuid.UUID) ([]domain.Task, error) {
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

	require.NoError(t, h.ListSubtasks(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	items := rawItems(t, raw)
	require.Len(t, items, 2)

	assert.Equal(t, true, items[0]["has_description"], "subtask with real text must report has_description:true")
	assert.Equal(t, "real text", items[0]["description"], "ListSubtasks never truncates — no caller asked it to")
	assert.NotEmpty(t, items[0]["url"], "decorateTaskList also fills in the URL, which this endpoint never set before")

	assert.Equal(t, false, items[1]["has_description"])
}
