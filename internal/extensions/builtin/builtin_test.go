package builtin_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/builtin"
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
