package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// TrPreviewURLHandler resolves authenticated iframe URLs for relay:// previews.
type TrPreviewURLHandler struct {
	piService  service.ProjectIntegrationService
	httpClient *http.Client
}

func NewTrPreviewURLHandler(piService service.ProjectIntegrationService) *TrPreviewURLHandler {
	return &TrPreviewURLHandler{
		piService:  piService,
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

// Get handles GET /projects/:proj_id/tr/preview-url?relay_url=<encoded>
//
// Returns { available: false } when TR is disabled or the slug doesn't match —
// the frontend falls back to public scheme-substitution in those cases.
//
// The returned iframe_src always uses a short-lived signed token from
// web-publish's /api/embed-token (D5-b). If that token cannot be obtained for any
// reason, the response is { available: false } — the long-lived agent key is
// never placed in a URL.
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

	if !strings.HasPrefix(rawRelayURL, "relay://") {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("relay_url must use relay:// scheme"))
	}
	without := strings.TrimPrefix(rawRelayURL, "relay://")
	parts := strings.SplitN(without, "/", 2)
	slug := parts[0]
	docPath := ""
	if len(parts) == 2 {
		docPath = parts[1]
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

	// Validate the constructed path before any URL building
	rawPath := slug + "/" + docPath
	if strings.Contains(rawPath, "..") {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid relay_url path"))
	}

	// Request a short-lived embed token from web-publish (D5-b).
	//
	// There is deliberately no fallback here. This path previously answered a
	// token failure by putting pi.AgentKey — the long-lived, non-rotating
	// TeamRelay integration key we encrypt at rest — into the iframe URL's query
	// string, where it reaches browser history, referrers, and any proxy log on
	// the TeamRelay side. That triggered on ordinary transient failures (network,
	// the 5s timeout, web-publish down), so a blip silently downgraded a
	// short-lived token to a permanent credential. Reporting the preview as
	// unavailable is the honest answer; the frontend already degrades to an
	// "Open in Team Relay" link.
	iframeSrc, tokenErr := h.fetchEmbedToken(c.Request().Context(), webBase, slug, docPath, pi.AgentKey)
	if tokenErr != nil {
		return c.JSON(http.StatusOK, map[string]any{"available": false})
	}

	return c.JSON(http.StatusOK, map[string]any{
		"available":  true,
		"iframe_src": iframeSrc,
	})
}

type embedTokenResponse struct {
	EmbedURL  string `json:"embed_url"`
	ExpiresAt string `json:"expires_at"`
}

// fetchEmbedToken calls the web-publish /api/embed-token endpoint to get a
// short-lived signed URL. Returns an error if the endpoint is unavailable or
// the key is rejected, so the caller can fall back gracefully.
func (h *TrPreviewURLHandler) fetchEmbedToken(ctx context.Context, webBase, slug, docPath, agentKey string) (string, error) {
	tokenURL := webBase + "/api/embed-token"
	q := url.Values{}
	q.Set("slug", slug)
	q.Set("path", docPath)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL+"?"+q.Encode(), http.NoBody)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+agentKey)

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", errors.New("embed-token endpoint returned non-200")
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}

	var result embedTokenResponse
	if err := json.Unmarshal(body, &result); err != nil || result.EmbedURL == "" {
		return "", errors.New("invalid embed-token response")
	}

	return result.EmbedURL, nil
}
