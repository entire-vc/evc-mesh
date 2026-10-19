package external

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
)

func helloManifest(domains ...string) extensions.Manifest {
	return extensions.Manifest{
		ID: "hello", Runtime: "external",
		Capabilities: extensions.Capabilities{Network: &extensions.Network{AllowedDomains: domains}},
		External: &extensions.External{
			BaseURLSetting: "hello_url",
			UI: map[string]extensions.ExternalUI{
				SlotSidebarItem: {Path: "/mesh/ui/hello", Title: "Hello"},
				SlotTaskPanel:   {Path: "/mesh/ui/task", Title: "Hello task"},
			},
		},
	}
}

func TestFrameFor(t *testing.T) {
	m := helloManifest("hello.example.com")
	f, err := FrameFor(m, map[string]any{"hello_url": "https://hello.example.com/"}, SlotSidebarItem)
	require.NoError(t, err)
	assert.Equal(t, "https://hello.example.com/mesh/ui/hello", f.URL)
	assert.Equal(t, "https://hello.example.com", f.Origin)

	t.Run("red: host outside allowed_domains", func(t *testing.T) {
		_, err := FrameFor(m, map[string]any{"hello_url": "https://evil.example"}, SlotSidebarItem)
		assert.ErrorIs(t, err, ErrDomainNotAllowed)
	})
	t.Run("red: no allowed_domains declared", func(t *testing.T) {
		_, err := FrameFor(helloManifest(), map[string]any{"hello_url": "https://hello.example.com"}, SlotSidebarItem)
		assert.ErrorIs(t, err, ErrDomainNotAllowed)
	})
	t.Run("red: slot without a screen", func(t *testing.T) {
		_, err := FrameFor(m, map[string]any{"hello_url": "https://hello.example.com"}, "task.badge")
		assert.ErrorIs(t, err, ErrNoScreen)
	})
	t.Run("red: plain http to a public host", func(t *testing.T) {
		_, err := FrameFor(m, map[string]any{"hello_url": "http://hello.example.com"}, SlotSidebarItem)
		assert.Error(t, err)
	})
	t.Run("red: protocol-relative screen path", func(t *testing.T) {
		bad := helloManifest("hello.example.com")
		bad.External.UI[SlotSidebarItem] = extensions.ExternalUI{Path: "//evil.example/x", Title: "x"}
		_, err := FrameFor(bad, map[string]any{"hello_url": "https://hello.example.com"}, SlotSidebarItem)
		assert.ErrorIs(t, err, ErrNoScreen)
	})
}

func TestHostAllowed(t *testing.T) {
	assert.True(t, HostAllowed("a.example.com", []string{"*.example.com"}))
	assert.False(t, HostAllowed("example.com", []string{"*.example.com"}), "wildcard does not cover the apex")
	assert.False(t, HostAllowed("evilexample.com", []string{"*.example.com"}))
	assert.True(t, HostAllowed("Svc.Example.com", []string{"https://svc.example.com:8443/x"}))
	assert.False(t, HostAllowed("", []string{""}))
}
