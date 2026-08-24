package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// fakeVCSIntegrationRepo is a minimal repository.IntegrationRepository stub
// for VCSIntegrationResolver's tests — unlike fakeIntegrationRepo
// (slack_service_test.go, single fixed config regardless of workspace/
// provider), this one is keyed by (workspace, provider) and supports
// multiple active rows for ListActiveByProvider, since the resolver's
// webhook-secret union path genuinely needs more than one.
type fakeVCSIntegrationRepo struct {
	byWorkspaceProvider map[string]*domain.IntegrationConfig
	activeByProvider    map[domain.IntegrationProvider][]domain.IntegrationConfig
	getErr              error
	listErr             error
}

func newFakeVCSIntegrationRepo() *fakeVCSIntegrationRepo {
	return &fakeVCSIntegrationRepo{
		byWorkspaceProvider: make(map[string]*domain.IntegrationConfig),
		activeByProvider:    make(map[domain.IntegrationProvider][]domain.IntegrationConfig),
	}
}

func (f *fakeVCSIntegrationRepo) put(workspaceID uuid.UUID, cfg domain.IntegrationConfig) {
	cfg.WorkspaceID = workspaceID
	f.byWorkspaceProvider[workspaceID.String()+"|"+string(cfg.Provider)] = &cfg
	if cfg.IsActive {
		f.activeByProvider[cfg.Provider] = append(f.activeByProvider[cfg.Provider], cfg)
	}
}

func (f *fakeVCSIntegrationRepo) Upsert(context.Context, *domain.IntegrationConfig) error {
	return nil
}
func (f *fakeVCSIntegrationRepo) GetByID(context.Context, uuid.UUID) (*domain.IntegrationConfig, error) {
	return nil, nil
}
func (f *fakeVCSIntegrationRepo) GetByProvider(_ context.Context, workspaceID uuid.UUID, provider domain.IntegrationProvider) (*domain.IntegrationConfig, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.byWorkspaceProvider[workspaceID.String()+"|"+string(provider)], nil
}
func (f *fakeVCSIntegrationRepo) Update(context.Context, uuid.UUID, domain.UpdateIntegrationInput) (*domain.IntegrationConfig, error) {
	return nil, nil
}
func (f *fakeVCSIntegrationRepo) Delete(context.Context, uuid.UUID) error { return nil }
func (f *fakeVCSIntegrationRepo) ListByWorkspace(context.Context, uuid.UUID) ([]domain.IntegrationConfig, error) {
	return nil, nil
}
func (f *fakeVCSIntegrationRepo) ListActiveByProvider(_ context.Context, provider domain.IntegrationProvider) ([]domain.IntegrationConfig, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.activeByProvider[provider], nil
}

func githubCfg(token, webhookSecret string) json.RawMessage {
	raw, _ := json.Marshal(GitHubIntegrationConfig{Token: token, WebhookSecret: webhookSecret})
	return raw
}

func gitlabCfg(baseURL, token, webhookSecret string) json.RawMessage {
	raw, _ := json.Marshal(GitLabIntegrationConfig{BaseURL: baseURL, Token: token, WebhookSecret: webhookSecret})
	return raw
}

// ---------------------------------------------------------------------------
// decodeGitHubIntegration / decodeGitLabIntegration
// ---------------------------------------------------------------------------

func TestDecodeGitHubIntegration_NilOrInactiveOrWrongProvider(t *testing.T) {
	if _, ok := decodeGitHubIntegration(nil); ok {
		t.Fatal("nil config must not be usable")
	}
	inactive := &domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: false, Config: githubCfg("tok", "sec")}
	if _, ok := decodeGitHubIntegration(inactive); ok {
		t.Fatal("inactive row must not be usable")
	}
	wrongProvider := &domain.IntegrationConfig{Provider: domain.IntegrationProviderGitLab, IsActive: true, Config: githubCfg("tok", "sec")}
	if _, ok := decodeGitHubIntegration(wrongProvider); ok {
		t.Fatal("wrong-provider row must not be usable")
	}
}

// An active row with a fully empty config must be treated as "no row" — see
// decodeGitHubIntegration's doc comment: this is what keeps a pre-existing
// placeholder row (active=true, config={}) from silently shadowing env.
func TestDecodeGitHubIntegration_ActiveButEmptyConfig_NotUsable(t *testing.T) {
	cfg := &domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: true, Config: json.RawMessage(`{}`)}
	if _, ok := decodeGitHubIntegration(cfg); ok {
		t.Fatal("an active row with neither token nor webhook_secret must not be usable")
	}
}

func TestDecodeGitHubIntegration_UsableRow_RoundTripsThroughEncryption(t *testing.T) {
	cfg := &domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: true, Config: githubCfg("plaintext-in-test-env", "whsec_123")}
	parsed, ok := decodeGitHubIntegration(cfg)
	if !ok {
		t.Fatal("row with a token must be usable")
	}
	if parsed.Token != "plaintext-in-test-env" || parsed.WebhookSecret != "whsec_123" {
		t.Fatalf("got %+v", parsed)
	}
}

