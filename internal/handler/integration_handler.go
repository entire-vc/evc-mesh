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
		maskSecrets(&cfgs[i])
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
	var configJSON json.RawMessage
	switch provider {
	case domain.IntegrationProviderSlack, domain.IntegrationProviderSpark, domain.IntegrationProviderMCP:
		var jsonErr error
		configJSON, jsonErr = marshalToRawJSON(req.Config)
		if jsonErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid config"))
		}
	case domain.IntegrationProviderTelegram:
		if apiErr := h.prepareTelegramConfig(c.Request().Context(), req.Config); apiErr != nil {
			return c.JSON(apiErr.StatusCode(), apiErr)
		}
		var jsonErr error
		configJSON, jsonErr = marshalToRawJSON(req.Config)
		if jsonErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid config"))
		}
	case domain.IntegrationProviderGitHub:
		existing, _ := h.findExisting(c.Request().Context(), wsID, provider)
		merged, apiErr := prepareGitHubConfig(existing, req.Config)
		if apiErr != nil {
			return c.JSON(apiErr.StatusCode(), apiErr)
		}
		configJSON = merged
	case domain.IntegrationProviderGitLab:
		existing, _ := h.findExisting(c.Request().Context(), wsID, provider)
		merged, apiErr := prepareGitLabConfig(existing, req.Config)
		if apiErr != nil {
			return c.JSON(apiErr.StatusCode(), apiErr)
		}
		configJSON = merged
	default:
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("unsupported provider: "+req.Provider))
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

	maskSecrets(cfg)
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

	// telegram/github/gitlab each need to know the current provider ahead of
	// the update — to encrypt (and, for telegram, validate) a new secret
	// field while merging it onto whatever the workspace already has stored
	// (Configure/Update replace the whole config JSON blob; see
	// prepareGitHubConfig's doc comment for why a naive pass-through would
	// let a webhook_secret-only PATCH silently wipe an existing token).
	// Every other provider's config is opaque here and passed straight
	// through, matching the pre-telegram behavior.
	existing, err := h.integrationService.GetByID(c.Request().Context(), intID)
	if err != nil {
		return handleError(c, err)
	}

	var configJSON []byte
	switch {
	case existing.Provider == domain.IntegrationProviderTelegram && req.Config != nil:
		if apiErr := h.prepareTelegramConfig(c.Request().Context(), req.Config); apiErr != nil {
			return c.JSON(apiErr.StatusCode(), apiErr)
		}
		configJSON, err = marshalToRawJSON(req.Config)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid config"))
		}
	case existing.Provider == domain.IntegrationProviderGitHub && req.Config != nil:
		merged, apiErr := prepareGitHubConfig(existing, req.Config)
		if apiErr != nil {
			return c.JSON(apiErr.StatusCode(), apiErr)
		}
		configJSON = merged
	case existing.Provider == domain.IntegrationProviderGitLab && req.Config != nil:
		merged, apiErr := prepareGitLabConfig(existing, req.Config)
		if apiErr != nil {
			return c.JSON(apiErr.StatusCode(), apiErr)
		}
		configJSON = merged
	case req.Config != nil:
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

	maskSecrets(cfg)
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

// maskSecrets dispatches to the right provider-specific masking function —
// the single call site List/Configure/Update use instead of picking a
// provider-specific function themselves.
func maskSecrets(cfg *domain.IntegrationConfig) {
	if cfg == nil {
		return
	}
	switch cfg.Provider {
	case domain.IntegrationProviderTelegram:
		maskTelegramConfig(cfg)
	case domain.IntegrationProviderGitHub:
		maskGitHubConfig(cfg)
	case domain.IntegrationProviderGitLab:
		maskGitLabConfig(cfg)
	}
}

// maskGitHubConfig is maskTelegramConfig's GitHub counterpart: strips
// token/webhook_secret from an API response, replacing each with a boolean
// the UI can render a masked placeholder from.
func maskGitHubConfig(cfg *domain.IntegrationConfig) {
	if cfg == nil || cfg.Provider != domain.IntegrationProviderGitHub {
		return
	}
	var parsed service.GitHubIntegrationConfig
	if len(cfg.Config) > 0 {
		_ = json.Unmarshal(cfg.Config, &parsed)
	}
	masked, err := json.Marshal(map[string]any{
		"token_set":          parsed.Token != "",
		"webhook_secret_set": parsed.WebhookSecret != "",
	})
	if err != nil {
		cfg.Config = json.RawMessage(`{}`)
		return
	}
	cfg.Config = masked
}

