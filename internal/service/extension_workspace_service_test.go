package service_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/exttest"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type fx struct {
	*exttest.Fixture
	svc *service.ExtensionWorkspaceService
	ws  uuid.UUID
}

func newFx(t *testing.T, inst extensions.InstanceStore, purge bool) *fx {
	t.Helper()
	f, err := exttest.NewFixture(inst, purge)
	require.NoError(t, err)
	return &fx{Fixture: f, svc: service.NewExtensionWorkspaceService(f.Registry, f.Integ, f.Consents), ws: uuid.New()}
}

func requireAPIErr(t *testing.T, err error, status int, code string) {
	t.Helper()
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, status, ae.Code)
	if code != "" {
		require.Equal(t, code, ae.ErrorCode)
	}
}

var fullConsent = []string{"api:tasks.read", "events:task.updated"}

func TestExtensionWorkspace_ConnectLifecycle(t *testing.T) {
	ctx := context.Background()
	f := newFx(t, nil, false)

	list, err := f.svc.List(ctx, f.ws)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Nil(t, list[0].Connection)
	require.False(t, list[0].Status.Enabled)
	require.Equal(t, "none", list[0].Status.Source)
	require.Nil(t, list[0].Status.Health, "no health while off")

	by := uuid.New()
	v, err := f.svc.Connect(ctx, f.ws, "demo", &by, map[string]any{"base_url": "https://x", "token": "s3cret"}, fullConsent)
	require.NoError(t, err)
	require.True(t, v.Status.Enabled)
	require.Equal(t, "workspace", v.Status.Source)
	require.Equal(t, fullConsent, v.Status.ConsentedCapabilities)
	require.NotNil(t, v.Connection)
	require.Equal(t, "***", v.Connection.Settings["token"], "secret is masked")
	require.Equal(t, "https://x", v.Connection.Settings["base_url"])
	require.Equal(t, &by, v.Connection.ConsentedBy)
	require.NotNil(t, v.Status.Health)
	require.True(t, v.Status.Health.OK)

	_, err = f.svc.Connect(ctx, f.ws, "demo", &by, nil, fullConsent)
	requireAPIErr(t, err, http.StatusConflict, "already_connected")

	// secret sent back as *** keeps the stored value; other keys merge
	v, err = f.svc.PatchSettings(ctx, f.ws, "demo", map[string]any{"token": "***", "base_url": "https://y"})
	require.NoError(t, err)
	require.Equal(t, "https://y", v.Connection.Settings["base_url"])
	require.Contains(t, string(f.Integ.Find(f.ws, "demo").Config), "s3cret")
	_, err = f.svc.PatchSettings(ctx, f.ws, "demo", map[string]any{"token": "new"})
	require.NoError(t, err)
	require.Contains(t, string(f.Integ.Find(f.ws, "demo").Config), "new")
	_, err = f.svc.PatchSettings(ctx, f.ws, "demo", map[string]any{"bad": 1})
	requireAPIErr(t, err, http.StatusBadRequest, "validation_failed")

	v, err = f.svc.SetEnabled(ctx, f.ws, "demo", false, false)
	require.NoError(t, err)
	require.False(t, v.Status.Enabled)
	v, err = f.svc.SetEnabled(ctx, f.ws, "demo", true, false)
	require.NoError(t, err)
	require.True(t, v.Status.Enabled)

	res, err := f.svc.Test(ctx, f.ws, "demo")
	require.NoError(t, err)
	require.True(t, res.OK)
	require.True(t, res.Status.Enabled)

	require.NoError(t, f.svc.Disconnect(ctx, f.ws, "demo", false))
	require.Nil(t, f.Integ.Find(f.ws, "demo"))
	_, err = f.svc.PatchSettings(ctx, f.ws, "demo", map[string]any{})
	requireAPIErr(t, err, http.StatusNotFound, "not_found")
	res, err = f.svc.Test(ctx, f.ws, "demo")
	require.NoError(t, err)
	require.False(t, res.OK)
	require.NotEmpty(t, res.Message)
}

