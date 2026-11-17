package extensions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
)

// widgetProvider is an extension whose resolution a test sets directly.
type widgetProvider struct {
	m   extensions.Manifest
	res extensions.Resolved
	err error
}

func (p *widgetProvider) Manifest() extensions.Manifest                   { return p.m }
func (p *widgetProvider) Validate(extensions.Level, map[string]any) error { return nil }
func (p *widgetProvider) Hooks() extensions.Hooks                         { return extensions.Hooks{} }
func (p *widgetProvider) Health(context.Context, extensions.Resolved) extensions.HealthReport {
	return extensions.HealthReport{}
}
func (p *widgetProvider) Resolve(context.Context, uuid.UUID) (extensions.Resolved, error) {
	return p.res, p.err
}

func widgetExtension(t *testing.T, id, widgets string) *widgetProvider {
	t.Helper()
	m, err := extensions.ParseManifest([]byte(`{"id":"` + id + `","name":"` + id + ` ext","version":"1.0.0","kind":"integration","runtime":"builtin",
	 "levels":{"instance":true,"workspace":true,"project":false},
	 "capabilities":{"ui":["task.panel","dashboard.widget"]},
	 "widgets":[` + widgets + `]}`))
	require.NoError(t, err)
	return &widgetProvider{m: m, res: extensions.Resolved{ExtensionID: id, Enabled: true, Source: extensions.SourceWorkspace}}
}

const (
	wsWidget = `{"id":"overview","title":"Overview","title_i18n":{"ru":"Обзор"},"category":"general","scope":"workspace",
	  "size":{"default":{"w":4,"h":3}},"requires":["tasks:read"],"render":{"builtin":"DemoOverview"}}`
	projectWidget = `{"id":"per-project","title":"Per project","category":"general","scope":"project",
	  "size":{"default":{"w":6,"h":4}},"render":{"builtin":"DemoPerProject"}}`
)

type consentsByExt map[string][]string

func (c consentsByExt) Consented(_ context.Context, _ uuid.UUID, ext string) ([]string, error) {
	if caps, ok := c[ext]; ok {
		return caps, nil
	}
	return nil, errors.New("consent store down")
}

type widgetProjectsFn func(ext string, user uuid.UUID) ([]uuid.UUID, error)

func (f widgetProjectsFn) WidgetProjects(_ context.Context, _ uuid.UUID, ext string, user uuid.UUID) ([]uuid.UUID, error) {
	return f(ext, user)
}

func ids(ws []extensions.DashboardWidget) []string {
	out := []string{}
	for _, w := range ws {
		out = append(out, w.ID)
	}
	return out
}

func TestDashboardWidgets_Catalog(t *testing.T) {
	ctx := context.Background()
	ws, member, outsider, project := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	demo := widgetExtension(t, "demo", wsWidget+","+projectWidget)
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(demo))
	consents := consentsByExt{"demo": {"ui:task.panel", "ui:dashboard.widget"}}
	reg.WithConsentStore(consents)
	calls := 0
	src := extensions.NewDashboardWidgets(reg, widgetProjectsFn(func(ext string, user uuid.UUID) ([]uuid.UUID, error) {
		calls++
		require.Equal(t, "demo", ext)
		if user == member {
			return []uuid.UUID{project}, nil
		}
		return nil, nil
	}))

	got, err := src.DashboardWidgets(ctx, ws, member)
	require.NoError(t, err)
	require.Equal(t, []string{"demo.overview", "demo.per-project"}, ids(got))
	require.Equal(t, 1, calls, "projects are read once per extension")
	over := got[0]
	require.Equal(t, "extension", over.Origin)
	require.Equal(t, "demo ext", over.ExtensionName)
	require.Equal(t, "Обзор", over.TitleI18n["ru"])
	require.Equal(t, []string{"tasks:read"}, over.Requires)
	require.Equal(t, "DemoOverview", over.Render.Builtin)
	require.Nil(t, over.ProjectIDs)
	require.Equal(t, []uuid.UUID{project}, got[1].ProjectIDs)

	// No bound project: the workspace widget stays, the project one goes.
	got, err = src.DashboardWidgets(ctx, ws, outsider)
	require.NoError(t, err)
	require.Equal(t, []string{"demo.overview"}, ids(got))

	// Without a projects source nobody is shown a project widget.
	got, err = extensions.NewDashboardWidgets(reg, nil).DashboardWidgets(ctx, ws, member)
	require.NoError(t, err)
	require.Equal(t, []string{"demo.overview"}, ids(got))

	// Off in the workspace: nothing.
	demo.res.Enabled = false
	got, err = src.DashboardWidgets(ctx, ws, member)
	require.NoError(t, err)
	require.Empty(t, got)
	demo.res.Enabled = true

	// Consent recorded before the slot existed: nothing until re-consent.
	consents["demo"] = []string{"ui:task.panel"}
	got, err = src.DashboardWidgets(ctx, ws, member)
	require.NoError(t, err)
	require.Empty(t, got)

	// The instance level needs no workspace consent.
	demo.res.Source = extensions.SourceInstance
	got, err = src.DashboardWidgets(ctx, ws, member)
	require.NoError(t, err)
	require.Len(t, got, 2)
}

func TestDashboardWidgets_StateErrors(t *testing.T) {
	ctx := context.Background()
	ws, user := uuid.New(), uuid.New()
	broken := widgetExtension(t, "broken", wsWidget)
	broken.err = errors.New("resolve failed")
	noConsent := widgetExtension(t, "noconsent", wsWidget) // the consent store has no answer for it
	fine := widgetExtension(t, "fine", wsWidget)
	reg := extensions.NewRegistry()
	for _, p := range []*widgetProvider{broken, noConsent, fine} {
		require.NoError(t, reg.Register(p))
	}
	reg.WithConsentStore(consentsByExt{"fine": {"ui:dashboard.widget"}})

	// An extension whose state cannot be read is left out; the rest of the catalog still answers.
	got, err := extensions.NewDashboardWidgets(reg, nil).DashboardWidgets(ctx, ws, user)
	require.NoError(t, err)
	require.Equal(t, []string{"fine.overview"}, ids(got))

	_, err = reg.WidgetsAvailable(ctx, ws, "broken")
	require.ErrorContains(t, err, "resolve")
	_, err = reg.WidgetsAvailable(ctx, ws, "noconsent")
	require.ErrorContains(t, err, "consents")
	ok, err := reg.WidgetsAvailable(ctx, ws, "missing")
	require.NoError(t, err)
	require.False(t, ok)

	// A failing projects source fails the catalog rather than offering a project widget blindly.
	reg = extensions.NewRegistry()
	require.NoError(t, reg.Register(widgetExtension(t, "demo", projectWidget)))
	_, err = extensions.NewDashboardWidgets(reg, widgetProjectsFn(func(string, uuid.UUID) ([]uuid.UUID, error) {
		return nil, errors.New("db down")
	})).DashboardWidgets(ctx, ws, user)
	require.ErrorContains(t, err, "projects")
}

func TestDashboardWidgets_SkipsExtensionsWithoutTheSlot(t *testing.T) {
	ctx := context.Background()
	plain := widgetExtension(t, "plain", wsWidget)
	plain.m.Capabilities.UI = []string{"task.panel"}
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(plain))
	got, err := extensions.NewDashboardWidgets(reg, nil).DashboardWidgets(ctx, uuid.New(), uuid.New())
	require.NoError(t, err)
	require.Empty(t, got)
	ok, err := reg.WidgetsAvailable(ctx, uuid.New(), "plain")
	require.NoError(t, err)
	require.False(t, ok)
}
