package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/extensions/external"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ExtensionActorHandler serves the extension actor's own endpoint and the
// admin's token rotation.
type ExtensionActorHandler struct {
	actors service.ExtensionActorService
	tokens ExtensionTokenDeliverer
}

// ExtensionTokenDeliverer rotates and delivers the token of a connection whose
// extension takes it from Mesh (external.token_path); handled is false for
// any other connection.
type ExtensionTokenDeliverer interface {
	RotateAndDeliver(ctx context.Context, ws, connID uuid.UUID) (res external.Delivered, handled bool, err error)
}

// WithTokenDelivery makes RotateToken deliver the new token to the service of
// an extension with token_path instead of returning it.
func (h *ExtensionActorHandler) WithTokenDelivery(d ExtensionTokenDeliverer) *ExtensionActorHandler {
	h.tokens = d
	return h
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
// in the response once; the previous one keeps working for five minutes. For an
// extension with token_path Mesh posts the new token to the service and the
// response carries no token: delivered=true, or 502 with the previous token
// working exactly as before the call.
func (h *ExtensionActorHandler) RotateToken(c echo.Context) error {
	connID, err := uuid.Parse(c.Param("int_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid integration_id"))
	}
	wsID, err := middleware.GetWorkspaceID(c)
	if err != nil {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace context required"))
	}
	if h.tokens != nil {
		res, handled, derr := h.tokens.RotateAndDeliver(c.Request().Context(), wsID, connID)
		if handled {
			return h.delivered(c, connID, res, derr)
		}
		if derr != nil {
			return handleError(c, derr)
		}
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

// delivered answers a rotate-token that went to the extension's service.
func (h *ExtensionActorHandler) delivered(c echo.Context, connID uuid.UUID, res external.Delivered, err error) error {
	var de *external.DeliveryError
	if errors.As(err, &de) {
		// The message names only the status and the host.
		slog.Warn("extension token delivery failed", "connection_id", connID, "error", de.Error())
		return c.JSON(http.StatusBadGateway, &apierror.Error{Code: http.StatusBadGateway, Message: de.Error()})
	}
	if err != nil {
		return handleError(c, err)
	}
	row, err := h.actors.Get(c.Request().Context(), connID)
	if err != nil || row == nil {
		return handleError(c, apierror.InternalError("extension actor is not readable after delivery"))
	}
	grace := 0
	if res.Rotated {
		grace = int(service.ExtensionTokenRotationGrace.Seconds())
	}
	slog.Info("extension token delivered to the service", "actor", row.Principal(), "connection_id", connID)
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, map[string]any{
		"actor":              row.Principal(),
		"previous_valid_for": grace,
		"delivered":          true,
	})
}

// SelfRotate handles POST /ext/me/token/rotate: the calling extension actor
// issues a new ext_ token for itself. The value is in this response once; the
// presented token keeps working for grace_seconds (default 300, max 3600).
func (h *ExtensionActorHandler) SelfRotate(c echo.Context) error {
	p := middleware.GetExtensionPrincipal(c)
	presented, ok := middleware.ExtensionBearerToken(c)
	if p == nil || !ok {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("only an extension actor can call this endpoint"))
	}
	var body struct {
		GraceSeconds int `json:"grace_seconds"`
	}
	if c.Request().ContentLength != 0 {
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid body"))
		}
	}
	// Range-check the integer BEFORE it becomes a Duration: a huge value would
	// overflow the multiplication to a non-positive duration and fall back to the
	// default instead of being refused.
	if body.GraceSeconds < 0 || body.GraceSeconds > int(service.ExtensionSelfRotateMaxGrace/time.Second) {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("grace_seconds must be between 0 and 3600"))
	}
	issued, err := h.actors.SelfRotate(c.Request().Context(), p, presented, time.Duration(body.GraceSeconds)*time.Second)
	if err != nil {
		return handleError(c, err)
	}
	// Only non-secret facts are logged: never the token itself.
	slog.Info("extension actor rotated its own token", "actor", p.Principal(), "connection_id", p.ConnectionID, "token_prefix", tokenPrefix(issued.Token))
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, map[string]any{
		"extension_token":      issued.Token,
		"token_prefix":         tokenPrefix(issued.Token),
		"token_length":         len(issued.Token),
		"actor":                issued.Actor,
		"actor_id":             issued.ActorID,
		"connection_id":        p.ConnectionID,
		"workspace_id":         p.WorkspaceID,
		"previous_valid_for":   int(issued.Grace.Seconds()),
		"previous_valid_until": issued.IssuedAt.Add(issued.Grace).UTC().Format(time.RFC3339),
	})
}

// SelfRevokePrevious handles DELETE /ext/me/token/previous: ends the transition
// window early. Only the current token may call it; a repeat is 204 as well.
func (h *ExtensionActorHandler) SelfRevokePrevious(c echo.Context) error {
	p := middleware.GetExtensionPrincipal(c)
	presented, ok := middleware.ExtensionBearerToken(c)
	if p == nil || !ok {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("only an extension actor can call this endpoint"))
	}
	if err := h.actors.SelfRevokePrevious(c.Request().Context(), p, presented); err != nil {
		return handleError(c, err)
	}
	slog.Info("extension actor revoked its previous token", "actor", p.Principal(), "connection_id", p.ConnectionID)
	return c.NoContent(http.StatusNoContent)
}

// tokenPrefix is the non-secret head of a token (prefix + 4 random chars).
func tokenPrefix(token string) string {
	if len(token) <= 8 {
		return ""
	}
	return token[:8]
}