func TestExtensionWorkspace_ConnectRejects(t *testing.T) {
	ctx := context.Background()
	f := newFx(t, nil, false)

	_, err := f.svc.Connect(ctx, f.ws, "demo", nil, nil, []string{"api:tasks.read"})
	requireAPIErr(t, err, http.StatusBadRequest, "validation_failed")
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	require.Contains(t, ae.Validation, "consent")
	require.Contains(t, ae.Validation["consent"], "events:task.updated")

	_, err = f.svc.Connect(ctx, f.ws, "demo", nil, map[string]any{"bad": true}, fullConsent)
	requireAPIErr(t, err, http.StatusBadRequest, "validation_failed")
	require.Nil(t, f.Integ.Find(f.ws, "demo"), "nothing written on rejection")

	_, err = f.svc.Connect(ctx, f.ws, "nope", nil, nil, fullConsent)
	requireAPIErr(t, err, http.StatusNotFound, "not_found")
	for _, call := range []func() error{
		func() error { _, e := f.svc.PatchSettings(ctx, f.ws, "nope", nil); return e },
		func() error { _, e := f.svc.SetEnabled(ctx, f.ws, "nope", true, false); return e },
		func() error { _, e := f.svc.Test(ctx, f.ws, "nope"); return e },
		func() error { return f.svc.Disconnect(ctx, f.ws, "nope", false) },
	} {
		requireAPIErr(t, call(), http.StatusNotFound, "not_found")
	}

	boom := errors.New("db down")
	f.Integ.Err = boom
	_, err = f.svc.Connect(ctx, f.ws, "demo", nil, nil, fullConsent)
	require.ErrorIs(t, err, boom)
	_, err = f.svc.List(ctx, f.ws)
	require.Error(t, err)
}

func TestExtensionWorkspace_DisableUnderInstanceNeedsConfirm(t *testing.T) {
	ctx := context.Background()
	f := newFx(t, exttest.InstanceOn{}, false)

	list, err := f.svc.List(ctx, f.ws)
	require.NoError(t, err)
	require.Equal(t, "instance", list[0].Status.Source)
	require.True(t, list[0].Status.Enabled)

	_, err = f.svc.SetEnabled(ctx, f.ws, "demo", false, false)
	requireAPIErr(t, err, http.StatusConflict, "confirmation_required")
	require.Nil(t, f.Integ.Find(f.ws, "demo"), "nothing changes without confirm")

	_, err = f.svc.SetEnabled(ctx, f.ws, "demo", true, false)
	requireAPIErr(t, err, http.StatusNotFound, "not_found")

	v, err := f.svc.SetEnabled(ctx, f.ws, "demo", false, true)
	require.NoError(t, err)
	require.False(t, v.Status.Enabled)
	require.Equal(t, "workspace", v.Status.Source, "the workspace row now overrides the instance")

	// without any instance level a missing row cannot be disabled
	g := newFx(t, nil, false)
	_, err = g.svc.SetEnabled(ctx, g.ws, "demo", false, true)
	requireAPIErr(t, err, http.StatusNotFound, "not_found")
}

func TestExtensionWorkspace_DeleteData(t *testing.T) {
	ctx := context.Background()

	f := newFx(t, nil, false)
	_, err := f.svc.Connect(ctx, f.ws, "demo", nil, nil, fullConsent)
	require.NoError(t, err)
	err = f.svc.Disconnect(ctx, f.ws, "demo", true)
	requireAPIErr(t, err, http.StatusBadRequest, "validation_failed")
	require.NotNil(t, f.Integ.Find(f.ws, "demo"), "connection kept when data=delete is unsupported")

	g := newFx(t, nil, true)
	_, err = g.svc.Connect(ctx, g.ws, "demo", nil, nil, fullConsent)
	require.NoError(t, err)
	require.NoError(t, g.svc.Disconnect(ctx, g.ws, "demo", true))
	require.Equal(t, 1, g.Purged)
	require.Nil(t, g.Integ.Find(g.ws, "demo"))
}

