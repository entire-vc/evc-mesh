package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions/exttest"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// demo 1.0.0 (exttest.Manifest) declares api:tasks.read and events:task.updated.
var demoDeclared = []string{"api:tasks.read", "events:task.updated"}

type memInstalled struct{ c *exttest.MemConsents }

func (r memInstalled) Get(_ context.Context, id string) (*domain.InstalledExtension, error) {
	return r.c.Installed[id], nil
}

func demoRepoRow(version, commit string, api, events []string) *domain.InstalledExtension {
	a, _ := json.Marshal(api)
	e, _ := json.Marshal(events)
	m := `{"id":"demo","name":"Demo","version":"` + version + `","kind":"integration","runtime":"builtin","category":"vcs",
 "levels":{"instance":true,"workspace":true,"project":false},
 "capabilities":{"api":` + string(a) + `,"events":` + string(e) + `}}`
	return &domain.InstalledExtension{ExtensionID: "demo", Version: version, Manifest: json.RawMessage(m),
		Source: domain.ExtensionSourceRepo, CommitSHA: commit}
}

func reconsentFx(t *testing.T) (*service.ExtensionWorkspaceService, *exttest.Fixture, uuid.UUID) {
	t.Helper()
	f, err := exttest.NewFixture(nil, false)
	require.NoError(t, err)
	f.Consents.Installed = map[string]*domain.InstalledExtension{}
	svc := service.NewExtensionWorkspaceService(f.Registry, f.Integ, f.Consents).WithInstalledReleases(memInstalled{c: f.Consents})
	return svc, f, uuid.New()
}

func asAPIError(t *testing.T, err error) *apierror.Error {
	t.Helper()
	var ae *apierror.Error
	require.True(t, errors.As(err, &ae), "%v", err)
	return ae
}

func TestReconsent_RefusesAnotherVersionOrCommit(t *testing.T) {
	ctx := context.Background()
	svc, f, ws := reconsentFx(t)
	f.Consents.Installed["demo"] = demoRepoRow("0.3.0", "c3", []string{"tasks.read"}, []string{"task.updated"})
	_, err := svc.Connect(ctx, ws, "demo", nil, map[string]any{}, demoDeclared)
	require.NoError(t, err)
	row := f.Integ.Find(ws, "demo")
	before := f.Consents.ByID[row.ID]
	f.Consents.Installed["demo"] = demoRepoRow("0.4.0", "c4", []string{"tasks.read", "tasks.write"}, []string{"task.updated"})

	for _, tc := range []struct{ name, version, commit string }{
		{"older version", "0.3.0", "c3"},
		{"right version, other commit", "0.4.0", "c3"},
		{"no commit", "0.4.0", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, rerr := svc.Reconsent(ctx, ws, "demo", nil, tc.version, tc.commit)
			ae := asAPIError(t, rerr)
			assert.Equal(t, http.StatusConflict, ae.Code)
			assert.Equal(t, "version_changed", ae.ErrorCode)
			assert.Equal(t, map[string]any{"version": "0.4.0", "commit": "c4"}, ae.Context, "names what is installed")
			assert.Equal(t, before, f.Consents.ByID[row.ID], "nothing recorded")
			assert.Nil(t, f.Consents.ActorCaps[row.ID], "actor snapshot untouched")
		})
	}

	// Positive control: the installed release is accepted.
	v, err := svc.Reconsent(ctx, ws, "demo", nil, "0.4.0", "c4")
	require.NoError(t, err)
	assert.Equal(t, []string{"api:tasks.read", "api:tasks.write", "events:task.updated"}, v.Connection.ConsentedCapabilities)

	_, err = svc.Reconsent(ctx, ws, "demo", nil, "", "")
	assert.Equal(t, http.StatusBadRequest, asAPIError(t, err).Code, "a version is required")
	_, err = svc.Reconsent(ctx, uuid.New(), "demo", nil, "0.4.0", "c4")
	assert.Equal(t, http.StatusNotFound, asAPIError(t, err).Code, "not connected")
}

// An instance update that lands while the consent is being recorded: the
// transaction sees the new release and refuses, so a consent never records
// rights of a release the admin did not look at.
func TestReconsent_ConcurrentManifestUpdateIsRefused(t *testing.T) {
	ctx := context.Background()
	svc, f, ws := reconsentFx(t)
	f.Consents.Installed["demo"] = demoRepoRow("0.4.0", "c4", []string{"tasks.read", "tasks.write"}, []string{"task.updated"})
	_, err := svc.Connect(ctx, ws, "demo", nil, map[string]any{}, demoDeclared)
	require.NoError(t, err)
	f.Consents.Hook = func() {
		f.Consents.Installed["demo"] = demoRepoRow("0.5.0", "c5", []string{"tasks.read", "tasks.write", "comments:write"}, []string{"task.updated"})
	}
	_, err = svc.Reconsent(ctx, ws, "demo", nil, "0.4.0", "c4")
	ae := asAPIError(t, err)
	assert.Equal(t, "version_changed", ae.ErrorCode)
	assert.Equal(t, "0.5.0", ae.Context["version"])
	row := f.Integ.Find(ws, "demo")
	assert.Equal(t, demoDeclared, f.Consents.ByID[row.ID].Capabilities, "the connect-time consent stays")
}

