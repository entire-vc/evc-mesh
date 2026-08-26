package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// fakeResolverIntegrationRepo is a minimal repository.IntegrationRepository
// stub for exercising VCSLinkHandler's dynamic-resolver path
// (WithVCSIntegrationResolver, #33a4bb57) end-to-end through a real
// service.VCSIntegrationResolver — not a mocked resolver, since
// VCSIntegrationResolver is a concrete type, not an interface: this mirrors
// production wiring (main.go constructs the exact same resolver over the
// exact same repository interface) rather than re-deriving its logic in a
// test double.
type fakeResolverIntegrationRepo struct {
	row *domain.IntegrationConfig
}

func (f *fakeResolverIntegrationRepo) Upsert(context.Context, *domain.IntegrationConfig) error {
	return nil
}
func (f *fakeResolverIntegrationRepo) GetByID(context.Context, uuid.UUID) (*domain.IntegrationConfig, error) {
	return nil, nil
}
func (f *fakeResolverIntegrationRepo) GetByProvider(_ context.Context, _ uuid.UUID, provider domain.IntegrationProvider) (*domain.IntegrationConfig, error) {
	if f.row != nil && f.row.Provider == provider {
		return f.row, nil
	}
	return nil, nil
}
func (f *fakeResolverIntegrationRepo) Update(context.Context, uuid.UUID, domain.UpdateIntegrationInput) (*domain.IntegrationConfig, error) {
	return nil, nil
}
func (f *fakeResolverIntegrationRepo) Delete(context.Context, uuid.UUID) error { return nil }
func (f *fakeResolverIntegrationRepo) ListByWorkspace(context.Context, uuid.UUID) ([]domain.IntegrationConfig, error) {
	return nil, nil
}
func (f *fakeResolverIntegrationRepo) ListActiveByProvider(_ context.Context, provider domain.IntegrationProvider) ([]domain.IntegrationConfig, error) {
	if f.row != nil && f.row.Provider == provider && f.row.IsActive {
		return []domain.IntegrationConfig{*f.row}, nil
	}
	return nil, nil
}
func (f *fakeResolverIntegrationRepo) ListByProvider(_ context.Context, provider domain.IntegrationProvider) ([]domain.IntegrationConfig, error) {
	if f.row != nil && f.row.Provider == provider {
		return []domain.IntegrationConfig{*f.row}, nil
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// GitHub — the AC1/AC2/AC3 sequence from specsintegration-provider-contract
// §5: positive control (delivers), negative control (explicit refusal, not
// a silent 200), re-enable (channel alive again). Run as ONE test, in that
// order, on the SAME resolver — exactly the shape §0x asks for: a check
// without a shown red run doesn't count, and a re-enable proves the
// negative wasn't "never worked in the first place".
// ---------------------------------------------------------------------------
func TestGitHubWebhook_DynamicResolver_ToggleSequence(t *testing.T) {
	const secret = "workspace-secret"
	repo := &fakeResolverIntegrationRepo{row: &domain.IntegrationConfig{
		Provider: domain.IntegrationProviderGitHub,
		IsActive: true,
		Config:   githubIntegrationCfgJSON(t, "", secret),
	}}
	resolver := service.NewVCSIntegrationResolver(repo, service.VCSEnvFallback{})

	svc := &stubVCSLinkService{handleResult: service.PRHandleResult{Transitioned: true, NewStatus: "review"}}
	dedup := newMemDedupStore()
	h := NewVCSLinkHandler(svc, WithVCSIntegrationResolver(resolver), WithWebhookDedupStore(dedup))

	body := newPullRequestPayload(t, "closed", 1, "MESH-"+uuid.New().String(), "", true, "sha1")

	// --- AC1: positive control — is_active=true, correct secret → delivered.
	req := newPullRequestRequest(t, body, "delivery-1", secret)
	rec := httptest.NewRecorder()
	require.NoError(t, h.GitHubWebhook(echo.New().NewContext(req, rec)))
	assert.Equal(t, http.StatusOK, rec.Code, "positive control must deliver")
	assert.Len(t, svc.handleCalls, 1, "service must have been invoked")

	// --- AC2: negative control — same secret, now is_active=false → refused
	// with a named reason, NOT a silent 200 (the pre-C2 behavior this
	// replaces treated "empty resolved secret" as "validation off").
	repo.row.IsActive = false
	req2 := newPullRequestRequest(t, body, "delivery-2", secret)
	rec2 := httptest.NewRecorder()
	require.NoError(t, h.GitHubWebhook(echo.New().NewContext(req2, rec2)))
	assert.Equal(t, http.StatusUnauthorized, rec2.Code, "disabled integration + no env fallback must refuse, not silently accept")
	var refusal map[string]any
	require.NoError(t, json.Unmarshal(rec2.Body.Bytes(), &refusal))
	assert.Contains(t, refusal["message"], "disabled", "refusal must name the reason")
	assert.Len(t, svc.handleCalls, 1, "service must NOT have been invoked while disabled")

	// --- AC3: re-enable → channel alive again, proving AC2 wasn't just "it
	// never worked at all".
	repo.row.IsActive = true
	req3 := newPullRequestRequest(t, body, "delivery-3", secret)
	rec3 := httptest.NewRecorder()
	require.NoError(t, h.GitHubWebhook(echo.New().NewContext(req3, rec3)))
	assert.Equal(t, http.StatusOK, rec3.Code, "re-enabling must restore delivery")
	assert.Len(t, svc.handleCalls, 2, "service must have been invoked again after re-enable")
}

// TestGitHubWebhook_DynamicResolver_DisabledRow_WithEnvAlsoConfigured_MustRefuse
// is the case TestGitHubWebhook_DynamicResolver_ToggleSequence's AC2 leg
// does NOT cover: that test runs with VCSEnvFallback{} (no env secret at
// all), so "disabled row → refuse" was proven only for the case where env
// had nothing to fall back to anyway. On real prod config
// (MESH_GITHUB_WEBHOOK_SECRET set), a disabled workspace row silently kept
// validating via the env secret — GitHubWebhookSecrets/ResolveGitHub folded
// "no row" and "row present but is_active=false" into the same "nothing
// usable, try env" outcome. Reproduced red against the pre-fix code
// (expected 401, got 200) before the decodeGitHub/GitLabIntegration +
// GitHubWebhookSecrets/GitLabWebhookSecrets fix landed; asserts the fixed
// contract now.
func TestGitHubWebhook_DynamicResolver_DisabledRow_WithEnvAlsoConfigured_MustRefuse(t *testing.T) {
	const wsSecret, envSecret = "workspace-secret", "env-secret"
	repo := &fakeResolverIntegrationRepo{row: &domain.IntegrationConfig{
		Provider: domain.IntegrationProviderGitHub,
		IsActive: false, // explicitly turned off
		Config:   githubIntegrationCfgJSON(t, "", wsSecret),
	}}
	resolver := service.NewVCSIntegrationResolver(repo, service.VCSEnvFallback{GitHubWebhookSecret: envSecret})

	svc := &stubVCSLinkService{handleResult: service.PRHandleResult{Transitioned: true}}
	h := NewVCSLinkHandler(svc, WithVCSIntegrationResolver(resolver), WithWebhookDedupStore(newMemDedupStore()))

	body := newPullRequestPayload(t, "closed", 4, "MESH-"+uuid.New().String(), "", true, "sha4")

	// Neither the (now-revoked) workspace secret nor the env secret must
	// validate — the row exists and says off, and env is not a fallback
	// once a workspace has taken ownership of this provider.
	for _, secret := range []string{wsSecret, envSecret} {
		req := newPullRequestRequest(t, body, "delivery-disabled-"+secret, secret)
		rec := httptest.NewRecorder()
		require.NoError(t, h.GitHubWebhook(echo.New().NewContext(req, rec)))
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "disabled workspace row must refuse even with %q even though env is configured", secret)
	}
	assert.Empty(t, svc.handleCalls, "service must never have been invoked")
}

// TestGitLabWebhook_DynamicResolver_DisabledRow_WithEnvAlsoConfigured_MustRefuse
// is TestGitHubWebhook_DynamicResolver_DisabledRow_WithEnvAlsoConfigured_MustRefuse's
// GitLab counterpart.
func TestGitLabWebhook_DynamicResolver_DisabledRow_WithEnvAlsoConfigured_MustRefuse(t *testing.T) {
	const wsSecret, envSecret = "gl-workspace-secret", "gl-env-secret"
	repo := &fakeResolverIntegrationRepo{row: &domain.IntegrationConfig{
		Provider: domain.IntegrationProviderGitLab,
		IsActive: false,
		Config:   gitlabIntegrationCfgJSON(t, "https://git.entire.host", "", wsSecret),
	}}
	resolver := service.NewVCSIntegrationResolver(repo, service.VCSEnvFallback{GitLabBaseURL: "https://git.entire.host", GitLabToken: "tok", GitLabWebhookSecret: envSecret})

	svc := &stubVCSLinkService{gitlabHandleResult: service.PRHandleResult{Transitioned: true}}
	h := NewVCSLinkHandler(svc, WithVCSIntegrationResolver(resolver))

	body := newMergeRequestPayload(t, "merge", 4, "MESH-"+uuid.New().String(), "", "merged", "sha4")

	for _, secret := range []string{wsSecret, envSecret} {
		req := newMergeRequestRequest(t, body, "Merge Request Hook", secret)
		rec := httptest.NewRecorder()
		require.NoError(t, h.GitLabWebhook(echo.New().NewContext(req, rec)))
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "disabled workspace row must refuse even with %q even though env is configured", secret)
	}
	assert.Empty(t, svc.gitlabHandleCalls, "service must never have been invoked")
}

