package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
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
	Title          string              `json:"title"`
	Description    string              `json:"description"`
	Priority       domain.Priority     `json:"priority"`
	StatusID       string              `json:"status_id"`
	ParentTaskID   *uuid.UUID          `json:"parent_task_id"`
	AssigneeID     *uuid.UUID          `json:"assignee_id"`
	AssigneeType   domain.AssigneeType `json:"assignee_type"`
	DueDate        *time.Time          `json:"due_date"`
	EstimatedHours *float64            `json:"estimated_hours"`
	Labels         []string            `json:"labels"`
	CustomFields   json.RawMessage     `json:"custom_fields"`
}

// flexTime is a *time.Time that also accepts date-only strings ("2026-03-20")
// in addition to the standard RFC3339 format. A JSON null sets the pointer to nil
// while still marking the field as "present" via the wasSet flag.
type flexTime struct {
	Time   *time.Time
	wasSet bool // true when the JSON key was present (even if null)
}

func (f *flexTime) UnmarshalJSON(b []byte) error {
	f.wasSet = true
	s := strings.Trim(string(b), `"`)
	if s == "null" || s == "" {
		f.Time = nil
		return nil
	}
	// Try RFC3339 first.
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		f.Time = &t
		return nil
	}
	// Fall back to date-only (YYYY-MM-DD).
	if t, err := time.Parse("2006-01-02", s); err == nil {
		f.Time = &t
		return nil
	}
	return errors.New("due_date must be RFC3339 or YYYY-MM-DD")
}

// updateTaskRequest represents the JSON body for partially updating a task.
type updateTaskRequest struct {
	Title          *string              `json:"title"`
	Description    *string              `json:"description"`
	Priority       *domain.Priority     `json:"priority"`
	AssigneeID     *uuid.UUID           `json:"assignee_id"`
	AssigneeType   *domain.AssigneeType `json:"assignee_type"`
	DueDate        flexTime             `json:"due_date"`
	EstimatedHours *float64             `json:"estimated_hours"`
	Labels         *[]string            `json:"labels"`
	CustomFields   json.RawMessage      `json:"custom_fields"`
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

	// Resolve status: use provided status_id or fall back to the project's default.
	var statusID uuid.UUID
	if req.StatusID != "" {
		statusID, err = uuid.Parse(req.StatusID)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid status_id"))
		}
	} else {
		defaultStatus, err := h.taskService.GetDefaultStatus(c.Request().Context(), projectID)
		if err != nil || defaultStatus == nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("project has no default status; provide status_id"))
		}
		statusID = defaultStatus.ID
	}

	// Resolve assignee type (default to "unassigned").
	assigneeType := req.AssigneeType
	if assigneeType == "" {
		assigneeType = domain.AssigneeTypeUnassigned
	}

	// Resolve priority (default to "medium").
	priority := req.Priority
	if priority == "" {
		priority = domain.PriorityMedium
	}

	// Resolve creator from auth context.
	var createdBy uuid.UUID
	var createdByType domain.ActorType
	if mw.IsAgent(c) {
		createdBy, _ = mw.GetAgentID(c)
		createdByType = domain.ActorTypeAgent
	} else {
		createdBy, _ = mw.GetUserID(c)
		createdByType = domain.ActorTypeUser
	}

	task := &domain.Task{
		ID:             uuid.New(),
		ProjectID:      projectID,
		StatusID:       statusID,
		Title:          req.Title,
		Description:    req.Description,
		Priority:       priority,
		ParentTaskID:   req.ParentTaskID,
		AssigneeID:     req.AssigneeID,
		AssigneeType:   assigneeType,
		DueDate:        req.DueDate,
		EstimatedHours: req.EstimatedHours,
		Labels:         pq.StringArray(req.Labels),
		CustomFields:   req.CustomFields,
		CreatedBy:      createdBy,
		CreatedByType:  createdByType,
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
	if req.DueDate.wasSet {
		task.DueDate = req.DueDate.Time // nil clears, non-nil sets
	}
	if req.EstimatedHours != nil {
		task.EstimatedHours = req.EstimatedHours
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

		switch {
		case strings.HasSuffix(fieldKey, "_gte"):
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
		case strings.HasSuffix(fieldKey, "_lte"):
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
		default:
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

	return c.JSON(http.StatusOK, map[string]any{"items": subtasks})
}

// assignTaskRequest represents the JSON body for assigning a task.
type assignTaskRequest struct {
	AssigneeID   *uuid.UUID          `json:"assignee_id"`
	AssigneeType domain.AssigneeType `json:"assignee_type"`
}

// AssignTask handles POST /tasks/:task_id/assign
func (h *TaskHandler) AssignTask(c echo.Context) error {
	taskIDStr := c.Param("task_id")
	taskID, err := uuid.Parse(taskIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}

	var req assignTaskRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	assigneeType := req.AssigneeType
	if assigneeType == "" {
		if req.AssigneeID == nil {
			assigneeType = domain.AssigneeTypeUnassigned
		} else {
			assigneeType = domain.AssigneeTypeAgent
		}
	}

	input := service.AssignTaskInput{
		AssigneeID:   req.AssigneeID,
		AssigneeType: assigneeType,
	}

	if err := h.taskService.AssignTask(c.Request().Context(), taskID, input); err != nil {
		return handleError(c, err)
	}

	// Return the updated task.
	task, err := h.taskService.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, task)
}

// createSubtaskRequest represents the JSON body for creating a subtask.
type createSubtaskRequest struct {
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Priority    domain.Priority `json:"priority"`
}

// CreateSubtask handles POST /tasks/:task_id/subtasks
func (h *TaskHandler) CreateSubtask(c echo.Context) error {
	parentTaskIDStr := c.Param("task_id")
	parentTaskID, err := uuid.Parse(parentTaskIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}

	var req createSubtaskRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.Title == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"title": "title is required",
		}))
	}

	priority := req.Priority
	if priority == "" {
		priority = domain.PriorityMedium
	}

	input := service.CreateSubtaskInput{
		Title:       req.Title,
		Description: req.Description,
		Priority:    priority,
	}

	subtask, err := h.taskService.CreateSubtask(c.Request().Context(), parentTaskID, input)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, subtask)
}

