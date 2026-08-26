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
	byProvider          map[domain.IntegrationProvider][]domain.IntegrationConfig
	getErr              error
	listErr             error
}

func newFakeVCSIntegrationRepo() *fakeVCSIntegrationRepo {
	return &fakeVCSIntegrationRepo{
		byWorkspaceProvider: make(map[string]*domain.IntegrationConfig),
		activeByProvider:    make(map[domain.IntegrationProvider][]domain.IntegrationConfig),
		byProvider:          make(map[domain.IntegrationProvider][]domain.IntegrationConfig),
	}
}

func (f *fakeVCSIntegrationRepo) put(workspaceID uuid.UUID, cfg domain.IntegrationConfig) {
	cfg.WorkspaceID = workspaceID
	f.byWorkspaceProvider[workspaceID.String()+"|"+string(cfg.Provider)] = &cfg
	if cfg.IsActive {
		f.activeByProvider[cfg.Provider] = append(f.activeByProvider[cfg.Provider], cfg)
	}
	f.byProvider[cfg.Provider] = append(f.byProvider[cfg.Provider], cfg)
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
func (f *fakeVCSIntegrationRepo) ListByProvider(_ context.Context, provider domain.IntegrationProvider) ([]domain.IntegrationConfig, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.byProvider[provider], nil
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

func TestDecodeGitHubIntegration_NilOrWrongProvider_NotUsableNotDisabled(t *testing.T) {
	if _, usable, disabled := decodeGitHubIntegration(nil); usable || disabled {
		t.Fatalf("nil config must be neither usable nor disabled, got usable=%v disabled=%v", usable, disabled)
	}
	wrongProvider := &domain.IntegrationConfig{Provider: domain.IntegrationProviderGitLab, IsActive: true, Config: githubCfg("tok", "sec")}
	if _, usable, disabled := decodeGitHubIntegration(wrongProvider); usable || disabled {
		t.Fatalf("wrong-provider row must be neither usable nor disabled, got usable=%v disabled=%v", usable, disabled)
	}
}

// An inactive row is a THIRD outcome distinct from "no row" — usable=false
// AND disabled=true, so callers refuse instead of falling through to env
// (#33a4bb57 AC2; this is the exact distinction the fix adds).
func TestDecodeGitHubIntegration_InactiveRow_DisabledNotUsable(t *testing.T) {
	inactive := &domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: false, Config: githubCfg("tok", "sec")}
	_, usable, disabled := decodeGitHubIntegration(inactive)
	if usable {
		t.Fatal("inactive row must not be usable")
	}
	if !disabled {
		t.Fatal("inactive row must be reported as disabled, not folded into the same outcome as a missing row")
	}
}

// An active row with a fully empty config must be treated as "no row" (not
// usable, not disabled) — see decodeGitHubIntegration's doc comment: this is
// what keeps a pre-existing placeholder row (active=true, config={}) from
// silently shadowing env.
func TestDecodeGitHubIntegration_ActiveButEmptyConfig_NotUsableNotDisabled(t *testing.T) {
	cfg := &domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: true, Config: json.RawMessage(`{}`)}
	if _, usable, disabled := decodeGitHubIntegration(cfg); usable || disabled {
		t.Fatalf("an active row with neither token nor webhook_secret must fall to env, not disable: usable=%v disabled=%v", usable, disabled)
	}
}

func TestDecodeGitHubIntegration_UsableRow_RoundTripsThroughEncryption(t *testing.T) {
	cfg := &domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: true, Config: githubCfg("plaintext-in-test-env", "whsec_123")}
	parsed, usable, disabled := decodeGitHubIntegration(cfg)
	if !usable || disabled {
		t.Fatalf("row with a token must be usable and not disabled: usable=%v disabled=%v", usable, disabled)
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
	parsed, usable, disabled := decodeGitHubIntegration(cfg)
	if !usable || disabled || parsed.WebhookSecret != "whsec_only" || parsed.Token != "" {
		t.Fatalf("got parsed=%+v usable=%v disabled=%v", parsed, usable, disabled)
	}
}

