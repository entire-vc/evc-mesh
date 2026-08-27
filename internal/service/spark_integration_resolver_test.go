package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func sparkCfg(baseURL string) json.RawMessage {
	raw, _ := json.Marshal(SparkIntegrationConfig{BaseURL: baseURL})
	return raw
}

// ---------------------------------------------------------------------------
// decodeSparkIntegration
// ---------------------------------------------------------------------------

func TestDecodeSparkIntegration_NilOrWrongProvider_NotUsableNotDisabled(t *testing.T) {
	if _, usable, disabled := decodeSparkIntegration(nil); usable || disabled {
		t.Fatalf("nil config must be neither usable nor disabled, got usable=%v disabled=%v", usable, disabled)
	}
	wrongProvider := &domain.IntegrationConfig{Provider: domain.IntegrationProviderSlack, IsActive: true, Config: sparkCfg("https://spark.entire.vc")}
	if _, usable, disabled := decodeSparkIntegration(wrongProvider); usable || disabled {
		t.Fatalf("wrong-provider row must be neither usable nor disabled, got usable=%v disabled=%v", usable, disabled)
	}
}

// An inactive row is a distinct outcome from "no row" — usable=false AND
// disabled=true, so ResolveSparkURL refuses instead of falling through to
// env (§4: "is_active=false означает «выключено», а не «читай env»" — same
// fix already applied to decodeGitHubIntegration/decodeGitLabIntegration).
func TestDecodeSparkIntegration_InactiveRow_DisabledNotUsable(t *testing.T) {
	inactive := &domain.IntegrationConfig{Provider: domain.IntegrationProviderSpark, IsActive: false, Config: sparkCfg("https://spark.entire.vc")}
	_, usable, disabled := decodeSparkIntegration(inactive)
	if usable {
		t.Fatal("inactive row must not be usable")
	}
	if !disabled {
		t.Fatal("inactive row must be reported as disabled, not folded into the same outcome as a missing row")
	}
}

// A placeholder row identical in shape to the two live prod rows this fix
// cleans up (active=true, config={}) must not shadow a working env URL —
// mirrors decodeGitHubIntegration's placeholder guard.
func TestDecodeSparkIntegration_ActiveButEmptyURL_NotUsableNotDisabled(t *testing.T) {
	cfg := &domain.IntegrationConfig{Provider: domain.IntegrationProviderSpark, IsActive: true, Config: json.RawMessage(`{}`)}
	if _, usable, disabled := decodeSparkIntegration(cfg); usable || disabled {
		t.Fatalf("an active row with an empty base_url must fall to env, not disable: usable=%v disabled=%v", usable, disabled)
	}
}

func TestDecodeSparkIntegration_UsableRow(t *testing.T) {
	cfg := &domain.IntegrationConfig{Provider: domain.IntegrationProviderSpark, IsActive: true, Config: sparkCfg("https://spark.entire.vc")}
	parsed, usable, disabled := decodeSparkIntegration(cfg)
	if !usable || disabled {
		t.Fatalf("row with a base_url must be usable and not disabled: usable=%v disabled=%v", usable, disabled)
	}
	if parsed.BaseURL != "https://spark.entire.vc" {
		t.Fatalf("got %+v", parsed)
	}
}

// ---------------------------------------------------------------------------
// SparkIntegrationResolver.ResolveSparkURL
// ---------------------------------------------------------------------------

func TestSparkIntegrationResolver_WorkspaceRowWinsOverEnv(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderSpark, IsActive: true, Config: sparkCfg("https://workspace-spark.example")})

	r := NewSparkIntegrationResolver(repo, SparkEnvFallback{URL: "https://env-spark.example", Enabled: true})
	url, source, ok := r.ResolveSparkURL(context.Background(), ws)
	if !ok || source != "workspace" || url != "https://workspace-spark.example" {
		t.Fatalf("got url=%q source=%q ok=%v", url, source, ok)
	}
}

func TestSparkIntegrationResolver_NoRow_FallsToEnv(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo() // nothing stored for ws
	r := NewSparkIntegrationResolver(repo, SparkEnvFallback{URL: "https://env-spark.example", Enabled: true})

	url, source, ok := r.ResolveSparkURL(context.Background(), ws)
	if !ok || source != "env" || url != "https://env-spark.example" {
		t.Fatalf("got url=%q source=%q ok=%v", url, source, ok)
	}
}

