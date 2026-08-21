// Package metrics exposes Prometheus metric variables and recording helpers
// that are shared across multiple internal packages (middleware, service,
// repository). Keeping definitions here avoids circular imports between
// packages that both declare and consume metrics.
package metrics

import (
	"log"
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

	// MemoryEmbedTruncatedTotal counts embeddings that represent only a PREFIX of
	// the text they were computed from.
	//
	// Embedding servers truncate oversized input by default and report it nowhere:
	// TEI ships auto_truncate=true, answers 200, and returns a well-formed vector
	// for the first N tokens of a document of any length. Nothing downstream can
	// distinguish that vector from a complete one, so the memory is simply absent
	// from semantic recall for any fact past the cut — while every health check
	// stays green and the row looks perfectly embedded in the database.
	//
	// Measured 2026-07-27 (#e8063a65): 96% of bench fixtures exceeded a 512-token
	// window, and the gold session for one question carried its answer at 75% of
	// the document — outside the window, so the dense arm ranked it 35/45 instead
	// of 1/45. Alert on any sustained non-zero rate: it means dense recall is
	// quietly operating on document openings rather than documents.
	MemoryEmbedTruncatedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mesh_memory_embed_truncated_total",
			Help: "Embeddings computed from a truncated prefix because the input exceeded the server's window",
		},
		[]string{"model"},
	)

	// AgentAuthTotal counts agent API-key verifications by cache outcome.
	AgentAuthTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mesh_agent_auth_total",
			Help: "Agent API-key verifications by cache result (hit|miss); a miss runs a cost-12 bcrypt comparison",
		},
		[]string{"result"},
	)
	// IntegrationEncryptionActive is 1 when integration credentials
	// (project_integrations.agent_key, Telegram bot tokens) are actually being
	// encrypted at rest, and 0 when the process is storing them in the clear
	// because MESH_INTEGRATION_ENCRYPTION_KEY is missing or malformed.
	//
	// It exists because those two situations are otherwise indistinguishable
	// from a working one at every layer a human looks at: the API returns 200,
	// writes succeed, and the only evidence is the shape of bytes in a column
	// nobody reads. Set once at startup — the key is read once per process.
	IntegrationEncryptionActive = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "mesh_integration_encryption_active",
			Help: "1 if integration credentials are encrypted at rest, 0 if stored as plaintext",
		},
	)

	// ClientIPTrusted is 1 when MESH_TRUSTED_PROXIES is configured (so
	// c.RealIP() reflects the actual client IP via a verified
	// X-Forwarded-For chain) and 0 when it is not.
	//
	// At 0, per-IP rate limiting on /auth/login has NO per-client
	// granularity — see RateLimitKeyByIP's doc comment
	// (internal/middleware/ratelimit.go) for the incident this exists to
	// surface: without a trusted proxy chain, every external client can
	// resolve to the SAME address, silently turning a per-IP limiter into
	// one shared bucket for the whole internet. Set once at startup —
	// this is a deployment-topology fact, not a per-request measurement.
	// Alert on this being 0 in any deployment that expects per-IP
	// granularity; it is expected (and loudly logged, see cmd/api/main.go)
	// to be 0 on a self-host instance that hasn't configured its reverse
	// proxy chain.
	ClientIPTrusted = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "mesh_client_ip_trusted",
			Help: "1 if MESH_TRUSTED_PROXIES is configured and c.RealIP() reflects a verified client IP, 0 if not (per-IP rate limiting has no client granularity)",
		},
	)

	// EventBusEnabled is 1 when mesh-api successfully connected to NATS/Redis
	// and is running with the event bus, 0 when eventbus.New() failed and the
	// process fell back to running without it.
	//
	// At 0, event publishing (WS broadcast, cross-agent notifications, event
	// history) is silently unavailable — the API keeps serving requests, but
	// nothing observes that a whole subsystem is missing. Set once at
	// startup — this is a deployment-topology fact, not a per-request
	// measurement. Surfaced three ways: the startup WARN log, the /health
	// field below, and this gauge on /metrics.
	EventBusEnabled = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "mesh_event_bus_enabled",
			Help: "1 if the event bus (NATS/Redis) connected successfully at startup, 0 if mesh-api is running without it",
		},
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

// RecordAgentAuth records one agent API-key verification, labelled "hit" when it
// was served from the in-process cache and "miss" when it had to run bcrypt.
//
// A miss costs ~163 ms of CPU (cost-12 bcrypt), so the miss RATE — not the
// absolute count — is the number that predicts API latency under load. A miss
// rate that stops falling after a deploy means the cache is not being reached
// (wrapper dropped from the wiring, or every request arriving with a distinct
// key).
func RecordAgentAuth(result string) {
	AgentAuthTotal.WithLabelValues(result).Inc()
}

// SetIntegrationEncryptionState publishes the encryption-at-rest state for
// integration credentials. Called once at startup with the state resolved by
// pkg/encryption; state is "ok", "invalid" or "absent", and required reports
// whether the deployment demanded encryption.
func SetIntegrationEncryptionState(state string, required bool) {
	if state == "ok" {
		IntegrationEncryptionActive.Set(1)
		return
	}
	IntegrationEncryptionActive.Set(0)
	log.Printf("metrics: integration encryption INACTIVE (state=%s required=%t) — "+
		"credentials are being stored as plaintext", state, required)
}

// SetClientIPTrusted publishes whether mesh-api trusts its reverse-proxy
// chain's X-Forwarded-For (MESH_TRUSTED_PROXIES configured). Called once at
// startup — see cmd/api/main.go.
func SetClientIPTrusted(trusted bool) {
	if trusted {
		ClientIPTrusted.Set(1)
		return
	}
	ClientIPTrusted.Set(0)
}

// SetEventBusEnabled publishes whether mesh-api is running with a working
// event bus (eventbus.New() succeeded) or fell back to running without one.
// Called once at startup — see cmd/api/main.go.
func SetEventBusEnabled(enabled bool) {
	if enabled {
		EventBusEnabled.Set(1)
		return
	}
	EventBusEnabled.Set(0)
}
