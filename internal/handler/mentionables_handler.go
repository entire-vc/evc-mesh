package handler

import (
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// MentionablesHandler handles GET /workspaces/:ws_id/mentionables.
type MentionablesHandler struct {
	svc service.MentionablesService
}

// NewMentionablesHandler creates a new MentionablesHandler.
func NewMentionablesHandler(svc service.MentionablesService) *MentionablesHandler {
	return &MentionablesHandler{svc: svc}
}

// Search handles GET /workspaces/:ws_id/mentionables?q=<prefix>&limit=20
func (h *MentionablesHandler) Search(c echo.Context) error {
	actorID, _ := actorctx.FromContext(c.Request().Context())
	if actorID == uuid.Nil {
		return apierror.Unauthorized("authentication required")
	}

	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return apierror.ValidationError(map[string]string{"ws_id": "must be a UUID"})
	}

	q := c.QueryParam("q")
	limit := 20
	if v := c.QueryParam("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 50 {
			return apierror.ValidationError(map[string]string{"limit": "must be 1-50"})
		}
		limit = n
	}

	results, err := h.svc.Search(c.Request().Context(), wsID, q, limit)
	if err != nil {
		return err
	}

	c.Response().Header().Set("Cache-Control", "private, max-age=30")
	return c.JSON(http.StatusOK, results)
}
