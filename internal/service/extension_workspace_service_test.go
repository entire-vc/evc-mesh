package service_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/exttest"
	"github.com/entire-vc/evc-mesh/internal/extensions/secretstore"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/encryption"
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

// placeholderProvider resolves like GitLab: an active row without settings
// governs nothing and falls through to the instance level.
type placeholderProvider struct{ exttest.DemoProvider }

func (p placeholderProvider) Resolve(ctx context.Context, ws uuid.UUID) (extensions.Resolved, error) {
	res, err := p.BaseProvider.Resolve(ctx, ws)
	if err != nil || !res.Enabled || res.Source != extensions.SourceWorkspace || len(res.Settings) > 0 {
		return res, err
	}
	return extensions.ResolveSettings(ctx, nil, p.Inst, ws, p.Man.ID)
}

func TestExtensionWorkspace_DisableAfterReenableUnderInstanceNeedsConfirm(t *testing.T) {
	ctx := context.Background()
	f, err := exttest.NewFixture(exttest.InstanceOn{}, false)
	require.NoError(t, err)
	man, err := extensions.ParseManifest([]byte(exttest.Manifest))
	require.NoError(t, err)
	base := extensions.BaseProvider{Man: man, Conns: extensions.IntegrationConnections{Repo: f.Integ}, Inst: exttest.InstanceOn{}}
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(placeholderProvider{exttest.DemoProvider{BaseProvider: base, Healthy: true}}))
	reg.WithConsentStore(extensions.IntegrationConsents{Repo: f.Consents, Declared: func(string) []string { return nil }})
	svc := service.NewExtensionWorkspaceService(reg, f.Integ, f.Consents)
	ws := uuid.New()

	_, err = svc.SetEnabled(ctx, ws, "demo", false, true)
	require.NoError(t, err)
	v, err := svc.SetEnabled(ctx, ws, "demo", true, false)
	require.NoError(t, err)
	require.True(t, v.Status.Enabled)
	require.Equal(t, "instance", v.Status.Source, "an empty active row still resolves to the instance")

	_, err = svc.SetEnabled(ctx, ws, "demo", false, false)
	requireAPIErr(t, err, http.StatusConflict, "confirmation_required")
	list, err := svc.List(ctx, ws)
	require.NoError(t, err)
	require.True(t, list[0].Status.Enabled, "nothing changes without confirm")
	require.Equal(t, "instance", list[0].Status.Source)

	v, err = svc.SetEnabled(ctx, ws, "demo", false, true)
	require.NoError(t, err)
	require.False(t, v.Status.Enabled)
	require.Equal(t, "workspace", v.Status.Source)

	other, err := svc.List(ctx, uuid.New())
	require.NoError(t, err)
	require.True(t, other[0].Status.Enabled, "another workspace stays on")
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

type hidingProvider struct{ exttest.DemoProvider }

func (hidingProvider) HideConnectionSettings() bool { return true }

func TestExtensionWorkspace_HidesInventorySettings(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		wrap func(exttest.DemoProvider) extensions.Provider
		hide bool
	}{
		{"hider", func(d exttest.DemoProvider) extensions.Provider { return hidingProvider{d} }, true},
		{"plain", func(d exttest.DemoProvider) extensions.Provider { return d }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := exttest.NewFixture(nil, false)
			require.NoError(t, err)
			man, err := extensions.ParseManifest([]byte(exttest.Manifest))
			require.NoError(t, err)
			base := extensions.BaseProvider{Man: man, Conns: extensions.IntegrationConnections{Repo: f.Integ}}
			reg := extensions.NewRegistry()
			require.NoError(t, reg.Register(tc.wrap(exttest.DemoProvider{BaseProvider: base, Healthy: true})))
			reg.WithConsentStore(extensions.IntegrationConsents{Repo: f.Consents, Declared: func(string) []string { return nil }})
			svc := service.NewExtensionWorkspaceService(reg, f.Integ, f.Consents)
			ws := uuid.New()
			seed := service.NewExtensionWorkspaceService(plainRegistry(t, f), f.Integ, f.Consents)
			_, err = seed.Connect(ctx, ws, "demo", nil, map[string]any{"base_url": "https://inventory.example"}, fullConsent)
			require.NoError(t, err)
			list, err := svc.List(ctx, ws)
			require.NoError(t, err)
			require.NotNil(t, list[0].Connection)
			if tc.hide {
				require.Empty(t, list[0].Connection.Settings)
			} else {
				require.Equal(t, "https://inventory.example", list[0].Connection.Settings["base_url"])
			}
		})
	}
}

