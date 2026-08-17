package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/encryption"
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
	telegramClient     service.TelegramClient
	telegramBots       *service.TelegramBotManager
}

// NewIntegrationHandler creates a new IntegrationHandler.
//
// telegramClient and telegramBots may be nil (e.g. in tests that only touch
// slack/github/spark/mcp) — every telegram-specific code path below checks
// before using them, and a telegram Configure/Update simply cannot be
// validated or take effect live without them.
func NewIntegrationHandler(svc service.IntegrationService, telegramClient service.TelegramClient, telegramBots *service.TelegramBotManager) *IntegrationHandler {
	return &IntegrationHandler{integrationService: svc, telegramClient: telegramClient, telegramBots: telegramBots}
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
	for i := range cfgs {
		maskTelegramConfig(&cfgs[i])
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
	case domain.IntegrationProviderTelegram:
		if apiErr := h.prepareTelegramConfig(c.Request().Context(), req.Config); apiErr != nil {
			return c.JSON(apiErr.StatusCode(), apiErr)
		}
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

	if provider == domain.IntegrationProviderTelegram && h.telegramBots != nil {
		h.telegramBots.Reload(c.Request().Context(), cfg.ID)
	}

	maskTelegramConfig(cfg)
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

	// Only telegram needs to know the current provider ahead of the update —
	// to validate/encrypt a new bot_token, or, if none was sent, to leave the
	// stored one alone. Every other provider's config is opaque here and
	// passed straight through, matching the pre-telegram behavior.
	existing, err := h.integrationService.GetByID(c.Request().Context(), intID)
	if err != nil {
		return handleError(c, err)
	}

	if existing.Provider == domain.IntegrationProviderTelegram && req.Config != nil {
		if apiErr := h.prepareTelegramConfig(c.Request().Context(), req.Config); apiErr != nil {
			return c.JSON(apiErr.StatusCode(), apiErr)
		}
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

	if cfg.Provider == domain.IntegrationProviderTelegram && h.telegramBots != nil {
		h.telegramBots.Reload(c.Request().Context(), cfg.ID)
	}

	maskTelegramConfig(cfg)
	return c.JSON(http.StatusOK, cfg)
}

// Delete handles DELETE /integrations/:int_id
func (h *IntegrationHandler) Delete(c echo.Context) error {
	intID, err := uuid.Parse(c.Param("int_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid integration_id"))
	}

	// Read the provider first so a telegram poller can be stopped after the
	// row is gone — Delete itself returns nothing to branch on.
	existing, getErr := h.integrationService.GetByID(c.Request().Context(), intID)
	wasTelegram := getErr == nil && existing != nil && existing.Provider == domain.IntegrationProviderTelegram

	if err := h.integrationService.Delete(c.Request().Context(), intID); err != nil {
		return handleError(c, err)
	}

	if wasTelegram && h.telegramBots != nil {
		h.telegramBots.Stop(intID)
	}

	return c.NoContent(http.StatusNoContent)
}

// prepareTelegramConfig validates and encrypts a bot_token present in config
// (a map[string]any coming straight off the request body). A map is a
// reference type, so mutating it here is visible to the caller's copy of the
// interface without needing to hand a new value back.
//
//   - No bot_token field at all (an Update that only toggles is_active, say):
//     left untouched — the caller's existing stored config carries through.
//   - An empty bot_token: rejected. There is no legitimate reason to send one;
//     accepting it would either wipe a working token or store an integration
//     that can never send anything.
//   - A non-empty bot_token: validated against Telegram's own getMe (this is
//     also how bot_username gets populated, for the settings page's link and
//     the workspace admin's confirmation that they typed the right token),
//     then AES-256-GCM encrypted via pkg/encryption before it is allowed
//     anywhere near Configure/Update — the DB never sees the plaintext.
func (h *IntegrationHandler) prepareTelegramConfig(ctx context.Context, config interface{}) *apierror.Error {
	raw, _ := config.(map[string]interface{})
	tokenVal, hasToken := raw["bot_token"]
	if !hasToken {
		return nil
	}
	token, _ := tokenVal.(string)
	if token == "" {
		return apierror.BadRequest("bot_token must not be empty")
	}
	if h.telegramClient == nil {
		return apierror.BadRequestWithDetails("Telegram is not available on this instance", "no Telegram client configured")
	}

	getMeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	botUsername, err := h.telegramClient.GetMe(getMeCtx, token)
	if err != nil {
		return apierror.BadRequestWithDetails("invalid Telegram bot token", err.Error())
	}

	encrypted, encErr := encryption.Encrypt(token)
	if encErr != nil {
		return apierror.BadRequestWithDetails("failed to store Telegram bot token", encErr.Error())
	}

	raw["bot_token"] = encrypted
	raw["bot_username"] = botUsername
	return nil
}

// maskTelegramConfig strips the (encrypted, but still no reason to expose)
// bot_token from a telegram integration before it goes into an API response,
// replacing it with a boolean the UI can render a masked placeholder from.
// GET /workspaces/:ws_id/integrations carries no RBAC guard — any workspace
// member can list integrations — so this runs on every response, not just
// for non-admin callers.
func maskTelegramConfig(cfg *domain.IntegrationConfig) {
	if cfg == nil || cfg.Provider != domain.IntegrationProviderTelegram {
		return
	}
	var parsed service.TelegramIntegrationConfig
	if len(cfg.Config) > 0 {
		_ = json.Unmarshal(cfg.Config, &parsed)
	}
	masked, err := json.Marshal(map[string]any{
		"bot_username":  parsed.BotUsername,
		"bot_token_set": parsed.BotToken != "",
	})
	if err != nil {
		cfg.Config = json.RawMessage(`{}`)
		return
	}
	cfg.Config = masked
}