// bulkUpdateRequest represents the JSON body for bulk-updating multiple tasks.
type bulkUpdateRequest struct {
	TaskIDs []uuid.UUID      `json:"task_ids"`
	Updates bulkUpdateFields `json:"updates"`
}

// bulkUpdateFields holds the optional fields that can be changed in a bulk update.
type bulkUpdateFields struct {
	StatusID     *uuid.UUID           `json:"status_id,omitempty"`
	Priority     *domain.Priority     `json:"priority,omitempty"`
	AssigneeID   *uuid.UUID           `json:"assignee_id,omitempty"`
	AssigneeType *domain.AssigneeType `json:"assignee_type,omitempty"`
	Labels       *[]string            `json:"labels,omitempty"`
}

// bulkUpdateResponse is returned after a bulk update operation.
type bulkUpdateResponse struct {
	Updated int      `json:"updated"`
	Errors  []string `json:"errors,omitempty"`
}

// BulkUpdate handles POST /projects/:proj_id/tasks/bulk-update
func (h *TaskHandler) BulkUpdate(c echo.Context) error {
	projectIDStr := c.Param("proj_id")
	projectID, err := uuid.Parse(projectIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	var req bulkUpdateRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if len(req.TaskIDs) == 0 {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("task_ids must not be empty"))
	}
	if len(req.TaskIDs) > 100 {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("max 100 tasks per bulk update"))
	}

	// Require at least one field to update.
	u := req.Updates
	if u.StatusID == nil && u.Priority == nil && u.AssigneeID == nil && u.AssigneeType == nil && u.Labels == nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("at least one field in updates is required"))
	}

	input := service.BulkUpdateTasksInput{
		TaskIDs:      req.TaskIDs,
		StatusID:     u.StatusID,
		Priority:     u.Priority,
		AssigneeID:   u.AssigneeID,
		AssigneeType: u.AssigneeType,
		Labels:       u.Labels,
	}

	result := h.taskService.BulkUpdate(c.Request().Context(), projectID, input)

	return c.JSON(http.StatusOK, bulkUpdateResponse{
		Updated: result.Updated,
		Errors:  result.Errors,
	})
}

// checkoutRequest represents the JSON body for POST /tasks/:task_id/checkout.
type checkoutRequest struct {
	TTLMinutes int `json:"ttl_minutes"`
}