type aliasedProvider struct{ exttest.DemoProvider }

func (aliasedProvider) Aliases() []string { return []string{"demo_legacy"} }

func TestExtensionWorkspace_LegacyIDResolvesToExtension(t *testing.T) {
	ctx := context.Background()
	f, err := exttest.NewFixture(nil, false)
	require.NoError(t, err)
	man, err := extensions.ParseManifest([]byte(exttest.Manifest))
	require.NoError(t, err)
	base := extensions.BaseProvider{Man: man, Conns: extensions.IntegrationConnections{Repo: f.Integ}}
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(aliasedProvider{exttest.DemoProvider{BaseProvider: base, Healthy: true}}))
	reg.WithConsentStore(extensions.IntegrationConsents{Repo: f.Consents, Declared: func(string) []string { return nil }})
	svc := service.NewExtensionWorkspaceService(reg, f.Integ, f.Consents)
	ws := uuid.New()

	_, err = svc.Connect(ctx, ws, "demo_legacy", nil, map[string]any{"base_url": "https://x.example"}, fullConsent)
	require.NoError(t, err)
	res, err := svc.Test(ctx, ws, "demo_legacy")
	require.NoError(t, err)
	require.True(t, res.OK)
	// The row is stored once, under the canonical id.
	_, err = svc.Connect(ctx, ws, "demo", nil, map[string]any{"base_url": "https://x.example"}, fullConsent)
	require.Error(t, err)
	require.NoError(t, svc.Disconnect(ctx, ws, "demo_legacy", false))
}

// A row still stored under the legacy provider name is the workspace's
// connection: listed, managed, and never duplicated by a connect.
func TestExtensionWorkspace_LegacyStoredRowIsTheConnection(t *testing.T) {
	ctx := context.Background()
	f, err := exttest.NewFixture(nil, false)
	require.NoError(t, err)
	man, err := extensions.ParseManifest([]byte(exttest.Manifest))
	require.NoError(t, err)
	base := extensions.BaseProvider{Man: man, Conns: extensions.IntegrationConnections{Repo: f.Integ}}
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(aliasedProvider{exttest.DemoProvider{BaseProvider: base, Healthy: true}}))
	reg.WithConsentStore(extensions.IntegrationConsents{Repo: f.Consents, Declared: func(string) []string { return nil }})
	svc := service.NewExtensionWorkspaceService(reg, f.Integ, f.Consents)
	ws := uuid.New()
	now := time.Now()
	created, err := f.Integ.InsertIfAbsent(ctx, &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws, Provider: domain.IntegrationProvider("demo_legacy"), Config: []byte(`{"base_url":"https://x.example"}`), IsActive: true, CreatedAt: now, UpdatedAt: now})
	require.NoError(t, err)
	require.True(t, created)

	list, err := svc.List(ctx, ws)
	require.NoError(t, err)
	require.NotNil(t, list[0].Connection, "a legacy-named row must show as the connection")

	_, err = svc.Connect(ctx, ws, "demo", nil, map[string]any{"base_url": "https://x.example"}, fullConsent)
	require.Error(t, err, "connect must not create a second row beside the legacy one")

	require.NoError(t, svc.Disconnect(ctx, ws, "demo", false))
}

