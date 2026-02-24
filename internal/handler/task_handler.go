package handler

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// validSlugRe allows only safe identifiers for custom field slugs (prevents SQL injection).
var validSlugRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// TaskHandler handles HTTP requests for task management.
type TaskHandler struct {
	taskService service.TaskService
}

// NewTaskHandler creates a new TaskHandler with the given service.
func NewTaskHandler(ts service.TaskService) *TaskHandler {
	return &TaskHandler{taskService: ts}
}

// createTaskRequest represents the JSON body for creating a task.
type createTaskRequest struct {
	Title        string              `json:"title"`
	Description  string              `json:"description"`
	Priority     domain.Priority     `json:"priority"`
	AssigneeID   *uuid.UUID          `json:"assignee_id"`
	AssigneeType domain.AssigneeType `json:"assignee_type"`
	Labels       []string            `json:"labels"`
	CustomFields json.RawMessage     `json:"custom_fields"`
}

// updateTaskRequest represents the JSON body for partially updating a task.
type updateTaskRequest struct {
	Title        *string              `json:"title"`
	Description  *string              `json:"description"`
	Priority     *domain.Priority     `json:"priority"`
	AssigneeID   *uuid.UUID           `json:"assignee_id"`
	AssigneeType *domain.AssigneeType `json:"assignee_type"`
	Labels       *[]string            `json:"labels"`
	CustomFields json.RawMessage      `json:"custom_fields"`
}

// moveTaskRequest represents the JSON body for moving a task.
type moveTaskRequest struct {
	StatusID *uuid.UUID `json:"status_id"`
	Position *float64   `json:"position"`
}

// Create handles POST /projects/:proj_id/tasks
func (h *TaskHandler) Create(c echo.Context) error {
	projectIDStr := c.Param("proj_id")
	projectID, err := uuid.Parse(projectIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	var req createTaskRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.Title == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"title": "title is required",
		}))
	}

	task := &domain.Task{
		ID:           uuid.New(),
		ProjectID:    projectID,
		Title:        req.Title,
		Description:  req.Description,
		Priority:     req.Priority,
		AssigneeID:   req.AssigneeID,
		AssigneeType: req.AssigneeType,
		Labels:       pq.StringArray(req.Labels),
		CustomFields: req.CustomFields,
	}

	if err := h.taskService.Create(c.Request().Context(), task); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, task)
}

// GetByID handles GET /tasks/:task_id
func (h *TaskHandler) GetByID(c echo.Context) error {
	taskIDStr := c.Param("task_id")
	taskID, err := uuid.Parse(taskIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}

	task, err := h.taskService.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, task)
}

// Update handles PATCH /tasks/:task_id
func (h *TaskHandler) Update(c echo.Context) error {
	taskIDStr := c.Param("task_id")
	taskID, err := uuid.Parse(taskIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}

	var req updateTaskRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	// Fetch existing task first
	task, err := h.taskService.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return handleError(c, err)
	}

	// Apply partial updates
	if req.Title != nil {
		task.Title = *req.Title
	}
	if req.Description != nil {
		task.Description = *req.Description
	}
	if req.Priority != nil {
		task.Priority = *req.Priority
	}
	if req.AssigneeID != nil {
		task.AssigneeID = req.AssigneeID
	}
	if req.AssigneeType != nil {
		task.AssigneeType = *req.AssigneeType
	}
	if req.Labels != nil {
		task.Labels = pq.StringArray(*req.Labels)
	}
	if req.CustomFields != nil {
		task.CustomFields = req.CustomFields
	}

	if err := h.taskService.Update(c.Request().Context(), task); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, task)
}

