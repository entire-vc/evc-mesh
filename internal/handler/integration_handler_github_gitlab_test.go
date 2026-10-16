package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// ---------------------------------------------------------------------------
// gitlab is now a supported provider (#33a4bb57 — it existed in code and
// worked entirely off env, but the API rejected the enum value outright).
// ---------------------------------------------------------------------------

func TestIntegrationConfigure_GitLabProvider_NoLongerUnsupported(t *testing.T) {
	svc := newFakeIntegrationService()
	e := newIntegrationTestServer(svc, nil)
	wsID := uuid.New()

	rec := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/integrations",
		`{"provider":"gitlab","config":{"base_url":"https://gitlab.example.invalid","token":"glpat-xxx","webhook_secret":"whsec"},"is_active":true}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
}

// ---------------------------------------------------------------------------
// GET /integrations never leaks a raw secret (accept. criterion #5).
// ---------------------------------------------------------------------------

func TestIntegrationConfigure_GitHub_ResponseNeverLeaksRawSecrets(t *testing.T) {
	svc := newFakeIntegrationService()
	e := newIntegrationTestServer(svc, nil)
	wsID := uuid.New()

	rec := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/integrations",
		`{"provider":"github","config":{"token":"ghp_supersecrettoken","webhook_secret":"whsec_supersecret"},"is_active":true}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	assert.NotContains(t, rec.Body.String(), "ghp_supersecrettoken", "Configure's own response must not echo the raw token back")
	assert.NotContains(t, rec.Body.String(), "whsec_supersecret", "Configure's own response must not echo the raw webhook secret back")

	var created struct {
		ID     string         `json:"id"`
		Config map[string]any `json:"config"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	assert.Equal(t, true, created.Config["token_set"])
	assert.Equal(t, true, created.Config["webhook_secret_set"])

	// GET /integrations (List) — the actual AC5 endpoint — must be equally clean.
	listRec := doJSON(t, e, http.MethodGet, "/api/v1/workspaces/"+wsID.String()+"/integrations", "")
	require.Equal(t, http.StatusOK, listRec.Code)
	assert.NotContains(t, listRec.Body.String(), "ghp_supersecrettoken")
	assert.NotContains(t, listRec.Body.String(), "whsec_supersecret")
}

func TestIntegrationConfigure_GitLab_ResponseNeverLeaksRawSecretsButKeepsBaseURL(t *testing.T) {
	svc := newFakeIntegrationService()
	e := newIntegrationTestServer(svc, nil)
	wsID := uuid.New()

	rec := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/integrations",
		`{"provider":"gitlab","config":{"base_url":"https://gitlab.example.invalid","token":"glpat-secret","webhook_secret":"whsec-secret"},"is_active":true}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "glpat-secret")
	assert.NotContains(t, rec.Body.String(), "whsec-secret")

	var created struct {
		Config map[string]any `json:"config"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	assert.Equal(t, "https://gitlab.example.invalid", created.Config["base_url"], "base_url is not a secret and should pass through unmasked")
	assert.Equal(t, true, created.Config["token_set"])
	assert.Equal(t, true, created.Config["webhook_secret_set"])
}

// ---------------------------------------------------------------------------
// Update merges onto the existing stored config instead of wholesale
// replacing it — a caller rotating ONLY the webhook_secret must not wipe an
// already-configured token (Configure/Update replace the whole config JSON
// blob at the repository layer; prepareGitHubConfig's merge is what stands
// in for a per-field update).
// ---------------------------------------------------------------------------

func TestIntegrationUpdate_GitHub_WebhookSecretOnly_PreservesExistingToken(t *testing.T) {
	svc := newFakeIntegrationService()
	e := newIntegrationTestServer(svc, nil)
	wsID := uuid.New()

	createRec := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/integrations",
		`{"provider":"github","config":{"token":"original-token","webhook_secret":"original-secret"},"is_active":true}`)
	require.Equal(t, http.StatusCreated, createRec.Code)
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(createRec.Body.Bytes(), &created))

	// PATCH with ONLY webhook_secret — token must survive.
	updRec := doJSON(t, e, http.MethodPatch, "/api/v1/integrations/"+created.ID,
		`{"config":{"webhook_secret":"rotated-secret"}}`)
	require.Equal(t, http.StatusOK, updRec.Code, updRec.Body.String())

	// Read back the RAW stored config directly from the fake service (not
	// through the handler's masking) to prove the token is still there.
	id := uuid.MustParse(created.ID)
	stored, ok := svc.byID[id]
	require.True(t, ok)
	var parsed service.GitHubIntegrationConfig
	require.NoError(t, json.Unmarshal(stored.Config, &parsed))
	assert.Equal(t, "original-token", parsed.Token, "a webhook_secret-only PATCH must not wipe the existing token")
	assert.Equal(t, "rotated-secret", parsed.WebhookSecret)
}

func TestIntegrationUpdate_GitHub_EmptyStringExplicitlyClearsField(t *testing.T) {
	svc := newFakeIntegrationService()
	e := newIntegrationTestServer(svc, nil)
	wsID := uuid.New()

	createRec := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/integrations",
		`{"provider":"github","config":{"token":"tok","webhook_secret":"sec"},"is_active":true}`)
	require.Equal(t, http.StatusCreated, createRec.Code)
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(createRec.Body.Bytes(), &created))

	updRec := doJSON(t, e, http.MethodPatch, "/api/v1/integrations/"+created.ID, `{"config":{"webhook_secret":""}}`)
	require.Equal(t, http.StatusOK, updRec.Code)

	id := uuid.MustParse(created.ID)
	stored := svc.byID[id]
	var parsed service.GitHubIntegrationConfig
	require.NoError(t, json.Unmarshal(stored.Config, &parsed))
	assert.Equal(t, "tok", parsed.Token, "token must survive an unrelated field's PATCH")
	assert.Equal(t, "", parsed.WebhookSecret, "an explicit empty string must clear webhook_secret")
}

func TestIntegrationUpdate_GitLab_BaseURLOnly_PreservesTokenAndSecret(t *testing.T) {
	svc := newFakeIntegrationService()
	e := newIntegrationTestServer(svc, nil)
	wsID := uuid.New()

	createRec := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/integrations",
		`{"provider":"gitlab","config":{"base_url":"https://gitlab.example.invalid","token":"tok","webhook_secret":"sec"},"is_active":true}`)
	require.Equal(t, http.StatusCreated, createRec.Code)
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(createRec.Body.Bytes(), &created))

	updRec := doJSON(t, e, http.MethodPatch, "/api/v1/integrations/"+created.ID,
		`{"config":{"base_url":"https://gitlab.new-host.example"}}`)
	require.Equal(t, http.StatusOK, updRec.Code, updRec.Body.String())

	id := uuid.MustParse(created.ID)
	stored := svc.byID[id]
	var parsed service.GitLabIntegrationConfig
	require.NoError(t, json.Unmarshal(stored.Config, &parsed))
	assert.Equal(t, "https://gitlab.new-host.example", parsed.BaseURL)
	assert.Equal(t, "tok", parsed.Token, "changing base_url must not wipe the token")
	assert.Equal(t, "sec", parsed.WebhookSecret, "changing base_url must not wipe the webhook_secret")
}

// ---------------------------------------------------------------------------
// #a4d108eb: env-backed integrations. Disabling one stores is_active=false,
// which blocks env — it needs explicit confirmation, enforced server-side.
// ---------------------------------------------------------------------------

func newEnvBackedServer(svc *fakeIntegrationService) *echo.Echo {
	h := NewIntegrationHandler(svc, nil, nil)
	h.WithStatusResolver(service.NewIntegrationStatusResolver(nil,
		service.NewVCSIntegrationResolver(nil, service.VCSEnvFallback{GitLabBaseURL: "https://git.example", GitLabToken: "t"}),
		nil, nil, true))
	e := echo.New()
	e.GET("/api/v1/workspaces/:ws_id/integrations", h.List)
	e.POST("/api/v1/workspaces/:ws_id/integrations", h.Configure)
	e.PATCH("/api/v1/integrations/:int_id", h.Update)
	return e
}

func TestIntegrationUpdate_DisableEnvBacked_RequiresConfirmation(t *testing.T) {
	svc := newFakeIntegrationService()
	e := newEnvBackedServer(svc)
	id := uuid.New()
	svc.byID[id] = &domain.IntegrationConfig{ID: id, WorkspaceID: uuid.New(), Provider: domain.IntegrationProviderGitLab, IsActive: true, Config: json.RawMessage(`{"base_url":"https://x"}`)}

	rec := doJSON(t, e, http.MethodPatch, "/api/v1/integrations/"+id.String(), `{"is_active":false}`)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.True(t, svc.byID[id].IsActive, "without confirmation the row must stay active")

	rec = doJSON(t, e, http.MethodPatch, "/api/v1/integrations/"+id.String(), `{"is_active":false,"confirm_env_override":true}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.False(t, svc.byID[id].IsActive)
}

func TestIntegrationConfigure_DisableEnvBacked_NoRowWithoutConfirmation(t *testing.T) {
	svc := newFakeIntegrationService()
	e := newEnvBackedServer(svc)
	rec := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+uuid.New().String()+"/integrations",
		`{"provider":"gitlab","config":{"base_url":"https://x"},"is_active":false}`)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Empty(t, svc.byID, "no is_active=false row may be created without confirmation")
}