type fakeAllow struct {
	ok  bool
	err error
}

func (f fakeAllow) Allowed(context.Context, string) (bool, error) { return f.ok, f.err }

func TestExtensionWorkspace_ConnectRefusedOutsideAllowList(t *testing.T) {
	ctx := context.Background()
	f := newFx(t, nil, false)

	svc := f.svc.WithAllowList(fakeAllow{ok: false})
	_, err := svc.Connect(ctx, f.ws, "demo", nil, map[string]any{"base_url": "https://x", "token": "s"}, fullConsent)
	requireAPIErr(t, err, http.StatusForbidden, "extension_not_allowed")
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	require.Contains(t, ae.Message, "allow-list")
	require.Nil(t, f.Integ.Find(f.ws, "demo"), "a refused connect writes nothing")

	// a failing allow-list lookup is an error, never a silent allow
	boom := errors.New("db down")
	_, err = f.svc.WithAllowList(fakeAllow{err: boom}).Connect(ctx, f.ws, "demo", nil, nil, fullConsent)
	require.ErrorIs(t, err, boom)

	// once allowed, the same call connects
	_, err = f.svc.WithAllowList(fakeAllow{ok: true}).Connect(ctx, f.ws, "demo", nil, map[string]any{"base_url": "https://x", "token": "s"}, fullConsent)
	require.NoError(t, err)
	require.NotNil(t, f.Integ.Find(f.ws, "demo"))
}