// Delete handles DELETE /tasks/:task_id
func (h *TaskHandler) Delete(c echo.Context) error {
	taskIDStr := c.Param("task_id")
	taskID, err := uuid.Parse(taskIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}

	if err := h.taskService.Delete(c.Request().Context(), taskID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// listTasksQuery represents query parameters for listing tasks.
type listTasksQuery struct {
	Status       string `query:"status"`
	AssigneeType string `query:"assignee_type"`
	Priority     string `query:"priority"`
	Labels       string `query:"labels"`
	Search       string `query:"search"`
}

// List handles GET /projects/:proj_id/tasks
func (h *TaskHandler) List(c echo.Context) error {
	projectIDStr := c.Param("proj_id")
	projectID, err := uuid.Parse(projectIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	var q listTasksQuery
	if err := c.Bind(&q); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid query parameters"))
	}

	var pg pagination.Params
	if err := c.Bind(&pg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination parameters"))
	}
	pg.Normalize()

	filter := repository.TaskFilter{
		Search: q.Search,
	}

	if q.AssigneeType != "" {
		at := domain.AssigneeType(q.AssigneeType)
		filter.AssigneeType = &at
	}
	if q.Priority != "" {
		p := domain.Priority(q.Priority)
		filter.Priority = &p
	}
	if q.Labels != "" {
		filter.Labels = []string{q.Labels}
	}
	if q.Status != "" {
		statusID, err := uuid.Parse(q.Status)
		if err == nil {
			filter.StatusIDs = []uuid.UUID{statusID}
		}
	}

	// Parse custom field filters from query params with "custom." prefix.
	// Supported: custom.{slug}=value, custom.{slug}_gte=5, custom.{slug}_lte=10
	cfFilters := parseCustomFieldFilters(c)
	if len(cfFilters) > 0 {
		filter.CustomFields = cfFilters
	}

	page, err := h.taskService.List(c.Request().Context(), projectID, filter, pg)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, page)
}

// parseCustomFieldFilters extracts custom field filter parameters from query string.
// Supports: custom.{slug}=value, custom.{slug}_gte=N, custom.{slug}_lte=N
func parseCustomFieldFilters(c echo.Context) map[string]repository.CustomFieldFilter {
	result := make(map[string]repository.CustomFieldFilter)

	for key, values := range c.QueryParams() {
		if !strings.HasPrefix(key, "custom.") || len(values) == 0 {
			continue
		}
		val := values[0]
		fieldKey := strings.TrimPrefix(key, "custom.")

		if strings.HasSuffix(fieldKey, "_gte") {
			slug := strings.TrimSuffix(fieldKey, "_gte")
			if !validSlugRe.MatchString(slug) {
				continue
			}
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				continue
			}
			cf := result[slug]
			cf.Gte = &f
			result[slug] = cf
		} else if strings.HasSuffix(fieldKey, "_lte") {
			slug := strings.TrimSuffix(fieldKey, "_lte")
			if !validSlugRe.MatchString(slug) {
				continue
			}
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				continue
			}
			cf := result[slug]
			cf.Lte = &f
			result[slug] = cf
		} else {
			// Exact equality.
			if !validSlugRe.MatchString(fieldKey) {
				continue
			}
			cf := result[fieldKey]
			cf.Eq = val
			result[fieldKey] = cf
		}
	}

	return result
}

// MoveTask handles POST /tasks/:task_id/move
func (h *TaskHandler) MoveTask(c echo.Context) error {
	taskIDStr := c.Param("task_id")
	taskID, err := uuid.Parse(taskIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}

	var req moveTaskRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.StatusID == nil && req.Position == nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("status_id or position is required"))
	}

	input := service.MoveTaskInput{
		StatusID: req.StatusID,
		Position: req.Position,
	}

	if err := h.taskService.MoveTask(c.Request().Context(), taskID, input); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// ListSubtasks handles GET /tasks/:task_id/subtasks
func (h *TaskHandler) ListSubtasks(c echo.Context) error {
	taskIDStr := c.Param("task_id")
	taskID, err := uuid.Parse(taskIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}

	subtasks, err := h.taskService.ListSubtasks(c.Request().Context(), taskID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, subtasks)
}

// handleError inspects the error type and returns appropriate JSON response.
func handleError(c echo.Context, err error) error {
	if apiErr, ok := err.(*apierror.Error); ok {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}
	return c.JSON(http.StatusInternalServerError, apierror.InternalError(""))
}
