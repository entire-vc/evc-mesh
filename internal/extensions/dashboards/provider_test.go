package dashboards

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
)

func TestProvider_SystemPartWithoutSettings(t *testing.T) {
	p := New(extensions.Deps{})
	require.Equal(t, ID, p.Manifest().ID)
	require.True(t, p.HiddenFromListings())
	require.True(t, extensions.IsSystemPart(ID), "the home screen cannot be switched off")
	for _, lvl := range []extensions.Level{extensions.LevelInstance, extensions.LevelWorkspace, extensions.LevelProject} {
		require.Error(t, p.Validate(lvl, map[string]any{"x": 1}))
	}
}
