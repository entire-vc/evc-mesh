package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// AgentKeyHandler serves the agent key API used by Keep's rotator: issue a new
// key next to the current one, revoke the old one afterwards.
type AgentKeyHandler struct {
	keys service.AgentKeyService
}

// NewAgentKeyHandler creates a new AgentKeyHandler.
func NewAgentKeyHandler(keys service.AgentKeyService) *AgentKeyHandler {
	return &AgentKeyHandler{keys: keys}
}

// keyCtx is the request context, confined to the token's workspace for an
// extension actor (its capability says "rotate keys", not "of any workspace").
func keyCtx(c echo.Context) context.Context {
	ctx := c.Request().Context()
	if p := mw.GetExtensionPrincipal(c); p != nil {
		ctx = service.WithAgentKeyScope(ctx, p.WorkspaceID)
	}
	return ctx
}

type issueAgentKeyRequest struct {
	Label string `json:"label"`
	// ExpiresInSeconds optionally bounds the key; 0 or absent means no expiry.
	ExpiresInSeconds int64 `json:"expires_in_seconds"`
	// OperatorUserID names the user who runs the agent; absent means the issuer.
	OperatorUserID *uuid.UUID `json:"operator_user_id"`
}

// Issue handles POST /agents/:agent_id/keys. The raw key is in the response once
// and is never logged or stored; the response is marked no-store.
func (h *AgentKeyHandler) Issue(c echo.Context) error {
	agentID, err := uuid.Parse(c.Param("agent_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid agent_id"))
	}
	var req issueAgentKeyRequest
	if c.Request().ContentLength != 0 {
		if err = c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
		}
	}
	if req.ExpiresInSeconds < 0 {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("expires_in_seconds must not be negative"))
	}
	ctx := keyCtx(c)
	if req.OperatorUserID != nil {
		ctx = service.WithOperatorUser(ctx, *req.OperatorUserID)
	}
	issued, err := h.keys.Issue(ctx, agentID, req.Label, time.Duration(req.ExpiresInSeconds)*time.Second)
	if err != nil {
		return handleError(c, err)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusCreated, issued)
}

// Revoke handles DELETE /agents/:agent_id/keys/:key_id; key_id "primary" is the
// agent's original key.
func (h *AgentKeyHandler) Revoke(c echo.Context) error {
	agentID, err := uuid.Parse(c.Param("agent_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid agent_id"))
	}
	if err = h.keys.Revoke(keyCtx(c), agentID, c.Param("key_id")); err != nil {
		return handleError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// List handles GET /agents/:agent_id/keys: ids and dates, never a key.
func (h *AgentKeyHandler) List(c echo.Context) error {
	agentID, err := uuid.Parse(c.Param("agent_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid agent_id"))
	}
	items, err := h.keys.List(keyCtx(c), agentID)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"keys": items})
}
