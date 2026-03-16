package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// TriageHandler handles HTTP requests for the triage inbox.
type TriageHandler struct {
	triageService service.TriageService
}

// NewTriageHandler creates a new TriageHandler.
func NewTriageHandler(svc service.TriageService) *TriageHandler {
	return &TriageHandler{triageService: svc}
}

// List handles GET /workspaces/:ws_id/triage
// Returns tasks across all workspace projects that are in a "triage" status category.
func (h *TriageHandler) List(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	var pg pagination.Params
	if err = c.Bind(&pg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination parameters"))
	}
	pg.Normalize()

	page, err := h.triageService.ListTriageTasks(c.Request().Context(), wsID, pg)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, page)
}
