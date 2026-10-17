package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

type fakeGitLabExtension struct {
	baseURL, token, secret, source string
	ok                             bool
	instanceSecret                 string
}

func (f fakeGitLabExtension) ResolveConfig(context.Context, uuid.UUID) (baseURL, token, webhookSecret, source string, ok bool) {
	return f.baseURL, f.token, f.secret, f.source, f.ok
}

func (f fakeGitLabExtension) InstanceWebhookSecret(context.Context) string { return f.instanceSecret }

// With the extension wired, it alone decides: the old env fields are not
// consulted, and "instance" keeps being reported as "env" for the Integrations
// screen and the gate.
func TestVCSIntegrationResolver_ResolveGitLab_ThroughExtension(t *testing.T) {
	env := VCSEnvFallback{GitLabBaseURL: "https://env.example", GitLabToken: "env-token", GitLabWebhookSecret: "env-secret"}
	ws := uuid.New()

	r := NewVCSIntegrationResolver(nil, env).WithGitLabExtension(fakeGitLabExtension{baseURL: "https://ws.example", token: "ws-token", secret: "ws-secret", source: "workspace", ok: true})
	cfg, source, ok := r.ResolveGitLab(context.Background(), ws)
	if !ok || source != "workspace" || cfg.BaseURL != "https://ws.example" || cfg.Token != "ws-token" || cfg.WebhookSecret != "ws-secret" {
		t.Fatalf("workspace level: cfg=%+v source=%q ok=%v", cfg, source, ok)
	}

	r = NewVCSIntegrationResolver(nil, env).WithGitLabExtension(fakeGitLabExtension{baseURL: "https://it.example", token: "it", source: "instance", ok: true})
	cfg, source, ok = r.ResolveGitLab(context.Background(), ws)
	if !ok || source != "env" || cfg.Token != "it" || cfg.BaseURL != "https://it.example" {
		t.Fatalf("instance level must read as env: cfg=%+v source=%q ok=%v", cfg, source, ok)
	}

	r = NewVCSIntegrationResolver(nil, env).WithGitLabExtension(fakeGitLabExtension{ok: false})
	cfg, source, ok = r.ResolveGitLab(context.Background(), ws)
	if ok || source != "" || cfg != (GitLabIntegrationConfig{}) {
		t.Fatalf("extension off must be off even with env set: cfg=%+v source=%q ok=%v", cfg, source, ok)
	}
}

func TestVCSIntegrationResolver_GitLabWebhookSecrets_ThroughExtension(t *testing.T) {
	env := VCSEnvFallback{GitLabWebhookSecret: "env-secret"}

	r := NewVCSIntegrationResolver(nil, env).WithGitLabExtension(fakeGitLabExtension{instanceSecret: "inst-secret"})
	secrets, source := r.GitLabWebhookSecrets(context.Background())
	if source != "env" || len(secrets) != 1 || secrets[0].Secret != "inst-secret" || secrets[0].WorkspaceID != uuid.Nil {
		t.Fatalf("instance secret: secrets=%v source=%q", secrets, source)
	}

	r = NewVCSIntegrationResolver(nil, env).WithGitLabExtension(fakeGitLabExtension{})
	secrets, source = r.GitLabWebhookSecrets(context.Background())
	if source != "" || len(secrets) != 0 {
		t.Fatalf("no instance secret must mean disabled, not the old env value: secrets=%v source=%q", secrets, source)
	}
}
