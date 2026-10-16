package builtin_test

import (
	"testing"

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
