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

	// MemoryEmbedFailuresTotal counts embedder failures per memory operation
	// ("recall" = query embedding for the dense arm, "store" = memory embedding).
	//
	// The recall path FAILS OPEN on an embedder error: it degrades to BM25-only
	// and still returns 200. Until this counter existed the only trace of a dead
	// embedder was a log line, so an out-of-credit provider served degraded
	// recall for 32h and paged nobody. Alert on rate(...{op="recall"}[15m]) > 0.
	MemoryEmbedFailuresTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mesh_memory_embed_failures_total",
			Help: "Total embedding failures by memory operation (recall|store)",
		},
		[]string{"op"},
	)

	// MemoryRecallTotal counts recall calls by the mode they were actually
	// SERVED in. search_mode="bm25-only" means the dense arm did not run.
	// The ratio bm25-only/total is the degradation signal.
	MemoryRecallTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mesh_memory_recall_total",
			Help: "Total memory recall calls by the search mode actually served (hybrid|bm25-only)",
		},
		[]string{"search_mode"},
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

// RecordMemoryEmbedFailure records an embedder failure for the given memory
// operation ("recall" or "store"). On the recall path this is the fail-open
// event: the call still succeeds, but degraded to BM25-only.
func RecordMemoryEmbedFailure(op string) {
	MemoryEmbedFailuresTotal.WithLabelValues(op).Inc()
}

// RecordMemoryRecall records a completed recall labelled with the search mode it
// was actually served in ("hybrid" or "bm25-only").
func RecordMemoryRecall(searchMode string) {
	MemoryRecallTotal.WithLabelValues(searchMode).Inc()
}
