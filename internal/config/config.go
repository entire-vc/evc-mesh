package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all application configuration, loaded from environment variables.
type Config struct {
	Server    ServerConfig
	Database  DatabaseConfig
	Redis     RedisConfig
	NATS      NATSConfig
	S3        S3Config
	Auth      AuthConfig
	CORS      CORSConfig
	RateLimit RateLimitConfig
	Spark     SparkConfig
	Webhook   WebhookConfig
	Embedding EmbeddingConfig
	VAPID     VAPIDConfig
	Email     EmailConfig
}

// EmailConfig holds SMTP settings for outbound email (workspace invites, notifications).
// When Host is empty, email sending is disabled and invite links are logged to stdout instead.
type EmailConfig struct {
	Host    string
	Port    int
	User    string
	Pass    string
	From    string
	BaseURL string // e.g. https://mesh.example.com — used to build invite accept links
}

// EmbeddingConfig holds configuration for the optional text embedding provider.
// When Provider is "none" (the default), all vector search operations are skipped
// and recall falls back to keyword-only search.
type EmbeddingConfig struct {
	// Provider selects the embedding backend: "ollama", "openai", or "none" (default).
	Provider string
	// Model is the embedding model identifier (e.g. "nomic-embed-text", "text-embedding-3-small").
	Model string
	// Endpoint is the base URL for the embedding API.
	// Defaults to "http://localhost:11434" for Ollama, "https://api.openai.com" for OpenAI.
	Endpoint string
	// APIKey is the authentication token for providers that require it (e.g. OpenAI).
	APIKey string
	// Dimensions is the expected output vector length (e.g. 768, 1536).
	Dimensions int
	// BatchSize controls how many texts are embedded in a single batch call (default: 32).
	BatchSize int
	// MaxInputTokens is the server's input window, used to detect silent truncation.
	// Embedding servers truncate oversized input by default and report it nowhere in
	// the response, so a response whose prompt_tokens lands exactly on this value is
	// the only client-visible sign that content was dropped. 0 disables the check.
	MaxInputTokens int
	// Concurrency bounds how many embed calls (write + recall + backfill combined —
	// see memoryService.acquireEmbedSem) may run against the embedder at once.
	// Default: 4. A CPU-bound self-hosted embedder (TEI, e5-small on 8 cores) gets
	// SLOWER past its core count — an unbounded stampede thrashes it into timeouts
	// rather than serving more throughput (measured live 2026-08-19, #9fe5bb71:
	// throughput +60% after capping in-flight requests from ~40 to 4). 0 restores
	// the old unbounded behavior; only use that against a horizontally-scaled or
	// hosted embedding API that can actually absorb unbounded concurrency.
	Concurrency int
	// HTTPTimeoutSecs is the timeout, in seconds, for the embedder's HTTP client (default: 30).
	HTTPTimeoutSecs int
}

// SparkConfig holds configuration for the Spark agent catalog integration.
type SparkConfig struct {
	// URL is the base URL of the Spark catalog API.
	URL string
	// Enabled controls whether Spark catalog routes are registered.
	Enabled bool
}

// CORSConfig holds cross-origin resource sharing settings.
type CORSConfig struct {
	// AllowOrigins is a comma-separated list of allowed origins.
	// Use "*" to allow all origins (development default).
	AllowOrigins []string
}