func TestExtensionWorkspace_ViewTimestamps(t *testing.T) {
	ctx := context.Background()
	f := newFx(t, nil, false)
	before := time.Now().Add(-time.Second)
	v, err := f.svc.Connect(ctx, f.ws, "demo", nil, nil, fullConsent)
	require.NoError(t, err)
	require.True(t, v.Connection.ConsentedAt.After(before))
	require.NotNil(t, v.Status.HealthCheckedAt)
}

func TestExtensionWorkspace_FailedPurgeKeepsConnectionForRetry(t *testing.T) {
	ctx := context.Background()
	f := newFx(t, nil, true)
	_, err := f.svc.Connect(ctx, f.ws, "demo", nil, nil, fullConsent)
	require.NoError(t, err)

	f.PurgeErr = errors.New("storage down")
	require.Error(t, f.svc.Disconnect(ctx, f.ws, "demo", true))
	require.NotNil(t, f.Integ.Find(f.ws, "demo"), "connection must survive a failed purge so the call can be retried")

	f.PurgeErr = nil
	require.NoError(t, f.svc.Disconnect(ctx, f.ws, "demo", true))
	require.Nil(t, f.Integ.Find(f.ws, "demo"))
	require.Equal(t, 2, f.Purged)
}

func TestExtensionWorkspace_ConnectRollsBackWhenConsentWriteFails(t *testing.T) {
	ctx := context.Background()
	f := newFx(t, nil, false)
	f.Consents.Err = errors.New("consent write failed")
	_, err := f.svc.Connect(ctx, f.ws, "demo", nil, nil, fullConsent)
	require.Error(t, err)
	require.Nil(t, f.Integ.Find(f.ws, "demo"), "no connection without recorded consent")

	f.Consents.Err = nil
	_, err = f.svc.Connect(ctx, f.ws, "demo", nil, nil, fullConsent)
	require.NoError(t, err, "retry must not hit already_connected")
}

// A connect that lands between the status read and the disable keeps its settings and
// consent: the disable only switches it off instead of overwriting the row.
func TestExtensionWorkspace_DisableUnderInstanceKeepsConcurrentConnection(t *testing.T) {
	ctx := context.Background()
	f := newFx(t, exttest.InstanceOn{}, false)
	raced := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: f.ws, Provider: "demo", Config: []byte(`{"k":"v"}`), IsActive: true}
	f.Integ.RaceRow = raced
	v, err := f.svc.SetEnabled(ctx, f.ws, "demo", false, true)
	require.NoError(t, err)
	require.False(t, v.Status.Enabled)
	row := f.Integ.Find(f.ws, "demo")
	require.Equal(t, raced.ID, row.ID)
	require.JSONEq(t, `{"k":"v"}`, string(row.Config), "the winner's settings survive")
	require.False(t, row.IsActive)
}

func TestExtensionWorkspace_ConnectReportsFailedRollback(t *testing.T) {
	ctx := context.Background()
	f := newFx(t, nil, false)
	f.Consents.Err = errors.New("consent write failed")
	f.Integ.DeleteErr = errors.New("delete failed")
	_, err := f.svc.Connect(ctx, f.ws, "demo", nil, nil, fullConsent)
	require.Error(t, err)
	require.Contains(t, err.Error(), "consent write failed")
	require.Contains(t, err.Error(), "rollback of the new connection also failed")
	require.Contains(t, err.Error(), "delete failed")
}

// With only the instance level on there is no workspace connection, so nothing is
// reported as consented by the workspace, whatever the store falls back to.
func TestExtensionWorkspace_InstanceOnlyReportsNoWorkspaceConsent(t *testing.T) {
	f := newFx(t, exttest.InstanceOn{}, false)
	list, err := f.svc.List(context.Background(), f.ws)
	require.NoError(t, err)
	require.Equal(t, "instance", list[0].Status.Source)
	require.Empty(t, list[0].Status.ConsentedCapabilities)
}
