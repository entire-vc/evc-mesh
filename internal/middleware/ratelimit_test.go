package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Minimal rate limiter implementation for testing purposes.
// This documents the expected contract for the real RateLimiter middleware
// that will be implemented in Phase 5-OSS.
//
// Contract:
//   - Each unique key (IP or auth identifier) has an independent token bucket.
//   - Requests within the limit pass through (200).
//   - Requests exceeding the limit receive 429 Too Many Requests.
//   - The window resets after the configured duration (sliding or fixed).
//   - Agents receive a higher rate limit than regular users.
// ---------------------------------------------------------------------------

// rateLimitConfig holds parameters for the rate limiter middleware.
type rateLimitConfig struct {
	// Limit is the maximum number of requests allowed per window.
	Limit int
	// Window is the duration of the rate limit window.
	Window time.Duration
	// KeyFunc extracts the rate-limit key from the request (e.g. IP address).
	KeyFunc func(c echo.Context) string
	// Clock is an injectable clock for testing; defaults to time.Now.
	Clock func() time.Time
}

// rateLimiterState stores per-key request counts with their window start.
type rateLimiterState struct {
	mu      sync.Mutex
	buckets map[string]*rateBucket
	clock   func() time.Time
}

type rateBucket struct {
	count       int
	windowStart time.Time
}

// testRateLimiter builds a rate-limiter middleware from a config.
// This is the reference contract implementation used in tests.
// The production middleware will follow the same behavior.
func testRateLimiter(cfg rateLimitConfig) echo.MiddlewareFunc {
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.KeyFunc == nil {
		cfg.KeyFunc = func(c echo.Context) string {
			return c.RealIP()
		}
	}
	state := &rateLimiterState{
		buckets: make(map[string]*rateBucket),
		clock:   cfg.Clock,
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			key := cfg.KeyFunc(c)
			now := state.clock()

			state.mu.Lock()
			bucket, ok := state.buckets[key]
			if !ok || now.Sub(bucket.windowStart) >= cfg.Window {
				bucket = &rateBucket{count: 0, windowStart: now}
				state.buckets[key] = bucket
			}
			bucket.count++
			count := bucket.count
			state.mu.Unlock()

			if count > cfg.Limit {
				return c.JSON(http.StatusTooManyRequests, map[string]string{
					"error": "rate limit exceeded",
				})
			}
			return next(c)
		}
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestRateLimiter_AllowsRequestsUnderLimit(t *testing.T) {
	e := echo.New()
	called := 0
	handler := func(c echo.Context) error {
		called++
		return c.NoContent(http.StatusOK)
	}

	clock := time.Now
	mw := testRateLimiter(rateLimitConfig{
		Limit:   10,
		Window:  time.Minute,
		Clock:   func() time.Time { return clock() },
		KeyFunc: func(c echo.Context) string { return "test-key" },
	})
	wrapped := mw(handler)

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := wrapped(c)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, rec.Code, "request %d should pass", i+1)
	}
	assert.Equal(t, 5, called)
}

func TestRateLimiter_BlocksRequestsOverLimit(t *testing.T) {
	e := echo.New()
	const limit = 3

	mw := testRateLimiter(rateLimitConfig{
		Limit:   limit,
		Window:  time.Minute,
		Clock:   time.Now,
		KeyFunc: func(c echo.Context) string { return "single-client" },
	})
	handler := func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	}
	wrapped := mw(handler)

	statuses := make([]int, 5)
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := wrapped(c)
		require.NoError(t, err)
		statuses[i] = rec.Code
	}

	// First 3 should pass.
	for i := 0; i < limit; i++ {
		assert.Equal(t, http.StatusOK, statuses[i], "request %d should be allowed", i+1)
	}
	// Requests 4 and 5 should be rate-limited.
	for i := limit; i < 5; i++ {
		assert.Equal(t, http.StatusTooManyRequests, statuses[i], "request %d should be blocked", i+1)
	}
}

