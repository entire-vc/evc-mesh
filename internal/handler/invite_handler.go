package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// InviteHandler handles HTTP requests for workspace invite management.
type InviteHandler struct {
	svc service.WorkspaceInviteService
}

// NewInviteHandler creates a new InviteHandler.
func NewInviteHandler(svc service.WorkspaceInviteService) *InviteHandler {
	return &InviteHandler{svc: svc}
}

// createInviteRequest is the JSON body for POST /workspaces/:ws_id/invites.
type createInviteRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

// Create handles POST /workspaces/:ws_id/invites
func (h *InviteHandler) Create(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	var req createInviteRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	var invitedBy uuid.UUID
	if userIDVal := c.Get("user_id"); userIDVal != nil {
		if uid, ok := userIDVal.(uuid.UUID); ok {
			invitedBy = uid
		}
	}

	invite, err := h.svc.CreateInvite(c.Request().Context(), service.CreateInviteInput{
		WorkspaceID: wsID,
		Email:       req.Email,
		Role:        req.Role,
		InvitedBy:   invitedBy,
	})
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, invite)
}

// List handles GET /workspaces/:ws_id/invites
func (h *InviteHandler) List(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	invites, err := h.svc.ListInvites(c.Request().Context(), wsID)
	if err != nil {
		return handleError(c, err)
	}
	if invites == nil {
		invites = []domain.WorkspaceInvite{}
	}

	return c.JSON(http.StatusOK, map[string]any{
		"invites": invites,
		"count":   len(invites),
	})
}

// Resend handles POST /workspaces/:ws_id/invites/:invite_id/resend
func (h *InviteHandler) Resend(c echo.Context) error {
	inviteID, err := uuid.Parse(c.Param("invite_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid invite_id"))
	}

	if err := h.svc.ResendInvite(c.Request().Context(), inviteID); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "sent"})
}

// Revoke handles DELETE /workspaces/:ws_id/invites/:invite_id
func (h *InviteHandler) Revoke(c echo.Context) error {
	inviteID, err := uuid.Parse(c.Param("invite_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid invite_id"))
	}

	if err := h.svc.RevokeInvite(c.Request().Context(), inviteID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// GetByToken handles GET /invites/:token (public — no auth required)
func (h *InviteHandler) GetByToken(c echo.Context) error {
	token := c.Param("token")
	if token == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("token is required"))
	}

	invite, err := h.svc.GetByToken(c.Request().Context(), token)
	if err != nil {
		return handleError(c, err)
	}
	if invite == nil {
		return c.JSON(http.StatusNotFound, apierror.NotFound("WorkspaceInvite"))
	}

	return c.JSON(http.StatusOK, map[string]any{
		"id":           invite.ID,
		"workspace_id": invite.WorkspaceID,
		"email":        invite.Email,
		"role":         invite.Role,
		"expires_at":   invite.ExpiresAt,
	})
}

// acceptInviteRequest is the JSON body for POST /invites/:token/accept (public).
type acceptInviteRequest struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

// Accept handles POST /invites/:token/accept (public — no auth required)
func (h *InviteHandler) Accept(c echo.Context) error {
	token := c.Param("token")
	if token == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("token is required"))
	}

	var req acceptInviteRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	accessToken, refreshToken, err := h.svc.AcceptInvite(c.Request().Context(), service.AcceptInviteInput{
		Token:    token,
		Name:     req.Name,
		Password: req.Password,
	})
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]string{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
	})
}
