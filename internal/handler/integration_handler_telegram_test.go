package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// --- fakes --------------------------------------------------------------------

// fakeIntegrationService is an in-memory stand-in for service.IntegrationService,
// scoped to what the Telegram config path exercises.
type fakeIntegrationService struct {
	byID map[uuid.UUID]*domain.IntegrationConfig
}

func newFakeIntegrationService() *fakeIntegrationService {
	return &fakeIntegrationService{byID: map[uuid.UUID]*domain.IntegrationConfig{}}
}

// Configure stores its own copy and returns a second, independent copy — the
// real integrationService does the equivalent by re-fetching from the
// repository after Upsert, so the handler's later in-place mask of the
// returned value (maskTelegramConfig mutates cfg.Config) must not also
// mutate what "the database" holds, the way sharing one pointer would.
func (f *fakeIntegrationService) Configure(_ context.Context, input domain.CreateIntegrationInput) (*domain.IntegrationConfig, error) {
	stored := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: input.WorkspaceID, Provider: input.Provider, Config: append(json.RawMessage(nil), input.Config...), IsActive: input.IsActive}
	f.byID[stored.ID] = stored
	returned := *stored
	returned.Config = append(json.RawMessage(nil), stored.Config...)
	return &returned, nil
}

func (f *fakeIntegrationService) GetByID(_ context.Context, id uuid.UUID) (*domain.IntegrationConfig, error) {
	cfg, ok := f.byID[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return cfg, nil
}

// Update mutates the stored copy, then returns a THIRD independent copy —
// same reasoning as Configure above: the caller mutates what it gets back
// (maskTelegramConfig), and that must not reach back into "the database".
func (f *fakeIntegrationService) Update(_ context.Context, id uuid.UUID, input domain.UpdateIntegrationInput) (*domain.IntegrationConfig, error) {
	cfg, ok := f.byID[id]
	if !ok {
		return nil, errors.New("not found")
	}
	if input.Config != nil {
		cfg.Config = append(json.RawMessage(nil), input.Config...)
	}
	if input.IsActive != nil {
		cfg.IsActive = *input.IsActive
	}
	returned := *cfg
	returned.Config = append(json.RawMessage(nil), cfg.Config...)
	return &returned, nil
}

func (f *fakeIntegrationService) Delete(_ context.Context, id uuid.UUID) error {
	if _, ok := f.byID[id]; !ok {
		return errors.New("not found")
	}
	delete(f.byID, id)
	return nil
}

func (f *fakeIntegrationService) ListByWorkspace(_ context.Context, wsID uuid.UUID) ([]domain.IntegrationConfig, error) {
	var out []domain.IntegrationConfig
	for _, cfg := range f.byID {
		if cfg.WorkspaceID == wsID {
			out = append(out, *cfg)
		}
	}
	return out, nil
}

var _ service.IntegrationService = (*fakeIntegrationService)(nil)

// fakeIntegrationTelegramClient stubs GetMe for the token-validation step.
type fakeIntegrationTelegramClient struct {
	username string
	err      error
}

func (f *fakeIntegrationTelegramClient) GetMe(context.Context, string) (string, error) {
	return f.username, f.err
}
func (f *fakeIntegrationTelegramClient) SendMessage(context.Context, string, int64, string) error {
	return nil
}
func (f *fakeIntegrationTelegramClient) GetUpdates(context.Context, string, int64, int) ([]service.TelegramUpdate, error) {
	return nil, nil
}

var _ service.TelegramClient = (*fakeIntegrationTelegramClient)(nil)

func newIntegrationTestServer(svc service.IntegrationService, client service.TelegramClient) *echo.Echo {
	h := NewIntegrationHandler(svc, client, nil)
	e := echo.New()
	e.POST("/api/v1/workspaces/:ws_id/integrations", h.Configure)
	e.PATCH("/api/v1/integrations/:int_id", h.Update)
	e.GET("/api/v1/workspaces/:ws_id/integrations", h.List)
	e.DELETE("/api/v1/integrations/:int_id", h.Delete)
	return e
}

func doJSON(t *testing.T, e *echo.Echo, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// --- tests ----------------------------------------------------------------

func TestConfigureIntegration_TelegramTokenIsValidatedAndEncrypted(t *testing.T) {
	svc := newFakeIntegrationService()
	client := &fakeIntegrationTelegramClient{username: "mesh_bot"}
	e := newIntegrationTestServer(svc, client)
	wsID := uuid.New()

	rec := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/integrations",
		`{"provider":"telegram","is_active":true,"config":{"bot_token":"123:ABC-real-token"}}`)

	require.Equal(t, http.StatusCreated, rec.Code)

	var resp domain.IntegrationConfig
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	// The response never carries the token, encrypted or not — only what the UI needs.
	var respCfg map[string]any
	require.NoError(t, json.Unmarshal(resp.Config, &respCfg))
	assert.Equal(t, "mesh_bot", respCfg["bot_username"])
	assert.Equal(t, true, respCfg["bot_token_set"])
	assert.NotContains(t, respCfg, "bot_token")
	assert.NotContains(t, string(resp.Config), "123:ABC-real-token", "the raw token leaked into the API response")

	// What actually got stored went through encryption.Encrypt (verified by
	// pkg/encryption's own tests; this only checks prepareTelegramConfig calls
	// it rather than storing the raw request field untouched — the encryption
	// package's key state is process-global and this package cannot force a
	// key to be configured for the duration of one test).
	stored := svc.byID[resp.ID]
	var storedCfg service.TelegramIntegrationConfig
	require.NoError(t, json.Unmarshal(stored.Config, &storedCfg))
	assert.Equal(t, "mesh_bot", storedCfg.BotUsername)
	assert.NotEmpty(t, storedCfg.BotToken)
}

func TestConfigureIntegration_TelegramInvalidTokenIsRejected(t *testing.T) {
	svc := newFakeIntegrationService()
	client := &fakeIntegrationTelegramClient{err: errors.New("401 Unauthorized")}
	e := newIntegrationTestServer(svc, client)
	wsID := uuid.New()

	rec := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/integrations",
		`{"provider":"telegram","is_active":true,"config":{"bot_token":"bad-token"}}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, svc.byID, "an invalid token was stored anyway")
}

func TestConfigureIntegration_TelegramEmptyTokenIsRejected(t *testing.T) {
	svc := newFakeIntegrationService()
	client := &fakeIntegrationTelegramClient{username: "mesh_bot"}
	e := newIntegrationTestServer(svc, client)
	wsID := uuid.New()

	rec := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/integrations",
		`{"provider":"telegram","is_active":true,"config":{"bot_token":""}}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, svc.byID)
}

