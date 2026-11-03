package service_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/external"
	"github.com/entire-vc/evc-mesh/internal/extensions/exttest"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

const externalManifest = `{"schema":1,"id":"keepish","name":"Keepish","version":"0.1.0","kind":"plugin","runtime":"external",
 "mesh_api":">=1","publisher":"example","category":"development",
 "levels":{"instance":true,"workspace":true,"project":false},
 "capabilities":{"api":["tasks:read"],"events":["task.updated"],"ui":[]},
 "external":{"base_url_setting":"keep_url","health_path":"/healthz","install_path":"/mesh/install","events_path":"/mesh/events"}}`

type instanceStub struct{ set *extensions.InstanceSetting }

func (i instanceStub) InstanceSetting(context.Context, string) (*extensions.InstanceSetting, error) {
	return i.set, nil
}

// externalFx wires the real service.Connect to a real external Provider over
// the in-memory stores; only the instance store is a stub.
func externalFx(t *testing.T, inst *extensions.InstanceSetting) (*service.ExtensionWorkspaceService, *exttest.Fixture, uuid.UUID) {
	t.Helper()
	f, err := exttest.NewFixture(nil, false)
	require.NoError(t, err)
	man, err := extensions.ParseManifest([]byte(externalManifest))
	require.NoError(t, err)
	p, err := external.NewProvider(man, extensions.Deps{
		Conns: extensions.IntegrationConnections{Repo: f.Integ}, Inst: instanceStub{set: inst}, Allow: f.Allow,
	}, &http.Client{}, nil)
	require.NoError(t, err)
	require.NoError(t, f.Registry.Register(p))
	return service.NewExtensionWorkspaceService(f.Registry, f.Integ, f.Consents), f, uuid.New()
}

var externalConsent = []string{"api:tasks:read", "events:task.updated"}

func TestExtensionWorkspace_ExternalConnectInheritsAndValidates(t *testing.T) {
	ctx := context.Background()
	const canary = "CANARY-instance-only"
	on := &extensions.InstanceSetting{Enabled: true, Settings: map[string]any{"keep_url": "https://keep.example.test", "admin_token": canary}}
	by := uuid.New()

	t.Run("valid connect inherits the address and stores no instance-only value", func(t *testing.T) {
		svc, f, ws := externalFx(t, on)
		v, err := svc.Connect(ctx, ws, "keepish", &by, nil, externalConsent)
		require.NoError(t, err)
		require.NotNil(t, v.Connection)
		row := f.Integ.Find(ws, "keepish")
		require.NotNil(t, row)
		require.True(t, row.IsActive)
		var cfg map[string]any
		require.NoError(t, json.Unmarshal(row.Config, &cfg))
		require.Equal(t, "https://keep.example.test", cfg["keep_url"])
		require.NotContains(t, string(row.Config), canary)
		require.Equal(t, string(extensions.SourceWorkspace), v.Status.Source)
	})

	t.Run("a valid submitted address wins over the instance one", func(t *testing.T) {
		svc, f, ws := externalFx(t, on)
		_, err := svc.Connect(ctx, ws, "keepish", &by, map[string]any{"keep_url": "https://own.example.test"}, externalConsent)
		require.NoError(t, err)
		var cfg map[string]any
		require.NoError(t, json.Unmarshal(f.Integ.Find(ws, "keepish").Config, &cfg))
		require.Equal(t, "https://own.example.test", cfg["keep_url"])
	})

	t.Run("refused connects create no connection", func(t *testing.T) {
		bad := &extensions.InstanceSetting{Enabled: true, Settings: map[string]any{"keep_url": "http://evil.example.test"}}
		for name, tc := range map[string]struct {
			inst     *extensions.InstanceSetting
			settings map[string]any
			consent  []string
			code     int
			errCode  string
			field    string
		}{
			"non-string submitted address":  {on, map[string]any{"keep_url": 5}, externalConsent, http.StatusBadRequest, "validation_failed", "settings"},
			"invalid submitted address":     {on, map[string]any{"keep_url": "ftp://x.example.test"}, externalConsent, http.StatusBadRequest, "validation_failed", "settings"},
			"invalid inherited address":     {bad, nil, externalConsent, http.StatusBadRequest, "validation_failed", "settings"},
			"instance disabled, no address": {&extensions.InstanceSetting{Enabled: false, Settings: on.Settings}, nil, externalConsent, http.StatusBadRequest, "validation_failed", "settings"},
			"no instance row, no address":   {nil, nil, externalConsent, http.StatusBadRequest, "validation_failed", "settings"},
			"missing consent":               {on, nil, []string{"events:task.updated"}, http.StatusBadRequest, "validation_failed", "consent"},
		} {
			svc, f, ws := externalFx(t, tc.inst)
			_, err := svc.Connect(ctx, ws, "keepish", &by, tc.settings, tc.consent)
			requireAPIErr(t, err, tc.code, tc.errCode)
			var ae *apierror.Error
			require.ErrorAs(t, err, &ae)
			require.Contains(t, ae.Validation, tc.field, name+": refused for the expected reason")
			require.Nil(t, f.Integ.Find(ws, "keepish"), name+": no row written")
		}
	})
}