func useEncryptionKey(t *testing.T) {
	t.Helper()
	encryption.ResetForTest()
	t.Setenv(encryption.EnvKey, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Cleanup(encryption.ResetForTest)
}

func TestExtensionWorkspace_SecretsAreEncryptedAtRest(t *testing.T) {
	useEncryptionKey(t)
	ctx := context.Background()
	f := newFx(t, nil, false)
	_, err := f.svc.Connect(ctx, f.ws, "demo", nil, map[string]any{"token": "s3cret"}, fullConsent)
	require.NoError(t, err)
	stored := tokenOf(t, f.Integ.Find(f.ws, "demo").Config)
	require.NotContains(t, stored, "s3cret")
	require.Contains(t, stored, encryption.Prefix)

	// a kept secret stays ciphertext, a replaced one is encrypted again
	_, err = f.svc.PatchSettings(ctx, f.ws, "demo", map[string]any{"token": "***", "base_url": "https://y"})
	require.NoError(t, err)
	require.Equal(t, stored, tokenOf(t, f.Integ.Find(f.ws, "demo").Config), "echoed mask must keep the stored ciphertext byte-for-byte")
	_, err = f.svc.PatchSettings(ctx, f.ws, "demo", map[string]any{"token": "rotated"})
	require.NoError(t, err)
	stored = string(f.Integ.Find(f.ws, "demo").Config)
	require.NotContains(t, stored, "rotated")
	require.Contains(t, stored, encryption.Prefix)
}

func TestExtensionWorkspace_SubmittedSecretWithCiphertextPrefixIsStillEncrypted(t *testing.T) {
	useEncryptionKey(t)
	ctx := context.Background()
	f := newFx(t, nil, false)
	plain := encryption.Prefix + "not-really-ciphertext"
	_, err := f.svc.Connect(ctx, f.ws, "demo", nil, map[string]any{"token": plain}, fullConsent)
	require.NoError(t, err)
	require.NotContains(t, string(f.Integ.Find(f.ws, "demo").Config), plain)

	_, err = f.svc.PatchSettings(ctx, f.ws, "demo", map[string]any{"token": plain + "-2"})
	require.NoError(t, err)
	require.NotContains(t, string(f.Integ.Find(f.ws, "demo").Config), plain+"-2")
}

type recordingHook struct {
	prepared, saved, removed int
	reject                   error
}

func (h *recordingHook) Prepare(_ context.Context, s, _ map[string]any) error {
	h.prepared++
	if h.reject != nil {
		return h.reject
	}
	s["base_url"] = "https://derived"
	return nil
}
func (h *recordingHook) Saved(context.Context, uuid.UUID)   { h.saved++ }
func (h *recordingHook) Removed(context.Context, uuid.UUID) { h.removed++ }

func TestExtensionWorkspace_SaveHookRunsOnConnectEnableAndRemove(t *testing.T) {
	ctx := context.Background()
	f := newFx(t, nil, false)
	h := &recordingHook{}
	f.svc.WithSaveHook("demo", h)

	v, err := f.svc.Connect(ctx, f.ws, "demo", nil, map[string]any{"token": "x"}, fullConsent)
	require.NoError(t, err)
	require.Equal(t, "https://derived", v.Connection.Settings["base_url"])
	_, err = f.svc.SetEnabled(ctx, f.ws, "demo", false, false)
	require.NoError(t, err)
	_, err = f.svc.PatchSettings(ctx, f.ws, "demo", map[string]any{"token": "y"})
	require.NoError(t, err)
	require.NoError(t, f.svc.Disconnect(ctx, f.ws, "demo", false))
	require.Equal(t, 2, h.prepared)
	require.Equal(t, 3, h.saved)
	require.Equal(t, 1, h.removed)

	h.reject = apierror.BadRequest("nope")
	_, err = f.svc.Connect(ctx, f.ws, "demo", nil, map[string]any{"token": "x"}, fullConsent)
	requireAPIErr(t, err, http.StatusBadRequest, "")
	require.Nil(t, f.Integ.Find(f.ws, "demo"), "a rejected save stores nothing")
}

type fakeTelegram struct {
	service.TelegramClient
	err   error
	token string
}

func (c *fakeTelegram) GetMe(_ context.Context, token string) (string, error) {
	c.token = token
	return "mesh_bot", c.err
}

func TestTelegramSaveHook_ChecksTokenAndFillsUsername(t *testing.T) {
	ctx := context.Background()
	c := &fakeTelegram{}
	h := service.NewTelegramSaveHook(c, nil, nil)

	s := map[string]any{"bot_token": "123:abc"}
	require.NoError(t, h.Prepare(ctx, s, map[string]any{"bot_token": "123:abc"}))
	require.Equal(t, "123:abc", c.token)
	require.Equal(t, "mesh_bot", s["bot_username"])

	c.token = ""
	require.NoError(t, h.Prepare(ctx, map[string]any{"bot_token": encryption.Prefix + "kept"}, map[string]any{}))
	require.Empty(t, c.token, "a kept (not submitted) token is not re-checked")

	prefixed := encryption.Prefix + "submitted"
	require.NoError(t, h.Prepare(ctx, map[string]any{"bot_token": prefixed}, map[string]any{"bot_token": prefixed}))
	require.Equal(t, prefixed, c.token, "a submitted token is checked even if it starts with the ciphertext prefix")

	c.err = errors.New("401 unauthorized")
	requireAPIErr(t, h.Prepare(ctx, map[string]any{"bot_token": "bad"}, map[string]any{"bot_token": "bad"}), http.StatusBadRequest, "")

	off := service.NewTelegramSaveHook(c, nil, func(context.Context) bool { return false })
	requireAPIErr(t, off.Prepare(ctx, map[string]any{"bot_token": "123:abc"}, map[string]any{"bot_token": "123:abc"}), http.StatusBadRequest, "")
}

func TestExtensionWorkspace_ViewFlagsPurgeAndManagedSettings(t *testing.T) {
	ctx := context.Background()
	f := newFx(t, nil, true)
	list, err := f.svc.List(ctx, f.ws)
	require.NoError(t, err)
	require.True(t, list[0].CanPurgeData)
	require.False(t, list[0].SettingsManagedElsewhere)

	g := newFx(t, nil, false)
	list, err = g.svc.List(ctx, g.ws)
	require.NoError(t, err)
	require.False(t, list[0].CanPurgeData)
}

// plainRegistry registers the demo provider without the managed-elsewhere flag,
// standing in for the other flow that creates such a connection.
func plainRegistry(t *testing.T, f *exttest.Fixture) *extensions.Registry {
	t.Helper()
	man, err := extensions.ParseManifest([]byte(exttest.Manifest))
	require.NoError(t, err)
	base := extensions.BaseProvider{Man: man, Conns: extensions.IntegrationConnections{Repo: f.Integ}}
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(exttest.DemoProvider{BaseProvider: base, Healthy: true}))
	reg.WithConsentStore(extensions.IntegrationConsents{Repo: f.Consents, Declared: func(string) []string { return nil }})
	return reg
}