// RateLimitConfig holds rate limiting settings.
type RateLimitConfig struct {
	// Enabled controls whether rate limiting is active.
	Enabled bool
	// AuthRPM is the maximum requests per minute for login/register (per IP).
	// Kept tight (default 5) as brute-force protection for credential endpoints.
	AuthRPM int
	// RefreshRPM is the maximum requests per minute for /auth/refresh (per IP).
	// Higher than AuthRPM: a valid refresh token is required, so credential
	// brute-force is not a concern. Must accommodate a fleet of agents on a
	// shared egress IP all refreshing around the same time.
	RefreshRPM int
	// APIRPM is the maximum requests per minute for API endpoints (per actor).
	APIRPM int
	// TrustedProxies is a list of CIDR ranges, in addition to the
	// loopback/link-local/private-net ranges Echo trusts by default, whose
	// X-Forwarded-For entries are trusted when resolving the client's real
	// IP (echo.ExtractIPFromXFFHeader). Empty (the default) means mesh-api
	// does not trust ANY additional hop's X-Forwarded-For — c.RealIP() (and
	// therefore RateLimitKeyByIP) falls back to Echo's untouched default
	// behavior, and the per-IP limiter on /auth/login is disabled outright
	// rather than pretending to have per-client granularity it cannot
	// verify. See RateLimitKeyByIP's doc comment (internal/middleware/
	// ratelimit.go) for the incident this exists to prevent.
	TrustedProxies []string
	// AuthMaxFailures is the number of failed /auth/login attempts a single
	// account identifier (normalized email, from the request body — not an
	// IP) may accrue within AuthLockoutWindow before further WRONG-password
	// attempts against that identifier are rejected with 429. A request
	// carrying the CORRECT password is never blocked by this counter,
	// regardless of how many prior failures were recorded — see
	// internal/handler/auth_handler.go Login. This is the primary
	// brute-force defense for login: unlike per-IP limiting, it works
	// identically no matter what the reverse-proxy topology does to
	// X-Forwarded-For. <= 0 disables the lockout.
	AuthMaxFailures int
	// AuthLockoutWindow is the sliding window AuthMaxFailures is counted over.
	AuthLockoutWindow time.Duration
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Host         string
	Port         int
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	// MetricsToken, when set, gates GET /metrics behind a matching
	// `Authorization: Bearer <token>` header. Empty (the default) leaves the
	// endpoint open — that's the existing behavior for deployments that
	// front it with their own network control (e.g. Caddy on the internal
	// prod install).
	//
	// Sourced from MESH_METRICS_TOKEN, or from the file named by
	// MESH_METRICS_TOKEN_FILE when that variable is empty. The self-host
	// docker-compose.prod.yml uses the file form so it can generate a token
	// the operator never has to set, and hand the same file to prometheus.
	MetricsToken string
}

// DatabaseConfig holds PostgreSQL connection settings.
type DatabaseConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Name     string
	SSLMode  string
}

// DSN returns a PostgreSQL connection string.
func (c DatabaseConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		c.Host, c.Port, c.User, c.Password, c.Name, c.SSLMode,
	)
}

// RedisConfig holds Redis connection settings.
type RedisConfig struct {
	Host     string
	Port     int
	Password string
	DB       int
}

// Addr returns the Redis address in host:port format.
func (c RedisConfig) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

// NATSConfig holds NATS connection settings, including the JetStream
// MESH_EVENTS stream's storage limits. StreamMaxBytes/StreamMaxAge/
// MaxMsgSize/Replicas mirror eventbus.DefaultConfig()'s hardcoded values as
// their defaults below — env vars override per-deployment (e.g. a smaller
// disk needs a smaller NATS_STREAM_MAX_BYTES than the 10 GB default).
type NATSConfig struct {
	URL string
	// MonitorURL is the NATS monitoring HTTP endpoint (started via
	// nats-server's --http_port), used only to discover the server's real
	// storage limit when the configured StreamMaxBytes is rejected as
	// insufficient — see internal/eventbus/nats.go ensureStream(). Distinct
	// from URL (the client protocol port) because nats-server exposes them
	// on two different ports/addresses, and — inside the prod compose
	// network — under different defaults than local dev.
	MonitorURL     string
	StreamMaxBytes int64         // NATS_STREAM_MAX_BYTES, default 10 GB
	StreamMaxAge   time.Duration // NATS_STREAM_MAX_AGE, default 30 days
	MaxMsgSize     int32         // NATS_MAX_MSG_SIZE, default 256 KB
	Replicas       int           // NATS_REPLICAS, default 1
}

// S3Config holds S3-compatible storage settings (MinIO or AWS S3).
type S3Config struct {
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	Bucket          string
	Region          string
	UseSSL          bool
	PublicURL       string // Optional: public base URL for presigned URLs (e.g. https://mesh.example.com/s3)
}

// AuthConfig holds authentication settings.
type AuthConfig struct {
	JWTSecret       string
	CasdoorEndpoint string
	CasdoorClientID string
	AgentKeyPrefix  string
	// AgentKeyPepper is an OPTIONAL server-side secret mixed into the fast
	// API-key digest (MESH_AGENT_KEY_PEPPER). Leaving it unset is the supported
	// default and is safe: agent keys are 192 bits of crypto/rand, so the digest
	// has nothing to stretch. Setting it means a database dump alone cannot be
	// used to forge a digest. Rotating it is self-healing — every key falls back
	// to its bcrypt hash once and the new digest is written on the way through.
	AgentKeyPepper string
	// AllowRegistration gates POST /auth/register once the instance already has
	// at least one user. Defaults to true so existing installs keep working
	// unchanged. The first user on a fresh install can always register
	// regardless of this setting (see auth.Service.RegistrationOpen).
	AllowRegistration bool
}

