package external

import (
	"errors"
	"net/url"
	"strings"

	"github.com/entire-vc/evc-mesh/internal/extensions"
)

// Iframe slots an external extension may fill with a screen of its own.
const (
	SlotSidebarItem = "sidebar.item"
	SlotTaskPanel   = "task.panel"
)

// IsFrameSlot reports whether slot can hold an iframe screen.
func IsFrameSlot(slot string) bool { return slot == SlotSidebarItem || slot == SlotTaskPanel }

// ErrNoScreen means the manifest declares no screen for the slot.
var ErrNoScreen = errors.New("the extension has no screen for this slot")

// ErrDomainNotAllowed means the service host is not in capabilities.network.allowed_domains.
var ErrDomainNotAllowed = errors.New("the service host is not in the extension's allowed_domains")

// Frame is where an iframe screen lives.
type Frame struct {
	// URL is the screen address without any token.
	URL string
	// Origin is scheme://host[:port] of the service: the only origin the host page
	// listens to and the iframe is allowed to come from.
	Origin string
	Title  string
}

// FrameFor resolves the screen of manifest m for slot against the workspace's
// settings. The service address is validated like for the install handshake and
// its host must be listed in allowed_domains: a tenant-chosen address can never
// put a foreign page into the Mesh UI.
func FrameFor(m extensions.Manifest, settings map[string]any, slot string) (Frame, error) {
	if m.Runtime != "external" || m.External == nil || !IsFrameSlot(slot) {
		return Frame{}, ErrNoScreen
	}
	screen, ok := m.External.UI[slot]
	if !ok || !strings.HasPrefix(screen.Path, "/") || strings.HasPrefix(screen.Path, "//") {
		return Frame{}, ErrNoScreen
	}
	raw, _ := settings[m.External.BaseURLSetting].(string)
	base, err := cleanBaseURL(raw)
	if err != nil {
		return Frame{}, err
	}
	u, err := url.Parse(base)
	if err != nil {
		return Frame{}, errAddress
	}
	var allowed []string
	if m.Capabilities.Network != nil {
		allowed = m.Capabilities.Network.AllowedDomains
	}
	if !HostAllowed(u.Hostname(), allowed) {
		return Frame{}, ErrDomainNotAllowed
	}
	return Frame{URL: base + screen.Path, Origin: u.Scheme + "://" + u.Host, Title: screen.Title}, nil
}

// HostAllowed matches a host against allowed_domains entries: an exact host, or
// `*.example.com` for any subdomain (not the apex). Entries are compared
// case-insensitively and may carry a port, which is ignored.
func HostAllowed(host string, allowed []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return false
	}
	for _, a := range allowed {
		a = strings.ToLower(strings.TrimSpace(a))
		if i := strings.Index(a, "://"); i >= 0 {
			a = a[i+3:]
		}
		if i := strings.IndexAny(a, "/:"); i >= 0 {
			a = a[:i]
		}
		switch {
		case a == "":
		case strings.HasPrefix(a, "*."):
			if suffix := a[1:]; len(host) > len(suffix) && strings.HasSuffix(host, suffix) {
				return true
			}
		case a == host:
			return true
		}
	}
	return false
}
