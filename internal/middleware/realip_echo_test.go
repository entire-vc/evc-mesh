package middleware

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

// realIPFor resolves c.RealIP() the way cmd/api does: ext == nil leaves
// Echo's default, otherwise it is installed as e.IPExtractor.
func realIPFor(ext echo.IPExtractor, peer, xff, xri string) string {
	e := echo.New()
	e.IPExtractor = ext
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.RemoteAddr = peer
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	if xri != "" {
		req.Header.Set("X-Real-IP", xri)
	}
	return e.NewContext(req, httptest.NewRecorder()).RealIP()
}

// Echo v4.16.3 migration: without an IPExtractor a client must not pick its
// own rate-limit key via X-Forwarded-For / X-Real-IP.
func TestEchoRealIP_NoExtractor_IgnoresClientHeaders(t *testing.T) {
	assert.Equal(t, "198.51.100.9", realIPFor(nil, "198.51.100.9:4444", "203.0.113.7", "203.0.113.8"))
}

// With trusted proxies configured (as cmd/api builds it), a relayed client IP
// is honoured only from a trusted hop; a forged XFF from an untrusted peer is not.
func TestEchoRealIP_TrustedProxy_HonoursOnlyTrustedHop(t *testing.T) {
	_, hop, _ := net.ParseCIDR("198.51.100.0/24")
	ext := echo.ExtractIPFromXFFHeader(echo.TrustIPRange(hop))
	assert.Equal(t, "203.0.113.7", realIPFor(ext, "198.51.100.9:4444", "203.0.113.7", ""))
	assert.Equal(t, "192.0.2.50", realIPFor(ext, "192.0.2.50:4444", "203.0.113.7", ""))
}
