package middleware

import (
	"strconv"
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
