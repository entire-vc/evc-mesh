//go:build e2eloopback

package service

import "net"

// The e2e-connect job runs a disposable API next to a synthetic extension
// service on loopback; the SSRF guard would refuse that address. Only loopback
// is let through, and only in a binary built with -tags e2eloopback.
func init() {
	extensionDialAllow = func(ip net.IP) bool { return ip.IsLoopback() || isPubliclyRoutable(ip) }
}
