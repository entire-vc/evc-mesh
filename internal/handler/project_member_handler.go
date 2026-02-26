package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ProjectMemberHandler handles HTTP requests for project member management.
type ProjectMemberHandler struct {
	svc service.ProjectMemberService
}

// NewProjectMemberHandler creates a new ProjectMemberHandler.
func NewProjectMemberHandler(svc service.ProjectMemberService) *ProjectMemberHandler {
	return &ProjectMemberHandler{svc: svc}
}

// addProjectMemberRequest represents the JSON body for adding a project member.
type addProjectMemberRequest struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// updateProjectMemberRoleRequest represents the JSON body for updating a project member's role.
type updateProjectMemberRoleRequest struct {
	Role string `json:"role"`
}

// List handles GET /projects/:proj_id/members
func (h *ProjectMemberHandler) List(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	members, err := h.svc.ListMembers(c.Request().Context(), projID)
	if err != nil {
		return handleError(c, err)
	}

	if members == nil {
		members = []domain.ProjectMemberWithUser{}
	}

	return c.JSON(http.StatusOK, map[string]any{
		"members": members,
		"count":   len(members),
	})
}

// Add handles POST /projects/:proj_id/members
func (h *ProjectMemberHandler) Add(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	var req addProjectMemberRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.UserID == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"user_id": "user_id is required",
		}))
	}

	userID, err := uuid.Parse(req.UserID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid user_id"))
	}

	member, err := h.svc.AddMember(c.Request().Context(), projID, userID, req.Role)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, member)
}

// UpdateRole handles PATCH /projects/:proj_id/members/:user_id
func (h *ProjectMemberHandler) UpdateRole(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	targetUserID, err := uuid.Parse(c.Param("user_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid user_id"))
	}

	var req updateProjectMemberRoleRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if err := h.svc.UpdateMemberRole(c.Request().Context(), projID, targetUserID, req.Role); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "updated"})
}

// Remove handles DELETE /projects/:proj_id/members/:user_id
func (h *ProjectMemberHandler) Remove(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	targetUserID, err := uuid.Parse(c.Param("user_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid user_id"))
	}

	if err := h.svc.RemoveMember(c.Request().Context(), projID, targetUserID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}
