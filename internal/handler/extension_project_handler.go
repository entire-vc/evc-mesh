package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// teamRelayExtensionID is the manifest id of the only extension that has a
// project level today; its project binding lives in project_integrations.
const teamRelayExtensionID = "team-relay"

// maskedSecret is what a secret setting reads back as once it is stored.
const maskedSecret = "***"

// extensionLookup is the part of the registry the project routes need.
type extensionLookup interface {
	Get(id string) (extensions.Provider, bool)
}

// ExtensionProjectHandler serves /projects/:proj_id/extensions/team-relay
// (docs/extensions/api.md, "Project routes"): the project level of the one
// extension that has it.
type ExtensionProjectHandler struct {
	registry extensionLookup
	pi       service.ProjectIntegrationService
}

func NewExtensionProjectHandler(registry extensionLookup, pi service.ProjectIntegrationService) *ExtensionProjectHandler {
	return &ExtensionProjectHandler{registry: registry, pi: pi}
}

// projectExtensionResponse is the ProjectExtension type of the API document.
type projectExtensionResponse struct {
	ExtensionID string         `json:"extension_id"`
	Enabled     bool           `json:"enabled"`
	Settings    map[string]any `json:"settings"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

func toProjectExtensionResponse(extID string, pi *domain.ProjectIntegration) projectExtensionResponse {
	settings := map[string]any{}
	_ = json.Unmarshal(pi.Settings, &settings)
	// The agent key is a secret setting: reported as set, never as a value.
	if pi.AgentKey != "" {
		settings["agent_key"] = maskedSecret
	}
	return projectExtensionResponse{
		ExtensionID: extID,
		Enabled:     pi.Enabled,
		Settings:    settings,
		CreatedAt:   pi.CreatedAt,
		UpdatedAt:   pi.UpdatedAt,
	}
}

// target resolves the path to a project, and refuses with 404 when Team Relay
// is not registered or its manifest no longer has a project level.
func (h *ExtensionProjectHandler) target(c echo.Context) (uuid.UUID, string, error) {
	projectID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return uuid.Nil, "", apierror.BadRequest("invalid proj_id")
	}
	p, ok := h.registry.Get(teamRelayExtensionID)
	if !ok || !p.Manifest().Levels.Project {
		return uuid.Nil, "", apierror.NotFound("Extension")
	}
	return projectID, teamRelayExtensionID, nil
}

// Get handles GET /projects/:proj_id/extensions/:ext_id.
func (h *ExtensionProjectHandler) Get(c echo.Context) error {
	projectID, extID, err := h.target(c)
	if err != nil {
		return handleError(c, err)
	}
	pi, err := h.pi.GetTeamRelay(c.Request().Context(), projectID)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, toProjectExtensionResponse(extID, pi))
}

// teamRelayProjectSettings are the keys the project level of Team Relay accepts.
type teamRelayProjectSettings struct {
	ShareID             string   `json:"share_id"`
	ShareSlug           string   `json:"share_slug"`
	AgentKey            string   `json:"agent_key"`
	Subfolder           string   `json:"subfolder"`
	IncludeProjectSlug  bool     `json:"include_project_slug"`
	DocsMountPath       string   `json:"docs_mount_path"`
	ExcludedPaths       []string `json:"excluded_paths"`
	SyncIntervalMinutes *int     `json:"sync_interval_minutes"`
}

// Put handles PUT /projects/:proj_id/extensions/:ext_id: the binding is
// created or replaced. A secret left out or sent back as "***" keeps the
// stored value.
func (h *ExtensionProjectHandler) Put(c echo.Context) error {
	projectID, extID, err := h.target(c)
	if err != nil {
		return handleError(c, err)
	}
	var req struct {
		Enabled  bool            `json:"enabled"`
		Settings json.RawMessage `json:"settings"`
	}
	if bindErr := c.Bind(&req); bindErr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	var settings teamRelayProjectSettings
	if len(req.Settings) > 0 {
		dec := json.NewDecoder(bytes.NewReader(req.Settings))
		dec.DisallowUnknownFields()
		if decErr := dec.Decode(&settings); decErr != nil {
			return handleError(c, apierror.ValidationError(map[string]string{"settings": decErr.Error()}))
		}
	}
	if settings.AgentKey == maskedSecret {
		settings.AgentKey = ""
	}
	var createdBy *uuid.UUID
	if uid, ok := currentUserID(c); ok {
		createdBy = &uid
	}
	pi, err := h.pi.UpsertTeamRelay(c.Request().Context(), projectID, service.UpsertProjectIntegrationInput{
		Enabled:             req.Enabled,
		ShareID:             settings.ShareID,
		ShareSlug:           settings.ShareSlug,
		AgentKey:            settings.AgentKey,
		Subfolder:           settings.Subfolder,
		IncludeProjectSlug:  settings.IncludeProjectSlug,
		DocsMountPath:       settings.DocsMountPath,
		ExcludedPaths:       settings.ExcludedPaths,
		SyncIntervalMinutes: settings.SyncIntervalMinutes,
		CreatedBy:           createdBy,
	})
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, toProjectExtensionResponse(extID, pi))
}

// Delete handles DELETE /projects/:proj_id/extensions/:ext_id.
func (h *ExtensionProjectHandler) Delete(c echo.Context) error {
	projectID, _, err := h.target(c)
	if err != nil {
		return handleError(c, err)
	}
	if err := h.pi.DeleteTeamRelay(c.Request().Context(), projectID); err != nil {
		return handleError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}
