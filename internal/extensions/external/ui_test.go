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

func TestFrameForSettingsDomain(t *testing.T) {
	keep := helloManifest("{settings.hello_url}")
	f, err := FrameFor(keep, map[string]any{"hello_url": "https://keep.example.com"}, SlotSidebarItem)
	require.NoError(t, err)
	assert.Equal(t, "https://keep.example.com/mesh/ui/hello", f.URL)
	assert.Equal(t, "https://keep.example.com", f.Origin)

	t.Run("red: placeholder names another setting than the service address", func(t *testing.T) {
		m := helloManifest("{settings.keep_url}")
		_, err := FrameFor(m, map[string]any{"hello_url": "https://evil.example", "keep_url": "https://keep.example.com"}, SlotSidebarItem)
		assert.ErrorIs(t, err, ErrDomainNotAllowed)
	})
	t.Run("red: placeholder setting missing", func(t *testing.T) {
		m := helloManifest("{settings.keep_url}")
		_, err := FrameFor(m, map[string]any{"hello_url": "https://keep.example.com"}, SlotSidebarItem)
		assert.ErrorIs(t, err, ErrDomainNotAllowed)
	})
	t.Run("red: no service address", func(t *testing.T) {
		_, err := FrameFor(keep, map[string]any{}, SlotSidebarItem)
		assert.Error(t, err)
	})
}

func TestExpandAllowedDomains(t *testing.T) {
	settings := map[string]any{"url": "https://keep.example.com/", "wild": "*.example.com", "num": 3, "empty": " "}
	assert.Equal(t, []string{"a.example.com", "https://keep.example.com/"},
		ExpandAllowedDomains([]string{"a.example.com", "{settings.url}", "{settings.wild}", "{settings.num}", "{settings.empty}", "{settings.none}"}, settings))
	assert.True(t, HostAllowed("keep.example.com", ExpandAllowedDomains([]string{"{settings.url}"}, settings)))
	assert.False(t, HostAllowed("x.example.com", ExpandAllowedDomains([]string{"{settings.wild}"}, settings)))
}

func TestHostAllowed(t *testing.T) {
	assert.True(t, HostAllowed("a.example.com", []string{"*.example.com"}))
	assert.False(t, HostAllowed("example.com", []string{"*.example.com"}), "wildcard does not cover the apex")
	assert.False(t, HostAllowed("evilexample.com", []string{"*.example.com"}))
	assert.True(t, HostAllowed("Svc.Example.com", []string{"https://svc.example.com:8443/x"}))
	assert.False(t, HostAllowed("", []string{""}))
}
