package middleware

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// forwardingHeaders are the headers any reverse proxy in front of this API
// adds on the way through. Caddy's reverse_proxy sets X-Forwarded-For,
// -Proto and -Host unconditionally; the rest cover other proxies and a
// hand-written config that renames them.
var forwardingHeaders = []string{
	"X-Forwarded-For",
	"X-Forwarded-Host",
	"X-Forwarded-Proto",
	"Forwarded",
	"X-Real-Ip",
}

// DirectOnly refuses a request that arrived through a reverse proxy, as if
// the route did not exist (404, before any auth runs — so the edge is not a
// spawn-token oracle either).
//
// It exists for /internal/* (task #1c9f527d). That route shares the API's
// listener with /api/*, so until now its closure rested on ONE thing: the
// edge Caddyfile having no block that proxies it. A catch-all
// `reverse_proxy 127.0.0.1:8005` added there for any unrelated reason would
// have published the one endpoint that returns plaintext secrets, with
// nothing in this repo noticing. With this guard that edit publishes a 404.
//
// Legitimate callers never carry these headers: the two spawners and the
// health check reach 127.0.0.1:8005 over ssh with plain curl (see
// bob/scripts/spawn_secrets.py, `ssh+http://`). A proxy configured to STRIP
// the forwarding headers defeats this — that has to be written on purpose,
// which is the difference from the failure this guards against.
func DirectOnly() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			h := c.Request().Header
			for _, name := range forwardingHeaders {
				if _, present := h[http.CanonicalHeaderKey(name)]; present {
					return c.JSON(http.StatusNotFound, map[string]string{"message": "Not Found"})
				}
			}
			return next(c)
		}
	}
}
