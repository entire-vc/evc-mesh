package external

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
)

// settingsManifest declares its workspace level through a "#/schemas/<name>"
// ref, the way catalog manifests do: a boolean, the address and an x-secret.
const settingsManifest = `{"schema":1,"id":"hello","name":"Hello","version":"0.1.0","kind":"plugin","runtime":"external",
 "mesh_api":">=1","publisher":"example","category":"development",
 "levels":{"instance":false,"workspace":true,"project":false},
 "capabilities":{"api":["tasks:read"],"events":["task.updated"],"ui":[],"secrets":["api_key"]},
 "external":{"base_url_setting":"hello_url","health_path":"/healthz","install_path":"/mesh/install","events_path":"/mesh/events"},
 "settings":{"workspace":{"$ref":"#/schemas/ws"}},
 "schemas":{"ws":{"type":"object","properties":{
   "hello_url":{"type":"string"},
   "approvals_via_mesh":{"type":"boolean"},
   "api_key":{"type":"string","x-secret":true}}}}}`

const settingsCanary = "CANARY-xsecret-9c21aa"

func newSettingsRig(t *testing.T, url string) *rig {
	t.Helper()
	r := newRig(t, url)
	m, err := extensions.ParseManifest([]byte(settingsManifest))
	require.NoError(t, err)
	r.p, err = NewProvider(m, extensions.Deps{Conns: r.conns}, r.hs.Client, r.hs)
	require.NoError(t, err)
	return r
}

func wsSettings(url string, approvals bool) map[string]any {
	return map[string]any{"hello_url": url, "approvals_via_mesh": approvals, "api_key": settingsCanary, "stray": "not in the schema"}
}

func sentSettings(t *testing.T, s *svc) (settings map[string]any, body string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var in struct {
		Settings map[string]any `json:"settings"`
	}
	require.NoError(t, json.Unmarshal(s.lastBody, &in))
	return in.Settings, string(s.lastBody)
}

// install carries the workspace settings the manifest declares, minus x-secret
// ones; keys outside the schema stay home. The canary secret is neither a key
// nor a substring of the signed body.
func TestHandshake_InstallSendsDeclaredNonSecretSettings(t *testing.T) {
	s := newSvc(t, "")
	r := newSettingsRig(t, s.srv.URL)
	require.NoError(t, r.hs.Run(context.Background(), r.p, r.ws, wsSettings(s.srv.URL, true)))

	got, body := sentSettings(t, s)
	assert.Equal(t, map[string]any{"hello_url": s.srv.URL, "approvals_via_mesh": true}, got)
	assert.NotContains(t, body, settingsCanary)
	assert.NotContains(t, body, "api_key")
	assert.NotContains(t, body, "stray")
	assert.Equal(t, Sign(r.secrets.vals[SecretName("hello")], s.lastTS, s.lastBody), s.lastSig, "settings travel under the signature")
}

// A changed setting changes the fingerprint, so Ensure (Health) repeats a
// handshake whose POST after the change failed; unchanged settings stay memoised.
func TestHandshake_SettingsChangeForcesNewHandshake(t *testing.T) {
	s := newSvc(t, "")
	r := newSettingsRig(t, s.srv.URL)
	ctx := context.Background()
	require.NoError(t, r.hs.Ensure(ctx, r.p, r.ws, wsSettings(s.srv.URL, true)))
	require.NoError(t, r.hs.Ensure(ctx, r.p, r.ws, wsSettings(s.srv.URL, true)))
	require.Equal(t, 1, s.installs, "same settings: memoised")

	s.mu.Lock()
	s.installCode = 503
	s.mu.Unlock()
	require.Error(t, r.hs.Run(ctx, r.p, r.ws, wsSettings(s.srv.URL, false)), "the save-time handshake failed")
	s.mu.Lock()
	s.installCode = 0
	s.mu.Unlock()

	require.NoError(t, r.hs.Ensure(ctx, r.p, r.ws, wsSettings(s.srv.URL, false)))
	assert.Equal(t, 2, s.installs, "health delivers the changed setting")
	got, _ := sentSettings(t, s)
	assert.Equal(t, false, got["approvals_via_mesh"])

	secretOnly := wsSettings(s.srv.URL, false)
	secretOnly["api_key"] = "rotated-" + settingsCanary
	require.NoError(t, r.hs.Ensure(ctx, r.p, r.ws, secretOnly))
	assert.Equal(t, 2, s.installs, "an x-secret change is not part of what install says")
}

// The save hook after a settings change (what PATCH .../settings calls) sends
// the stored value.
func TestHook_SavedSendsChangedSetting(t *testing.T) {
	s := newSvc(t, "")
	r := newSettingsRig(t, s.srv.URL)
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(r.p))
	h := &Hook{Reg: reg, Conns: r.conns, HS: r.hs}
	ctx := context.Background()
	store := func(v bool) {
		cfg, err := json.Marshal(wsSettings(s.srv.URL, v))
		require.NoError(t, err)
		r.conns.rows[r.connID].Config = cfg
	}

	store(true)
	h.Saved(ctx, r.connID)
	got, _ := sentSettings(t, s)
	require.Equal(t, true, got["approvals_via_mesh"])

	store(false)
	h.Saved(ctx, r.connID)
	assert.Equal(t, 2, s.installs)
	got, body := sentSettings(t, s)
	assert.Equal(t, false, got["approvals_via_mesh"])
	assert.NotContains(t, body, settingsCanary)
}

// A manifest without workspace settings sends no "settings" key: the v1 body
// stays byte-for-byte what earlier Mesh versions sent.
func TestHandshake_NoDeclaredSettingsOmitsTheField(t *testing.T) {
	s := newSvc(t, "")
	r := newRig(t, s.srv.URL)
	extra := r.settings(s.srv.URL)
	extra["approvals_via_mesh"] = true
	require.NoError(t, r.hs.Run(context.Background(), r.p, r.ws, extra))
	assert.NotContains(t, string(s.lastBody), `"settings"`)
}
