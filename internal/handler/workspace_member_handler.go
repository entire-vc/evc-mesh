package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// WorkspaceMemberHandler handles HTTP requests for workspace member management.
type WorkspaceMemberHandler struct {
	svc service.WorkspaceMemberService
	// loginLockout, when set, is cleared for the member's address after an
	// admin sets their password: the failures recorded against the old one
	// must not lock the person out of the new one.
	loginLockout loginLockoutResetter
}

// loginLockoutResetter is the part of mw.LoginLockout this handler needs.
type loginLockoutResetter interface {
	Reset(ctx context.Context, identifier string) error
}

// NewWorkspaceMemberHandler creates a new WorkspaceMemberHandler.
func NewWorkspaceMemberHandler(svc service.WorkspaceMemberService) *WorkspaceMemberHandler {
	return &WorkspaceMemberHandler{svc: svc}
}

// WithLoginLockout wires the per-account login lockout that a password reset
// clears. Optional; returns the handler for chaining.
func (h *WorkspaceMemberHandler) WithLoginLockout(l loginLockoutResetter) *WorkspaceMemberHandler {
	h.loginLockout = l
	return h
}

// addMemberRequest represents the JSON body for adding a member.
// When Password is provided and the email is not yet registered, a new user account is created.
// Name is the display name for such a newly created account and is ignored when
// the address already belongs to somebody — an existing account's name is not
// the inviter's to set here (see PATCH .../members/:user_id for that path).
type addMemberRequest struct {
	Email    string `json:"email"`
	Name     string `json:"name,omitempty"`
	Role     string `json:"role"`
	Password string `json:"password,omitempty"`
}

// updateMemberRequest represents the JSON body for updating a member.
// Every field is optional: role changes the role, name fills in a display name
// that was never chosen, and email / username / password change the member's
// sign-in identity (see service.WorkspaceMemberService.SetMemberEmail for who
// may). Sending nothing is a no-op, not an error.
type updateMemberRequest struct {
	Role     string  `json:"role"`
	Name     *string `json:"name"`
	Email    *string `json:"email"`
	Username *string `json:"username"`
	Password *string `json:"password"`
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

	member, err := h.svc.AddMemberWithCreate(c.Request().Context(), wsID, req.Email, req.Name, req.Role, req.Password, invitedBy)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, member)
}

// UpdateRole handles PATCH /workspaces/:ws_id/members/:user_id
//
// It responds with the updated member rather than {"status":"updated"}. The web
// client already assumed that — it splices the response straight into its member
// list — so every role change replaced a member row with a status envelope and
// the name, address and role rendered blank until the next reload.
func (h *WorkspaceMemberHandler) UpdateRole(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	targetUserID, err := uuid.Parse(c.Param("user_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid user_id"))
	}

	var req updateMemberRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	ctx := c.Request().Context()
	if req.Role != "" {
		if err = h.svc.UpdateMemberRole(ctx, wsID, targetUserID, req.Role); err != nil {
			return handleError(c, err)
		}
	}
	if req.Name != nil {
		if err = h.svc.SetMemberDisplayName(ctx, wsID, targetUserID, *req.Name); err != nil {
			return handleError(c, err)
		}
	}
	if req.Username != nil {
		if err = h.svc.SetMemberUsername(ctx, wsID, targetUserID, *req.Username); err != nil {
			return handleError(c, err)
		}
	}
	if req.Email != nil {
		if err = h.svc.SetMemberEmail(ctx, wsID, targetUserID, *req.Email); err != nil {
			return handleError(c, err)
		}
	}
	// Password last: it is the one change that ends the member's sessions, so
	// it should not happen if an earlier field in the same request was refused.
	if req.Password != nil {
		if err = h.svc.SetMemberPassword(ctx, wsID, targetUserID, *req.Password); err != nil {
			// The new password is stored even though the sessions were not
			// ended: the lockout earned by the old one must still go.
			if errors.Is(err, service.ErrSessionsNotRevoked) {
				if member, getErr := h.svc.GetMember(ctx, wsID, targetUserID); getErr == nil && member != nil {
					h.resetLoginLockout(c, member)
				}
			}
			return handleError(c, err)
		}
	}

	member, err := h.svc.GetMember(ctx, wsID, targetUserID)
	if err != nil {
		return handleError(c, err)
	}
	if member == nil {
		return c.JSON(http.StatusNotFound, apierror.NotFound("WorkspaceMember"))
	}
	if req.Password != nil {
		h.resetLoginLockout(c, member)
	}
	return c.JSON(http.StatusOK, member)
}

// resetLoginLockout clears the failed-login counter for the member's address
// after their password was set; a failure is logged, the change stands.
func (h *WorkspaceMemberHandler) resetLoginLockout(c echo.Context, member *domain.WorkspaceMemberWithUser) {
	if h.loginLockout == nil {
		return
	}
	if err := h.loginLockout.Reset(c.Request().Context(), auth.NormalizeEmail(member.User.Email)); err != nil {
		c.Logger().Errorf("login lockout: reset after admin password change: %v", err)
	}
}

// UpdateProfile handles PUT /workspaces/:ws_id/members/:user_id/profile.
//
// Like PUT /agents/:agent_id/profile this is a partial update (see
// domain.MemberProfileUpdate). A person may always edit their own profile;
// editing someone else's needs PermManageMembers (RequireSelfUserOrPermission,
// wired at the route in cmd/api/main.go).
func (h *WorkspaceMemberHandler) UpdateProfile(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	targetUserID, err := uuid.Parse(c.Param("user_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid user_id"))
	}

	var upd domain.MemberProfileUpdate
	if err = c.Bind(&upd); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if err := h.svc.UpdateMemberProfile(c.Request().Context(), wsID, targetUserID, upd); err != nil {
		return handleError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
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

	// The acting user, not the workspace, is what bounds this search: it exists
	// to surface people who are NOT in this workspace yet, so the visibility
	// boundary has to be "workspaces the caller is in". An agent key names a
	// workspace rather than a person, leaving uuid.Nil, which narrows the search
	// to exact-address matches only.
	var callerID uuid.UUID
	if v := c.Get("user_id"); v != nil {
		if uid, ok := v.(uuid.UUID); ok {
			callerID = uid
		}
	}

	query := c.QueryParam("q")

	users, err := h.svc.SearchUsers(c.Request().Context(), wsID, callerID, query)
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