// WebhookConfig holds inbound webhook validation settings, plus the token
// used for the one OUTBOUND GitHub call the done-evidence gate makes (live
// PR-status verification, #5f7f8c6e) — kept alongside the webhook secret
// since both are "how Mesh talks to GitHub about PRs" settings.
type WebhookConfig struct {
	// GitHubSecret is the HMAC-SHA256 secret for validating GitHub webhook payloads.
	// If empty, signature validation is skipped (backward-compatible).
	GitHubSecret string
	// GitHubToken authenticates the done-evidence gate's live PR-status
	// check against the GitHub REST API. If empty, the live check is
	// disabled entirely (the gate falls back to the cached vcs_links.status,
	// exactly as it did before this existed) — anonymous GitHub reads are
	// rate-limited to 60/hour and cannot see private repos, so an empty
	// token isn't a usable "read-only anonymous" mode for a private org.
	GitHubToken string
}

// VAPIDConfig holds Web Push VAPID key material.
// When PublicKey or PrivateKey is empty, browser push is silently disabled.
type VAPIDConfig struct {
	PublicKey  string
	PrivateKey string
	Subject    string // mailto: contact address sent in VAPID JWT
}

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	return &Config{
		Server: ServerConfig{
			Host:         getEnv("SERVER_HOST", "0.0.0.0"),
			Port:         getEnvInt("SERVER_PORT", 8005),
			ReadTimeout:  getEnvDuration("SERVER_READ_TIMEOUT", 30*time.Second),
			WriteTimeout: getEnvDuration("SERVER_WRITE_TIMEOUT", 30*time.Second),
			MetricsToken: getEnvOrFile("MESH_METRICS_TOKEN", "MESH_METRICS_TOKEN_FILE"),
		},
		Database: DatabaseConfig{
			Host:     getEnv("DB_HOST", "localhost"),
			Port:     getEnvInt("DB_PORT", 5437),
			User:     getEnv("DB_USER", "mesh"),
			Password: getEnv("DB_PASSWORD", "mesh"),
			Name:     getEnv("DB_NAME", "mesh"),
			SSLMode:  getEnv("DB_SSL_MODE", "disable"),
		},
		Redis: RedisConfig{
			Host:     getEnv("REDIS_HOST", "localhost"),
			Port:     getEnvInt("REDIS_PORT", 6383),
			Password: getEnv("REDIS_PASSWORD", ""),
			DB:       getEnvInt("REDIS_DB", 0),
		},
		NATS: NATSConfig{
			URL:            getEnv("NATS_URL", "nats://localhost:4223"),
			MonitorURL:     getEnv("NATS_MONITOR_URL", "http://localhost:8223"),
			StreamMaxBytes: getEnvInt64("NATS_STREAM_MAX_BYTES", 10*1024*1024*1024), // 10 GB
			StreamMaxAge:   getEnvDuration("NATS_STREAM_MAX_AGE", 30*24*time.Hour),  // 30 days
			MaxMsgSize:     int32(getEnvInt("NATS_MAX_MSG_SIZE", 256*1024)),         // 256 KB
			Replicas:       getEnvInt("NATS_REPLICAS", 1),
		},
		S3: S3Config{
			Endpoint:        getEnv("S3_ENDPOINT", "localhost:9002"),
			AccessKeyID:     getEnv("S3_ACCESS_KEY_ID", "minioadmin"),
			SecretAccessKey: getEnv("S3_SECRET_ACCESS_KEY", "minioadmin"),
			Bucket:          getEnv("S3_BUCKET", "mesh-artifacts"),
			Region:          getEnv("S3_REGION", "us-east-1"),
			UseSSL:          getEnvBool("S3_USE_SSL", false),
			PublicURL:       getEnv("S3_PUBLIC_URL", ""),
		},
		Auth: AuthConfig{
			JWTSecret:         getEnv("JWT_SECRET", "change-me-in-production"),
			CasdoorEndpoint:   getEnv("CASDOOR_ENDPOINT", ""),
			CasdoorClientID:   getEnv("CASDOOR_CLIENT_ID", ""),
			AgentKeyPrefix:    getEnv("AGENT_KEY_PREFIX", "agk"),
			AllowRegistration: getEnvBool("MESH_ALLOW_REGISTRATION", true),
		},
		CORS: CORSConfig{
			AllowOrigins: getEnvStringSlice("MESH_CORS_ORIGINS", []string{"*"}),
		},
		RateLimit: RateLimitConfig{
			Enabled:           getEnvBool("MESH_RATE_LIMIT_ENABLED", true),
			AuthRPM:           getEnvInt("MESH_RATE_LIMIT_AUTH_RPM", 5),
			RefreshRPM:        getEnvInt("MESH_RATE_LIMIT_REFRESH_RPM", 60),
			APIRPM:            getEnvInt("MESH_RATE_LIMIT_API_RPM", 600),
			TrustedProxies:    getEnvStringSlice("MESH_TRUSTED_PROXIES", nil),
			AuthMaxFailures:   getEnvInt("MESH_RATE_LIMIT_AUTH_MAX_FAILURES", 10),
			AuthLockoutWindow: getEnvDuration("MESH_RATE_LIMIT_AUTH_LOCKOUT_WINDOW", 15*time.Minute),
		},
		Spark: SparkConfig{
			URL:     getEnv("MESH_SPARK_URL", ""),
			Enabled: getEnvBool("MESH_SPARK_ENABLED", false),
		},
		Webhook: WebhookConfig{
			GitHubSecret: getEnv("MESH_GITHUB_WEBHOOK_SECRET", ""),
			GitHubToken:  getEnvOrFile("MESH_GITHUB_TOKEN", "MESH_GITHUB_TOKEN_FILE"),
		},
		Embedding: EmbeddingConfig{
			Provider:        getEnv("EMBEDDING_PROVIDER", "none"),
			Model:           getEnv("EMBEDDING_MODEL", ""),
			Endpoint:        getEnv("EMBEDDING_ENDPOINT", ""),
			APIKey:          getEnv("EMBEDDING_API_KEY", ""),
			Dimensions:      getEnvInt("EMBEDDING_DIMENSIONS", 0),
			BatchSize:       getEnvInt("EMBEDDING_BATCH_SIZE", 32),
			Concurrency:     getEnvInt("EMBEDDING_CONCURRENCY", 4),
			HTTPTimeoutSecs: getEnvInt("EMBEDDING_HTTP_TIMEOUT_SECS", 30),
			MaxInputTokens:  getEnvInt("EMBEDDING_MAX_INPUT_TOKENS", 0),
		},
		VAPID: VAPIDConfig{
			PublicKey:  getEnv("MESH_VAPID_PUBLIC_KEY", ""),
			PrivateKey: getEnv("MESH_VAPID_PRIVATE_KEY", ""),
			Subject:    getEnv("MESH_VAPID_SUBJECT", ""),
		},
		Email: EmailConfig{
			Host:    getEnv("SMTP_HOST", ""),
			Port:    getEnvInt("SMTP_PORT", 587),
			User:    getEnv("SMTP_USER", ""),
			Pass:    getEnv("SMTP_PASSWORD", ""),
			From:    getEnv("SMTP_FROM", ""),
			BaseURL: getEnv("MESH_BASE_URL", DefaultBaseURL),
		},
	}
}

