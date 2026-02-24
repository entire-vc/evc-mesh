package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// TaskStatusHandler handles HTTP requests for task status management.
type TaskStatusHandler struct {
	statusService service.TaskStatusService
}

// NewTaskStatusHandler creates a new TaskStatusHandler with the given service.
func NewTaskStatusHandler(ss service.TaskStatusService) *TaskStatusHandler {
	return &TaskStatusHandler{statusService: ss}
}

// createTaskStatusRequest represents the JSON body for creating a task status.
type createTaskStatusRequest struct {
	Name     string                `json:"name"`
	Color    string                `json:"color"`
	Category domain.StatusCategory `json:"category"`
}

// updateTaskStatusRequest represents the JSON body for updating a task status.
type updateTaskStatusRequest struct {
	Name     *string                `json:"name"`
	Color    *string                `json:"color"`
	Category *domain.StatusCategory `json:"category"`
}

// reorderStatusesRequest represents the JSON body for reordering statuses.
type reorderStatusesRequest struct {
	StatusIDs []uuid.UUID `json:"status_ids"`
}

// List handles GET /projects/:proj_id/statuses
func (h *TaskStatusHandler) List(c echo.Context) error {
	projIDStr := c.Param("proj_id")
	projID, err := uuid.Parse(projIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	statuses, err := h.statusService.ListByProject(c.Request().Context(), projID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, statuses)
}

// Create handles POST /projects/:proj_id/statuses
func (h *TaskStatusHandler) Create(c echo.Context) error {
	projIDStr := c.Param("proj_id")
	projID, err := uuid.Parse(projIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	var req createTaskStatusRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"name": "name is required",
		}))
	}

	status := &domain.TaskStatus{
		ID:        uuid.New(),
		ProjectID: projID,
		Name:      req.Name,
		Color:     req.Color,
		Category:  req.Category,
	}

	if err := h.statusService.Create(c.Request().Context(), status); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, status)
}

// Update handles PATCH /projects/:proj_id/statuses/:status_id
func (h *TaskStatusHandler) Update(c echo.Context) error {
	statusIDStr := c.Param("status_id")
	statusID, err := uuid.Parse(statusIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid status_id"))
	}

	var req updateTaskStatusRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	// Build an update from the request. We pass a minimal TaskStatus with
	// only the fields that need updating; the service layer handles the merge.
	status := &domain.TaskStatus{
		ID: statusID,
	}

	if req.Name != nil {
		status.Name = *req.Name
	}
	if req.Color != nil {
		status.Color = *req.Color
	}
	if req.Category != nil {
		status.Category = *req.Category
	}

	if err := h.statusService.Update(c.Request().Context(), status); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, status)
}

// Reorder handles PUT /projects/:proj_id/statuses/reorder
func (h *TaskStatusHandler) Reorder(c echo.Context) error {
	projIDStr := c.Param("proj_id")
	projID, err := uuid.Parse(projIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	var req reorderStatusesRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if len(req.StatusIDs) == 0 {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"status_ids": "status_ids is required",
		}))
	}

	if err := h.statusService.Reorder(c.Request().Context(), projID, req.StatusIDs); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}