func TestUpdateIntegration_TelegramTogglingActiveWithoutTokenPreservesIt(t *testing.T) {
	svc := newFakeIntegrationService()
	client := &fakeIntegrationTelegramClient{username: "mesh_bot"}
	e := newIntegrationTestServer(svc, client)
	wsID := uuid.New()

	created := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/integrations",
		`{"provider":"telegram","is_active":true,"config":{"bot_token":"123:ABC"}}`)
	var cfg domain.IntegrationConfig
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &cfg))

	stored := svc.byID[cfg.ID]
	var beforeCfg service.TelegramIntegrationConfig
	require.NoError(t, json.Unmarshal(stored.Config, &beforeCfg))

	// Client GetMe would fail loudly if called again with no token — proves the
	// toggle-only path never re-validates a token it was never given.
	client.err = errors.New("should not be called")

	rec := doJSON(t, e, http.MethodPatch, "/api/v1/integrations/"+cfg.ID.String(), `{"is_active":false}`)
	require.Equal(t, http.StatusOK, rec.Code)

	afterStored := svc.byID[cfg.ID]
	var afterCfg service.TelegramIntegrationConfig
	require.NoError(t, json.Unmarshal(afterStored.Config, &afterCfg))
	assert.Equal(t, beforeCfg.BotToken, afterCfg.BotToken, "the encrypted token was lost on a toggle-only update")
	assert.False(t, afterStored.IsActive)
}

func TestListIntegrations_TelegramConfigIsMasked(t *testing.T) {
	svc := newFakeIntegrationService()
	client := &fakeIntegrationTelegramClient{username: "mesh_bot"}
	e := newIntegrationTestServer(svc, client)
	wsID := uuid.New()

	created := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/integrations",
		`{"provider":"telegram","is_active":true,"config":{"bot_token":"123:ABC-real-token"}}`)
	require.Equal(t, http.StatusCreated, created.Code)

	rec := doJSON(t, e, http.MethodGet, "/api/v1/workspaces/"+wsID.String()+"/integrations", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "123:ABC-real-token")
	assert.Contains(t, rec.Body.String(), "mesh_bot")
	assert.Contains(t, rec.Body.String(), "bot_token_set")
}

func TestDeleteIntegration_TelegramStopsThePoller(t *testing.T) {
	// No botManager wired (nil) — Delete must not panic when there is nothing
	// to stop, which is the state every non-telegram-touching test runs in.
	svc := newFakeIntegrationService()
	client := &fakeIntegrationTelegramClient{username: "mesh_bot"}
	e := newIntegrationTestServer(svc, client)
	wsID := uuid.New()

	created := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/integrations",
		`{"provider":"telegram","is_active":true,"config":{"bot_token":"123:ABC"}}`)
	var cfg domain.IntegrationConfig
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &cfg))

	rec := doJSON(t, e, http.MethodDelete, "/api/v1/integrations/"+cfg.ID.String(), "")
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, svc.byID)
}