func TestRateLimiter_DifferentLimitsPerAuthType(t *testing.T) {
	// Contract: agent keys get a higher rate limit than user tokens.
	// This is documented as a Phase 5 feature requirement:
	//   - Default user limit:  100 req/min
	//   - Default agent limit: 200 req/min
	//
	// Simulation: use two separate middleware instances with different limits.

	e := echo.New()
	const userLimit = 3
	const agentLimit = 6

	userMW := testRateLimiter(rateLimitConfig{
		Limit:   userLimit,
		Window:  time.Minute,
		Clock:   time.Now,
		KeyFunc: func(c echo.Context) string { return "user-client" },
	})
	agentMW := testRateLimiter(rateLimitConfig{
		Limit:   agentLimit,
		Window:  time.Minute,
		Clock:   time.Now,
		KeyFunc: func(c echo.Context) string { return "agent-client" },
	})
	handler := func(c echo.Context) error { return c.NoContent(http.StatusOK) }

	wrappedUser := userMW(handler)
	wrappedAgent := agentMW(handler)

	// User is blocked after userLimit requests.
	userAllowed := 0
	for i := 0; i < userLimit+2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		require.NoError(t, wrappedUser(c))
		if rec.Code == http.StatusOK {
			userAllowed++
		}
	}
	assert.Equal(t, userLimit, userAllowed, "user should be blocked after %d requests", userLimit)

	// Agent gets more requests through.
	agentAllowed := 0
	for i := 0; i < agentLimit+2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		require.NoError(t, wrappedAgent(c))
		if rec.Code == http.StatusOK {
			agentAllowed++
		}
	}
	assert.Equal(t, agentLimit, agentAllowed, "agent should be blocked after %d requests", agentLimit)
	assert.Greater(t, agentAllowed, userAllowed, "agent limit should be higher than user limit")
}

func TestRateLimiter_ResetsAfterWindow(t *testing.T) {
	e := echo.New()
	const limit = 2

	// Controllable clock: starts at t0.
	nowUnix := time.Now().Unix()
	clock := func() time.Time { return time.Unix(atomic.LoadInt64(&nowUnix), 0) }

	mw := testRateLimiter(rateLimitConfig{
		Limit:   limit,
		Window:  time.Minute,
		Clock:   clock,
		KeyFunc: func(c echo.Context) string { return "resettable-client" },
	})
	handler := func(c echo.Context) error { return c.NoContent(http.StatusOK) }
	wrapped := mw(handler)

	sendRequest := func() int {
		req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		require.NoError(t, wrapped(c))
		return rec.Code
	}

	// Exhaust the limit.
	for i := 0; i < limit; i++ {
		assert.Equal(t, http.StatusOK, sendRequest(), "request %d should pass", i+1)
	}
	// Next request should be blocked.
	assert.Equal(t, http.StatusTooManyRequests, sendRequest(), "request after limit should be blocked")

	// Advance time past the window (61 seconds).
	atomic.AddInt64(&nowUnix, 61)

	// After window reset, new requests should pass again.
	assert.Equal(t, http.StatusOK, sendRequest(), "first request after window reset should pass")
	assert.Equal(t, http.StatusOK, sendRequest(), "second request after window reset should pass")
}

func TestRateLimiter_IndependentKeysDoNotInterfere(t *testing.T) {
	// Two different clients (keys) should have independent counters.
	e := echo.New()
	const limit = 2

	var keyToUse string
	mw := testRateLimiter(rateLimitConfig{
		Limit:   limit,
		Window:  time.Minute,
		Clock:   time.Now,
		KeyFunc: func(c echo.Context) string { return keyToUse },
	})
	handler := func(c echo.Context) error { return c.NoContent(http.StatusOK) }
	wrapped := mw(handler)

	sendAs := func(key string) int {
		keyToUse = key
		req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		require.NoError(t, wrapped(c))
		return rec.Code
	}

	// Exhaust limit for client-A.
	for i := 0; i < limit; i++ {
		assert.Equal(t, http.StatusOK, sendAs("client-A"))
	}
	assert.Equal(t, http.StatusTooManyRequests, sendAs("client-A"), "client-A should be blocked")

	// client-B should still be allowed (independent counter).
	assert.Equal(t, http.StatusOK, sendAs("client-B"), "client-B counter is independent")
	assert.Equal(t, http.StatusOK, sendAs("client-B"), "client-B still within limit")
}

func TestRateLimiter_ExactBoundary(t *testing.T) {
	// The request at exactly the limit should be allowed;
	// the request at limit+1 should be blocked.
	e := echo.New()
	const limit = 5

	mw := testRateLimiter(rateLimitConfig{
		Limit:   limit,
		Window:  time.Minute,
		Clock:   time.Now,
		KeyFunc: func(c echo.Context) string { return "boundary-client" },
	})
	handler := func(c echo.Context) error { return c.NoContent(http.StatusOK) }
	wrapped := mw(handler)

	var allowed, blocked int
	for i := 0; i < limit+1; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		require.NoError(t, wrapped(c))
		if rec.Code == http.StatusOK {
			allowed++
		} else {
			blocked++
		}
	}

	assert.Equal(t, limit, allowed, "exactly %d requests should be allowed", limit)
	assert.Equal(t, 1, blocked, "exactly 1 request should be blocked")
}
