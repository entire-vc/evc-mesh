package themes_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/repoinstall"
	"github.com/entire-vc/evc-mesh/internal/themes"
	"github.com/entire-vc/evc-mesh/internal/themes/themestest"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func parse(t *testing.T, raw []byte) extensions.Manifest {
	t.Helper()
	m, err := extensions.ParseManifest(raw)
	require.NoError(t, err)
	return m
}

func newService() (*themes.Service, *themestest.Installed, *themestest.Themes, *themestest.Store) {
	inst, th, st := themestest.NewInstalled(), themestest.NewThemes(), themestest.NewStore()
	return &themes.Service{Installed: inst, Themes: th, Store: st}, inst, th, st
}

func filesOf(files map[string][]byte) repoinstall.FetchFunc {
	return func(_ context.Context, path string) ([]byte, error) {
		b, ok := files[path]
		if !ok {
			return nil, errors.New("no such file " + path)
		}
		return b, nil
	}
}

func apiErr(t *testing.T, err error) *apierror.Error {
	t.Helper()
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	return ae
}

func TestTokenContractV1Size(t *testing.T) {
	require.Equal(t, 61, themes.TokenCount(), "57 colours + 4 others, token-contract-v1")
	for _, name := range []string{"background", "sidebar-background", "chart-5", "avatar-8", "status-in-progress", "priority-none", "radius", "font-mono", "space-unit"} {
		require.True(t, themes.IsToken(name), name)
	}
	require.False(t, themes.IsToken("sidebar"), "the Tailwind name is not the contract name")
}

func TestParseSpec_RejectsWhatTheSchemaCannotSay(t *testing.T) {
	cases := map[string]struct {
		m   themestest.Manifest
		key string
	}{
		"token outside the contract":  {themestest.Manifest{Tokens: map[string]map[string]string{"light": {"brand-pink": "1 2% 3%"}}}, "theme.tokens.light.brand-pink"},
		"colour is not an HSL triple": {themestest.Manifest{Tokens: map[string]map[string]string{"light": {"primary": "red; } body { display:none"}}}, "theme.tokens.light.primary"},
		"font with url()":             {themestest.Manifest{Tokens: map[string]map[string]string{"light": {"font-sans": "url(//evil/x)"}}}, "theme.tokens.light.font-sans"},
		"slot version not served":     {themestest.Manifest{Views: []byte("v"), Slots: map[string]string{"board.card": "^9"}}, "theme.slots.board.card"},
		"slot does not exist":         {themestest.Manifest{Views: []byte("v"), Slots: map[string]string{"task.form": "^1"}}, "theme.slots.task.form"},
		"stale contract report":       {themestest.Manifest{Views: []byte("v"), ReportBundleSHA: strings.Repeat("b", 64)}, "theme.contract_report.bundle_sha256"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := themes.ParseSpec(parse(t, tc.m.JSON()))
			ae := apiErr(t, err)
			require.Equal(t, http.StatusUnprocessableEntity, ae.StatusCode())
			require.Contains(t, ae.Validation, tc.key)
		})
	}
	// Control: the same builder without the defect is accepted.
	_, err := themes.ParseSpec(parse(t, themestest.Manifest{Views: []byte("v"), CSS: []byte("a{}")}.JSON()))
	require.NoError(t, err)
}

func TestParseSpec_ReportMustCoverEverySlot(t *testing.T) {
	raw := themestest.Manifest{Views: []byte("v"), Slots: map[string]string{"board.card": "^1", "nav.sidebar": "^1"}}.JSON()
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	passed := doc["theme"].(map[string]any)["contract_report"].(map[string]any)["passed"].(map[string]any)
	delete(passed, "nav.sidebar")
	broken, _ := json.Marshal(doc)
	_, err := themes.ParseSpec(parse(t, broken))
	require.Contains(t, apiErr(t, err).Validation, "theme.contract_report.passed.nav.sidebar")
}

func TestParseSpec_BindingMustNameAContractToken(t *testing.T) {
	settings := map[string]any{"workspace": map[string]any{"properties": map[string]any{"c": map[string]any{"type": "string", "x-token": "nope"}}}}
	_, err := themes.ParseSpec(parse(t, themestest.Manifest{Settings: settings}.JSON()))
	require.Contains(t, apiErr(t, err).Validation, "settings.workspace.c.x-token")
}

