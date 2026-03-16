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

// marshalToRawJSON converts any value to a JSON raw message.
func marshalToRawJSON(v interface{}) (json.RawMessage, error) {
	if v == nil {
		return json.RawMessage("{}"), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

// IntegrationHandler handles HTTP requests for workspace integration management.
type IntegrationHandler struct {
	integrationService service.IntegrationService
}

// NewIntegrationHandler creates a new IntegrationHandler.
func NewIntegrationHandler(svc service.IntegrationService) *IntegrationHandler {
	return &IntegrationHandler{integrationService: svc}
}

// configureIntegrationRequest is the JSON body for creating/configuring an integration.
type configureIntegrationRequest struct {
	Provider string      `json:"provider"`
	Config   interface{} `json:"config"`
	IsActive bool        `json:"is_active"`
}

// updateIntegrationRequest is the JSON body for updating an integration.
type updateIntegrationRequest struct {
	Config   interface{} `json:"config"`
	IsActive *bool       `json:"is_active"`
}

// List handles GET /workspaces/:ws_id/integrations
func (h *IntegrationHandler) List(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	cfgs, err := h.integrationService.ListByWorkspace(c.Request().Context(), wsID)
	if err != nil {
		return handleError(c, err)
	}

	if cfgs == nil {
		cfgs = []domain.IntegrationConfig{}
	}

	return c.JSON(http.StatusOK, map[string]any{
		"integrations": cfgs,
		"count":        len(cfgs),
	})
}

// Configure handles POST /workspaces/:ws_id/integrations
func (h *IntegrationHandler) Configure(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	var req configureIntegrationRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.Provider == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("provider is required"))
	}

	provider := domain.IntegrationProvider(req.Provider)
	switch provider {
	case domain.IntegrationProviderSlack, domain.IntegrationProviderGitHub, domain.IntegrationProviderSpark, domain.IntegrationProviderMCP:
	default:
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("unsupported provider: "+req.Provider))
	}

	configJSON, err := marshalToRawJSON(req.Config)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid config"))
	}

	input := domain.CreateIntegrationInput{
		WorkspaceID: wsID,
		Provider:    provider,
		Config:      configJSON,
		IsActive:    req.IsActive,
	}

	cfg, err := h.integrationService.Configure(c.Request().Context(), input)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, cfg)
}

// Update handles PATCH /integrations/:int_id
func (h *IntegrationHandler) Update(c echo.Context) error {
	intID, err := uuid.Parse(c.Param("int_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid integration_id"))
	}

	var req updateIntegrationRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	var configJSON []byte
	if req.Config != nil {
		configJSON, err = marshalToRawJSON(req.Config)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid config"))
		}
	}

	input := domain.UpdateIntegrationInput{
		Config:   configJSON,
		IsActive: req.IsActive,
	}

	cfg, err := h.integrationService.Update(c.Request().Context(), intID, input)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, cfg)
}

// Delete handles DELETE /integrations/:int_id
func (h *IntegrationHandler) Delete(c echo.Context) error {
	intID, err := uuid.Parse(c.Param("int_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid integration_id"))
	}

	if err := h.integrationService.Delete(c.Request().Context(), intID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}
