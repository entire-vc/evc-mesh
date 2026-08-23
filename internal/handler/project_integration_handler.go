package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// teamRelayKeyExpiringSoonWindow is how far ahead of key_expires_at the "on
// the way out" flag turns on. Spec doc 55cf5d7e §3.9 names the credential's
// actual lifetime (90 days) and requires a warning "заранее" but does not
// name a lead time — this is a judgment call, not a value read out of the
// spec. 14 days: long enough that rotating the credential (a human action,
// not an automated one — R5-A's key_expiry_source exists precisely because
// this date can be a manual claim rather than a verified fact) doesn't have
// to happen same-day, short enough that the flag isn't lit for most of the
// credential's life. Revisit if this reads as too noisy or too late in
// practice.
const teamRelayKeyExpiringSoonWindow = 14 * 24 * time.Hour

// teamRelayResponse is the API-facing shape (agent_key redacted to a hint —
// never the value, in this field or any other; see toTeamRelayResponse).
type teamRelayResponse struct {
	ID                 uuid.UUID `json:"id"`
	ProjectID          uuid.UUID `json:"project_id"`
	Type               string    `json:"type"`
	Enabled            bool      `json:"enabled"`
	ShareID            string    `json:"share_id,omitempty"`
	ShareSlug          string    `json:"share_slug"`
	AgentKeyHint       string    `json:"agent_key_hint"` // last 4 chars, e.g. "••••abcd"
	Subfolder          string    `json:"subfolder"`
	IncludeProjectSlug bool      `json:"include_project_slug"`
	// DocsMountPath is the R3/R5-A mount-point switch (empty = project root).
	DocsMountPath string `json:"docs_mount_path,omitempty"`

	// KeyExpiresAt/KeyExpirySource surface R5-A's schema as-is: a nil
	// KeyExpiresAt means the expiry is unknown, and KeyExpirySource
	// ("manual"|"source") is the label a UI is required to show alongside any
	// date it renders — see domain.ProjectIntegration.KeyExpiresAt for why an
	// unlabeled date would be an indicator nobody can trust.
	KeyExpiresAt    *time.Time `json:"key_expires_at,omitempty"`
	KeyExpirySource *string    `json:"key_expiry_source,omitempty"`
	// KeyExpiringSoon is computed here, server-side, so the threshold is one
	// number in one place rather than a policy every caller has to reimplement
	// consistently. True once expiry is within teamRelayKeyExpiringSoonWindow —
	// including if it has already passed. False (not omitted) when
	// KeyExpiresAt is nil: "unknown expiry" and "not expiring soon" are
	// different facts, and a caller checking only this field must not read
	// "false" as "safe" when the truth is "we don't know".
	KeyExpiringSoon bool `json:"key_expiring_soon"`

	// LastSyncCheckedAt/LastSyncStatus/LastSyncError are R5-A's sync-check
	// columns as-is; LastSyncStatus of "key_expired" is what turns the empty
	// tree from spec §3.9 into a named, distinguishable state instead of an
	// unexplained empty folder.
	LastSyncCheckedAt *time.Time `json:"last_sync_checked_at,omitempty"`
	LastSyncStatus    *string    `json:"last_sync_status,omitempty"`
	LastSyncError     *string    `json:"last_sync_error,omitempty"`
}

func toTeamRelayResponse(pi *domain.ProjectIntegration) teamRelayResponse {
	var settings domain.TeamRelaySettings
	_ = json.Unmarshal(pi.Settings, &settings)

	hint := ""
	if len(pi.AgentKey) >= 4 {
		hint = "••••" + pi.AgentKey[len(pi.AgentKey)-4:]
	} else if pi.AgentKey != "" {
		hint = "••••"
	}

	expiringSoon := false
	if pi.KeyExpiresAt != nil {
		expiringSoon = time.Until(*pi.KeyExpiresAt) <= teamRelayKeyExpiringSoonWindow
	}

	return teamRelayResponse{
		ID:                 pi.ID,
		ProjectID:          pi.ProjectID,
		Type:               pi.Type,
		Enabled:            pi.Enabled,
		ShareID:            settings.ShareID,
		ShareSlug:          settings.ShareSlug,
		AgentKeyHint:       hint,
		Subfolder:          settings.Subfolder,
		IncludeProjectSlug: settings.IncludeProjectSlug,
		DocsMountPath:      settings.DocsMountPath,
		KeyExpiresAt:       pi.KeyExpiresAt,
		KeyExpirySource:    pi.KeyExpirySource,
		KeyExpiringSoon:    expiringSoon,
		LastSyncCheckedAt:  pi.LastSyncCheckedAt,
		LastSyncStatus:     pi.LastSyncStatus,
		LastSyncError:      pi.LastSyncError,
	}
}