func TestDecodeGitLabIntegration_ActiveButEmptyConfig_NotUsableNotDisabled(t *testing.T) {
	cfg := &domain.IntegrationConfig{Provider: domain.IntegrationProviderGitLab, IsActive: true, Config: json.RawMessage(`{}`)}
	if _, usable, disabled := decodeGitLabIntegration(cfg); usable || disabled {
		t.Fatalf("an active row with no base_url/token/webhook_secret must fall to env, not disable: usable=%v disabled=%v", usable, disabled)
	}
}

func TestDecodeGitLabIntegration_InactiveRow_DisabledNotUsable(t *testing.T) {
	inactive := &domain.IntegrationConfig{Provider: domain.IntegrationProviderGitLab, IsActive: false, Config: gitlabCfg("https://git.entire.host", "tok", "sec")}
	_, usable, disabled := decodeGitLabIntegration(inactive)
	if usable || !disabled {
		t.Fatalf("inactive row must be disabled, not usable: usable=%v disabled=%v", usable, disabled)
	}
}

func TestDecodeGitLabIntegration_UsableRow(t *testing.T) {
	cfg := &domain.IntegrationConfig{Provider: domain.IntegrationProviderGitLab, IsActive: true, Config: gitlabCfg("https://git.entire.host", "glpat-xxx", "whsec")}
	parsed, usable, disabled := decodeGitLabIntegration(cfg)
	if !usable || disabled {
		t.Fatalf("expected usable, not disabled: usable=%v disabled=%v", usable, disabled)
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

// A row that exists and explicitly says is_active=false must disable the
// provider for this workspace OUTRIGHT — env is not consulted, even though
// env has something usable. Falling through to env here is the exact
// #33a4bb57 AC2 defect: a workspace turning its integration off would
// silently keep working off the instance-wide fallback. (This test used to
// assert the opposite — "InactiveRow_FallsToEnv" — which locked the bug in
// as expected behavior; corrected together with the fix.)
func TestVCSIntegrationResolver_ResolveGitHub_InactiveRow_EnvConfigured_StillDisabled(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: false, Config: githubCfg("workspace-token", "workspace-secret")})
	r := NewVCSIntegrationResolver(repo, VCSEnvFallback{GitHubToken: "env-token", GitHubWebhookSecret: "env-secret"})

	cfg, source, ok := r.ResolveGitHub(context.Background(), ws)
	if ok || source != "" {
		t.Fatalf("is_active=false must disable outright, not fall through to env: got cfg=%+v source=%q ok=%v", cfg, source, ok)
	}
}

// GitLab counterpart of the above.
func TestVCSIntegrationResolver_ResolveGitLab_InactiveRow_EnvConfigured_StillDisabled(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitLab, IsActive: false, Config: gitlabCfg("https://git.entire.host", "workspace-token", "workspace-secret")})
	r := NewVCSIntegrationResolver(repo, VCSEnvFallback{GitLabBaseURL: "https://git.entire.host", GitLabToken: "env-token", GitLabWebhookSecret: "env-secret"})

	cfg, source, ok := r.ResolveGitLab(context.Background(), ws)
	if ok || source != "" {
		t.Fatalf("is_active=false must disable outright, not fall through to env: got cfg=%+v source=%q ok=%v", cfg, source, ok)
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

// The realistic prod case #33a4bb57 AC2 is actually about: env IS
// configured (as it is on prod — MESH_GITHUB_WEBHOOK_SECRET), and a
// workspace toggles its own row off. ListActiveByProvider alone can't tell
// this apart from "no workspace ever configured GitHub" (both come back as
// zero active rows), so the pre-fix code fell through to env and kept
// validating webhooks off the instance-wide secret even though the
// workspace explicitly disabled its integration. Reproduced red against the
// pre-fix code (secrets=[env-secret], source="env") before the
// ListByProvider-based fix landed.
func TestVCSIntegrationResolver_GitHubWebhookSecrets_ToggledOff_WithEnvConfigured_StillDisabled(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: false, Config: githubCfg("tok", "sec")})
	r := NewVCSIntegrationResolver(repo, VCSEnvFallback{GitHubWebhookSecret: "env-secret"})

	secrets, source := r.GitHubWebhookSecrets(context.Background())
	if len(secrets) != 0 || source != "" {
		t.Fatalf("a disabled workspace row must not fall through to env even though env is configured: got secrets=%v source=%q", secrets, source)
	}
}

