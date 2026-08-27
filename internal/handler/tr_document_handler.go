package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/integration/teamrelay"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// TrDocumentHandler reads a Team Relay document's source so that Docs can render
// it with our own editor.
//
// This replaces the iframe. Embedding Team Relay's rendered page meant a fixed
// 256px viewport, a 6-second timeout, a "Preview not available" dead end, and a
// second HTML document with its own styling inside ours. Reading the source and
// rendering it ourselves means a Team Relay document looks and behaves like any
// other document in Docs, because by the time it reaches the page it IS one.
//
// The integration key stays on this side. It is never in a response body, never
// in a URL, and never reaches the browser — which is the whole reason this is a
// server endpoint rather than a fetch from the page.
type TrDocumentHandler struct {
	piService     service.ProjectIntegrationService
	projectSvc    service.ProjectService
	relayResolver *service.TeamRelayIntegrationResolver
}

// NewTrDocumentHandler creates a new TrDocumentHandler. relayResolver answers
// "where does the relay live for this workspace" (specsintegration-provider-contract
// §4) — it replaces a raw environment-variable read that used to live inline
// here. projectSvc resolves the project's workspace_id, which the resolver
// needs.
func NewTrDocumentHandler(piService service.ProjectIntegrationService, projectSvc service.ProjectService, relayResolver *service.TeamRelayIntegrationResolver) *TrDocumentHandler {
	return &TrDocumentHandler{piService: piService, projectSvc: projectSvc, relayResolver: relayResolver}
}

// Get handles GET /projects/:proj_id/tr/document?relay_url=<encoded>
//
// Answers {"available": false} rather than an error when this project has no
// Team Relay, when the link points at a different share, or when the link is a
// folder. Those are all ordinary states of a link somebody pasted into a task
// months ago, not failures, and the page degrades to an "Open in Team Relay"
// link for every one of them.
func (h *TrDocumentHandler) Get(c echo.Context) error {
	projectID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	rawRelayURL := c.QueryParam("relay_url")
	if rawRelayURL == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("relay_url required"))
	}
	if !strings.HasPrefix(rawRelayURL, "relay://") {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("relay_url must use relay:// scheme"))
	}

	slug, docPath := splitRelayURL(rawRelayURL)
	// `..` in a path handed to another service is a traversal attempt whichever
	// way that service resolves it. Refused here rather than forwarded.
	if strings.Contains(slug+"/"+docPath, "..") {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid relay_url path"))
	}
	if docPath == "" {
		// A link to the share root is a folder, and there is no document to open.
		return c.JSON(http.StatusOK, map[string]any{"available": false})
	}

	pi, err := h.piService.GetTeamRelay(c.Request().Context(), projectID)
	if err != nil {
		var apiErr *apierror.Error
		if errors.As(err, &apiErr) {
			return c.JSON(http.StatusOK, map[string]any{"available": false})
		}
		return handleError(c, err)
	}
	if !pi.Enabled || pi.AgentKey == "" {
		return c.JSON(http.StatusOK, map[string]any{"available": false})
	}

	var settings domain.TeamRelaySettings
	if jsonErr := json.Unmarshal(pi.Settings, &settings); jsonErr != nil || settings.ShareSlug == "" {
		return c.JSON(http.StatusOK, map[string]any{"available": false})
	}
	// A link to somebody else's share is not something this project's key may
	// read, and asking anyway would be using our credential to fetch content the
	// caller has no relationship with.
	if slug != settings.ShareSlug {
		return c.JSON(http.StatusOK, map[string]any{"available": false})
	}

	project, err := h.projectSvc.GetByID(c.Request().Context(), projectID)
	if err != nil {
		return handleError(c, err)
	}
	relayURL, _, ok := h.relayResolver.ResolveRelayURL(c.Request().Context(), project.WorkspaceID)
	if !ok {
		// The link itself is fine (right share, real path) — what's missing is
		// the relay's base URL for this whole workspace, which is not this
		// caller's problem to see as an error: the page still has "Open in Team
		// Relay" to fall back to. Named and logged so a misconfiguration doesn't
		// look, from the ops side, like a relay that is simply unreachable.
		log.Printf("[tr-document] relay URL not configured for workspace %s: no active team_relay integration and no MESH_TEAMRELAY_RELAY_URL fallback", project.WorkspaceID)
		return c.JSON(http.StatusOK, map[string]any{"available": false})
	}

	doc, err := teamrelay.FetchDocument(c.Request().Context(), relayURL, settings.ShareSlug, docPath, pi.AgentKey)
	if err != nil {
		// The relay was unreachable, or refused the key. Reported as unavailable
		// rather than as a 500: nothing on OUR side is broken, and the page has
		// somewhere to fall back to.
		return c.JSON(http.StatusOK, map[string]any{"available": false})
	}
	if doc == nil {
		return c.JSON(http.StatusOK, map[string]any{"available": false})
	}

	body, _ := teamrelay.StripFrontMatter(doc.Content)

	return c.JSON(http.StatusOK, map[string]any{
		"available": true,
		"path":      doc.Path,
		"name":      doc.Name,
		"content":   body,
	})
}

// splitRelayURL takes `relay://<slug>/<path...>` apart. A URL with no path
// yields an empty path, which the caller reads as "this points at a folder".
func splitRelayURL(relayURL string) (slug, docPath string) {
	without := strings.TrimPrefix(relayURL, "relay://")
	parts := strings.SplitN(without, "/", 2)
	slug = parts[0]
	if len(parts) == 2 {
		docPath = parts[1]
	}
	return slug, docPath
}
