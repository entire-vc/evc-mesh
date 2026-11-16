package dashboards

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
)

func layoutFields(t *testing.T, err error) map[string]string {
	t.Helper()
	require.Error(t, err)
	le, ok := err.(*LayoutError)
	require.True(t, ok, "%T", err)
	require.Contains(t, le.Error(), "invalid layout: ")
	return le.Fields
}

func TestNormalizeSettings(t *testing.T) {
	for _, in := range []string{"", "null", "  "} {
		got, err := NormalizeSettings(json.RawMessage(in))
		require.NoError(t, err)
		require.JSONEq(t, `{}`, string(got))
	}
	got, err := NormalizeSettings(json.RawMessage(`{ "limit" : 10 }`))
	require.NoError(t, err)
	require.Equal(t, `{"limit":10}`, string(got))
	for _, bad := range []string{`[1]`, `"x"`, `{"a":`, `7`} {
		_, err := NormalizeSettings(json.RawMessage(bad))
		require.Error(t, err, bad)
	}
}

func TestValidateLayout_EveryRefusal(t *testing.T) {
	cat := NewCatalog(Theme{})

	many := make([]Widget, MaxWidgetsPerBoard+1)
	require.Contains(t, layoutFields(t, ValidateLayout(cat, many)), "widgets")

	f := layoutFields(t, ValidateLayout(cat, []Widget{
		{ID: "a", WidgetType: " ", X: 0, Y: 0, W: 1, H: 1},                                          // 0: no type
		{ID: "a", WidgetType: "ext.x", X: 0, Y: 2, W: 0, H: 1},                                      // 1: dup id, bad size
		{WidgetType: "ext.x", X: -1, Y: 4, W: 1, H: 1},                                              // 2: negative x
		{WidgetType: "ext.x", X: 11, Y: 6, W: 3, H: 1},                                              // 3: past the grid
		{WidgetType: "my-tasks", X: 0, Y: 8, W: 1, H: 1},                                            // 4: below min size
		{WidgetType: "ext.x", X: 0, Y: 12, W: 1, H: 1, Settings: json.RawMessage(`[]`)},             // 5: not an object
		{WidgetType: "my-tasks", X: 0, Y: 14, W: 6, H: 4, Settings: json.RawMessage(`{"limit":1}`)}, // 6: schema
		{WidgetType: "ext.y", X: 2, Y: 15, W: 6, H: 4},                                              // 7: overlaps 6
	}))
	for _, k := range []string{
		"widgets[0].widget_type", "widgets[1].id", "widgets[1].size", "widgets[2].position", "widgets[3].position",
		"widgets[4].size", "widgets[5].settings", "widgets[6].settings", "widgets[7].position",
	} {
		require.Contains(t, f, k)
	}
	require.Contains(t, f["widgets[6].settings"], "does not match the my-tasks settings")

	// x near MaxInt must not wrap x + w and slip past the grid check.
	f = layoutFields(t, ValidateLayout(cat, []Widget{{WidgetType: "ext.x", X: math.MaxInt, W: 1, H: 1}}))
	require.Equal(t, "x + w must be at most 12", f["widgets[0].position"])

	big := `{"k":"` + strings.Repeat("x", MaxSettingsBytes) + `"}`
	f = layoutFields(t, ValidateLayout(cat, []Widget{{WidgetType: "ext.x", W: 1, H: 1, Settings: json.RawMessage(big)}}))
	require.Contains(t, f["widgets[0].settings"], "KB")

	// An unknown type is kept: it only has to fit the grid.
	require.NoError(t, ValidateLayout(cat, []Widget{{WidgetType: "ext.someone.chart", W: 4, H: 3, Settings: json.RawMessage(`{"a":1}`)}}))
}

func TestCheckSchema_BrokenSchemaIsReported(t *testing.T) {
	err := checkSchema("broken-"+t.Name(), json.RawMessage(`{"type":`), json.RawMessage(`{}`))
	require.ErrorContains(t, err, "settings schema of broken-")
	err = checkSchema("bad-type-"+t.Name(), json.RawMessage(`{"type":"nope"}`), json.RawMessage(`{}`))
	require.Error(t, err)
}

func TestCatalogAndDefaults_WithTheme(t *testing.T) {
	theme := Theme{ID: "board-studio", OwnWidgets: []string{"focus", "my-tasks", "bad name", ""},
		Widgets: []string{"triage", "board-studio.focus", "triage", "ext.unknown", " "}}
	cat := NewCatalog(theme)
	focus := ThemeWidgetType("board-studio", "focus")
	tp, ok := cat.Lookup(focus)
	require.True(t, ok)
	require.Equal(t, OriginTheme, tp.Origin)
	_, ok = cat.Lookup(ThemeWidgetType("board-studio", "bad name"))
	require.False(t, ok)

	ws := DefaultWidgets(theme, cat)
	require.Equal(t, []string{"triage-queue", focus, "ext.unknown"}, []string{ws[0].WidgetType, ws[1].WidgetType, ws[2].WidgetType})
	require.Len(t, ws, 3)
	require.NoError(t, ValidateLayout(cat, ws))

	// Only unusable entries: the standard composition.
	require.Equal(t, DefaultWidgets(Theme{}, NewCatalog(Theme{})), DefaultWidgets(Theme{Widgets: []string{" "}}, cat))
}

func TestErrorsAndProvider(t *testing.T) {
	require.Equal(t, "404 not_found: Dashboard not found", errNotFound().Error())
	require.Equal(t, 403, errForbidden("x").Status)
	require.Equal(t, 409, errLast().Status)
	require.Equal(t, "stale_layout", stale(&Detail{}).Code)
	require.NoError(t, mapDBError(nil))

	p := New(extensions.Deps{})
	require.True(t, p.HiddenFromListings())
	require.Error(t, p.Validate(extensions.LevelInstance, nil))
}
