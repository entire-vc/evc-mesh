package themes_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/themes/themestest"
)

// The dashboards read a theme's `widgets[]` setting and its
// dashboard.widget.<name> slots; mesh-default contributes nothing.
func TestDashboard_ThemeContribution(t *testing.T) {
	svc, inst, _, _ := newService()
	ctx, actor, ws := context.Background(), uuid.New(), uuid.New()

	none, err := svc.Dashboard(ctx, ws)
	require.NoError(t, err)
	require.Empty(t, none.ThemeID, "no theme chosen: mesh-default")

	inst.Add("studio", themestest.Manifest{
		ID:    "studio",
		Views: []byte("v"),
		Slots: map[string]string{"dashboard.widget.focus": "^1", "dashboard.widget.pulse": "^1", "board.card": "^1"},
		Settings: map[string]any{"workspace": map[string]any{"type": "object", "properties": map[string]any{
			"widgets": map[string]any{"type": "array"},
		}}},
	}.JSON())
	id := "studio"
	_, err = svc.Select(ctx, &ws, actor, &id, map[string]any{"widgets": []any{"triage", "  ", 7, " studio.focus "}})
	require.NoError(t, err)

	got, err := svc.Dashboard(ctx, ws)
	require.NoError(t, err)
	require.Equal(t, "studio", got.ThemeID)
	require.Equal(t, []string{"triage", "studio.focus"}, got.Widgets, "non-strings and blanks are dropped")
	require.Equal(t, []string{"focus", "pulse"}, got.WidgetSlots)
}
