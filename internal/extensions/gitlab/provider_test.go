package gitlab_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/gitlab"
)

type conns struct{ row *extensions.Connection }

func (c conns) Connection(context.Context, uuid.UUID, string) (*extensions.Connection, error) {
	return c.row, nil
}

type inst struct{ s *extensions.InstanceSetting }

func (i inst) InstanceSetting(context.Context, string) (*extensions.InstanceSetting, error) {
	return i.s, nil
}

func prov(c *extensions.Connection, i *extensions.InstanceSetting) *gitlab.Provider {
	return gitlab.New(extensions.Deps{Conns: conns{c}, Inst: inst{i}})
}

func instOn(token, secret string) *extensions.InstanceSetting {
	return &extensions.InstanceSetting{Enabled: true, Settings: map[string]any{"base_url": "https://git.example", "token": token, "webhook_secret": secret}}
}

func TestValidate(t *testing.T) {
	p := prov(nil, nil)
	require.NoError(t, p.Validate(extensions.LevelWorkspace, map[string]any{"token": "t"}))
	require.NoError(t, p.Validate(extensions.LevelInstance, map[string]any{"webhook_secret": "s"}))
	require.Error(t, p.Validate(extensions.LevelWorkspace, map[string]any{}), "a connection with no base_url, token or secret governs nothing")
	require.NoError(t, p.Validate(extensions.LevelProject, map[string]any{"repo_path": "entire-vc/evc-mesh"}))
	require.NoError(t, p.Validate(extensions.LevelProject, map[string]any{"repo_path": "entire-vc/sub/evc-mesh"}), "GitLab allows nested groups")
	for _, bad := range []string{"noslash", "group//project", "/project", "group/", ""} {
		require.Error(t, p.Validate(extensions.LevelProject, map[string]any{"repo_path": bad}), "repo_path %q", bad)
	}
}

func TestResolveConfig_OrderAndPlaceholders(t *testing.T) {
	ctx := context.Background()
	ws := uuid.New()

	// workspace row with its own values wins wholly over the instance level
	_, tok, sec, src, ok := prov(&extensions.Connection{IsActive: true, Settings: map[string]any{"token": "wt"}}, instOn("it", "is")).ResolveConfig(ctx, ws)
	require.True(t, ok)
	require.Equal(t, []string{"wt", "", "workspace"}, []string{tok, sec, src})

	// placeholder row (active, nothing in it) falls through to the instance level
	_, tok, sec, src, ok = prov(&extensions.Connection{IsActive: true, Settings: map[string]any{}}, instOn("it", "is")).ResolveConfig(ctx, ws)
	require.True(t, ok)
	require.Equal(t, []string{"it", "is", "instance"}, []string{tok, sec, src})

	// an explicit off row switches GitLab off whatever the instance says
	_, _, _, _, ok = prov(&extensions.Connection{IsActive: false}, instOn("it", "is")).ResolveConfig(ctx, ws)
	require.False(t, ok)

	// no row: instance level decides; instance switched off or empty = off
	_, _, _, src, ok = prov(nil, instOn("it", "")).ResolveConfig(ctx, ws)
	require.True(t, ok)
	require.Equal(t, "instance", src)
	_, _, _, _, ok = prov(nil, &extensions.InstanceSetting{Enabled: false, Settings: map[string]any{"token": "it"}}).ResolveConfig(ctx, ws)
	require.False(t, ok)
	_, _, _, _, ok = prov(nil, nil).ResolveConfig(ctx, ws)
	require.False(t, ok)
}

func TestInstanceWebhookSecret(t *testing.T) {
	ctx := context.Background()
	require.Equal(t, "is", prov(nil, instOn("it", "is")).InstanceWebhookSecret(ctx))
	require.Empty(t, prov(nil, &extensions.InstanceSetting{Enabled: false, Settings: map[string]any{"webhook_secret": "is"}}).InstanceWebhookSecret(ctx))
	require.Empty(t, prov(nil, nil).InstanceWebhookSecret(ctx))
}

func TestHooks_CanonicalRouteOnlyOnceHandlerIsSet(t *testing.T) {
	p := prov(nil, nil)
	require.Empty(t, p.Hooks().Routes)
	p.WebhookHandler = func(echo.Context) error { return nil }
	routes := p.Hooks().Routes
	require.Len(t, routes, 1)
	require.Equal(t, http.MethodPost, routes[0].Method)
	require.Equal(t, "/webhooks/gitlab/gitlab", routes[0].Path)
}

type failingConns struct{}

func (failingConns) Connection(context.Context, uuid.UUID, string) (*extensions.Connection, error) {
	return nil, errors.New("db down")
}

func TestValidate_BadShapeAndUnknownLevel(t *testing.T) {
	p := prov(nil, nil)
	require.Error(t, p.Validate(extensions.LevelWorkspace, map[string]any{"token": 5}), "a non-string token is refused")
	require.Error(t, p.Validate(extensions.Level("galaxy"), map[string]any{"token": "t"}))
}

func TestHealth(t *testing.T) {
	ctx := context.Background()
	p := prov(nil, nil)
	require.True(t, p.Health(ctx, extensions.Resolved{Settings: map[string]any{"token": "t"}}).OK)
	require.True(t, p.Health(ctx, extensions.Resolved{Settings: map[string]any{"webhook_secret": "s"}}).OK)
	h := p.Health(ctx, extensions.Resolved{Settings: map[string]any{}})
	require.False(t, h.OK)
	require.NotEmpty(t, h.Message)
	require.False(t, p.Health(ctx, extensions.Resolved{Settings: map[string]any{"token": 5}}).OK, "undecodable settings are not healthy")
}

// A failing workspace lookup falls back to the instance level; if that is off
// too, GitLab is off, never "open".
func TestResolveConfig_LookupFailureFallsBackToInstance(t *testing.T) {
	ctx := context.Background()
	ws := uuid.New()

	p := gitlab.New(extensions.Deps{Conns: failingConns{}, Inst: inst{instOn("it", "is")}})
	_, tok, sec, src, ok := p.ResolveConfig(ctx, ws)
	require.True(t, ok)
	require.Equal(t, []string{"it", "is", "instance"}, []string{tok, sec, src})

	p = gitlab.New(extensions.Deps{Conns: failingConns{}, Inst: inst{nil}})
	_, _, _, _, ok = p.ResolveConfig(ctx, ws)
	require.False(t, ok)
}

// An enabled row whose settings decode to nothing usable is off.
func TestResolveConfig_EnabledButEmptyIsOff(t *testing.T) {
	ctx := context.Background()
	_, _, _, _, ok := prov(nil, &extensions.InstanceSetting{Enabled: true, Settings: map[string]any{}}).ResolveConfig(ctx, uuid.New())
	require.False(t, ok)
	_, _, _, _, ok = prov(nil, &extensions.InstanceSetting{Enabled: true, Settings: map[string]any{"token": 5}}).ResolveConfig(ctx, uuid.New())
	require.False(t, ok)
}

func TestResolve_ErrorIsReturned(t *testing.T) {
	p := gitlab.New(extensions.Deps{Conns: failingConns{}, Inst: inst{nil}})
	_, err := p.Resolve(context.Background(), uuid.New())
	require.Error(t, err)
}