func TestParseSpec_SameFieldAtBothLevelsNeedsOneToken(t *testing.T) {
	level := func(tok string) map[string]any {
		return map[string]any{"properties": map[string]any{"c": map[string]any{"type": "string", "x-token": tok}}}
	}
	// Red: the same name drives two different tokens.
	_, err := themes.ParseSpec(parse(t, themestest.Manifest{Settings: map[string]any{"instance": level("primary"), "workspace": level("accent")}}.JSON()))
	require.Contains(t, apiErr(t, err).Validation, "settings.workspace.c.x-token")
	// Green: the same token at both levels is one binding.
	_, err = themes.ParseSpec(parse(t, themestest.Manifest{Settings: map[string]any{"instance": level("primary"), "workspace": level("primary")}}.JSON()))
	require.NoError(t, err)
}

func TestPrepareInstall_HashMismatchRejected(t *testing.T) {
	svc, _, _, store := newService()
	css, views := []byte(".a{color:red}"), []byte("export default 1")
	m := parse(t, themestest.Manifest{CSS: css, Views: views}.JSON())

	// Red: the repository serves other bytes than the manifest's hash names.
	tampered := filesOf(map[string][]byte{"theme.css": css, "views.js": []byte("export default 2")})
	err := svc.PrepareInstall(context.Background(), m, tampered)
	ae := apiErr(t, err)
	require.Equal(t, http.StatusUnprocessableEntity, ae.StatusCode())
	require.Equal(t, themes.CodeHashMismatch, ae.ErrorCode)
	require.Contains(t, ae.Validation, "theme.views.sha256")
	require.Empty(t, store.Objects, "a rejected theme must leave nothing in storage, not even the file that matched")

	// Green: the same call with the right bytes stores both under their hash.
	require.NoError(t, svc.PrepareInstall(context.Background(), m, filesOf(map[string][]byte{"theme.css": css, "views.js": views})))
	require.Contains(t, store.Objects, "themes/"+themestest.SHA(css)+".css")
	require.Contains(t, store.Objects, "themes/"+themestest.SHA(views)+".js")
}

func TestPrepareInstall_CSSPolicy(t *testing.T) {
	svc, _, _, _ := newService()
	for name, css := range map[string]string{
		"import":          `@import url("https://evil.example/x.css");`,
		"remote url":      `a{background:url(https://evil.example/p.png)}`,
		"escaped url":     `a{background:\75rl(https://evil.example/p.png)}`,
		"escaped import":  `@\69mport "x.css";`,
		"image-set":       `a{background:image-set("https://e/p.png" 1x)}`,
		"commented split": `a{background:u/**/rl(x)}` + "\n" + `b{background:url(http://e/x)}`,
	} {
		t.Run(name, func(t *testing.T) {
			m := parse(t, themestest.Manifest{CSS: []byte(css)}.JSON())
			err := svc.PrepareInstall(context.Background(), m, filesOf(map[string][]byte{"theme.css": []byte(css)}))
			require.Equal(t, themes.CodeThemeInvalid, apiErr(t, err).ErrorCode)
		})
	}
	ok := `a{background:url("data:image/png;base64,AAAA")} .md\:flex{display:flex} b{content:"\201C"}`
	m := parse(t, themestest.Manifest{CSS: []byte(ok)}.JSON())
	require.NoError(t, svc.PrepareInstall(context.Background(), m, filesOf(map[string][]byte{"theme.css": []byte(ok)})))
}

func TestInstallBuiltin(t *testing.T) {
	svc, inst, _, _ := newService()
	actor := uuid.New()
	row, err := svc.InstallBuiltin(context.Background(), actor, "graphite")
	require.NoError(t, err)
	require.Equal(t, domain.ExtensionSourceBuiltin, row.Source)
	require.Contains(t, inst.Rows, "graphite")

	_, err = svc.InstallBuiltin(context.Background(), actor, "no-such-theme")
	require.Equal(t, http.StatusNotFound, apiErr(t, err).StatusCode())

	// Every shipped theme must pass the manifest schema and the server checks.
	for _, id := range themes.BuiltinIDs() {
		_, err := svc.InstallBuiltin(context.Background(), actor, id)
		require.NoError(t, err, id)
	}
}

