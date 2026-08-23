package middleware

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	pkgmetrics "github.com/entire-vc/evc-mesh/pkg/metrics"
)

// Metrics returns Echo middleware that records HTTP metrics.
func Metrics() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			pkgmetrics.HTTPRequestsInFlight.Inc()
			defer pkgmetrics.HTTPRequestsInFlight.Dec()

			start := time.Now()
			err := next(c)
			duration := time.Since(start).Seconds()

			status := c.Response().Status
			// Use route path pattern not actual URL to avoid high cardinality.
			path := c.Path()
			if path == "" {
				path = "unknown"
			}
			method := c.Request().Method

			pkgmetrics.HTTPRequestsTotal.WithLabelValues(method, path, strconv.Itoa(status)).Inc()
			pkgmetrics.HTTPRequestDuration.WithLabelValues(method, path).Observe(duration)

			return err
		}
	}
}

// IncrementWSConnections increments the active WebSocket connections gauge.
func IncrementWSConnections() { pkgmetrics.WSConnectionsActive.Inc() }

// DecrementWSConnections decrements the active WebSocket connections gauge.
func DecrementWSConnections() { pkgmetrics.WSConnectionsActive.Dec() }

// RecordMCPToolCall records a single MCP tool call with its outcome status.
func RecordMCPToolCall(tool, status string) { pkgmetrics.RecordMCPToolCall(tool, status) }

// RecordDBQuery records the duration of a database query for the given operation label.
func RecordDBQuery(op string, d time.Duration) { pkgmetrics.RecordDBQuery(op, d) }

// RecordWebhookDispatch records a webhook dispatch attempt and whether it succeeded.
func RecordWebhookDispatch(eventType string, success bool) {
	pkgmetrics.RecordWebhookDispatch(eventType, success)
}

// RecordRateLimitHit records a rate-limit rejection for the given key type (ip, user, agent).
func RecordRateLimitHit(keyType string) { pkgmetrics.RecordRateLimitHit(keyType) }

// MetricsAuthHTTP gates a plain net/http handler behind a bearer token. When
// token is empty it is a no-op — the endpoint stays open, matching the
// historical behavior for deployments that gate it at the network layer
// instead (e.g. the internal prod install, fronted by Caddy). When
// non-empty, every request must carry a matching "Authorization: Bearer
// <token>" header; comparison is constant-time so response timing can't be
// used to guess the token.
//
// This is the base implementation. MetricsAuth (below) is a thin Echo
// adapter over it — there is exactly one comparison to audit, not one per
// HTTP stack. Use this directly for any server that doesn't run Echo; do not
// re-derive the check there. (The example that used to stand here — cmd/mcp's
// plain net/http mux — is gone: that server was a duplicate of
// entire-vc/evc-mesh-mcp and was deleted, Mesh #e85e4e05.)
func MetricsAuthHTTP(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, prefix) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		got := auth[len(prefix):]
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// MetricsAuth returns Echo middleware gating GET /metrics behind a bearer
// token, per the same semantics as MetricsAuthHTTP (which it wraps).
func MetricsAuth(token string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			passed := false
			marker := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				passed = true
			})
			MetricsAuthHTTP(token, marker).ServeHTTP(c.Response(), c.Request())
			if !passed {
				// MetricsAuthHTTP already wrote the 401 to c.Response().
				return nil
			}
			return next(c)
		}
	}
}
