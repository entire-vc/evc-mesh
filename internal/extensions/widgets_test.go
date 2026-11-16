package extensions_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
)

func manifestWithWidgets(widgets string) []byte {
	return []byte(`{"id":"demo","name":"Demo","version":"1.0.0","kind":"integration","runtime":"builtin",
 "levels":{"instance":true,"workspace":true,"project":false},
 "capabilities":{"ui":["dashboard.widget"]},
 "widgets":[` + widgets + `]}`)
}

func TestParseManifest_Widgets(t *testing.T) {
	m, err := extensions.ParseManifest(manifestWithWidgets(`{"id":"w","title":"W","category":"x","scope":"workspace",
	  "size":{"default":{"w":4,"h":3},"min":{"w":2,"h":2}},"render":{"builtin":"DemoWidget"},
	  "settings":{"type":"object","properties":{"project_id":{"type":"string","x-widget":"project-picker"}}}}`))
	require.NoError(t, err, "control: a plain widget parses")
	require.Len(t, m.Widgets, 1)
	require.Equal(t, "DemoWidget", m.Widgets[0].Render.Builtin)
	require.Equal(t, 2, m.Widgets[0].Size.Min.W)
	require.Equal(t, "demo.w", extensions.WidgetType(m.ID, m.Widgets[0].ID))

	_, err = extensions.ParseManifest(manifestWithWidgets(`{"id":"w","settings":{"properties":{"g":{"type":"object","properties":{"key":{"type":"string","x-secret":true}}}}}}`))
	require.ErrorContains(t, err, "x-secret", "a nested secret is refused too")

	_, err = extensions.ParseManifest(manifestWithWidgets(`{"id":"w","settings":{"allOf":[{"properties":{"k":{"x-secret":false}}}]}}`))
	require.ErrorContains(t, err, "x-secret", "a secret inside a list is found as well")

	_, err = extensions.ParseManifest(manifestWithWidgets(`{"id":"w"},{"id":"w"}`))
	require.ErrorContains(t, err, "duplicate")
}
