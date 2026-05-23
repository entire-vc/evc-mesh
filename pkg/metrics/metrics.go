// Package metrics exposes Prometheus metric variables and recording helpers
// that are shared across multiple internal packages (middleware, service,
// repository). Keeping definitions here avoids circular imports between
// packages that both declare and consume metrics.
package metrics

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	HTTPRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mesh_http_requests_total",
			Help: "Total number of HTTP requests",
		},
		[]string{"method", "path", "status"},
	)

	HTTPRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "mesh_http_request_duration_seconds",
			Help:    "HTTP request duration in seconds",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		},
		[]string{"method", "path"},
	)

	HTTPRequestsInFlight = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "mesh_http_requests_in_flight",
			Help: "Number of HTTP requests currently being processed",
		},
	)

	WSConnectionsActive = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "mesh_ws_connections_active",
			Help: "Number of active WebSocket connections",
		},
	)

	MCPToolCallsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mesh_mcp_tool_calls_total",
			Help: "Total MCP tool calls",
		},
		[]string{"tool", "status"},
	)

	DBQueryDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "mesh_db_query_duration_seconds",
			Help:    "Database query duration",
			Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1},
		},
		[]string{"operation"},
	)

	WebhookDispatchTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mesh_webhook_dispatches_total",
			Help: "Total webhook dispatch attempts",
		},
		[]string{"event_type", "success"},
	)

	RateLimitHitsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mesh_rate_limit_hits_total",
			Help: "Total rate limit hits (429 responses)",
		},
		[]string{"key_type"},
	)
)

// RecordMCPToolCall records a single MCP tool call with its outcome status.
func RecordMCPToolCall(tool, status string) {
	MCPToolCallsTotal.WithLabelValues(tool, status).Inc()
}

// RecordDBQuery records the duration of a database query for the given operation label.
func RecordDBQuery(op string, d time.Duration) {
	DBQueryDuration.WithLabelValues(op).Observe(d.Seconds())
}

// RecordWebhookDispatch records a webhook dispatch attempt and whether it succeeded.
func RecordWebhookDispatch(eventType string, success bool) {
	WebhookDispatchTotal.WithLabelValues(eventType, strconv.FormatBool(success)).Inc()
}

// RecordRateLimitHit records a rate-limit rejection for the given key type (ip, user, agent).
func RecordRateLimitHit(keyType string) {
	RateLimitHitsTotal.WithLabelValues(keyType).Inc()
}
