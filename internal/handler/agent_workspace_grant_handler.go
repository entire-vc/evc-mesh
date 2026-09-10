package handler

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// AgentWorkspaceGrantHandler handles HTTP requests for connecting an agent to
// a workspace other than its home one (task U3).
type AgentWorkspaceGrantHandler struct {
	svc service.AgentWorkspaceGrantService
}

// NewAgentWorkspaceGrantHandler creates a new AgentWorkspaceGrantHandler.
func NewAgentWorkspaceGrantHandler(svc service.AgentWorkspaceGrantService) *AgentWorkspaceGrantHandler {
	return &AgentWorkspaceGrantHandler{svc: svc}
}

// inviteAgentGrantRequest is the JSON body for POST .../agent-grants.
type inviteAgentGrantRequest struct {
	AgentID uuid.UUID `json:"agent_id"`
	Role    string    `json:"role"`
}

// inviteAgentGrantResponse is deliberately NOT domain.AgentWorkspaceGrant
// serialized as-is — every field is named explicitly here, APIKey included,
// so this is the ONLY response shape in this handler that can ever carry the
// raw key. Every other response in this file returns a domain type or a slice
// of one, none of which have an APIKey field to begin with.
type inviteAgentGrantResponse struct {
	ID           uuid.UUID  `json:"id"`
	AgentID      uuid.UUID  `json:"agent_id"`
	WorkspaceID  uuid.UUID  `json:"workspace_id"`
	Role         string     `json:"role"`
	APIKeyPrefix string     `json:"api_key_prefix"`
	APIKey       string     `json:"api_key"`
	InvitedBy    *uuid.UUID `json:"invited_by,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// Invite handles POST /workspaces/:ws_id/agent-grants
//
// Status code carries the AC6 branch: 201 for a brand-new connection, 200 for
// a reactivated one (same row, fresh key) — the body shape is identical
// either way, only the code differs, so a caller that doesn't care can ignore
// it.
func (h *AgentWorkspaceGrantHandler) Invite(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	var req inviteAgentGrantRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	if req.AgentID == uuid.Nil {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"agent_id": "agent_id is required",
		}))
	}

	var invitedBy uuid.UUID
	if userIDVal := c.Get("user_id"); userIDVal != nil {
		if uid, ok := userIDVal.(uuid.UUID); ok {
			invitedBy = uid
		}
	}

	result, err := h.svc.InviteAgent(c.Request().Context(), wsID, req.AgentID, req.Role, invitedBy)
	if err != nil {
		return handleError(c, err)
	}

	resp := inviteAgentGrantResponse{
		ID:           result.Grant.ID,
		AgentID:      result.Grant.AgentID,
		WorkspaceID:  result.Grant.WorkspaceID,
		Role:         result.Grant.Role,
		APIKeyPrefix: result.Grant.APIKeyPrefix,
		APIKey:       result.APIKey,
		InvitedBy:    result.Grant.InvitedBy,
		CreatedAt:    result.Grant.CreatedAt,
	}

	status := http.StatusCreated
	if result.Reactivated {
		status = http.StatusOK
	}
	return c.JSON(status, resp)
}

// Revoke handles DELETE /workspaces/:ws_id/agent-grants/:grant_id
func (h *AgentWorkspaceGrantHandler) Revoke(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}
	grantID, err := uuid.Parse(c.Param("grant_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid grant_id"))
	}

	if err := h.svc.RevokeGrant(c.Request().Context(), wsID, grantID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// List handles GET /workspaces/:ws_id/agent-grants
func (h *AgentWorkspaceGrantHandler) List(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	grants, err := h.svc.ListWorkspaceAgents(c.Request().Context(), wsID)
	if err != nil {
		return handleError(c, err)
	}
	if grants == nil {
		grants = []domain.AgentWorkspaceGrantWithAgent{}
	}

	return c.JSON(http.StatusOK, map[string]any{
		"agent_grants": grants,
		"count":        len(grants),
	})
}

// ListAgentWorkspaces handles GET /agents/:agent_id/workspaces
func (h *AgentWorkspaceGrantHandler) ListAgentWorkspaces(c echo.Context) error {
	agentID, err := uuid.Parse(c.Param("agent_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid agent_id"))
	}

	grants, err := h.svc.ListAgentWorkspaces(c.Request().Context(), agentID)
	if err != nil {
		return handleError(c, err)
	}
	if grants == nil {
		grants = []domain.AgentWorkspaceGrantWithWorkspace{}
	}

	return c.JSON(http.StatusOK, map[string]any{
		"workspaces": grants,
		"count":      len(grants),
	})
}