func TestIntegrationConfigure_EnableWithEmptyConfig_Rejected(t *testing.T) {
	svc := newFakeIntegrationService()
	e := newEnvBackedServer(svc)
	rec := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+uuid.New().String()+"/integrations",
		`{"provider":"gitlab","config":{},"is_active":true}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Empty(t, svc.byID, "an empty placeholder row must not be created")
}

func TestIntegrationList_NoRowEnvSet_ReportsEffectiveEnabledFromEnv(t *testing.T) {
	svc := newFakeIntegrationService()
	e := newEnvBackedServer(svc)
	rec := doJSON(t, e, http.MethodGet, "/api/v1/workspaces/"+uuid.New().String()+"/integrations", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var body struct {
		Integrations []json.RawMessage `json:"integrations"`
		Effective    []struct {
			Provider         string `json:"provider"`
			EffectiveEnabled bool   `json:"effective_enabled"`
			Source           string `json:"source"`
			HasWorkspaceRow  bool   `json:"has_workspace_row"`
		} `json:"effective"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Empty(t, body.Integrations, "no workspace row exists")
	var found bool
	for _, st := range body.Effective {
		if st.Provider == "gitlab" {
			found = true
			assert.True(t, st.EffectiveEnabled)
			assert.Equal(t, "env", st.Source)
			assert.False(t, st.HasWorkspaceRow)
		}
	}
	assert.True(t, found, "effective[] must contain gitlab: %s", rec.Body.String())
}

func TestIntegrationUpdate_EnableEmptyPlaceholderRow_Rejected(t *testing.T) {
	svc := newFakeIntegrationService()
	e := newEnvBackedServer(svc)
	id := uuid.New()
	svc.byID[id] = &domain.IntegrationConfig{ID: id, WorkspaceID: uuid.New(), Provider: domain.IntegrationProviderGitLab, IsActive: false, Config: json.RawMessage(`{}`)}

	rec := doJSON(t, e, http.MethodPatch, "/api/v1/integrations/"+id.String(), `{"is_active":true}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.False(t, svc.byID[id].IsActive, "an empty placeholder row must not be activated")
}
