package themes_test

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/themes"
	"github.com/entire-vc/evc-mesh/internal/themes/themestest"
)

var hexColour = regexp.MustCompile(`^#[0-9a-f]{6}$`)

func requireComplete(t *testing.T, b themes.Brand) {
	t.Helper()
	for name, v := range map[string]string{
		"theme_color": b.ThemeColor, "theme_color_dark": b.ThemeColorDark, "background_color": b.BackgroundColor,
		"primary": b.Primary, "primary_foreground": b.PrimaryForeground, "background": b.Background,
		"foreground": b.Foreground, "muted_foreground": b.MutedForeground, "border": b.Border,
	} {
		require.Regexp(t, hexColour, v, name)
	}
}

// The brand without any theme chosen is mesh-default, complete, never empty —
// the red run of the card: an instance with no theme must not serve blanks.
func TestBrand_NoThemeIsMeshDefault(t *testing.T) {
	svc, _, _, _ := newService()
	r, err := svc.Resolve(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, themes.DefaultBrand(), r.Brand)
	requireComplete(t, r.Brand)
	require.Equal(t, "#006b6b", r.Brand.ThemeColor, "the colour the static manifest shipped with")
}

func TestBrand_FollowsTheInstanceAndWorkspaceTheme(t *testing.T) {
	svc, inst, _, _ := newService()
	ctx := context.Background()
	ws, actor := uuid.New(), uuid.New()
	inst.Add("graphite", themestest.Manifest{ID: "graphite", Tokens: map[string]map[string]string{
		"light": {"primary": "0 100% 50%", "background": "0 0% 100%"}, "dark": {"primary": "120 100% 50%"},
	}}.JSON())
	inst.Add("harbor", themestest.Manifest{ID: "harbor", Tokens: map[string]map[string]string{"light": {"primary": "240 100% 50%"}}}.JSON())
	graphite, harbor := "graphite", "harbor"
	_, err := svc.Select(ctx, nil, actor, &graphite, nil)
	require.NoError(t, err)
	_, err = svc.Select(ctx, &ws, actor, &harbor, nil)
	require.NoError(t, err)

	b := svc.BrandFor(ctx, nil)
	require.Equal(t, "#ff0000", b.ThemeColor)
	require.Equal(t, "#00ff00", b.ThemeColorDark)
	require.Equal(t, "#ff0000", b.Primary)
	// Tokens the theme does not set keep the mesh-default value.
	require.Equal(t, themes.DefaultBrand().Foreground, b.Foreground)
	requireComplete(t, b)

	// The workspace's own theme wins for the workspace; a one-mode theme serves both modes.
	w := svc.BrandFor(ctx, &ws)
	require.Equal(t, "#0000ff", w.Primary)
	require.Equal(t, "#0000ff", w.ThemeColorDark)
}

func TestBrand_UnreadableTokenFallsBack(t *testing.T) {
	b := themes.BrandOf(map[string]map[string]string{"light": {"primary": "not a colour", "foreground": "999 50% 50%"}})
	requireComplete(t, b)
	require.Equal(t, themes.DefaultBrand().Primary, b.Primary)
}

func TestBrandFor_StorageErrorGivesDefault(t *testing.T) {
	svc, inst, _, _ := newService()
	inst.Err = errors.New("db down")
	svc.Themes = themestest.NewThemes()
	require.Equal(t, themes.DefaultBrand(), svc.BrandFor(context.Background(), nil))
}

func TestWebManifest_CarriesThemeColoursAndFixedName(t *testing.T) {
	m := string(themes.WebManifest(themes.Brand{ThemeColor: "#ff0000", BackgroundColor: "#00ff00"}))
	require.Contains(t, m, `"theme_color": "#ff0000"`)
	require.Contains(t, m, `"background_color": "#00ff00"`)
	require.Contains(t, m, `"name": "Entire VC Mesh"`)
	require.Contains(t, m, `/icon-512.png`)
}
