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
	// AuthRPM is the maximum requests per minute for auth endpoints (per IP).
	AuthRPM int
	// APIRPM is the maximum requests per minute for API endpoints (per actor).
	APIRPM int
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Host         string
	Port         int
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
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
}

// WebhookConfig holds inbound webhook validation settings.
type WebhookConfig struct {
	// GitHubSecret is the HMAC-SHA256 secret for validating GitHub webhook payloads.
	// If empty, signature validation is skipped (backward-compatible).
	GitHubSecret string
}

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	return &Config{
		Server: ServerConfig{
			Host:         getEnv("SERVER_HOST", "0.0.0.0"),
			Port:         getEnvInt("SERVER_PORT", 8005),
			ReadTimeout:  getEnvDuration("SERVER_READ_TIMEOUT", 30*time.Second),
			WriteTimeout: getEnvDuration("SERVER_WRITE_TIMEOUT", 30*time.Second),
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
			JWTSecret:       getEnv("JWT_SECRET", "change-me-in-production"),
			CasdoorEndpoint: getEnv("CASDOOR_ENDPOINT", ""),
			CasdoorClientID: getEnv("CASDOOR_CLIENT_ID", ""),
			AgentKeyPrefix:  getEnv("AGENT_KEY_PREFIX", "agk"),
		},
		CORS: CORSConfig{
			AllowOrigins: getEnvStringSlice("MESH_CORS_ORIGINS", []string{"*"}),
		},
		RateLimit: RateLimitConfig{
			Enabled: getEnvBool("MESH_RATE_LIMIT_ENABLED", true),
			AuthRPM: getEnvInt("MESH_RATE_LIMIT_AUTH_RPM", 20),
			APIRPM:  getEnvInt("MESH_RATE_LIMIT_API_RPM", 600),
		},
		Spark: SparkConfig{
			URL:     getEnv("MESH_SPARK_URL", "https://spark.entire.vc"),
			Enabled: getEnvBool("MESH_SPARK_ENABLED", false),
		},
		Webhook: WebhookConfig{
			GitHubSecret: getEnv("MESH_GITHUB_WEBHOOK_SECRET", ""),
		},
	}
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