func TestExtensionWorkspace_GenericWritesRefuseManagedElsewhere(t *testing.T) {
	ctx := context.Background()
	f, err := exttest.NewFixture(nil, false)
	require.NoError(t, err)
	man, err := extensions.ParseManifest([]byte(exttest.Manifest))
	require.NoError(t, err)
	base := extensions.BaseProvider{Man: man, Conns: extensions.IntegrationConnections{Repo: f.Integ}}
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(hidingProvider{exttest.DemoProvider{BaseProvider: base, Healthy: true}}))
	reg.WithConsentStore(extensions.IntegrationConsents{Repo: f.Consents, Declared: func(string) []string { return nil }})
	svc := service.NewExtensionWorkspaceService(reg, f.Integ, f.Consents)

	// no connection yet: generic connect must not create one
	fresh := uuid.New()
	_, err = svc.Connect(ctx, fresh, "demo", nil, nil, fullConsent)
	requireAPIErr(t, err, http.StatusBadRequest, "validation_failed")
	require.Nil(t, f.Integ.Find(fresh, "demo"), "generic connect must not create a managed-elsewhere connection")

	// connection created by the owning flow: settings and delete are refused, row survives
	ws := uuid.New()
	_, err = service.NewExtensionWorkspaceService(plainRegistry(t, f), f.Integ, f.Consents).Connect(ctx, ws, "demo", nil, nil, fullConsent)
	require.NoError(t, err)

	_, err = svc.PatchSettings(ctx, ws, "demo", map[string]any{"base_url": "https://changed.example"})
	requireAPIErr(t, err, http.StatusBadRequest, "validation_failed")

	err = svc.Disconnect(ctx, ws, "demo", false)
	requireAPIErr(t, err, http.StatusBadRequest, "validation_failed")
	require.NotNil(t, f.Integ.Find(ws, "demo"), "managed-elsewhere connection must survive the generic DELETE")
}

func TestExtensionWorkspace_ConfirmedInstanceDisableRunsSaveHook(t *testing.T) {
	ctx := context.Background()
	f := newFx(t, exttest.InstanceOn{}, false)
	h := &recordingHook{}
	f.svc.WithSaveHook("demo", h)

	_, err := f.svc.SetEnabled(ctx, f.ws, "demo", false, true)
	require.NoError(t, err)
	require.NotNil(t, f.Integ.Find(f.ws, "demo"))
	require.Equal(t, 1, h.saved, "the new override row must reach the save hook (Telegram stops its poller there)")
}

func tokenOf(t *testing.T, raw []byte) string {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	tok, _ := m["token"].(string)
	return tok
}

// guardedIntegrations stands in for the repository: it refuses the generic Update
// on every row, like Runner rows in Postgres, while the provider hides its settings.
// The fault fields make the enable path's error branches reachable.
type guardedIntegrations struct {
	*exttest.MemIntegrations
	insertErr  error // InsertIfAbsent fails
	insertLost bool  // InsertIfAbsent reports a lost race but leaves no row
	lostGetErr error // after a lost insert the follow-up read fails
	setErr     error // SetActive fails
	lost       bool
}