// maskGitLabConfig mirrors maskGitHubConfig. BaseURL is not a secret and
// passes through unmasked — it is the self-hosted instance's URL, useful
// for the settings page to display back to whoever configured it.
func maskGitLabConfig(cfg *domain.IntegrationConfig) {
	if cfg == nil || cfg.Provider != domain.IntegrationProviderGitLab {
		return
	}
	var parsed service.GitLabIntegrationConfig
	if len(cfg.Config) > 0 {
		_ = json.Unmarshal(cfg.Config, &parsed)
	}
	masked, err := json.Marshal(map[string]any{
		"base_url":           parsed.BaseURL,
		"token_set":          parsed.Token != "",
		"webhook_secret_set": parsed.WebhookSecret != "",
	})
	if err != nil {
		cfg.Config = json.RawMessage(`{}`)
		return
	}
	cfg.Config = masked
}

// findExisting looks up a workspace's already-stored config for provider,
// or nil if none exists yet — used by Configure's github/gitlab branch to
// merge a fresh token/webhook_secret onto whatever is already stored,
// exactly as Update's existing (fetched via GetByID) does. Uses
// ListByWorkspace + filter rather than a new IntegrationService method: the
// integration list per workspace is small (one row per provider), and this
// avoids widening IntegrationService's interface for a single call site.
func (h *IntegrationHandler) findExisting(ctx context.Context, workspaceID uuid.UUID, provider domain.IntegrationProvider) (*domain.IntegrationConfig, error) {
	cfgs, err := h.integrationService.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range cfgs {
		if cfgs[i].Provider == provider {
			return &cfgs[i], nil
		}
	}
	return nil, nil
}

// prepareGitHubConfig validates and encrypts token/webhook_secret fields
// present in config (a map[string]any from the request body), merging them
// onto whatever the workspace already has stored (existing, nil if this is
// a brand-new integration) so that a caller updating ONLY one field (e.g.
// rotating just the webhook_secret) does not silently wipe the other —
// Configure/Update replace the whole config JSON blob wholesale
// (repository.IntegrationRepository has no per-field merge; see
// IntegrationRepo.Upsert/Update), so this handler-level read-merge-write
// stands in for it.
//
//   - A key absent from the map: left at whatever existing already had
//     (possibly nothing, for a brand-new integration).
//   - An empty string: explicitly clears that field.
//   - A non-empty string: AES-256-GCM encrypted via pkg/encryption before
//     it is allowed anywhere near Configure/Update — the DB never sees the
//     plaintext, mirroring prepareTelegramConfig's bot_token handling.
func prepareGitHubConfig(existing *domain.IntegrationConfig, config interface{}) (json.RawMessage, *apierror.Error) {
	var merged service.GitHubIntegrationConfig
	if existing != nil && len(existing.Config) > 0 {
		_ = json.Unmarshal(existing.Config, &merged)
	}
	raw, _ := config.(map[string]interface{})
	if v, ok := raw["token"]; ok {
		s, _ := v.(string)
		if s == "" {
			merged.Token = ""
		} else {
			enc, err := encryption.Encrypt(s)
			if err != nil {
				return nil, apierror.BadRequestWithDetails("failed to store GitHub token", err.Error())
			}
			merged.Token = enc
		}
	}
	if v, ok := raw["webhook_secret"]; ok {
		s, _ := v.(string)
		if s == "" {
			merged.WebhookSecret = ""
		} else {
			enc, err := encryption.Encrypt(s)
			if err != nil {
				return nil, apierror.BadRequestWithDetails("failed to store GitHub webhook secret", err.Error())
			}
			merged.WebhookSecret = enc
		}
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return nil, apierror.BadRequestWithDetails("invalid config", err.Error())
	}
	return out, nil
}

// prepareGitLabConfig mirrors prepareGitHubConfig, plus a plaintext
// base_url field (not a secret — the self-hosted instance's URL, e.g.
// "https://git.entire.host" — so it is never encrypted or masked-out).
func prepareGitLabConfig(existing *domain.IntegrationConfig, config interface{}) (json.RawMessage, *apierror.Error) {
	var merged service.GitLabIntegrationConfig
	if existing != nil && len(existing.Config) > 0 {
		_ = json.Unmarshal(existing.Config, &merged)
	}
	raw, _ := config.(map[string]interface{})
	if v, ok := raw["base_url"]; ok {
		s, _ := v.(string)
		merged.BaseURL = s
	}
	if v, ok := raw["token"]; ok {
		s, _ := v.(string)
		if s == "" {
			merged.Token = ""
		} else {
			enc, err := encryption.Encrypt(s)
			if err != nil {
				return nil, apierror.BadRequestWithDetails("failed to store GitLab token", err.Error())
			}
			merged.Token = enc
		}
	}
	if v, ok := raw["webhook_secret"]; ok {
		s, _ := v.(string)
		if s == "" {
			merged.WebhookSecret = ""
		} else {
			enc, err := encryption.Encrypt(s)
			if err != nil {
				return nil, apierror.BadRequestWithDetails("failed to store GitLab webhook secret", err.Error())
			}
			merged.WebhookSecret = enc
		}
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return nil, apierror.BadRequestWithDetails("invalid config", err.Error())
	}
	return out, nil
}