// releaseCheckoutRequest represents the JSON body for DELETE /tasks/:task_id/checkout.
type releaseCheckoutRequest struct {
	CheckoutToken string `json:"checkout_token"`
}

// extendCheckoutRequest represents the JSON body for PATCH /tasks/:task_id/checkout.
type extendCheckoutRequest struct {
	CheckoutToken string `json:"checkout_token"`
	TTLMinutes    int    `json:"ttl_minutes"`
}

// Checkout handles POST /tasks/:task_id/checkout
func (h *TaskHandler) Checkout(c echo.Context) error {
	taskID, err := uuid.Parse(c.Param("task_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}

	var req checkoutRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	result, err := h.taskService.CheckoutTask(c.Request().Context(), taskID, req.TTLMinutes)
	if err != nil {
		var conflict *service.CheckoutConflictError
		if errors.As(err, &conflict) {
			return c.JSON(http.StatusConflict, map[string]interface{}{
				"code":    409,
				"message": "Task is already checked out",
				"details": map[string]interface{}{
					"checked_out_by": conflict.CheckedOutBy,
					"expires_at":     conflict.ExpiresAt,
				},
			})
		}
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, result)
}

// ReleaseCheckout handles DELETE /tasks/:task_id/checkout
func (h *TaskHandler) ReleaseCheckout(c echo.Context) error {
	taskID, err := uuid.Parse(c.Param("task_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}

	var req releaseCheckoutRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.CheckoutToken == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("checkout_token is required"))
	}

	token, err := uuid.Parse(req.CheckoutToken)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid checkout_token"))
	}

	if err := h.taskService.ReleaseCheckout(c.Request().Context(), taskID, token); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// ExtendCheckout handles PATCH /tasks/:task_id/checkout
func (h *TaskHandler) ExtendCheckout(c echo.Context) error {
	taskID, err := uuid.Parse(c.Param("task_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}

	var req extendCheckoutRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.CheckoutToken == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("checkout_token is required"))
	}

	token, err := uuid.Parse(req.CheckoutToken)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid checkout_token"))
	}

	result, err := h.taskService.ExtendCheckout(c.Request().Context(), taskID, token, req.TTLMinutes)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, result)
}

// moveToProjectRequest represents the JSON body for moving a task to another project.
type moveToProjectRequest struct {
	ProjectID string `json:"project_id"`
}

// MoveToProject handles POST /tasks/:task_id/move-to-project
func (h *TaskHandler) MoveToProject(c echo.Context) error {
	taskIDStr := c.Param("task_id")
	taskID, err := uuid.Parse(taskIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}

	var req moveToProjectRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.ProjectID == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"project_id": "project_id is required",
		}))
	}

	projectID, err := uuid.Parse(req.ProjectID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	task, err := h.taskService.MoveToProject(c.Request().Context(), taskID, projectID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, task)
}

// ruleViolationAPIResponse is the JSON shape for 422 rule violation responses.
type ruleViolationAPIResponse struct {
	Error      string                 `json:"error"`
	Message    string                 `json:"message"`
	Violations []domain.RuleViolation `json:"violations"`
}

// handleError inspects the error type and returns appropriate JSON response.
func handleError(c echo.Context, err error) error {
	var ruleErr *service.RuleViolationError
	if errors.As(err, &ruleErr) {
		return c.JSON(http.StatusUnprocessableEntity, ruleViolationAPIResponse{
			Error:      "rule_violation",
			Message:    "Action blocked by governance rules",
			Violations: ruleErr.Violations,
		})
	}

	if apiErr, ok := err.(*apierror.Error); ok {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}

	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		switch pqErr.Code {
		case "23505": // unique_violation
			return c.JSON(http.StatusConflict, apierror.Conflict("already exists"))
		case "23503": // foreign_key_violation
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("referenced entity not found"))
		case "23514": // check_violation
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("value violates constraint"))
		case "22P02": // invalid_text_representation (bad enum)
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid value for field"))
		}
	}

	log.Printf("ERROR %s %s: %v", c.Request().Method, c.Request().URL.Path, err)
	return c.JSON(http.StatusInternalServerError, apierror.InternalError(""))
}
