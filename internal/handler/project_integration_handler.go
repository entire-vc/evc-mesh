package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// teamRelayResponse is the API-facing shape (agent_key redacted to a hint).
type teamRelayResponse struct {
	ID                 uuid.UUID `json:"id"`
	ProjectID          uuid.UUID `json:"project_id"`
	Type               string    `json:"type"`
	Enabled            bool      `json:"enabled"`
	ShareID            string    `json:"share_id"`
	ShareSlug          string    `json:"share_slug"`
	AgentKeyHint       string    `json:"agent_key_hint"` // last 4 chars, e.g. "••••abcd"
	Subfolder          string    `json:"subfolder"`
	IncludeProjectSlug bool      `json:"include_project_slug"`
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
