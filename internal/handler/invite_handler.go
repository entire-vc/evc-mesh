package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// InviteHandler handles HTTP requests for workspace invite management.
type InviteHandler struct {
	svc         service.WorkspaceInviteService
	authService *auth.Service
}

// NewInviteHandler creates a new InviteHandler. authService is only used to
// set the refresh cookie at the same TTL as every other login path — see
// Accept.
func NewInviteHandler(svc service.WorkspaceInviteService, authService *auth.Service) *InviteHandler {
	return &InviteHandler{svc: svc, authService: authService}
}

// createInviteRequest is the JSON body for POST /workspaces/:ws_id/invites.
type createInviteRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

// InviteDeliveryFields describes what became of the invitation email.
//
// A 201 on its own only ever meant "the invite row was written" — but with no
// delivery information in the body, every client read it as "the email is on its
// way", which is false whenever SMTP is unconfigured (the default for a
// self-hosted instance) or the send failed. These fields make the two outcomes
// distinguishable, and always carry the link so the inviter has a way to
// deliver the invitation themselves.
type InviteDeliveryFields struct {
	// EmailSent is true only when an invitation email actually went out.
	EmailSent bool `json:"email_sent"`
	// DeliveryStatus is "sent", "not_configured", or "failed".
	DeliveryStatus string `json:"delivery_status"`
	// InviteURL is the accept link. Always present.
	InviteURL string `json:"invite_url"`
	// DeliveryError is the send failure detail, present only on "failed".
	DeliveryError string `json:"delivery_error,omitempty"`
}

func deliveryFields(d service.InviteDelivery) InviteDeliveryFields {
	return InviteDeliveryFields{
		EmailSent:      d.Sent(),
		DeliveryStatus: d.Status,
		InviteURL:      d.URL,
		DeliveryError:  d.Error,
	}
}

// createInviteResponse embeds the invite so the existing object keeps its exact
// shape at the top level — the delivery fields are purely additive and no
// current client breaks.
type createInviteResponse struct {
	domain.WorkspaceInvite
	InviteDeliveryFields
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

	res, err := h.svc.CreateInvite(c.Request().Context(), service.CreateInviteInput{
		WorkspaceID: wsID,
		Email:       req.Email,
		Role:        req.Role,
		InvitedBy:   invitedBy,
	})
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, createInviteResponse{
		WorkspaceInvite:      *res.Invite,
		InviteDeliveryFields: deliveryFields(res.Delivery),
	})
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
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	inviteID, err := uuid.Parse(c.Param("invite_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid invite_id"))
	}

	// The old response was a hardcoded {"status":"sent"} that did not depend on
	// anything the send actually did.
	delivery, err := h.svc.ResendInvite(c.Request().Context(), wsID, inviteID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, deliveryFields(delivery))
}

// Revoke handles DELETE /workspaces/:ws_id/invites/:invite_id
func (h *InviteHandler) Revoke(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	inviteID, err := uuid.Parse(c.Param("invite_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid invite_id"))
	}

	if err := h.svc.RevokeInvite(c.Request().Context(), wsID, inviteID); err != nil {
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

	setRefreshCookie(c, refreshToken, h.authService.RefreshTokenTTL())

	return c.JSON(http.StatusOK, map[string]string{
		"access_token": accessToken,
	})
}
