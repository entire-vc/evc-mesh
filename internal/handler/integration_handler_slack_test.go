package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// A workspace's Slack webhook_url is a blind-SSRF vector the same way a
// workspace webhook's url is (see internal/service/slack_webhook_ssrf_test.go
// for the delivery-time guard). These tests cover the write-time courtesy
// check: prepareSlackConfig, wired into Configure and Update.

func TestConfigureIntegration_SlackPrivateWebhookURLIsRejected(t *testing.T) {
	svc := newFakeIntegrationService()
	e := newIntegrationTestServer(svc, nil)
	wsID := uuid.New()

	rec := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/integrations",
		`{"provider":"slack","is_active":true,"config":{"webhook_url":"http://127.0.0.1:8080/T000/B000/xxxx"}}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, svc.byID, "a private-address webhook_url was stored anyway")
}

func TestConfigureIntegration_SlackLocalhostWebhookURLIsRejected(t *testing.T) {
	svc := newFakeIntegrationService()
	e := newIntegrationTestServer(svc, nil)
	wsID := uuid.New()

	rec := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/integrations",
		`{"provider":"slack","is_active":true,"config":{"webhook_url":"http://localhost/hook"}}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, svc.byID)
}

func TestConfigureIntegration_SlackPublicWebhookURLIsStored(t *testing.T) {
	svc := newFakeIntegrationService()
	e := newIntegrationTestServer(svc, nil)
	wsID := uuid.New()

	rec := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/integrations",
		`{"provider":"slack","is_active":true,"config":{"webhook_url":"https://hooks.slack.com/services/T000/B000/xxxx"}}`)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.Len(t, svc.byID, 1)
}

func TestConfigureIntegration_SlackEmptyWebhookURLIsAllowed(t *testing.T) {
	// Empty webhook_url means "notifications off, not yet configured" —
	// slackService.SendMessage's own caller (NotifyTaskEvent) already treats
	// it that way; the write-time guard must not block that state.
	svc := newFakeIntegrationService()
	e := newIntegrationTestServer(svc, nil)
	wsID := uuid.New()

	rec := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/integrations",
		`{"provider":"slack","is_active":false,"config":{"webhook_url":""}}`)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.Len(t, svc.byID, 1)
}

func TestConfigureIntegration_SlackConfigWithoutWebhookURLKeyIsAllowed(t *testing.T) {
	// No webhook_url key at all (only channel/notify_events set) is a
	// different shape from an explicit empty string, and must be a no-op the
	// same way: prepareSlackConfig's "no field" branch, distinct from its
	// "empty field" branch above.
	svc := newFakeIntegrationService()
	e := newIntegrationTestServer(svc, nil)
	wsID := uuid.New()

	rec := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/integrations",
		`{"provider":"slack","is_active":false,"config":{"channel":"#general"}}`)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.Len(t, svc.byID, 1)
}

func TestUpdateIntegration_SlackPrivateWebhookURLIsRejected(t *testing.T) {
	svc := newFakeIntegrationService()
	e := newIntegrationTestServer(svc, nil)
	wsID := uuid.New()

	created := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/integrations",
		`{"provider":"slack","is_active":true,"config":{"webhook_url":"https://hooks.slack.com/services/T000/B000/xxxx"}}`)
	var cfg domain.IntegrationConfig
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &cfg))

	rec := doJSON(t, e, http.MethodPatch, "/api/v1/integrations/"+cfg.ID.String(),
		`{"config":{"webhook_url":"http://169.254.169.254/latest/meta-data/"}}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	stored := svc.byID[cfg.ID]
	var storedCfg map[string]any
	require.NoError(t, json.Unmarshal(stored.Config, &storedCfg))
	assert.Equal(t, "https://hooks.slack.com/services/T000/B000/xxxx", storedCfg["webhook_url"],
		"the rejected update must not have overwritten the previously stored URL")
}

func TestUpdateIntegration_SlackTogglingActiveWithoutWebhookURLIsUnaffected(t *testing.T) {
	svc := newFakeIntegrationService()
	e := newIntegrationTestServer(svc, nil)
	wsID := uuid.New()

	created := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/integrations",
		`{"provider":"slack","is_active":true,"config":{"webhook_url":"https://hooks.slack.com/services/T000/B000/xxxx"}}`)
	var cfg domain.IntegrationConfig
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &cfg))

	rec := doJSON(t, e, http.MethodPatch, "/api/v1/integrations/"+cfg.ID.String(), `{"is_active":false}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.False(t, svc.byID[cfg.ID].IsActive)
}
