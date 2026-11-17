package external

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
)

// An installed external extension whose mesh_api this Mesh does not satisfy is
// never registered, at start or on a later Sync: no slots, no events. The
// installed row stays, and the reason is reported by InactiveReason and logged
// once per change, not on every tick of Run.
func TestRuntime_SyncSkipsIncompatibleMeshAPI(t *testing.T) {
	ctx := context.Background()
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })

	reg := extensions.NewRegistry()
	future := strings.Replace(strings.Replace(manifestJSON, `"id":"hello"`, `"id":"future"`, 1),
		`"mesh_api":">=1"`, `"mesh_api":">=2"`, 1)
	inst := &fakeInstalled{rows: []domain.InstalledExtension{
		{ExtensionID: "hello", Manifest: []byte(manifestJSON), Source: domain.ExtensionSourceCatalog},
		{ExtensionID: "future", Manifest: []byte(future), Source: domain.ExtensionSourceRepo},
	}}
	rt := &Runtime{Reg: reg, Installed: inst, Client: http.DefaultClient}

	require.NoError(t, rt.Sync(ctx))
	_, ok := reg.Get("future")
	assert.False(t, ok, "an extension needing mesh_api >=2 is not active on API version %d", extensions.ExtensionsAPIVersion)
	assert.Equal(t, ReasonMeshAPIIncompatible, rt.InactiveReason("future"))
	assert.Contains(t, logs.String(), "future")
	assert.Contains(t, logs.String(), ReasonMeshAPIIncompatible)
	assert.Len(t, inst.rows, 2, "the installed row is kept")

	// Positive control: the compatible extension is active, reports no reason
	// and keeps declaring its events, which is what delivery is gated on.
	p, ok := reg.Get("hello")
	require.True(t, ok, "a compatible extension is active")
	assert.Empty(t, rt.InactiveReason("hello"))
	assert.Contains(t, extensions.DeclaredCapabilities(p.Manifest().Capabilities), "events:task.updated")

	logs.Reset()
	require.NoError(t, rt.Sync(ctx))
	assert.NotContains(t, logs.String(), "future", "an unchanged reason is not logged again")

	// A registered extension whose new manifest is incompatible is taken out.
	inst.rows[0].Manifest = []byte(strings.Replace(manifestJSON, `"mesh_api":">=1"`, `"mesh_api":"<1"`, 1))
	require.NoError(t, rt.Sync(ctx))
	_, ok = reg.Get("hello")
	assert.False(t, ok, "a now-incompatible extension is unregistered")
	assert.Equal(t, ReasonMeshAPIIncompatible, rt.InactiveReason("hello"))

	// Compatible again: active again, the reason is gone.
	inst.rows[0].Manifest = []byte(manifestJSON)
	require.NoError(t, rt.Sync(ctx))
	_, ok = reg.Get("hello")
	assert.True(t, ok)
	assert.Empty(t, rt.InactiveReason("hello"))

	// A removed row leaves no stale reason.
	inst.rows = inst.rows[:1]
	require.NoError(t, rt.Sync(ctx))
	assert.Empty(t, rt.InactiveReason("future"))
}
