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
	BaseURL string // e.g. https://mesh.entire.host — used to build invite accept links
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
	// Concurrency bounds how many embed calls may run concurrently (default: 0, meaning
	// unbounded — every write spawns its own embed goroutine, today's exact behavior).
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
	// prod install). The self-host docker-compose.prod.yml requires this
	// var rather than leaving it empty, since it publishes the port and has
	// no such front proxy by default.
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

// NATSConfig holds NATS connection settings.
type NATSConfig struct {
	URL string
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
	// AllowRegistration gates POST /auth/register once the instance already has
	// at least one user. Defaults to true so existing installs keep working
	// unchanged. The first user on a fresh install can always register
	// regardless of this setting (see auth.Service.RegistrationOpen).
	AllowRegistration bool
}

// WebhookConfig holds inbound webhook validation settings.
type WebhookConfig struct {
	// GitHubSecret is the HMAC-SHA256 secret for validating GitHub webhook payloads.
	// If empty, signature validation is skipped (backward-compatible).
	GitHubSecret string
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
			MetricsToken: getEnv("MESH_METRICS_TOKEN", ""),
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
			URL: getEnv("NATS_URL", "nats://localhost:4223"),
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
			Enabled:    getEnvBool("MESH_RATE_LIMIT_ENABLED", true),
			AuthRPM:    getEnvInt("MESH_RATE_LIMIT_AUTH_RPM", 5),
			RefreshRPM: getEnvInt("MESH_RATE_LIMIT_REFRESH_RPM", 60),
			APIRPM:     getEnvInt("MESH_RATE_LIMIT_API_RPM", 600),
		},
		Spark: SparkConfig{
			URL:     getEnv("MESH_SPARK_URL", "https://spark.entire.vc"),
			Enabled: getEnvBool("MESH_SPARK_ENABLED", false),
		},
		Webhook: WebhookConfig{
			GitHubSecret: getEnv("MESH_GITHUB_WEBHOOK_SECRET", ""),
		},
		Embedding: EmbeddingConfig{
			Provider:        getEnv("EMBEDDING_PROVIDER", "none"),
			Model:           getEnv("EMBEDDING_MODEL", ""),
			Endpoint:        getEnv("EMBEDDING_ENDPOINT", ""),
			APIKey:          getEnv("EMBEDDING_API_KEY", ""),
			Dimensions:      getEnvInt("EMBEDDING_DIMENSIONS", 0),
			BatchSize:       getEnvInt("EMBEDDING_BATCH_SIZE", 32),
			Concurrency:     getEnvInt("EMBEDDING_CONCURRENCY", 0),
			HTTPTimeoutSecs: getEnvInt("EMBEDDING_HTTP_TIMEOUT_SECS", 30),
		},
		VAPID: VAPIDConfig{
			PublicKey:  getEnv("MESH_VAPID_PUBLIC_KEY", ""),
			PrivateKey: getEnv("MESH_VAPID_PRIVATE_KEY", ""),
			Subject:    getEnv("MESH_VAPID_SUBJECT", "mailto:rj@entire.vc"),
		},
		Email: EmailConfig{
			Host:    getEnv("SMTP_HOST", ""),
			Port:    getEnvInt("SMTP_PORT", 587),
			User:    getEnv("SMTP_USER", ""),
			Pass:    getEnv("SMTP_PASSWORD", ""),
			From:    getEnv("SMTP_FROM", "noreply@mesh.entire.host"),
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

func getEnvInt(key string, defaultVal int) int {
	if val, ok := os.LookupEnv(key); ok {
		if i, err := strconv.Atoi(val); err == nil {
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
