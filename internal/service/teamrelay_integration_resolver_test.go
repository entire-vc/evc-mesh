package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Reuses fakeVCSIntegrationRepo (vcs_integration_resolver_test.go, same
// package) — it is a generic (workspace, provider)-keyed
// repository.IntegrationRepository fake, not GitHub/GitLab-specific.

func teamRelayCfg(relayURL string) json.RawMessage {
	raw, _ := json.Marshal(TeamRelayIntegrationConfig{RelayURL: relayURL})
	return raw
}

// ---------------------------------------------------------------------------
// decodeTeamRelayIntegration
// ---------------------------------------------------------------------------

func TestDecodeTeamRelayIntegration_NilOrWrongProvider_NotUsableNotDisabled(t *testing.T) {
	if _, usable, disabled := decodeTeamRelayIntegration(nil); usable || disabled {
		t.Fatalf("nil config must be neither usable nor disabled, got usable=%v disabled=%v", usable, disabled)
	}
	wrongProvider := &domain.IntegrationConfig{Provider: domain.IntegrationProviderSlack, IsActive: true, Config: teamRelayCfg("https://cp.tr.entire.vc")}
	if _, usable, disabled := decodeTeamRelayIntegration(wrongProvider); usable || disabled {
		t.Fatalf("wrong-provider row must be neither usable nor disabled, got usable=%v disabled=%v", usable, disabled)
	}
}

// An inactive row is a THIRD outcome distinct from "no row" — usable=false
// AND disabled=true, so callers refuse instead of falling through to env
// (#33a4bb57 AC2, same fix already applied to decodeGitHubIntegration /
// decodeGitLabIntegration — a workspace turning Team Relay off must actually
// turn it off, not silently keep working via the instance-wide fallback).
func TestDecodeTeamRelayIntegration_InactiveRow_DisabledNotUsable(t *testing.T) {
	inactive := &domain.IntegrationConfig{Provider: domain.IntegrationProviderTeamRelay, IsActive: false, Config: teamRelayCfg("https://cp.tr.entire.vc")}
	_, usable, disabled := decodeTeamRelayIntegration(inactive)
	if usable {
		t.Fatal("inactive row must not be usable")
	}
	if !disabled {
		t.Fatal("inactive row must be reported as disabled, not folded into the same outcome as a missing row")
	}
}

// An active row with a fully empty config (or an explicit empty relay_url)
// must be treated as "no row" (not usable, not disabled) — mirrors
// decodeGitHubIntegration's placeholder guard: a pre-existing active-but-empty
// row must not shadow a working env fallback.
func TestDecodeTeamRelayIntegration_ActiveButEmptyURL_NotUsableNotDisabled(t *testing.T) {
	cfg := &domain.IntegrationConfig{Provider: domain.IntegrationProviderTeamRelay, IsActive: true, Config: json.RawMessage(`{}`)}
	if _, usable, disabled := decodeTeamRelayIntegration(cfg); usable || disabled {
		t.Fatalf("an active row with an empty relay_url must fall to env, not disable: usable=%v disabled=%v", usable, disabled)
	}
}

func TestDecodeTeamRelayIntegration_UsableRow(t *testing.T) {
	cfg := &domain.IntegrationConfig{Provider: domain.IntegrationProviderTeamRelay, IsActive: true, Config: teamRelayCfg("https://cp.tr.entire.vc")}
	parsed, usable, disabled := decodeTeamRelayIntegration(cfg)
	if !usable || disabled {
		t.Fatalf("row with a relay_url must be usable and not disabled: usable=%v disabled=%v", usable, disabled)
	}
	if parsed.RelayURL != "https://cp.tr.entire.vc" {
		t.Fatalf("got %+v", parsed)
	}
}

// ---------------------------------------------------------------------------
// TeamRelayIntegrationResolver.ResolveRelayURL — §4 order
// ---------------------------------------------------------------------------

func TestTeamRelayIntegrationResolver_WorkspaceRowWinsOverEnv(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderTeamRelay, IsActive: true, Config: teamRelayCfg("https://workspace.tr.example")})

	r := NewTeamRelayIntegrationResolver(repo, TeamRelayEnvFallback{RelayURL: "https://env.tr.example"})
	relayURL, source, ok := r.ResolveRelayURL(context.Background(), ws)
	if !ok || source != "workspace" {
		t.Fatalf("expected workspace source, got source=%q ok=%v", source, ok)
	}
	if relayURL != "https://workspace.tr.example" {
		t.Fatalf("row must win wholly, no smearing with env: got %q", relayURL)
	}
}

