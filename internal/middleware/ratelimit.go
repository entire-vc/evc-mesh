package middleware

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// RateLimitConfig holds rate limiter configuration.
type RateLimitConfig struct {
	// Enabled controls whether the middleware actively limits requests.
	// When false, the middleware is a no-op (passes all requests through).
	Enabled bool
	// RPM is the maximum number of requests allowed per minute for each key.
	RPM int
	// KeyFunc extracts the rate-limit key from the request context.
	// A unique key (e.g. IP address or user ID) gets its own independent bucket.
	KeyFunc func(c echo.Context) string
	// RedisClient is an optional Redis client for distributed rate limiting.
	// When non-nil, a Redis sliding window counter is used instead of the
	// in-memory token bucket, enabling shared state across multiple API instances.
	// When nil, the in-memory token bucket is used (single-instance mode).
	RedisClient *redis.Client
}

// limiterEntry wraps a token-bucket limiter with the last-used timestamp
// so that stale entries can be evicted by the cleanup goroutine.
type limiterEntry struct {
	limiter  *rate.Limiter
	lastUsed time.Time
}

// rateLimitStore manages per-key token-bucket limiters.
type rateLimitStore struct {
	mu      sync.Mutex
	entries map[string]*limiterEntry
	rps     rate.Limit // tokens per second derived from RPM
	burst   int        // burst size (== RPM, i.e. 1 full minute of quota)
}

func newRateLimitStore(rpm int) *rateLimitStore {
	// Convert RPM → requests-per-second for the token-bucket algorithm.
	// Burst is set to RPM so a client that was idle for a full minute can
	// immediately make RPM requests without being throttled.
	rps := rate.Limit(float64(rpm) / 60.0)
	return &rateLimitStore{
		entries: make(map[string]*limiterEntry),
		rps:     rps,
		burst:   rpm,
	}
}

// getLimiter returns the existing limiter for key, or creates a new one.
func (s *rateLimitStore) getLimiter(key string) *rate.Limiter {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[key]
	if !ok {
		entry = &limiterEntry{
			limiter: rate.NewLimiter(s.rps, s.burst),
		}
		s.entries[key] = entry
	}
	entry.lastUsed = time.Now()
	return entry.limiter
}

// cleanup removes entries that have not been accessed for idleThreshold.
func (s *rateLimitStore) cleanup(idleThreshold time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-idleThreshold)
	for key, entry := range s.entries {
		if entry.lastUsed.Before(cutoff) {
			delete(s.entries, key)
		}
	}
}

// startCleanup spawns a goroutine that periodically evicts stale entries.
// It runs every cleanupInterval and removes entries idle for idleThreshold.
func (s *rateLimitStore) startCleanup(cleanupInterval, idleThreshold time.Duration) {
	go func() {
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			s.cleanup(idleThreshold)
		}
	}()
}

// RateLimit returns an Echo middleware that enforces the configured per-key
// rate limit. When Enabled is false the middleware is a no-op.
//
// Two backends are supported:
//   - Redis (distributed): used when cfg.RedisClient is non-nil. Implements a
//     sliding window counter via Redis INCR+EXPIRE. Suitable for multi-instance
//     deployments where all API servers share the same Redis.
//   - In-memory (single-instance): used when cfg.RedisClient is nil. Implements
//     a token-bucket algorithm via golang.org/x/time/rate.
//
// On limit exceeded the middleware responds with HTTP 429 and a Retry-After
// header indicating when the client may retry.
func RateLimit(cfg RateLimitConfig) echo.MiddlewareFunc {
	if !cfg.Enabled {
		// No-op middleware — passes every request through.
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}

	if cfg.KeyFunc == nil {
		cfg.KeyFunc = RateLimitKeyByIP
	}

	// Prefer Redis-backed limiter when a client is provided.
	if cfg.RedisClient != nil {
		return rateLimitRedis(cfg)
	}

	// Fall back to the in-memory token-bucket limiter.
	store := newRateLimitStore(cfg.RPM)
	// Evict limiters not used for 10 minutes; run cleanup every 5 minutes.
	store.startCleanup(5*time.Minute, 10*time.Minute)

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			key := cfg.KeyFunc(c)
			limiter := store.getLimiter(key)

			reservation := limiter.Reserve()
			if !reservation.OK() {
				// Limiter burst is 0 — should never happen with valid config.
				return tooManyRequestsJSON(c, 0)
			}

			delay := reservation.Delay()
			if delay > 0 {
				// Cancel the reservation — we are not going to wait.
				reservation.Cancel()
				// Retry-After: seconds until the next token is available.
				retryAfter := int(delay.Seconds()) + 1
				return tooManyRequestsJSON(c, retryAfter)
			}

			return next(c)
		}
	}
}

// RateLimitKeyByIP extracts the client IP address as the rate-limit key.
// Suitable for auth endpoints where brute-force protection is per-IP.
func RateLimitKeyByIP(c echo.Context) string {
	return c.RealIP()
}

// RateLimitKeyByActor extracts the authenticated actor (user or agent) as the
// rate-limit key. Falls back to IP if no auth context is present.
// Suitable for API endpoints where throttling is per-identity.
func RateLimitKeyByActor(c echo.Context) string {
	if id, ok := c.Get(ContextKeyUserID).(interface{ String() string }); ok {
		return "user:" + id.String()
	}
	if id, ok := c.Get(ContextKeyAgentID).(interface{ String() string }); ok {
		return "agent:" + id.String()
	}
	return "ip:" + c.RealIP()
}

// --- internal helpers ---

func tooManyRequestsJSON(c echo.Context, retryAfterSecs int) error {
	if retryAfterSecs > 0 {
		c.Response().Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSecs))
	}
	return c.JSON(http.StatusTooManyRequests, apierror.TooManyRequests("Rate limit exceeded"))
}