func TestResolve_ThreeLevels(t *testing.T) {
	svc, inst, _, _ := newService()
	ctx := context.Background()
	ws, other, actor := uuid.New(), uuid.New(), uuid.New()
	inst.Add("graphite", themestest.Manifest{ID: "graphite", Tokens: map[string]map[string]string{"light": {"primary": "10 10% 10%"}}}.JSON())
	inst.Add("harbor", themestest.Manifest{ID: "harbor", Tokens: map[string]map[string]string{"light": {"primary": "200 50% 40%"}}}.JSON())

	// Nothing chosen anywhere: mesh-default.
	r, err := svc.Resolve(ctx, &ws)
	require.NoError(t, err)
	require.Equal(t, domain.DefaultThemeID, r.ThemeID)
	require.Equal(t, "default", r.Source)
	require.Empty(t, r.TokensCSS)

	// Instance theme reaches a workspace that chose nothing, and the bare request.
	graphite, harbor := "graphite", "harbor"
	_, err = svc.Select(ctx, nil, actor, &graphite, nil)
	require.NoError(t, err)
	for _, id := range []*uuid.UUID{nil, &ws} {
		r, err = svc.Resolve(ctx, id)
		require.NoError(t, err)
		require.Equal(t, "graphite", r.ThemeID)
		require.Equal(t, "instance", r.Source)
		require.Contains(t, r.TokensCSS, "--primary:10 10% 10%")
	}

	// A workspace's own choice wins for that workspace only.
	_, err = svc.Select(ctx, &ws, actor, &harbor, nil)
	require.NoError(t, err)
	r, err = svc.Resolve(ctx, &ws)
	require.NoError(t, err)
	require.Equal(t, "harbor", r.ThemeID)
	require.Equal(t, "workspace", r.Source)
	r, _ = svc.Resolve(ctx, &other)
	require.Equal(t, "graphite", r.ThemeID, "another workspace still sees the instance theme")
	r, _ = svc.Resolve(ctx, nil)
	require.Equal(t, "graphite", r.ThemeID, "the public instance theme is unaffected by a workspace choice")

	// Back to "as the instance".
	_, err = svc.Select(ctx, &ws, actor, nil, nil)
	require.NoError(t, err)
	r, _ = svc.Resolve(ctx, &ws)
	require.Equal(t, "graphite", r.ThemeID)

	// A theme that disappears falls through instead of breaking the page.
	delete(inst.Rows, "graphite")
	r, err = svc.Resolve(ctx, &ws)
	require.NoError(t, err)
	require.Equal(t, domain.DefaultThemeID, r.ThemeID)
}

func TestSelect_SettingsOutsideSchemaAre422(t *testing.T) {
	svc, inst, _, _ := newService()
	ctx, actor, ws := context.Background(), uuid.New(), uuid.New()
	inst.Add("graphite", themestest.Manifest{Settings: themestest.ColourSettings()}.JSON())
	id := "graphite"

	for name, settings := range map[string]map[string]any{
		"unknown field": {"nope": 1},
		"wrong type":    {"accent_colour": 5},
		"instance-only": {"dense": "yes"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Select(ctx, &ws, actor, &id, settings)
			ae := apiErr(t, err)
			require.Equal(t, http.StatusUnprocessableEntity, ae.StatusCode())
			require.Equal(t, themes.CodeSettingsInvalid, ae.ErrorCode)
			require.NotEmpty(t, ae.Validation)
		})
	}
	// "dense" is a workspace-level field; at instance level it is outside the schema.
	_, err := svc.Select(ctx, nil, actor, &id, map[string]any{"dense": true})
	require.Equal(t, http.StatusUnprocessableEntity, apiErr(t, err).StatusCode())

	// Control: valid settings are stored.
	sel, err := svc.Select(ctx, &ws, actor, &id, map[string]any{"accent_colour": "10 20% 30%", "dense": true})
	require.NoError(t, err)
	require.Equal(t, 1, sel.Version)

	// Settings without a theme, and a theme that is not installed.
	_, err = svc.Select(ctx, &ws, actor, nil, map[string]any{"dense": true})
	require.Equal(t, http.StatusUnprocessableEntity, apiErr(t, err).StatusCode())
	ghost := "ghost"
	_, err = svc.Select(ctx, &ws, actor, &ghost, nil)
	require.Equal(t, themes.CodeNotInstalled, apiErr(t, err).ErrorCode)
}