// A connection with no consent record (made before consent was stored, no
// actor to backfill from) is shown as needing consent with every declared
// right pending, not as having the declared set; consenting fixes it.
func TestReconsent_ConnectionWithoutConsentRecord(t *testing.T) {
	ctx := context.Background()
	svc, f, ws := reconsentFx(t)
	created, err := f.Integ.InsertIfAbsent(ctx, &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws, Provider: "demo",
		Config: []byte(`{"base_url":"https://x"}`), IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now()})
	require.NoError(t, err)
	require.True(t, created)

	list, err := svc.List(ctx, ws)
	require.NoError(t, err)
	require.Len(t, list, 1)
	c := list[0].Connection
	require.NotNil(t, c)
	assert.Empty(t, c.ConsentedCapabilities)
	assert.Empty(t, list[0].Status.ConsentedCapabilities, "status no longer reports the declared fallback")
	require.True(t, c.NeedsConsent)
	require.NotNil(t, c.PendingConsent)
	assert.Equal(t, service.PendingConsentView{Version: "1.0.0", Commit: "", AddedCapabilities: demoDeclared, AddedEvents: []string{"task.updated"}}, *c.PendingConsent,
		"builtin: the registered manifest, no installed row, no commit")

	v, err := svc.Reconsent(ctx, ws, "demo", nil, "1.0.0", "")
	require.NoError(t, err)
	assert.False(t, v.Connection.NeedsConsent)
	assert.Nil(t, v.Connection.PendingConsent)
	assert.Equal(t, "1.0.0", v.Connection.ConsentedVersion)
	assert.JSONEq(t, `{"base_url":"https://x"}`, string(f.Integ.Find(ws, "demo").Config), "settings untouched")
}

// Rights a newer manifest dropped are not pending: they stop acting at once
// (the token is narrowed to the declared set) and the next consent removes them
// from the record.
func TestReconsent_DroppedRightsAreNotPendingAndLeaveTheRecord(t *testing.T) {
	ctx := context.Background()
	svc, f, ws := reconsentFx(t)
	f.Consents.Installed["demo"] = demoRepoRow("0.3.0", "c3", []string{"tasks.read", "docs:read"}, []string{"task.updated"})
	_, err := svc.Connect(ctx, ws, "demo", nil, map[string]any{}, []string{"api:docs:read", "api:tasks.read", "events:task.updated"})
	require.NoError(t, err)
	f.Consents.Installed["demo"] = demoRepoRow("0.4.0", "c4", []string{"tasks.read"}, []string{"task.updated"})

	v, err := svc.List(ctx, ws)
	require.NoError(t, err)
	assert.False(t, v[0].Connection.NeedsConsent, "only a drop: nothing to agree to")
	assert.Nil(t, v[0].Connection.PendingConsent)

	r, err := svc.Reconsent(ctx, ws, "demo", nil, "0.4.0", "c4")
	require.NoError(t, err)
	assert.Equal(t, []string{"api:tasks.read", "events:task.updated"}, r.Connection.ConsentedCapabilities)
}

type reconsentHook struct {
	recordingHook
	reconsented int
}

func (h *reconsentHook) Reconsented(context.Context, uuid.UUID) { h.reconsented++ }

// A re-consent runs the hook's own re-consent reaction when it has one (the
// external hook: a handshake without token delivery), else its Saved.
func TestReconsent_RunsTheReconsentHook(t *testing.T) {
	ctx := context.Background()
	for _, own := range []bool{false, true} {
		svc, f, ws := reconsentFx(t)
		plain, withOwn := &recordingHook{}, &reconsentHook{}
		if own {
			svc.WithSaveHook("demo", withOwn)
		} else {
			svc.WithSaveHook("demo", plain)
		}
		f.Consents.Installed["demo"] = demoRepoRow("0.3.0", "c3", []string{"tasks.read"}, []string{"task.updated"})
		_, err := svc.Connect(ctx, ws, "demo", nil, map[string]any{}, demoDeclared)
		require.NoError(t, err)
		f.Consents.Installed["demo"] = demoRepoRow("0.4.0", "c4", []string{"tasks.read", "tasks.write"}, []string{"task.updated"})
		_, err = svc.Reconsent(ctx, ws, "demo", nil, "0.4.0", "c4")
		require.NoError(t, err)
		if own {
			assert.Equal(t, 1, withOwn.reconsented)
			assert.Equal(t, 1, withOwn.saved, "only the connect ran Saved")
		} else {
			assert.Equal(t, 2, plain.saved, "connect and re-consent")
		}
	}
}
