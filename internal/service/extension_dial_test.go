//go:build !e2eloopback

package service

import (
	"net"
	"testing"
)

// The default build keeps the SSRF policy for extension services: loopback,
// private and link-local ranges are refused. The e2eloopback tag is the only
// way around it.
func TestExtensionDialAllow_DefaultRefusesLoopback(t *testing.T) {
	for name, ip := range map[string]net.IP{
		"loopback":   net.IPv4(127, 0, 0, 1),
		"loopback6":  net.IPv6loopback,
		"private":    net.IPv4(10, 0, 0, 5),
		"link-local": net.IPv4(169, 254, 169, 254),
	} {
		if extensionDialAllow(ip) {
			t.Fatalf("%s must be refused by the extension client", name)
		}
	}
	if !extensionDialAllow(net.IPv4(93, 184, 216, 34)) {
		t.Fatal("a public address must be allowed")
	}
}