// DefaultBaseURL is where MESH_BASE_URL lands when nobody sets it: the Vite dev
// server. It is the right default for a developer running the frontend locally
// and the wrong one for every deployed instance — invite links built from it
// point at the invitee's own machine. Callers use BaseURLIsDefault to say so out
// loud at startup rather than mailing out links that quietly go nowhere.
const DefaultBaseURL = "http://localhost:5173"

// BaseURLIsDefault reports whether invite links will be built from the
// development fallback rather than a configured public URL.
func (c EmailConfig) BaseURLIsDefault() bool {
	return c.BaseURL == DefaultBaseURL
}

// --- Helper functions ---

func getEnv(key, defaultVal string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return defaultVal
}

// getEnvOrFile returns the value of key, falling back to the contents of the
// file named by fileKey when key is unset or empty. Trailing newlines are
// trimmed so a file written with `echo` behaves like one written with
// `printf`. An unreadable file yields the empty string: the caller treats
// that as "not configured", which for the metrics token means the endpoint
// keeps its historical open behavior rather than the API refusing to boot
// over a monitoring knob.
func getEnvOrFile(key, fileKey string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	path := os.Getenv(fileKey)
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func getEnvInt(key string, defaultVal int) int {
	if val, ok := os.LookupEnv(key); ok {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return defaultVal
}

func getEnvInt64(key string, defaultVal int64) int64 {
	if val, ok := os.LookupEnv(key); ok {
		if i, err := strconv.ParseInt(val, 10, 64); err == nil {
			return i
		}
	}
	return defaultVal
}

func getEnvBool(key string, defaultVal bool) bool {
	if val, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(val); err == nil {
			return b
		}
	}
	return defaultVal
}

func getEnvDuration(key string, defaultVal time.Duration) time.Duration {
	if val, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(val); err == nil {
			return d
		}
	}
	return defaultVal
}

// getEnvStringSlice reads a comma-separated env var and returns a slice of trimmed strings.
// Falls back to defaultVal if the variable is not set or empty.
func getEnvStringSlice(key string, defaultVal []string) []string {
	val, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(val) == "" {
		return defaultVal
	}
	parts := strings.Split(val, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	if len(result) == 0 {
		return defaultVal
	}
	return result
}