// A row that supplies ONLY a webhook secret (no live-check token) is a
// legitimate configuration — see the resolver's checker-construction gate,
// which separately requires Token!="" before building a live-check client.
func TestDecodeGitHubIntegration_WebhookSecretOnly_Usable(t *testing.T) {
	cfg := &domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: true, Config: githubCfg("", "whsec_only")}
	parsed, ok := decodeGitHubIntegration(cfg)
	if !ok || parsed.WebhookSecret != "whsec_only" || parsed.Token != "" {
		t.Fatalf("got parsed=%+v ok=%v", parsed, ok)
	}
}

func TestDecodeGitLabIntegration_ActiveButEmptyConfig_NotUsable(t *testing.T) {
	cfg := &domain.IntegrationConfig{Provider: domain.IntegrationProviderGitLab, IsActive: true, Config: json.RawMessage(`{}`)}
	if _, ok := decodeGitLabIntegration(cfg); ok {
		t.Fatal("an active row with no base_url/token/webhook_secret must not be usable")
	}
}

func TestDecodeGitLabIntegration_UsableRow(t *testing.T) {
	cfg := &domain.IntegrationConfig{Provider: domain.IntegrationProviderGitLab, IsActive: true, Config: gitlabCfg("https://git.entire.host", "glpat-xxx", "whsec")}
	parsed, ok := decodeGitLabIntegration(cfg)
	if !ok {
		t.Fatal("expected usable")
	}
	if parsed.BaseURL != "https://git.entire.host" || parsed.Token != "glpat-xxx" || parsed.WebhookSecret != "whsec" {
		t.Fatalf("got %+v", parsed)
	}
}

// ---------------------------------------------------------------------------
// VCSIntegrationResolver.ResolveGitHub / ResolveGitLab — §4 order
// ---------------------------------------------------------------------------

func TestVCSIntegrationResolver_ResolveGitHub_WorkspaceRowWinsWhollyOverEnv(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: true, Config: githubCfg("workspace-token", "workspace-secret")})

	r := NewVCSIntegrationResolver(repo, VCSEnvFallback{GitHubToken: "env-token", GitHubWebhookSecret: "env-secret"})
	cfg, source, ok := r.ResolveGitHub(context.Background(), ws)
	if !ok || source != "workspace" {
		t.Fatalf("expected workspace source, got source=%q ok=%v", source, ok)
	}
	if cfg.Token != "workspace-token" || cfg.WebhookSecret != "workspace-secret" {
		t.Fatalf("row must win WHOLLY, no smearing with env: got %+v", cfg)
	}
}

func TestVCSIntegrationResolver_ResolveGitHub_NoRow_FallsToEnv(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo() // nothing stored for ws
	r := NewVCSIntegrationResolver(repo, VCSEnvFallback{GitHubToken: "env-token", GitHubWebhookSecret: "env-secret"})

	cfg, source, ok := r.ResolveGitHub(context.Background(), ws)
	if !ok || source != "env" || cfg.Token != "env-token" || cfg.WebhookSecret != "env-secret" {
		t.Fatalf("got cfg=%+v source=%q ok=%v", cfg, source, ok)
	}
}

func TestVCSIntegrationResolver_ResolveGitHub_InactiveRow_FallsToEnv(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: false, Config: githubCfg("workspace-token", "workspace-secret")})
	r := NewVCSIntegrationResolver(repo, VCSEnvFallback{GitHubToken: "env-token", GitHubWebhookSecret: "env-secret"})

	cfg, source, ok := r.ResolveGitHub(context.Background(), ws)
	if !ok || source != "env" || cfg.Token != "env-token" {
		t.Fatalf("is_active=false must fall through to env, got cfg=%+v source=%q ok=%v", cfg, source, ok)
	}
}

// The three-way case that maps directly onto the parent spec's §5 acceptance
// test #2: row inactive AND no env configured → disabled outright, not a
// silent open door.
func TestVCSIntegrationResolver_ResolveGitHub_NeitherRowNorEnv_Disabled(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: false, Config: githubCfg("workspace-token", "workspace-secret")})
	r := NewVCSIntegrationResolver(repo, VCSEnvFallback{}) // no env configured

	_, source, ok := r.ResolveGitHub(context.Background(), ws)
	if ok || source != "" {
		t.Fatalf("expected disabled (ok=false, source=\"\"), got source=%q ok=%v", source, ok)
	}
}

// A placeholder row identical in shape to the pre-#33a4bb57 prod row
// (active=true, config={}) must not shadow a working env secret — this is
// the exact regression AC6 of #33a4bb57 guards against.
func TestVCSIntegrationResolver_ResolveGitHub_ActiveEmptyPlaceholderRow_FallsToEnv(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: true, Config: json.RawMessage(`{}`)})
	r := NewVCSIntegrationResolver(repo, VCSEnvFallback{GitHubToken: "env-token", GitHubWebhookSecret: "env-secret"})

	cfg, source, ok := r.ResolveGitHub(context.Background(), ws)
	if !ok || source != "env" || cfg.Token != "env-token" {
		t.Fatalf("got cfg=%+v source=%q ok=%v", cfg, source, ok)
	}
}

