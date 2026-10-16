package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

type fakeGitHubExtension struct {
	token, secret, source string
	ok                    bool
	instanceSecret        string
}

func (f fakeGitHubExtension) ResolveConfig(context.Context, uuid.UUID) (token, webhookSecret, source string, ok bool) {
	return f.token, f.secret, f.source, f.ok
}

func (f fakeGitHubExtension) InstanceWebhookSecret(context.Context) string { return f.instanceSecret }

// With the extension wired, it alone decides: the old env fields are not
// consulted, and "instance" keeps being reported as "env" for the Integrations
// screen and the gate.
func TestVCSIntegrationResolver_ResolveGitHub_ThroughExtension(t *testing.T) {
	env := VCSEnvFallback{GitHubToken: "env-token", GitHubWebhookSecret: "env-secret"}
	ws := uuid.New()

	r := NewVCSIntegrationResolver(nil, env).WithGitHubExtension(fakeGitHubExtension{token: "ws-token", secret: "ws-secret", source: "workspace", ok: true})
	cfg, source, ok := r.ResolveGitHub(context.Background(), ws)
	if !ok || source != "workspace" || cfg.Token != "ws-token" || cfg.WebhookSecret != "ws-secret" {
		t.Fatalf("workspace level: cfg=%+v source=%q ok=%v", cfg, source, ok)
	}

	r = NewVCSIntegrationResolver(nil, env).WithGitHubExtension(fakeGitHubExtension{token: "it", source: "instance", ok: true})
	cfg, source, ok = r.ResolveGitHub(context.Background(), ws)
	if !ok || source != "env" || cfg.Token != "it" {
		t.Fatalf("instance level must read as env: cfg=%+v source=%q ok=%v", cfg, source, ok)
	}

	r = NewVCSIntegrationResolver(nil, env).WithGitHubExtension(fakeGitHubExtension{ok: false})
	cfg, source, ok = r.ResolveGitHub(context.Background(), ws)
	if ok || source != "" || cfg != (GitHubIntegrationConfig{}) {
		t.Fatalf("extension off must be off even with env set: cfg=%+v source=%q ok=%v", cfg, source, ok)
	}
}

func TestVCSIntegrationResolver_GitHubWebhookSecrets_ThroughExtension(t *testing.T) {
	env := VCSEnvFallback{GitHubWebhookSecret: "env-secret"}

	r := NewVCSIntegrationResolver(nil, env).WithGitHubExtension(fakeGitHubExtension{instanceSecret: "inst-secret"})
	secrets, source := r.GitHubWebhookSecrets(context.Background())
	if source != "env" || len(secrets) != 1 || secrets[0].Secret != "inst-secret" || secrets[0].WorkspaceID != uuid.Nil {
		t.Fatalf("instance secret: secrets=%v source=%q", secrets, source)
	}

	r = NewVCSIntegrationResolver(nil, env).WithGitHubExtension(fakeGitHubExtension{})
	secrets, source = r.GitHubWebhookSecrets(context.Background())
	if source != "" || len(secrets) != 0 {
		t.Fatalf("no instance secret must mean disabled, not the old env value: secrets=%v source=%q", secrets, source)
	}
}
