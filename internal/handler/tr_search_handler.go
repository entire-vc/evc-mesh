package handler

import (
	"encoding/json"
	"net/http"
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
	piService     service.ProjectIntegrationService
	projectSvc    service.ProjectService
	relayResolver *service.TeamRelayIntegrationResolver
}

// NewTrSearchHandler creates a new TrSearchHandler. relayResolver answers
// "where does the relay live for this workspace" (specsintegration-provider-contract
// §4) — it replaces a raw environment-variable read that used to live inline
// here, which meant a typo in the variable's name only surfaced at request
// time. projectSvc resolves the project's workspace_id, which the resolver
// needs and this handler otherwise has no reason to look up.
func NewTrSearchHandler(piService service.ProjectIntegrationService, projectSvc service.ProjectService, relayResolver *service.TeamRelayIntegrationResolver) *TrSearchHandler {
	return &TrSearchHandler{piService: piService, projectSvc: projectSvc, relayResolver: relayResolver}
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

	project, err := h.projectSvc.GetByID(c.Request().Context(), projectID)
	if err != nil {
		return handleError(c, err)
	}
	relayURL, _, ok := h.relayResolver.ResolveRelayURL(c.Request().Context(), project.WorkspaceID)
	if !ok {
		return c.JSON(http.StatusServiceUnavailable, apierror.ServiceUnavailable("relay URL not configured: no active team_relay integration for this workspace and no MESH_TEAMRELAY_RELAY_URL fallback"))
	}

	docs, err := teamrelay.SearchDocs(c.Request().Context(), relayURL, settings.ShareSlug, pi.AgentKey, q, limit)
	if err != nil {
		return c.JSON(http.StatusBadGateway, apierror.InternalError("relay search failed"))
	}

	return c.JSON(http.StatusOK, map[string]any{"docs": docs})
}
