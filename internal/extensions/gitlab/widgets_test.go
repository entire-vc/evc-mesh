package gitlab_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/gitlab"
)

// switchable is a workspace connection a test turns on and off.
type switchable struct{ row *extensions.Connection }

func (s *switchable) Connection(context.Context, uuid.UUID, string) (*extensions.Connection, error) {
	return s.row, nil
}

// consents is the recorded consent of the workspace connection.
type consents struct{ caps []string }

func (c *consents) Consented(context.Context, uuid.UUID, string) ([]string, error) {
	return c.caps, nil
}

// projectsFor answers WidgetProjects per user.
type projectsFor map[uuid.UUID][]uuid.UUID

func (p projectsFor) WidgetProjects(_ context.Context, _ uuid.UUID, ext string, user uuid.UUID) ([]uuid.UUID, error) {
	if ext != gitlab.ID {
		return nil, nil
	}
	return p[user], nil
}

func widgetIDs(ws []extensions.DashboardWidget) []string {
	out := []string{}
	for _, w := range ws {
		out = append(out, w.ID)
	}
	return out
}

func TestDashboardWidgets_FollowEnabledConsentAndProjects(t *testing.T) {
	ctx := context.Background()
	ws := uuid.New()
	member, outsider := uuid.New(), uuid.New()
	bound := uuid.New()

	conn := &switchable{row: &extensions.Connection{IsActive: true, Settings: map[string]any{"base_url": "https://git.example", "token": "t"}}}
	cons := &consents{}
	cons.caps = append(cons.caps, extensions.DeclaredCapabilities(gitlab.New(extensions.Deps{}).Manifest().Capabilities)...)
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(gitlab.New(extensions.Deps{Conns: conn})))
	reg.WithConsentStore(cons)
	src := extensions.NewDashboardWidgets(reg, projectsFor{member: {bound}})

	// Enabled, consented, the viewer works in a bound project: the widget is offered with that project.
	got, err := src.DashboardWidgets(ctx, ws, member)
	require.NoError(t, err)
	require.Equal(t, []string{"gitlab.failed-pipelines"}, widgetIDs(got))
	w := got[0]
	require.Equal(t, "extension", w.Origin)
	require.Equal(t, "gitlab", w.ExtensionID)
	require.Equal(t, "project", w.Scope)
	require.Equal(t, "GitlabFailedPipelinesWidget", w.Render.Builtin)
	require.Equal(t, []uuid.UUID{bound}, w.ProjectIDs)
	require.Equal(t, extensions.WidgetCells{W: 6, H: 4}, w.Size.Default)
	require.Equal(t, "Упавшие пайплайны", w.TitleI18n["ru"])

	// A viewer with no bound project gets no scope "project" widget.
	got, err = src.DashboardWidgets(ctx, ws, outsider)
	require.NoError(t, err)
	require.Empty(t, got)

	// Switched off in the workspace: the widget leaves the catalog.
	conn.row.IsActive = false
	got, err = src.DashboardWidgets(ctx, ws, member)
	require.NoError(t, err)
	require.Empty(t, got)

	// Back on: it returns.
	conn.row.IsActive = true
	got, err = src.DashboardWidgets(ctx, ws, member)
	require.NoError(t, err)
	require.Equal(t, []string{"gitlab.failed-pipelines"}, widgetIDs(got))

	// A consent recorded before the release that added the slot hides the
	// widget until the administrator consents again.
	var old []string
	for _, c := range cons.caps {
		if c != "ui:dashboard.widget" {
			old = append(old, c)
		}
	}
	cons.caps = old
	got, err = src.DashboardWidgets(ctx, ws, member)
	require.NoError(t, err)
	require.Empty(t, got)
	ok, err := reg.WidgetsAvailable(ctx, ws, gitlab.ID)
	require.NoError(t, err)
	require.False(t, ok)
}

// Without a workspace connection the instance level governs a builtin: there
// is no per-workspace consent to wait for.
func TestDashboardWidgets_InstanceLevelNeedsNoWorkspaceConsent(t *testing.T) {
	ctx := context.Background()
	user, project := uuid.New(), uuid.New()
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(gitlab.New(extensions.Deps{Conns: conns{nil}, Inst: inst{instOn("it", "is")}})))
	reg.WithConsentStore(&consents{})
	got, err := extensions.NewDashboardWidgets(reg, projectsFor{user: {project}}).DashboardWidgets(ctx, uuid.New(), user)
	require.NoError(t, err)
	require.Equal(t, []string{"gitlab.failed-pipelines"}, widgetIDs(got))

	// Control: the instance level switched off hides it.
	reg = extensions.NewRegistry()
	require.NoError(t, reg.Register(gitlab.New(extensions.Deps{Conns: conns{nil}, Inst: inst{&extensions.InstanceSetting{Enabled: false}}})))
	got, err = extensions.NewDashboardWidgets(reg, projectsFor{user: {project}}).DashboardWidgets(ctx, uuid.New(), user)
	require.NoError(t, err)
	require.Empty(t, got)
}
