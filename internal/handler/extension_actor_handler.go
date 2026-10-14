package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ExtensionActorHandler serves the extension actor's own endpoint and the
// admin's token rotation.
type ExtensionActorHandler struct {
	actors service.ExtensionActorService
}

// NewExtensionActorHandler creates a new ExtensionActorHandler.
func NewExtensionActorHandler(actors service.ExtensionActorService) *ExtensionActorHandler {
	return &ExtensionActorHandler{actors: actors}
}

// Me handles GET /ext/me: who the calling extension actor is and what it may do.
// Only an extension token reaches it; any other principal gets 403.
func (h *ExtensionActorHandler) Me(c echo.Context) error {
	p := middleware.GetExtensionPrincipal(c)
	if p == nil {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("only an extension actor can call this endpoint"))
	}
	resp := map[string]any{
		"actor":         p.Principal(),
		"actor_id":      p.ActorID,
		"extension_id":  p.ExtensionID,
		"connection_id": p.ConnectionID,
		"workspace_id":  p.WorkspaceID,
		"capabilities":  p.Capabilities,
	}
	if row, err := h.actors.Get(c.Request().Context(), p.ConnectionID); err == nil && row != nil {
		resp["violations"] = map[string]any{
			"count":   row.ViolationCount,
			"last_at": row.LastViolationAt,
			"last":    row.LastViolation,
		}
	}
	return c.JSON(http.StatusOK, resp)
}

// RotateToken handles POST /integrations/:int_id/rotate-token. The new token is
// in the response once; the previous one keeps working for five minutes.
func (h *ExtensionActorHandler) RotateToken(c echo.Context) error {
	connID, err := uuid.Parse(c.Param("int_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid integration_id"))
	}
	wsID, err := middleware.GetWorkspaceID(c)
	if err != nil {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace context required"))
	}
	issued, err := h.actors.Rotate(c.Request().Context(), wsID, connID)
	if err != nil {
		return handleError(c, err)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, map[string]any{
		"extension_token":    issued.Token,
		"actor":              issued.Actor,
		"previous_valid_for": int(issued.Grace.Seconds()),
	})
}
