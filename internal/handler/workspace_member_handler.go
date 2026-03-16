package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// WorkspaceMemberHandler handles HTTP requests for workspace member management.
type WorkspaceMemberHandler struct {
	svc service.WorkspaceMemberService
}

// NewWorkspaceMemberHandler creates a new WorkspaceMemberHandler.
func NewWorkspaceMemberHandler(svc service.WorkspaceMemberService) *WorkspaceMemberHandler {
	return &WorkspaceMemberHandler{svc: svc}
}

// addMemberRequest represents the JSON body for adding a member.
type addMemberRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

// updateMemberRoleRequest represents the JSON body for updating a member's role.
type updateMemberRoleRequest struct {
	Role string `json:"role"`
}

// List handles GET /workspaces/:ws_id/members
func (h *WorkspaceMemberHandler) List(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	members, err := h.svc.ListMembers(c.Request().Context(), wsID)
	if err != nil {
		return handleError(c, err)
	}

	if members == nil {
		members = []domain.WorkspaceMemberWithUser{}
	}

	return c.JSON(http.StatusOK, map[string]any{
		"members": members,
		"count":   len(members),
	})
}

// Me handles GET /workspaces/:ws_id/members/me
func (h *WorkspaceMemberHandler) Me(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	userIDVal := c.Get("user_id")
	if userIDVal == nil {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("user_id not found in context"))
	}
	userID, ok := userIDVal.(uuid.UUID)
	if !ok {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid user_id in context"))
	}

	role, err := h.svc.GetMyRole(c.Request().Context(), wsID, userID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]string{
		"workspace_id": wsID.String(),
		"user_id":      userID.String(),
		"role":         role,
	})
}

// Add handles POST /workspaces/:ws_id/members
func (h *WorkspaceMemberHandler) Add(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	var req addMemberRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	// Resolve the inviter's identity.
	var invitedBy uuid.UUID
	if userIDVal := c.Get("user_id"); userIDVal != nil {
		if uid, ok := userIDVal.(uuid.UUID); ok {
			invitedBy = uid
		}
	}

	member, err := h.svc.AddMember(c.Request().Context(), wsID, req.Email, req.Role, invitedBy)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, member)
}

// UpdateRole handles PATCH /workspaces/:ws_id/members/:user_id
func (h *WorkspaceMemberHandler) UpdateRole(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	targetUserID, err := uuid.Parse(c.Param("user_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid user_id"))
	}

	var req updateMemberRoleRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if err := h.svc.UpdateMemberRole(c.Request().Context(), wsID, targetUserID, req.Role); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "updated"})
}

// Remove handles DELETE /workspaces/:ws_id/members/:user_id
func (h *WorkspaceMemberHandler) Remove(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	targetUserID, err := uuid.Parse(c.Param("user_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid user_id"))
	}

	if err := h.svc.RemoveMember(c.Request().Context(), wsID, targetUserID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// SearchUsers handles GET /workspaces/:ws_id/users/search?q=...
func (h *WorkspaceMemberHandler) SearchUsers(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	query := c.QueryParam("q")

	users, err := h.svc.SearchUsers(c.Request().Context(), wsID, query)
	if err != nil {
		return handleError(c, err)
	}

	if users == nil {
		users = []domain.UserWithMemberStatus{}
	}

	return c.JSON(http.StatusOK, map[string]any{
		"users": users,
		"count": len(users),
	})
}