func TestTeamRelayIntegrationResolver_NoRow_FallsToEnv(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo() // nothing stored for ws
	r := NewTeamRelayIntegrationResolver(repo, TeamRelayEnvFallback{RelayURL: "https://env.tr.example"})

	relayURL, source, ok := r.ResolveRelayURL(context.Background(), ws)
	if !ok || source != "env" || relayURL != "https://env.tr.example" {
		t.Fatalf("got relayURL=%q source=%q ok=%v", relayURL, source, ok)
	}
}

// A row that exists and explicitly says is_active=false must disable the
// provider for this workspace OUTRIGHT — env is not consulted, even though
// env has something usable. Falling through to env here is the exact
// #33a4bb57 AC2 defect: a workspace turning Team Relay off would silently
// keep working off the instance-wide fallback. (This test used to assert
// the opposite — "InactiveRow_FallsToEnv" — which locked the bug in as
// expected behavior; corrected together with the fix.)
func TestTeamRelayIntegrationResolver_InactiveRow_EnvConfigured_StillDisabled(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderTeamRelay, IsActive: false, Config: teamRelayCfg("https://workspace.tr.example")})
	r := NewTeamRelayIntegrationResolver(repo, TeamRelayEnvFallback{RelayURL: "https://env.tr.example"})

	relayURL, source, ok := r.ResolveRelayURL(context.Background(), ws)
	if ok || source != "" || relayURL != "" {
		t.Fatalf("is_active=false must disable outright, not fall through to env: got relayURL=%q source=%q ok=%v", relayURL, source, ok)
	}
}

// The three-way case that matches the parent spec's §5 acceptance test #2 and
// this task's AC3: row inactive/absent AND no env configured → disabled
// outright, and the caller must show a NAMED refusal rather than proceed
// with an empty URL.
func TestTeamRelayIntegrationResolver_NeitherRowNorEnv_Disabled(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderTeamRelay, IsActive: false, Config: teamRelayCfg("https://workspace.tr.example")})
	r := NewTeamRelayIntegrationResolver(repo, TeamRelayEnvFallback{}) // no env configured

	relayURL, source, ok := r.ResolveRelayURL(context.Background(), ws)
	if ok || source != "" || relayURL != "" {
		t.Fatalf("expected disabled (ok=false, source=\"\", relayURL=\"\"), got relayURL=%q source=%q ok=%v", relayURL, source, ok)
	}
}

// A placeholder row (active=true, config={}) must not shadow a working env
// value — the team_relay analogue of #33a4bb57's AC6 regression guard.
func TestTeamRelayIntegrationResolver_ActiveEmptyPlaceholderRow_FallsToEnv(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderTeamRelay, IsActive: true, Config: json.RawMessage(`{}`)})
	r := NewTeamRelayIntegrationResolver(repo, TeamRelayEnvFallback{RelayURL: "https://env.tr.example"})

	relayURL, source, ok := r.ResolveRelayURL(context.Background(), ws)
	if !ok || source != "env" || relayURL != "https://env.tr.example" {
		t.Fatalf("got relayURL=%q source=%q ok=%v", relayURL, source, ok)
	}
}

func TestTeamRelayIntegrationResolver_RepoErrorFallsToEnv(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.getErr = errors.New("db unavailable")
	r := NewTeamRelayIntegrationResolver(repo, TeamRelayEnvFallback{RelayURL: "https://env.tr.example"})

	relayURL, source, ok := r.ResolveRelayURL(context.Background(), ws)
	if !ok || source != "env" || relayURL != "https://env.tr.example" {
		t.Fatalf("a repo lookup error must fall back to env, not disable: got relayURL=%q source=%q ok=%v", relayURL, source, ok)
	}
}

// repo == nil (a caller that only ever wants env) must not panic and must
// resolve straight to env — the same contract NewVCSIntegrationResolver
// documents for a nil repo.
func TestTeamRelayIntegrationResolver_NilRepo_FallsToEnv(t *testing.T) {
	r := NewTeamRelayIntegrationResolver(nil, TeamRelayEnvFallback{RelayURL: "https://env.tr.example"})
	relayURL, source, ok := r.ResolveRelayURL(context.Background(), uuid.New())
	if !ok || source != "env" || relayURL != "https://env.tr.example" {
		t.Fatalf("got relayURL=%q source=%q ok=%v", relayURL, source, ok)
	}
}

func TestTeamRelayIntegrationResolver_NilRepoAndNoEnv_Disabled(t *testing.T) {
	r := NewTeamRelayIntegrationResolver(nil, TeamRelayEnvFallback{})
	relayURL, source, ok := r.ResolveRelayURL(context.Background(), uuid.New())
	if ok || source != "" || relayURL != "" {
		t.Fatalf("expected disabled, got relayURL=%q source=%q ok=%v", relayURL, source, ok)
	}
}
