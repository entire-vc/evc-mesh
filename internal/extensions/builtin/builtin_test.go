package builtin_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/builtin"
	"github.com/entire-vc/evc-mesh/internal/extensions/github"
	"github.com/entire-vc/evc-mesh/internal/extensions/gitlab"
	"github.com/entire-vc/evc-mesh/internal/handler"
)

// Every builtin provider is listed once, carries a valid manifest and can be
// registered side by side with the others.
func TestProviders_ListedOnceAndRegistrable(t *testing.T) {
	ps := builtin.Providers(extensions.Deps{})
	reg := extensions.NewRegistry()
	seen := map[string]bool{}
	for _, p := range ps {
		id := p.Manifest().ID
		require.False(t, seen[id], "%s listed twice", id)
		seen[id] = true
		require.NoError(t, reg.Register(p))
	}
	for _, want := range []string{"github", "gitlab", "spark", "telegram"} {
		require.True(t, seen[want], "%s is not a builtin provider", want)
	}
}

type activeConns struct{}

func (activeConns) Connection(context.Context, uuid.UUID, string) (*extensions.Connection, error) {
	return &extensions.Connection{IsActive: true, Settings: map[string]any{"token": "t", "bot_token": "t", "relay_url": "https://r.example", "base_url": "https://b.example"}}, nil
}

type instanceOn struct{}

func (instanceOn) InstanceSetting(context.Context, string) (*extensions.InstanceSetting, error) {
	return &extensions.InstanceSetting{Enabled: true, Settings: map[string]any{"token": "t", "relay_url": "https://r.example"}}, nil
}

type denyAll struct{}

func (denyAll) Allowed(context.Context, string) (bool, error) { return false, nil }

// Every builtin provider, including those with their own Resolve, honours the
// instance allow-list: an active connection of a disallowed extension
// resolves as off and hands no settings to the runtime.
func TestProviders_ResolveHonoursAllowList(t *testing.T) {
	ctx := context.Background()
	for _, p := range builtin.Providers(extensions.Deps{Conns: activeConns{}, Inst: instanceOn{}, Allow: denyAll{}}) {
		res, err := p.Resolve(ctx, uuid.New())
		require.NoError(t, err, p.Manifest().ID)
		require.False(t, res.Enabled, "%s resolves as on while disallowed", p.Manifest().ID)
		require.Nil(t, res.Settings, "%s hands settings while disallowed", p.Manifest().ID)
	}
}

// Every declared inbound webhook names a real POST route, rather than only
// appearing in the consent dialog. A fabricated name must fail the same check.
func TestProviders_WebhooksHaveRoutes(t *testing.T) {
	webhooks := handler.NewVCSLinkHandler(nil)
	for _, p := range builtin.Providers(extensions.Deps{}) {
		// main installs the HTTP handlers before registering these hooks.
		switch p := p.(type) {
		case *github.Provider:
			p.WebhookHandler = webhooks.GitHubWebhook
		case *gitlab.Provider:
			p.WebhookHandler = webhooks.GitLabWebhook
		}
		for _, name := range p.Manifest().Capabilities.WebhooksIn {
			require.True(t, hasWebhookRoute(p, name), "%s: no route for %s", p.Manifest().ID, name)
		}
		require.False(t, hasWebhookRoute(p, "invented-webhook"), "%s: negative control", p.Manifest().ID)
	}
}

func hasWebhookRoute(p extensions.Provider, name string) bool {
	for _, route := range p.Hooks().Routes {
		if route.Method == "POST" && route.Path == "/webhooks/"+p.Manifest().ID+"/"+name && route.Handler != nil {
			return true
		}
	}
	return false
}
