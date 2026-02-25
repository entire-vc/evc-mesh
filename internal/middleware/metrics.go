package middleware

import (
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	httpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mesh_http_requests_total",
			Help: "Total number of HTTP requests",
		},
		[]string{"method", "path", "status"},
	)

	httpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "mesh_http_request_duration_seconds",
			Help:    "HTTP request duration in seconds",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		},
		[]string{"method", "path"},
	)

	httpRequestsInFlight = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "mesh_http_requests_in_flight",
			Help: "Number of HTTP requests currently being processed",
		},
	)

	wsConnectionsActive = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "mesh_ws_connections_active",
			Help: "Number of active WebSocket connections",
		},
	)

	mcpToolCallsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mesh_mcp_tool_calls_total",
			Help: "Total MCP tool calls",
		},
		[]string{"tool", "status"},
	)

	dbQueryDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "mesh_db_query_duration_seconds",
			Help:    "Database query duration",
			Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1},
		},
		[]string{"operation"},
	)

	webhookDispatchTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mesh_webhook_dispatches_total",
			Help: "Total webhook dispatch attempts",
		},
		[]string{"event_type", "success"},
	)

	rateLimitHitsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mesh_rate_limit_hits_total",
			Help: "Total rate limit hits (429 responses)",
		},
		[]string{"key_type"},
	)
)

// Metrics returns Echo middleware that records HTTP metrics.
func Metrics() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			httpRequestsInFlight.Inc()
			defer httpRequestsInFlight.Dec()

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

			httpRequestsTotal.WithLabelValues(method, path, strconv.Itoa(status)).Inc()
			httpRequestDuration.WithLabelValues(method, path).Observe(duration)

			return err
		}
	}
}

// IncrementWSConnections increments the active WebSocket connections gauge.
func IncrementWSConnections() { wsConnectionsActive.Inc() }

// DecrementWSConnections decrements the active WebSocket connections gauge.
func DecrementWSConnections() { wsConnectionsActive.Dec() }

// RecordMCPToolCall records a single MCP tool call with its outcome status.
func RecordMCPToolCall(tool, status string) {
	mcpToolCallsTotal.WithLabelValues(tool, status).Inc()
}

// RecordDBQuery records the duration of a database query for the given operation label.
func RecordDBQuery(op string, d time.Duration) {
	dbQueryDuration.WithLabelValues(op).Observe(d.Seconds())
}

// RecordWebhookDispatch records a webhook dispatch attempt and whether it succeeded.
func RecordWebhookDispatch(eventType string, success bool) {
	webhookDispatchTotal.WithLabelValues(eventType, strconv.FormatBool(success)).Inc()
}

// RecordRateLimitHit records a rate-limit rejection for the given key type (ip, user, agent).
func RecordRateLimitHit(keyType string) {
	rateLimitHitsTotal.WithLabelValues(keyType).Inc()
}