// AC4 (partial — see task_service_test.go for the live-check half): a
// workspace's own secret must be honored even when the request presents it
// and env carries a DIFFERENT (or no) secret — i.e. the resolved value is
// what governs, not a leftover static one.
func TestGitHubWebhook_DynamicResolver_WorkspaceSecretWinsOverEnv(t *testing.T) {
	repo := &fakeResolverIntegrationRepo{row: &domain.IntegrationConfig{
		Provider: domain.IntegrationProviderGitHub,
		IsActive: true,
		Config:   githubIntegrationCfgJSON(t, "", "workspace-secret"),
	}}
	resolver := service.NewVCSIntegrationResolver(repo, service.VCSEnvFallback{GitHubWebhookSecret: "env-secret"})

	svc := &stubVCSLinkService{handleResult: service.PRHandleResult{Transitioned: true}}
	h := NewVCSLinkHandler(svc, WithVCSIntegrationResolver(resolver), WithWebhookDedupStore(newMemDedupStore()))

	body := newPullRequestPayload(t, "closed", 2, "MESH-"+uuid.New().String(), "", true, "sha2")

	// Signed with env's secret — must be REJECTED, because the active
	// workspace row governs wholly (§4: no smearing).
	reqEnv := newPullRequestRequest(t, body, "delivery-env", "env-secret")
	recEnv := httptest.NewRecorder()
	require.NoError(t, h.GitHubWebhook(echo.New().NewContext(reqEnv, recEnv)))
	assert.Equal(t, http.StatusUnauthorized, recEnv.Code, "env's secret must not validate once a workspace row is active")

	// Signed with the workspace's own secret — must be accepted.
	reqWs := newPullRequestRequest(t, body, "delivery-ws", "workspace-secret")
	recWs := httptest.NewRecorder()
	require.NoError(t, h.GitHubWebhook(echo.New().NewContext(reqWs, recWs)))
	assert.Equal(t, http.StatusOK, recWs.Code)
}