// GitLab counterpart.
func TestVCSIntegrationResolver_GitLabWebhookSecrets_ToggledOff_WithEnvConfigured_StillDisabled(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitLab, IsActive: false, Config: gitlabCfg("https://git.entire.host", "", "sec")})
	r := NewVCSIntegrationResolver(repo, VCSEnvFallback{GitLabBaseURL: "https://git.entire.host", GitLabToken: "tok", GitLabWebhookSecret: "env-secret"})

	secrets, source := r.GitLabWebhookSecrets(context.Background())
	if len(secrets) != 0 || source != "" {
		t.Fatalf("a disabled workspace row must not fall through to env even though env is configured: got secrets=%v source=%q", secrets, source)
	}
}

// The regression this MR itself introduced: a placeholder row (active=true,
// empty config — the exact shape ResolveGitHub/ResolveGitLab already fall
// through to env for, see ..._ActiveEmptyPlaceholderRow_FallsToEnv) must not
// be able to claim the provider away from a perfectly good env secret just
// by existing. Only a row that is usable or explicitly disabled may do that.
func TestVCSIntegrationResolver_GitHubWebhookSecrets_PlaceholderRowOnly_FallsToEnv(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: true, Config: json.RawMessage(`{}`)})
	r := NewVCSIntegrationResolver(repo, VCSEnvFallback{GitHubWebhookSecret: "env-secret"})

	secrets, source := r.GitHubWebhookSecrets(context.Background())
	if source != "env" || len(secrets) != 1 || secrets[0] != "env-secret" {
		t.Fatalf("a placeholder-only row must not blind env fallback: got secrets=%v source=%q", secrets, source)
	}
}

// GitLab counterpart.
func TestVCSIntegrationResolver_GitLabWebhookSecrets_PlaceholderRowOnly_FallsToEnv(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitLab, IsActive: true, Config: json.RawMessage(`{}`)})
	r := NewVCSIntegrationResolver(repo, VCSEnvFallback{GitLabBaseURL: "https://git.entire.host", GitLabToken: "tok", GitLabWebhookSecret: "env-secret"})

	secrets, source := r.GitLabWebhookSecrets(context.Background())
	if source != "env" || len(secrets) != 1 || secrets[0] != "env-secret" {
		t.Fatalf("a placeholder-only row must not blind env fallback: got secrets=%v source=%q", secrets, source)
	}
}

// A placeholder row alongside a genuinely disabled row: the disabled row
// governs (owned=true), so this must still refuse — the placeholder must not
// be able to weaken an explicit disable either.
func TestVCSIntegrationResolver_GitHubWebhookSecrets_PlaceholderPlusDisabledRow_StillDisabled(t *testing.T) {
	wsPlaceholder, wsDisabled := uuid.New(), uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(wsPlaceholder, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: true, Config: json.RawMessage(`{}`)})
	repo.put(wsDisabled, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: false, Config: githubCfg("tok", "sec")})
	r := NewVCSIntegrationResolver(repo, VCSEnvFallback{GitHubWebhookSecret: "env-secret"})

	secrets, source := r.GitHubWebhookSecrets(context.Background())
	if len(secrets) != 0 || source != "" {
		t.Fatalf("an explicitly disabled row must still govern even alongside a placeholder: got secrets=%v source=%q", secrets, source)
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