func (g *guardedIntegrations) Update(context.Context, uuid.UUID, domain.UpdateIntegrationInput) (*domain.IntegrationConfig, error) {
	return nil, apierror.BadRequest("use the versioned runtime API")
}

func (g *guardedIntegrations) InsertIfAbsent(ctx context.Context, c *domain.IntegrationConfig) (bool, error) {
	if g.insertErr != nil {
		return false, g.insertErr
	}
	if g.insertLost {
		g.lost = true
		return false, nil
	}
	created, err := g.MemIntegrations.InsertIfAbsent(ctx, c)
	g.lost = !created
	return created, err
}

func (g *guardedIntegrations) GetByProvider(ctx context.Context, ws uuid.UUID, p domain.IntegrationProvider) (*domain.IntegrationConfig, error) {
	if g.lost && g.lostGetErr != nil {
		return nil, g.lostGetErr
	}
	return g.MemIntegrations.GetByProvider(ctx, ws, p)
}

func (g *guardedIntegrations) SetActive(ctx context.Context, id uuid.UUID, active bool) error {
	if g.setErr != nil {
		return g.setErr
	}
	return g.MemIntegrations.SetActive(ctx, id, active)
}

func hidingService(t *testing.T, g *guardedIntegrations) (*service.ExtensionWorkspaceService, *exttest.Fixture) {
	t.Helper()
	f, err := exttest.NewFixture(nil, false)
	require.NoError(t, err)
	man, err := extensions.ParseManifest([]byte(exttest.Manifest))
	require.NoError(t, err)
	base := extensions.BaseProvider{Man: man, Conns: extensions.IntegrationConnections{Repo: f.Integ}}
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(hidingProvider{exttest.DemoProvider{BaseProvider: base, Healthy: true}}))
	reg.WithConsentStore(extensions.IntegrationConsents{Repo: f.Consents, Declared: func(string) []string { return nil }})
	if g == nil {
		g = &guardedIntegrations{}
	}
	g.MemIntegrations = f.Integ
	return service.NewExtensionWorkspaceService(reg, g, f.Consents), f
}

func TestExtensionWorkspace_EnableManagedElsewhereCreatesMissingRow(t *testing.T) {
	ctx := context.Background()
	svc, f := hidingService(t, nil)
	ws := uuid.New()

	v, err := svc.SetEnabled(ctx, ws, "demo", true, false)
	require.NoError(t, err)
	require.True(t, v.Status.Enabled)
	row := f.Integ.Find(ws, "demo")
	require.NotNil(t, row)
	require.True(t, row.IsActive)
	require.JSONEq(t, `{}`, string(row.Config))

	// repeating it keeps the single row
	_, err = svc.SetEnabled(ctx, ws, "demo", true, false)
	require.NoError(t, err)
	require.Len(t, f.Integ.Rows, 1)
}

func TestExtensionWorkspace_EnableManagedElsewhereSwitchesInactiveRowOn(t *testing.T) {
	ctx := context.Background()
	svc, f := hidingService(t, nil)
	ws := uuid.New()
	require.NoError(t, f.Integ.Upsert(ctx, &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws, Provider: "demo", Config: []byte(`{}`), IsActive: false}))

	_, err := svc.SetEnabled(ctx, ws, "demo", true, false)
	require.NoError(t, err)
	require.True(t, f.Integ.Find(ws, "demo").IsActive)
}

func TestExtensionWorkspace_EnableManagedElsewhereLosingRaceSwitchesWinnerOn(t *testing.T) {
	ctx := context.Background()
	svc, f := hidingService(t, nil)
	ws := uuid.New()
	f.Integ.RaceRow = &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws, Provider: "demo", Config: []byte(`{}`), IsActive: false}

	_, err := svc.SetEnabled(ctx, ws, "demo", true, false)
	require.NoError(t, err)
	require.Len(t, f.Integ.Rows, 1)
	require.True(t, f.Integ.Find(ws, "demo").IsActive)
}

