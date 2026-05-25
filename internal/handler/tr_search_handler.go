package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/integration/teamrelay"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// TrSearchHandler handles TR document search for a project.
type TrSearchHandler struct {
	piService service.ProjectIntegrationService
}

// NewTrSearchHandler creates a new TrSearchHandler.
func NewTrSearchHandler(piService service.ProjectIntegrationService) *TrSearchHandler {
	return &TrSearchHandler{piService: piService}
}

// Search handles GET /projects/:proj_id/tr/search?q=&limit=
func (h *TrSearchHandler) Search(c echo.Context) error {
	projectID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	q := c.QueryParam("q")
	limit := 20
	if ls := c.QueryParam("limit"); ls != "" {
		if n, parseErr := strconv.Atoi(ls); parseErr == nil && n > 0 && n <= 100 {
			limit = n
		}
	}

	pi, err := h.piService.GetTeamRelay(c.Request().Context(), projectID)
	if err != nil {
		return handleError(c, err)
	}
	if !pi.Enabled {
		return c.JSON(http.StatusNotFound, apierror.NotFound("TeamRelayIntegration"))
	}

	var settings domain.TeamRelaySettings
	if jsonErr := json.Unmarshal(pi.Settings, &settings); jsonErr != nil || settings.ShareSlug == "" {
		return c.JSON(http.StatusUnprocessableEntity, apierror.BadRequest("TR integration not configured: missing share_slug"))
	}

	relayURL := os.Getenv("MESH_TEAMRELAY_RELAY_URL")
	if relayURL == "" {
		return c.JSON(http.StatusServiceUnavailable, apierror.ServiceUnavailable("relay URL not configured"))
	}

	docs, err := teamrelay.SearchDocs(c.Request().Context(), relayURL, settings.ShareSlug, pi.AgentKey, q, limit)
	if err != nil {
		return c.JSON(http.StatusBadGateway, apierror.InternalError("relay search failed"))
	}

	return c.JSON(http.StatusOK, map[string]any{"docs": docs})
}