func TestVCSIntegrationResolver_ResolveGitLab_RequiresBothURLAndToken_FromEnv(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	// Only URL set, no token — env fallback must not activate (mirrors the
	// pre-#33a4bb57 `cfg.Webhook.GitLabURL != "" && cfg.Webhook.GitLabToken != ""` gate).
	r := NewVCSIntegrationResolver(repo, VCSEnvFallback{GitLabBaseURL: "https://git.entire.host"})
	_, _, ok := r.ResolveGitLab(context.Background(), ws)
	if ok {
		t.Fatal("env fallback with URL but no token must not resolve")
	}

	r2 := NewVCSIntegrationResolver(repo, VCSEnvFallback{GitLabBaseURL: "https://git.entire.host", GitLabToken: "tok"})
	cfg, source, ok := r2.ResolveGitLab(context.Background(), ws)
	if !ok || source != "env" || cfg.BaseURL != "https://git.entire.host" || cfg.Token != "tok" {
		t.Fatalf("got cfg=%+v source=%q ok=%v", cfg, source, ok)
	}
}

func TestVCSIntegrationResolver_ResolveGitHub_RepoErrorFallsToEnv(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.getErr = errors.New("db unavailable")
	r := NewVCSIntegrationResolver(repo, VCSEnvFallback{GitHubToken: "env-token"})
	cfg, source, ok := r.ResolveGitHub(context.Background(), ws)
	if !ok || source != "env" || cfg.Token != "env-token" {
		t.Fatalf("a repo lookup error must fall back to env, not disable: got cfg=%+v source=%q ok=%v", cfg, source, ok)
	}
}

// ---------------------------------------------------------------------------
// GitHubWebhookSecrets / GitLabWebhookSecrets — union across workspaces
// ---------------------------------------------------------------------------

func TestVCSIntegrationResolver_GitHubWebhookSecrets_UnionOfActiveWorkspaces(t *testing.T) {
	wsA, wsB := uuid.New(), uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(wsA, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: true, Config: githubCfg("tokA", "secretA")})
	repo.put(wsB, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: true, Config: githubCfg("tokB", "secretB")})
	r := NewVCSIntegrationResolver(repo, VCSEnvFallback{GitHubWebhookSecret: "env-secret"})

	secrets, source := r.GitHubWebhookSecrets(context.Background())
	if source != "workspace" {
		t.Fatalf("expected workspace source with active rows present, got %q", source)
	}
	got := map[string]bool{}
	for _, s := range secrets {
		got[s] = true
	}
	if len(got) != 2 || !got["secretA"] || !got["secretB"] {
		t.Fatalf("expected union {secretA, secretB}, got %v", secrets)
	}
}

func TestVCSIntegrationResolver_GitHubWebhookSecrets_NoActiveRows_FallsToEnv(t *testing.T) {
	repo := newFakeVCSIntegrationRepo()
	r := NewVCSIntegrationResolver(repo, VCSEnvFallback{GitHubWebhookSecret: "env-secret"})
	secrets, source := r.GitHubWebhookSecrets(context.Background())
	if source != "env" || len(secrets) != 1 || secrets[0] != "env-secret" {
		t.Fatalf("got secrets=%v source=%q", secrets, source)
	}
}

// This is the exact scenario the parent spec's §5 AC2 exercises: toggling a
// row's is_active off, with nothing else configured, must resolve to
// "disabled" (empty secrets, empty source) — the handler layer turns that
// into a refusal with a named reason, never a silent 200.
func TestVCSIntegrationResolver_GitHubWebhookSecrets_ToggledOff_NoEnv_Disabled(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: false, Config: githubCfg("tok", "sec")})
	r := NewVCSIntegrationResolver(repo, VCSEnvFallback{})

	secrets, source := r.GitHubWebhookSecrets(context.Background())
	if len(secrets) != 0 || source != "" {
		t.Fatalf("expected disabled, got secrets=%v source=%q", secrets, source)
	}
}

func TestVCSIntegrationResolver_GitLabWebhookSecrets_UnionOfActiveWorkspaces(t *testing.T) {
	wsA, wsB := uuid.New(), uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(wsA, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitLab, IsActive: true, Config: gitlabCfg("https://git.entire.host", "", "secretA")})
	repo.put(wsB, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitLab, IsActive: true, Config: gitlabCfg("https://gitlab.other", "", "secretB")})
	r := NewVCSIntegrationResolver(repo, VCSEnvFallback{})

	secrets, source := r.GitLabWebhookSecrets(context.Background())
	if source != "workspace" || len(secrets) != 2 {
		t.Fatalf("got secrets=%v source=%q", secrets, source)
	}
}