// ---------------------------------------------------------------------------
// GitLab — same sequence, GitLab's plain-token compare instead of HMAC.
// ---------------------------------------------------------------------------
func TestGitLabWebhook_DynamicResolver_ToggleSequence(t *testing.T) {
	const secret = "gl-workspace-secret"
	repo := &fakeResolverIntegrationRepo{row: &domain.IntegrationConfig{
		Provider: domain.IntegrationProviderGitLab,
		IsActive: true,
		Config:   gitlabIntegrationCfgJSON(t, "https://git.entire.host", "", secret),
	}}
	resolver := service.NewVCSIntegrationResolver(repo, service.VCSEnvFallback{})

	svc := &stubVCSLinkService{gitlabHandleResult: service.PRHandleResult{Transitioned: true, NewStatus: "review"}}
	h := NewVCSLinkHandler(svc, WithVCSIntegrationResolver(resolver))

	body := newMergeRequestPayload(t, "merge", 1, "MESH-"+uuid.New().String(), "", "merged", "sha1")

	// AC1: positive control.
	req := newMergeRequestRequest(t, body, "Merge Request Hook", secret)
	rec := httptest.NewRecorder()
	require.NoError(t, h.GitLabWebhook(echo.New().NewContext(req, rec)))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, svc.gitlabHandleCalls, 1)

	// AC2: negative control.
	repo.row.IsActive = false
	req2 := newMergeRequestRequest(t, body, "Merge Request Hook", secret)
	rec2 := httptest.NewRecorder()
	require.NoError(t, h.GitLabWebhook(echo.New().NewContext(req2, rec2)))
	assert.Equal(t, http.StatusUnauthorized, rec2.Code)
	var refusal map[string]any
	require.NoError(t, json.Unmarshal(rec2.Body.Bytes(), &refusal))
	assert.Contains(t, refusal["message"], "disabled")
	assert.Len(t, svc.gitlabHandleCalls, 1, "must not have been invoked while disabled")

	// AC3: re-enable.
	repo.row.IsActive = true
	req3 := newMergeRequestRequest(t, body, "Merge Request Hook", secret)
	rec3 := httptest.NewRecorder()
	require.NoError(t, h.GitLabWebhook(echo.New().NewContext(req3, rec3)))
	assert.Equal(t, http.StatusOK, rec3.Code)
	assert.Len(t, svc.gitlabHandleCalls, 2)
}

