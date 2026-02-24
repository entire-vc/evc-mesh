package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// WorkspaceHandler handles HTTP requests for workspace management.
type WorkspaceHandler struct {
	workspaceService service.WorkspaceService
}

// NewWorkspaceHandler creates a new WorkspaceHandler with the given service.
func NewWorkspaceHandler(ws service.WorkspaceService) *WorkspaceHandler {
	return &WorkspaceHandler{workspaceService: ws}
}

// createWorkspaceRequest represents the JSON body for creating a workspace.
type createWorkspaceRequest struct {
	Name     string          `json:"name"`
	Slug     string          `json:"slug"`
	Settings json.RawMessage `json:"settings"`
}

// updateWorkspaceRequest represents the JSON body for partially updating a workspace.
type updateWorkspaceRequest struct {
	Name     *string          `json:"name"`
	Slug     *string          `json:"slug"`
	Settings *json.RawMessage `json:"settings"`
}

// List handles GET /workspaces
func (h *WorkspaceHandler) List(c echo.Context) error {
	userIDVal := c.Get("user_id")
	if userIDVal == nil {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("user_id not found in context"))
	}

	userID, ok := userIDVal.(uuid.UUID)
	if !ok {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid user_id in context"))
	}

	workspaces, err := h.workspaceService.ListByOwner(c.Request().Context(), userID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, workspaces)
}

// Create handles POST /workspaces
func (h *WorkspaceHandler) Create(c echo.Context) error {
	var req createWorkspaceRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"name": "name is required",
		}))
	}

	userIDVal := c.Get("user_id")
	var ownerID uuid.UUID
	if userIDVal != nil {
		if uid, ok := userIDVal.(uuid.UUID); ok {
			ownerID = uid
		}
	}

	workspace := &domain.Workspace{
		ID:       uuid.New(),
		Name:     req.Name,
		Slug:     req.Slug,
		OwnerID:  ownerID,
		Settings: req.Settings,
	}

	if err := h.workspaceService.Create(c.Request().Context(), workspace); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, workspace)
}

// GetByID handles GET /workspaces/:ws_id
func (h *WorkspaceHandler) GetByID(c echo.Context) error {
	wsIDStr := c.Param("ws_id")
	wsID, err := uuid.Parse(wsIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	workspace, err := h.workspaceService.GetByID(c.Request().Context(), wsID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, workspace)
}

// Update handles PATCH /workspaces/:ws_id
func (h *WorkspaceHandler) Update(c echo.Context) error {
	wsIDStr := c.Param("ws_id")
	wsID, err := uuid.Parse(wsIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	var req updateWorkspaceRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	// Fetch existing workspace first.
	workspace, err := h.workspaceService.GetByID(c.Request().Context(), wsID)
	if err != nil {
		return handleError(c, err)
	}

	// Apply partial updates.
	if req.Name != nil {
		workspace.Name = *req.Name
	}
	if req.Slug != nil {
		workspace.Slug = *req.Slug
	}
	if req.Settings != nil {
		workspace.Settings = *req.Settings
	}

	if err := h.workspaceService.Update(c.Request().Context(), workspace); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, workspace)
}

// Delete handles DELETE /workspaces/:ws_id
func (h *WorkspaceHandler) Delete(c echo.Context) error {
	wsIDStr := c.Param("ws_id")
	wsID, err := uuid.Parse(wsIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	if err := h.workspaceService.Delete(c.Request().Context(), wsID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}