func TestExtensionWorkspace_EnablePlainExtensionStillNeedsConnect(t *testing.T) {
	f := newFx(t, nil, false)
	_, err := f.svc.SetEnabled(context.Background(), uuid.New(), "demo", true, false)
	requireAPIErr(t, err, http.StatusNotFound, "")
}

func TestExtensionWorkspace_EnableManagedElsewhereReportsStoreFailures(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("store down")
	for _, tc := range []struct {
		name string
		g    *guardedIntegrations
		seed bool // an inactive row already exists
		race bool // an inactive row appears between the read and the insert
		code int
	}{
		{"insert fails", &guardedIntegrations{insertErr: boom}, false, false, 0},
		{"read after lost insert fails", &guardedIntegrations{insertLost: true, lostGetErr: boom}, false, false, 0},
		{"lost insert leaves no row", &guardedIntegrations{insertLost: true}, false, false, http.StatusConflict},
		{"switch on after lost insert fails", &guardedIntegrations{setErr: boom}, false, true, 0},
		{"switch on existing row fails", &guardedIntegrations{setErr: boom}, true, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, f := hidingService(t, tc.g)
			ws := uuid.New()
			if tc.seed {
				require.NoError(t, f.Integ.Upsert(ctx, &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws, Provider: "demo", Config: []byte(`{}`)}))
			}
			if tc.race {
				f.Integ.RaceRow = &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws, Provider: "demo", Config: []byte(`{}`)}
			}
			_, err := svc.SetEnabled(ctx, ws, "demo", true, false)
			require.Error(t, err)
			if tc.code != 0 {
				requireAPIErr(t, err, tc.code, "conflict")
			} else {
				require.ErrorIs(t, err, boom)
			}
		})
	}
}

type recordingStore struct{ refs []secretstore.Ref }

func (r *recordingStore) Put(_ context.Context, ref secretstore.Ref, plain string) (string, error) {
	r.refs = append(r.refs, ref)
	return secretstore.KeepPrefix + "id-" + plain, nil
}

func (r *recordingStore) Resolve(context.Context, string) secretstore.Result {
	return secretstore.Result{Degraded: true, Reason: "not used"}
}

// A workspace secret must reach the store with its workspace and extension, or
// Keep registers it at organization scope under a colliding name.
func TestExtensionWorkspace_SecretRefCarriesWorkspaceAndExtension(t *testing.T) {
	ctx := context.Background()
	rec := &recordingStore{}
	prev := secretstore.SetDefault(rec)
	t.Cleanup(func() { secretstore.SetDefault(prev) })
	f := newFx(t, nil, false)

	by := uuid.New()
	_, err := f.svc.Connect(ctx, f.ws, "demo", &by, map[string]any{"base_url": "https://x", "token": "s3cret"}, fullConsent)
	require.NoError(t, err)
	require.Equal(t, []secretstore.Ref{{WorkspaceID: f.ws, ExtensionID: "demo", Field: "token"}}, rec.refs)
}

type failingStore struct{}

func (failingStore) Put(context.Context, secretstore.Ref, string) (string, error) {
	return "", errors.New("keep down")
}

func (failingStore) Resolve(context.Context, string) secretstore.Result {
	return secretstore.Result{Degraded: true, Reason: "keep down"}
}

// A store that refuses the secret fails the save; the plaintext is not kept.
func TestExtensionWorkspace_SecretStoreFailureRejectsSave(t *testing.T) {
	prev := secretstore.SetDefault(failingStore{})
	t.Cleanup(func() { secretstore.SetDefault(prev) })
	f := newFx(t, nil, false)

	by := uuid.New()
	_, err := f.svc.Connect(context.Background(), f.ws, "demo", &by, map[string]any{"base_url": "https://x", "token": "s3cret"}, fullConsent)
	requireAPIErr(t, err, http.StatusBadRequest, "")
}