func TestResolve_SettingsMergeAndTokenBinding(t *testing.T) {
	svc, inst, _, _ := newService()
	ctx, actor, ws := context.Background(), uuid.New(), uuid.New()
	inst.Add("graphite", themestest.Manifest{Settings: themestest.ColourSettings()}.JSON())
	id := "graphite"

	r, err := svc.Resolve(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, domain.DefaultThemeID, r.ThemeID)

	_, err = svc.Select(ctx, nil, actor, &id, map[string]any{"accent_colour": "100 10% 10%"})
	require.NoError(t, err)
	r, _ = svc.Resolve(ctx, &ws)
	require.Equal(t, "100 10% 10%", r.Settings["accent_colour"])
	require.Contains(t, r.TokensCSS, "--primary:100 10% 10%", "an instance setting bound to a token overrides the theme's value")

	_, err = svc.Select(ctx, &ws, actor, &id, map[string]any{"accent_colour": "300 30% 30%"})
	require.NoError(t, err)
	r, _ = svc.Resolve(ctx, &ws)
	require.Equal(t, "300 30% 30%", r.Settings["accent_colour"], "workspace beats instance")
	require.Contains(t, r.TokensCSS, ":root{--primary:300 30% 30%;}")
	require.Contains(t, r.TokensCSS, ".dark{--primary:300 30% 30%;}")
	rInst, _ := svc.Resolve(ctx, nil)
	require.NotEqual(t, rInst.Hash, r.Hash, "different settings, different hash")
}

func TestResolve_AssetURLsCarryTheHash(t *testing.T) {
	svc, inst, _, _ := newService()
	ctx := context.Background()
	css, views := []byte("a{}"), []byte("export default 1")
	inst.Add("graphite", themestest.Manifest{CSS: css, Views: views}.JSON())
	id := "graphite"
	_, err := svc.Select(ctx, nil, uuid.New(), &id, nil)
	require.NoError(t, err)
	r, err := svc.Resolve(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "/api/v1/theme/assets/"+themestest.SHA(css)+".css", r.ThemeCSSURL)
	require.Equal(t, "/api/v1/theme/assets/"+themestest.SHA(views)+".js", r.ViewsURL)
}

func TestAsset_ServedWrappedInTheLayer(t *testing.T) {
	svc, _, _, store := newService()
	css := []byte(".a{color:red}")
	m := parse(t, themestest.Manifest{CSS: css}.JSON())
	require.NoError(t, svc.PrepareInstall(context.Background(), m, filesOf(map[string][]byte{"theme.css": css})))
	data, ctype, err := svc.Asset(context.Background(), themestest.SHA(css), "css")
	require.NoError(t, err)
	require.Contains(t, ctype, "text/css")
	require.True(t, strings.HasPrefix(string(data), "@layer mesh-theme {"), string(data))
	require.Equal(t, css, store.Objects["themes/"+themestest.SHA(css)+".css"], "the stored file is untouched, the wrapper is added on the way out")

	_, _, err = svc.Asset(context.Background(), strings.Repeat("0", 64), "css")
	require.ErrorIs(t, err, themes.ErrAssetNotFound)
}

func TestResolve_SecretSettingsNeverLeave(t *testing.T) {
	svc, inst, _, _ := newService()
	ctx, actor, ws := context.Background(), uuid.New(), uuid.New()
	inst.Add("vault", themestest.Manifest{ID: "vault", Settings: map[string]any{
		"instance": map[string]any{"type": "object", "properties": map[string]any{
			"label": map[string]any{"type": "string"},
			"key":   map[string]any{"type": "string", "x-secret": true, "default": "built-in"},
		}},
	}}.JSON())
	id := "vault"
	_, err := svc.Select(ctx, nil, actor, &id, map[string]any{"label": "ops", "key": "s3cret"})
	require.NoError(t, err)

	r, err := svc.Resolve(ctx, &ws)
	require.NoError(t, err)
	require.Equal(t, "ops", r.Settings["label"])
	require.NotContains(t, r.Settings, "key", "an x-secret value, stored or default, is not served")
}

func TestResolve_SecretBesideARefNeverLeaves(t *testing.T) {
	svc, inst, _, _ := newService()
	ctx, actor := context.Background(), uuid.New()
	inst.Add("vault", themestest.Manifest{
		ID: "vault",
		Schemas: map[string]any{"base": map[string]any{"type": "object", "properties": map[string]any{
			"label": map[string]any{"type": "string"},
		}}},
		Settings: map[string]any{"instance": map[string]any{
			"$ref": "#/schemas/base",
			"properties": map[string]any{
				"key": map[string]any{"type": "string", "x-secret": true},
			},
		}},
	}.JSON())
	id := "vault"
	_, err := svc.Select(ctx, nil, actor, &id, map[string]any{"label": "ops", "key": "s3cret"})
	require.NoError(t, err)

	r, err := svc.Resolve(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "ops", r.Settings["label"])
	require.NotContains(t, r.Settings, "key", "a secret declared beside a $ref is filtered too")
}
