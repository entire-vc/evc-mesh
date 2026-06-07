package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// TrPreviewURLHandler resolves authenticated iframe URLs for relay:// previews.
type TrPreviewURLHandler struct {
	piService service.ProjectIntegrationService
}

func NewTrPreviewURLHandler(piService service.ProjectIntegrationService) *TrPreviewURLHandler {
	return &TrPreviewURLHandler{piService: piService}
}

// Get handles GET /projects/:proj_id/tr/preview-url?relay_url=<encoded>
//
// Returns { available: false } when TR is disabled or the slug doesn't match —
// the frontend falls back to public scheme-substitution in those cases.
func (h *TrPreviewURLHandler) Get(c echo.Context) error {
	projectID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	rawRelayURL := c.QueryParam("relay_url")
	if rawRelayURL == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("relay_url required"))
	}

	pi, err := h.piService.GetTeamRelay(c.Request().Context(), projectID)
	if err != nil {
		return handleError(c, err)
	}
	if !pi.Enabled || pi.AgentKey == "" {
		return c.JSON(http.StatusOK, map[string]any{"available": false})
	}

	var settings domain.TeamRelaySettings
	if jsonErr := json.Unmarshal(pi.Settings, &settings); jsonErr != nil || settings.ShareSlug == "" {
		return c.JSON(http.StatusOK, map[string]any{"available": false})
	}

	if !strings.HasPrefix(rawRelayURL, "relay://") {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("relay_url must use relay:// scheme"))
	}
	without := strings.TrimPrefix(rawRelayURL, "relay://")
	parts := strings.SplitN(without, "/", 2)
	slug := parts[0]
	path := ""
	if len(parts) == 2 {
		path = parts[1]
	}

	if slug != settings.ShareSlug {
		return c.JSON(http.StatusOK, map[string]any{"available": false})
	}

	webBase := os.Getenv("MESH_TEAMRELAY_WEB_BASE_URL")
	if webBase == "" {
		webBase = os.Getenv("MESH_TEAMRELAY_RELAY_URL")
	}
	if webBase == "" {
		return c.JSON(http.StatusServiceUnavailable, apierror.ServiceUnavailable("relay URL not configured"))
	}
	webBase = strings.TrimRight(webBase, "/")

	iframeSrc := fmt.Sprintf("%s/%s/%s?agent_key=%s",
		webBase, slug, path, url.QueryEscape(pi.AgentKey))

	return c.JSON(http.StatusOK, map[string]any{
		"available":  true,
		"iframe_src": iframeSrc,
	})
}
