package service

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/pkg/encryption"
)

// helloSettingsManifest is helloManifest with the workspace settings an
// extension like Keep declares: a boolean switch and an x-secret field.
const helloSettingsManifest = `{"schema":1,"id":"hello","name":"Hello","version":"0.1.0","kind":"plugin","runtime":"external",
 "mesh_api":">=1","publisher":"example","category":"development",
 "levels":{"instance":false,"workspace":true,"project":false},
 "capabilities":{"api":["tasks:read"],"events":["task.updated"],"ui":[],"secrets":["api_key"]},
 "external":{"base_url_setting":"hello_url","health_path":"/healthz","install_path":"/mesh/install","events_path":"/mesh/events"},
 "settings":{"workspace":{"type":"object","properties":{
   "hello_url":{"type":"string","format":"uri"},
   "approvals_via_mesh":{"type":"boolean"},
   "api_key":{"type":"string","x-secret":true}},"required":["hello_url"]}}}`

// Acceptance: switching a workspace setting through the real
// PATCH .../settings path re-runs the install handshake, and the service gets
// the new value in "settings"; the x-secret field is never in the body, neither
// as a key nor as a value.
func TestExternalRuntime_PatchSettingsReachesServiceWithoutSecrets(t *testing.T) {
	encryption.ResetForTest()
	t.Setenv(encryption.EnvKey, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Cleanup(encryption.ResetForTest)
	const canary = "CANARY-xsecret-4b7e1d"
	ctx := context.Background()
	r := newExternalRig(t)
	hello := newHelloService(t, "")
	r.inst.put("hello", helloSettingsManifest, false)
	require.NoError(t, r.rt.Sync(ctx))
	p, ok := r.reg.Get("hello")
	require.True(t, ok)

	_, err := r.wsvc.Connect(ctx, r.ws, "hello", nil,
		map[string]any{"hello_url": hello.srv.URL, "approvals_via_mesh": true, "api_key": canary},
		extensions.DeclaredCapabilities(p.Manifest().Capabilities))
	require.NoError(t, err)
	installs, _, _ := hello.snapshot()
	require.Equal(t, 1, installs)
	settings := func() map[string]any {
		hello.mu.Lock()
		defer hello.mu.Unlock()
		got, _ := hello.installed["settings"].(map[string]any)
		return got
	}
	assert.Equal(t, true, settings()["approvals_via_mesh"], "connect delivers the workspace settings")

	_, err = r.wsvc.PatchSettings(ctx, r.ws, "hello", map[string]any{"approvals_via_mesh": false})
	require.NoError(t, err)
	installs, _, _ = hello.snapshot()
	require.Equal(t, 2, installs, "PATCH settings repeats the install handshake")
	got := settings()
	assert.Equal(t, false, got["approvals_via_mesh"], "the new value reaches the service")
	assert.Equal(t, hello.srv.URL, got["hello_url"])
	assert.NotContains(t, got, "api_key", "an x-secret field is not sent")

	hello.mu.Lock()
	body := string(hello.lastBody)
	hello.mu.Unlock()
	assert.NotContains(t, body, canary)
	assert.NotContains(t, body, "api_key")

	st := r.health(t)
	assert.True(t, st.Health.OK, st.Health.Message)
	installs, _, _ = hello.snapshot()
	assert.Equal(t, 2, installs, "health after the PATCH sees the same settings: no extra handshake")
}