// AC6: the existing GitHub path WITHOUT any integration row (the legacy
// static-secret construction, main.go's pre-#33a4bb57 wiring) must behave
// byte-identically — this is covered by the pre-existing test suite
// (TestGitHubWebhook_* / TestGitLabWebhook_NoSecretConfigured_*, unmodified
// by this change and still green). This test adds the ONE case those don't
// cover: a dynamic resolver wired, but resolving to nothing for THIS
// specific workspace while env is configured — i.e. "no row" behaves like
// "no row" always did (falls to env), not like "disabled".
func TestGitHubWebhook_DynamicResolver_NoRowAtAll_FallsToEnvExactlyLikeBefore(t *testing.T) {
	repo := &fakeResolverIntegrationRepo{} // no row stored at all
	const envSecret = "env-only-secret"
	resolver := service.NewVCSIntegrationResolver(repo, service.VCSEnvFallback{GitHubWebhookSecret: envSecret})

	svc := &stubVCSLinkService{handleResult: service.PRHandleResult{Transitioned: true}}
	h := NewVCSLinkHandler(svc, WithVCSIntegrationResolver(resolver), WithWebhookDedupStore(newMemDedupStore()))

	body := newPullRequestPayload(t, "closed", 3, "MESH-"+uuid.New().String(), "", true, "sha3")
	req := newPullRequestRequest(t, body, "delivery-noRow", envSecret)
	rec := httptest.NewRecorder()
	require.NoError(t, h.GitHubWebhook(echo.New().NewContext(req, rec)))
	assert.Equal(t, http.StatusOK, rec.Code, "no workspace row at all must fall through to env, exactly like the pre-#33a4bb57 static wiring")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func githubIntegrationCfgJSON(t *testing.T, token, webhookSecret string) []byte {
	t.Helper()
	b, err := json.Marshal(service.GitHubIntegrationConfig{Token: token, WebhookSecret: webhookSecret})
	require.NoError(t, err)
	return b
}

func gitlabIntegrationCfgJSON(t *testing.T, baseURL, token, webhookSecret string) []byte {
	t.Helper()
	b, err := json.Marshal(service.GitLabIntegrationConfig{BaseURL: baseURL, Token: token, WebhookSecret: webhookSecret})
	require.NoError(t, err)
	return b
}
