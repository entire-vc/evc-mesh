package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// ProjectHandler handles HTTP requests for project management.
type ProjectHandler struct {
	projectService service.ProjectService
}

// NewProjectHandler creates a new ProjectHandler with the given service.
func NewProjectHandler(ps service.ProjectService) *ProjectHandler {
	return &ProjectHandler{projectService: ps}
}

// createProjectRequest represents the JSON body for creating a project.
type createProjectRequest struct {
	Name        string          `json:"name"`
	Slug        string          `json:"slug"`
	Description string          `json:"description"`
	Icon        string          `json:"icon"`
	Settings    json.RawMessage `json:"settings"`
}

// updateProjectRequest represents the JSON body for partially updating a project.
type updateProjectRequest struct {
	Name        *string          `json:"name"`
	Slug        *string          `json:"slug"`
	Description *string          `json:"description"`
	Icon        *string          `json:"icon"`
	Settings    *json.RawMessage `json:"settings"`
}

// listProjectsQuery represents query parameters for listing projects.
type listProjectsQuery struct {
	IsArchived string `query:"is_archived"`
	Search     string `query:"search"`
}

// List handles GET /workspaces/:ws_id/projects
func (h *ProjectHandler) List(c echo.Context) error {
	wsIDStr := c.Param("ws_id")
	wsID, err := uuid.Parse(wsIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	var q listProjectsQuery
	if err = c.Bind(&q); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid query parameters"))
	}

	var pg pagination.Params
	if err = c.Bind(&pg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination parameters"))
	}
	pg.Normalize()

	filter := repository.ProjectFilter{
		Search: q.Search,
	}

	if q.IsArchived != "" {
		var v bool
		v, err = strconv.ParseBool(q.IsArchived)
		if err == nil {
			filter.IsArchived = &v
		}
	}

	// Workspace owners/admins see all projects; others see only their memberships.
	wsRole, _ := c.Get(mw.ContextKeyWorkspaceRole).(string)
	if mw.IsAgent(c) {
		// Agents always filter by membership.
		if agentID, err := mw.GetAgentID(c); err == nil {
			filter.MemberAgentID = &agentID
		}
	} else if wsRole != domain.RoleOwner && wsRole != domain.RoleAdmin {
		if userID, err := mw.GetUserID(c); err == nil {
			filter.MemberUserID = &userID
		}
	}

	page, err := h.projectService.List(c.Request().Context(), wsID, filter, pg)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, page)
}

// Create handles POST /workspaces/:ws_id/projects
func (h *ProjectHandler) Create(c echo.Context) error {
	wsIDStr := c.Param("ws_id")
	wsID, err := uuid.Parse(wsIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	var req createProjectRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"name": "name is required",
		}))
	}

	project := &domain.Project{
		ID:                  uuid.New(),
		WorkspaceID:         wsID,
		Name:                req.Name,
		Slug:                req.Slug,
		Description:         req.Description,
		Icon:                req.Icon,
		Settings:            req.Settings,
		DefaultAssigneeType: domain.DefaultAssigneeNone,
	}

	if err := h.projectService.Create(c.Request().Context(), project); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, project)
}

// GetByID handles GET /projects/:proj_id
func (h *ProjectHandler) GetByID(c echo.Context) error {
	projIDStr := c.Param("proj_id")
	projID, err := uuid.Parse(projIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	project, err := h.projectService.GetByID(c.Request().Context(), projID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, project)
}

// Update handles PATCH /projects/:proj_id
func (h *ProjectHandler) Update(c echo.Context) error {
	projIDStr := c.Param("proj_id")
	projID, err := uuid.Parse(projIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	var req updateProjectRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	// Fetch existing project first.
	project, err := h.projectService.GetByID(c.Request().Context(), projID)
	if err != nil {
		return handleError(c, err)
	}

	// Apply partial updates.
	if req.Name != nil {
		project.Name = *req.Name
	}
	if req.Slug != nil {
		project.Slug = *req.Slug
	}
	if req.Description != nil {
		project.Description = *req.Description
	}
	if req.Icon != nil {
		project.Icon = *req.Icon
	}
	if req.Settings != nil {
		project.Settings = *req.Settings
	}

	if err := h.projectService.Update(c.Request().Context(), project); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, project)
}

// Delete handles DELETE /projects/:proj_id (archive)
func (h *ProjectHandler) Delete(c echo.Context) error {
	projIDStr := c.Param("proj_id")
	projID, err := uuid.Parse(projIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	if err := h.projectService.Archive(c.Request().Context(), projID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}