// ProjectIntegrationHandler handles HTTP requests for project-level integrations.
type ProjectIntegrationHandler struct {
	piService service.ProjectIntegrationService
}

// NewProjectIntegrationHandler creates a new ProjectIntegrationHandler.
func NewProjectIntegrationHandler(piService service.ProjectIntegrationService) *ProjectIntegrationHandler {
	return &ProjectIntegrationHandler{piService: piService}
}

// List handles GET /projects/:proj_id/integrations
func (h *ProjectIntegrationHandler) List(c echo.Context) error {
	projectID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}
	integrations, err := h.piService.List(c.Request().Context(), projectID)
	if err != nil {
		return handleError(c, err)
	}
	responses := make([]teamRelayResponse, len(integrations))
	for i := range integrations {
		responses[i] = toTeamRelayResponse(&integrations[i])
	}
	return c.JSON(http.StatusOK, map[string]any{"integrations": responses})
}

// GetTeamRelay handles GET /projects/:proj_id/integrations/team-relay
func (h *ProjectIntegrationHandler) GetTeamRelay(c echo.Context) error {
	projectID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}
	pi, err := h.piService.GetTeamRelay(c.Request().Context(), projectID)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, toTeamRelayResponse(pi))
}

// UpsertTeamRelay handles PATCH /projects/:proj_id/integrations/team-relay
func (h *ProjectIntegrationHandler) UpsertTeamRelay(c echo.Context) error {
	projectID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	var body struct {
		Enabled            bool   `json:"enabled"`
		ShareID            string `json:"share_id"`
		ShareSlug          string `json:"share_slug"`
		AgentKey           string `json:"agent_key"`
		Subfolder          string `json:"subfolder"`
		IncludeProjectSlug bool   `json:"include_project_slug"`
		DocsMountPath      string `json:"docs_mount_path"`
	}
	if bindErr := c.Bind(&body); bindErr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	var createdBy *uuid.UUID
	if userIDVal := c.Get("user_id"); userIDVal != nil {
		if uid, ok := userIDVal.(uuid.UUID); ok {
			createdBy = &uid
		}
	}

	input := service.UpsertProjectIntegrationInput{
		Enabled:            body.Enabled,
		ShareID:            body.ShareID,
		ShareSlug:          body.ShareSlug,
		AgentKey:           body.AgentKey,
		Subfolder:          body.Subfolder,
		IncludeProjectSlug: body.IncludeProjectSlug,
		DocsMountPath:      body.DocsMountPath,
		CreatedBy:          createdBy,
	}

	pi, err := h.piService.UpsertTeamRelay(c.Request().Context(), projectID, input)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, toTeamRelayResponse(pi))
}

// DeleteTeamRelay handles DELETE /projects/:proj_id/integrations/team-relay
func (h *ProjectIntegrationHandler) DeleteTeamRelay(c echo.Context) error {
	projectID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}
	if err := h.piService.DeleteTeamRelay(c.Request().Context(), projectID); err != nil {
		return handleError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// TrSearch handles GET /api/v1/tr/search?share_id=<slug>&q=<query>&limit=<n>
func (h *ProjectIntegrationHandler) TrSearch(c echo.Context) error {
	shareSlug := c.QueryParam("share_id")
	if shareSlug == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("share_id is required"))
	}

	q := c.QueryParam("q")

	limit := 20
	if lStr := c.QueryParam("limit"); lStr != "" {
		if n, parseErr := strconv.Atoi(lStr); parseErr == nil && n > 0 {
			if n > 50 {
				n = 50
			}
			limit = n
		}
	}

	docs, err := h.piService.SearchTR(c.Request().Context(), shareSlug, q, limit)
	if err != nil {
		return handleError(c, err)
	}

	type docItem struct {
		Title    string `json:"title"`
		Path     string `json:"path"`
		RelayURL string `json:"relay_url"`
	}
	items := make([]docItem, 0, len(docs))
	for _, d := range docs {
		items = append(items, docItem{
			Title:    d.Name,
			Path:     d.Path,
			RelayURL: "relay://" + shareSlug + "/" + d.Path,
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"docs": items})
}