// The exact regression class this whole fix exists to close: a workspace
// that has explicitly turned Spark off must NOT keep working via the
// instance-wide env fallback.
func TestSparkIntegrationResolver_InactiveRow_EnvConfigured_StillDisabled(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderSpark, IsActive: false, Config: sparkCfg("https://workspace-spark.example")})
	r := NewSparkIntegrationResolver(repo, SparkEnvFallback{URL: "https://env-spark.example", Enabled: true})

	url, source, ok := r.ResolveSparkURL(context.Background(), ws)
	if ok || source != "" || url != "" {
		t.Fatalf("is_active=false must disable outright, not fall through to env: got url=%q source=%q ok=%v", url, source, ok)
	}
}

func TestSparkIntegrationResolver_NeitherRowNorEnv_Disabled(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo() // nothing stored, no env
	r := NewSparkIntegrationResolver(repo, SparkEnvFallback{})

	url, source, ok := r.ResolveSparkURL(context.Background(), ws)
	if ok || source != "" || url != "" {
		t.Fatalf("expected disabled (ok=false), got url=%q source=%q ok=%v", url, source, ok)
	}
}

// The two live prod rows this fix cleans up: active=true, config={}. Must
// fall through to env exactly like a missing row would.
func TestSparkIntegrationResolver_ActiveEmptyPlaceholderRow_FallsToEnv(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderSpark, IsActive: true, Config: json.RawMessage(`{}`)})
	r := NewSparkIntegrationResolver(repo, SparkEnvFallback{URL: "https://env-spark.example", Enabled: true})

	url, source, ok := r.ResolveSparkURL(context.Background(), ws)
	if !ok || source != "env" || url != "https://env-spark.example" {
		t.Fatalf("got url=%q source=%q ok=%v", url, source, ok)
	}
}

// MESH_SPARK_ENABLED and MESH_SPARK_URL are two independent env vars —
// pre-C1 behavior only ever served Spark when BOTH were set (routes were
// registered solely on Enabled, but a URL-less client would fail at request
// time anyway). URL-only must not silently start serving Spark for a
// deployment that set the URL in preparation without flipping Enabled.
func TestSparkIntegrationResolver_EnvURLWithoutEnabled_Disabled(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	r := NewSparkIntegrationResolver(repo, SparkEnvFallback{URL: "https://env-spark.example", Enabled: false})

	url, source, ok := r.ResolveSparkURL(context.Background(), ws)
	if ok || source != "" || url != "" {
		t.Fatalf("URL without Enabled must not activate the env fallback: got url=%q source=%q ok=%v", url, source, ok)
	}
}

func TestSparkIntegrationResolver_RepoErrorFallsToEnv(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.getErr = context.DeadlineExceeded
	r := NewSparkIntegrationResolver(repo, SparkEnvFallback{URL: "https://env-spark.example", Enabled: true})

	url, source, ok := r.ResolveSparkURL(context.Background(), ws)
	if !ok || source != "env" || url != "https://env-spark.example" {
		t.Fatalf("a repo error must fall through to env, not fail closed: got url=%q source=%q ok=%v", url, source, ok)
	}
}

func TestSparkIntegrationResolver_NilRepo_FallsToEnv(t *testing.T) {
	ws := uuid.New()
	r := NewSparkIntegrationResolver(nil, SparkEnvFallback{URL: "https://env-spark.example", Enabled: true})

	url, source, ok := r.ResolveSparkURL(context.Background(), ws)
	if !ok || source != "env" || url != "https://env-spark.example" {
		t.Fatalf("got url=%q source=%q ok=%v", url, source, ok)
	}
}

// uuid.Nil (the "no workspace_id supplied" sentinel SparkHandler passes for
// callers that don't yet send workspace_id) never matches a stored row —
// it must fall straight through to env, preserving pre-fix behavior for
// those callers.
func TestSparkIntegrationResolver_NilWorkspaceID_FallsToEnv(t *testing.T) {
	repo := newFakeVCSIntegrationRepo()
	// Even with unrelated rows stored under real workspace ids, uuid.Nil
	// must not accidentally match any of them.
	repo.put(uuid.New(), domain.IntegrationConfig{Provider: domain.IntegrationProviderSpark, IsActive: true, Config: sparkCfg("https://workspace-spark.example")})
	r := NewSparkIntegrationResolver(repo, SparkEnvFallback{URL: "https://env-spark.example", Enabled: true})

	url, source, ok := r.ResolveSparkURL(context.Background(), uuid.Nil)
	if !ok || source != "env" || url != "https://env-spark.example" {
		t.Fatalf("got url=%q source=%q ok=%v", url, source, ok)
	}
}
