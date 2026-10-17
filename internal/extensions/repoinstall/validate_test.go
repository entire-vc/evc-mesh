package repoinstall

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
)

func TestValidateManifestFailures(t *testing.T) {
	_, err := ValidateManifest([]byte(`not json`))
	assert.Equal(t, CodeManifestInvalid, errCode(err))

	_, err = ValidateManifest([]byte(manifestJSON(t, func(m map[string]any) { m["runtime"] = "builtin" })))
	assert.Equal(t, CodeBuiltinRuntime, errCode(err))

	_, err = ValidateManifest([]byte(manifestJSON(t, func(m map[string]any) { m["surprise"] = true })))
	assert.Equal(t, CodeManifestInvalid, errCode(err), "schema rejects unknown fields")

	_, err = ValidateManifest([]byte(manifestJSON(t, func(m map[string]any) { m["mesh_api"] = ">=99" })))
	assert.Equal(t, CodeIncompatible, errCode(err))

	m, err := ValidateManifest([]byte(manifestJSON(t, nil)))
	require.NoError(t, err)
	assert.NotEmpty(t, m.ID)
}

func TestMeshAPICompatibleOperators(t *testing.T) {
	cases := []struct {
		expr string
		ver  int
		want bool
	}{
		{"", 1, false}, {"x", 1, false}, {">=x", 1, false},
		{">=1", 1, true}, {">=2", 1, false},
		{"<=1", 1, true}, {"<=0", 1, false},
		{">0", 1, true}, {">1", 1, false},
		{"<2", 1, true}, {"<1", 1, false},
		{"=1", 1, true}, {"1", 1, true}, {"2", 1, false},
		{">=1 <3", 2, true}, {">=1 <2", 2, false},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, MeshAPICompatible(c.expr, c.ver), "%q vs %d", c.expr, c.ver)
	}
}

func TestCapabilitiesAndDelta(t *testing.T) {
	var m extensions.Manifest
	m.Capabilities.API = []string{"tasks:read"}
	m.Capabilities.Events = []string{"task.updated"}
	m.Capabilities.WebhooksIn = []string{"hook"}
	m.Capabilities.UI = []string{"task.panel"}
	m.Capabilities.Secrets = []string{"token"}
	got := Capabilities(m)
	assert.Equal(t, []string{"api:tasks:read", "events:task.updated", "secrets:token", "ui:task.panel", "webhooks_in:hook"}, got)

	assert.Equal(t, []string{"b"}, NewCapabilities([]string{"a"}, []string{"a", "b"}))
	assert.Empty(t, NewCapabilities([]string{"a", "b"}, []string{"a"}))
	assert.NotNil(t, NewCapabilities(nil, nil), "empty delta serialises as [] not null")
}

func TestSemverOrdering(t *testing.T) {
	for _, bad := range []string{"", "v1", "v1.2", "v1.2.x", "v1.-2.3", "latest"} {
		_, ok := parseSemver(bad)
		assert.False(t, ok, bad)
	}
	rel, ok := parseSemver("v1.2.3")
	require.True(t, ok)
	pre, _ := parseSemver("1.2.3-beta")
	pre2, _ := parseSemver("1.2.3-rc")
	assert.True(t, pre.less(rel), "pre-release sorts below the release")
	assert.False(t, rel.less(pre))
	assert.False(t, rel.less(rel))
	assert.True(t, pre.less(pre2))
	patch, _ := parseSemver("1.2.4")
	minor, _ := parseSemver("1.3.0")
	major, _ := parseSemver("2.0.0")
	assert.True(t, rel.less(patch) && patch.less(minor) && minor.less(major))
	assert.False(t, major.less(minor) || minor.less(patch))

	best, ok := NewestTag([]Ref{{Name: "nightly"}, {Name: "v1.0.0"}, {Name: "v1.10.0"}, {Name: "v1.9.0"}})
	require.True(t, ok)
	assert.Equal(t, "v1.10.0", best.Name)
	_, ok = NewestTag([]Ref{{Name: "nightly"}})
	assert.False(t, ok)

	assert.True(t, IsNewerTag("v1.1.0", "v1.0.0"))
	assert.False(t, IsNewerTag("v1.0.0", "v1.1.0"))
	assert.False(t, IsNewerTag("edge", "v1.0.0"))
}
