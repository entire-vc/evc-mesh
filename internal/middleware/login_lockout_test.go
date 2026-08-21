package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoginLockout_AllowsUnderBudget(t *testing.T) {
	rdb := newTestRedis(t)
	l := NewLoginLockout(rdb, 3, time.Hour)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		locked, _, err := l.RecordFailure(ctx, "user@example.com")
		require.NoError(t, err)
		assert.False(t, locked, "failure %d of 3 (== maxFailures) must not lock yet", i+1)
	}
}

func TestLoginLockout_LocksAfterMaxFailuresExceeded(t *testing.T) {
	rdb := newTestRedis(t)
	l := NewLoginLockout(rdb, 3, time.Hour)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, _, err := l.RecordFailure(ctx, "user@example.com")
		require.NoError(t, err)
	}

	// The 4th failure — one past maxFailures=3 — must lock.
	locked, retryAfter, err := l.RecordFailure(ctx, "user@example.com")
	require.NoError(t, err)
	assert.True(t, locked, "4th failure (maxFailures=3) must lock")
	assert.Greater(t, retryAfter, 0, "a locked response must carry a positive Retry-After")
}

// TestLoginLockout_DifferentIdentifiers_NeverShareBudget is the direct proof
// for acceptance criterion (a): two different accounts do not share one
// failure budget. Unlike RateLimitKeyByIP (see the BEFORE contrast test
// below), LoginLockout never looks at the network layer at all — it is keyed
// purely on the identifier string, so this holds regardless of whatever
// c.RealIP() would have resolved to for either request.
func TestLoginLockout_DifferentIdentifiers_NeverShareBudget(t *testing.T) {
	rdb := newTestRedis(t)
	l := NewLoginLockout(rdb, 3, time.Hour)
	ctx := context.Background()

	// Exhaust account A's budget and lock it.
	for i := 0; i < 4; i++ {
		_, _, err := l.RecordFailure(ctx, "alice@example.com")
		require.NoError(t, err)
	}
	lockedA, _, err := l.RecordFailure(ctx, "alice@example.com")
	require.NoError(t, err)
	assert.True(t, lockedA, "alice's own budget should now be exhausted")

	// Bob's very first failure must NOT be locked — his budget is untouched.
	lockedB, _, err := l.RecordFailure(ctx, "bob@example.com")
	require.NoError(t, err)
	assert.False(t, lockedB, "bob must have his own independent budget, unaffected by alice's failures")
}

func TestLoginLockout_ResetClearsBudget(t *testing.T) {
	rdb := newTestRedis(t)
	l := NewLoginLockout(rdb, 3, time.Hour)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, _, err := l.RecordFailure(ctx, "user@example.com")
		require.NoError(t, err)
	}
	require.NoError(t, l.Reset(ctx, "user@example.com"))

	// After reset, 3 more failures must again be allowed before locking —
	// if Reset had not actually cleared the counter, the very next failure
	// (a cumulative 4th) would already lock.
	for i := 0; i < 3; i++ {
		locked, _, err := l.RecordFailure(ctx, "user@example.com")
		require.NoError(t, err)
		assert.False(t, locked, "post-reset failure %d of 3 must not lock (budget was reset)", i+1)
	}
	locked, _, err := l.RecordFailure(ctx, "user@example.com")
	require.NoError(t, err)
	assert.True(t, locked, "the 4th post-reset failure must lock, same as a fresh identifier would")
}

func TestLoginLockout_MaxFailuresNonPositive_Disabled(t *testing.T) {
	rdb := newTestRedis(t)
	l := NewLoginLockout(rdb, 0, time.Hour)
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		locked, _, err := l.RecordFailure(ctx, "user@example.com")
		require.NoError(t, err)
		assert.False(t, locked, "maxFailures<=0 must disable the lockout entirely")
	}
}

// TestRateLimitKeyByIP_TwoAccountsSameIP_ShareOneBucket_BEFORE is the "before"
// contrast the task's acceptance criteria call for: it demonstrates the
// defect #5d759aad is about using the EXISTING RateLimitKeyByIP mechanism
// /auth/login was (and, when MESH_TRUSTED_PROXIES is unset, still is) keyed
// by. When every external client resolves to one IP (the incident's actual
// scenario — Caddy's default trusted_proxies overwrites X-Forwarded-For with
// its own peer address), a per-IP-only limiter becomes ONE shared bucket
// regardless of which account each request names. Proven here: two different
// (simulated) accounts hitting the same IP-keyed limiter — one account's
// failed attempts exhaust the exact budget the other account's, entirely
// unrelated, login request then needs.
func TestRateLimitKeyByIP_TwoAccountsSameIP_ShareOneBucket_BEFORE(t *testing.T) {
	e := echo.New()
	const sharedIP = "10.10.10.1" // every external client, per #5d759aad
	const rpm = 3

	limiter := RateLimit(RateLimitConfig{
		Enabled: true,
		RPM:     rpm,
		KeyFunc: func(c echo.Context) string { return sharedIP }, // RealIP() collapses everyone to this
	})
	handler := func(c echo.Context) error { return c.NoContent(http.StatusOK) }
	wrapped := limiter(handler)

	doReq := func() int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", http.NoBody)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		require.NoError(t, wrapped(c))
		return rec.Code
	}

	// "Account A" (an attacker) spends the entire shared budget.
	for i := 0; i < rpm; i++ {
		assert.Equal(t, http.StatusOK, doReq(), "attacker request %d spends the shared bucket", i+1)
	}

	// "Account B" (an unrelated, legitimate user on the same IP, e.g. behind
	// the same misconfigured proxy) gets blocked on their FIRST request —
	// nothing about their own identity mattered; the bucket was already
	// empty before they ever tried.
	assert.Equal(t, http.StatusTooManyRequests, doReq(),
		"BEFORE the fix: an unrelated account sharing the same (mis-resolved) IP is denied service")
}

// TestLoginLockout_ZeroWindow_FallsBackToOneSecondBucket exercises the
// windowSecs<=0 defensive fallback in both windowKey and RecordFailure's
// Retry-After computation — a caller that (mis)configures a zero or
// negative window must not divide by zero or produce a nonsensical bucket.
func TestLoginLockout_ZeroWindow_FallsBackToOneSecondBucket(t *testing.T) {
	rdb := newTestRedis(t)
	l := NewLoginLockout(rdb, 1, 0) // window=0, deliberately misconfigured
	ctx := context.Background()

	locked, _, err := l.RecordFailure(ctx, "user@example.com")
	require.NoError(t, err)
	assert.False(t, locked, "1st failure (maxFailures=1) must not lock yet")

	locked, retryAfter, err := l.RecordFailure(ctx, "user@example.com")
	require.NoError(t, err)
	assert.True(t, locked, "2nd failure must lock — proves the zero-window fallback still buckets correctly")
	assert.GreaterOrEqual(t, retryAfter, 0, "retryAfter must still be a sane (non-negative) value with a degenerate window")
}

// TestLoginLockout_RecordFailure_RedisError_FailsOpen exercises the fail-open
// path when Redis itself errors (not merely "budget exceeded") — a closed
// client is the simplest reliable way to force go-redis to return an error.
func TestLoginLockout_RecordFailure_RedisError_FailsOpen(t *testing.T) {
	rdb := newTestRedis(t)
	l := NewLoginLockout(rdb, 3, time.Hour)
	require.NoError(t, rdb.Close())

	locked, retryAfter, err := l.RecordFailure(context.Background(), "user@example.com")
	assert.Error(t, err, "a Redis error must be returned to the caller for logging")
	assert.False(t, locked, "on a Redis error the lockout must fail OPEN, never locked")
	assert.Equal(t, 0, retryAfter)
}

// TestLoginLockout_Reset_MaxFailuresNonPositive_NoOp covers Reset's disabled
// path (mirrors TestLoginLockout_MaxFailuresNonPositive_Disabled, which only
// exercised RecordFailure's disabled branch).
func TestLoginLockout_Reset_MaxFailuresNonPositive_NoOp(t *testing.T) {
	rdb := newTestRedis(t)
	l := NewLoginLockout(rdb, 0, time.Hour)
	assert.NoError(t, l.Reset(context.Background(), "user@example.com"))
}

// TestLoginLockout_Reset_RedisError_IsReported mirrors the RecordFailure
// Redis-error test above, for Reset's own error path.
func TestLoginLockout_Reset_RedisError_IsReported(t *testing.T) {
	rdb := newTestRedis(t)
	l := NewLoginLockout(rdb, 3, time.Hour)
	require.NoError(t, rdb.Close())

	err := l.Reset(context.Background(), "user@example.com")
	assert.Error(t, err, "a Redis error on Reset must be returned to the caller for logging")
}

// TestLoginLockout_CountIsLostAcrossAWindowBoundary pins down the mechanism
// behind the CI flake that failed the deploy of #689 on 2026-08-21, and
// documents the production property it exposes.
//
// LoginLockout buckets by wall clock (windowKey: nowUnix / windowSeconds).
// Increments therefore accrue against whichever bucket happens to be current
// at the moment of each call — so a sequence of failures that straddles a
// boundary does NOT accumulate: the attempts after the boundary start from
// zero in a fresh key.
//
// Two consequences, and both are worth having in a test rather than in
// someone's head:
//
//  1. TESTS: any test firing a sequence against the real clock is flaky by
//     construction. That is why the handler-level tests pin their clock.
//     This test is the negative control for that decision — remove the
//     pinning there and this is the failure you get, non-deterministically.
//
//  2. PRODUCTION: an attacker who times attempts around a boundary gets up
//     to 2*maxFailures guesses in quick succession (maxFailures at the end of
//     one window, maxFailures at the start of the next) instead of
//     maxFailures. That is inherent to every fixed-window counter, it is a
//     known and accepted trade for the cheapness of INCR+EXPIRE, and at the
//     production window (15 min) it means 20 guesses rather than 10 in the
//     worst case — still far below what brute-forcing a password needs. It
//     is accepted, not unnoticed.
func TestLoginLockout_CountIsLostAcrossAWindowBoundary(t *testing.T) {
	const maxFailures = 3
	const window = time.Hour

	rdb := newTestRedis(t)

	// Start 2 seconds before a bucket boundary, so the sequence below
	// straddles it at a moment we choose rather than one the CI scheduler
	// chooses for us.
	boundary := (time.Now().Unix()/int64(window.Seconds()) + 1) * int64(window.Seconds())
	var offset int64
	clock := func() time.Time { return time.Unix(boundary-2+offset, 0) }

	l := NewLoginLockout(rdb, maxFailures, window, WithLoginLockoutClock(clock))
	ctx := context.Background()

	// Three failures BEFORE the boundary — budget exactly exhausted, not yet
	// locked (the lock fires on the maxFailures+1'th).
	for i := 0; i < maxFailures; i++ {
		locked, _, err := l.RecordFailure(ctx, "victim@example.com")
		require.NoError(t, err)
		require.False(t, locked, "failure %d of %d must not lock yet", i+1, maxFailures)
	}

	// Cross the boundary. In the same window this next failure would be the
	// (maxFailures+1)'th and MUST lock.
	offset = 4

	locked, _, err := l.RecordFailure(ctx, "victim@example.com")
	require.NoError(t, err)
	assert.False(t, locked,
		"documents the fixed-window property: the count does not carry across a "+
			"bucket boundary, so this attempt starts a fresh budget instead of locking. "+
			"If this ever becomes true, the bucketing was replaced by something with "+
			"carry-over — update the handler tests' clock-pinning rationale to match.")

	// Same call without crossing a boundary DOES lock — proving the assertion
	// above is about the boundary and not about RecordFailure being broken.
	offset = 5
	for i := 0; i < maxFailures-1; i++ {
		_, _, err := l.RecordFailure(ctx, "victim@example.com")
		require.NoError(t, err)
	}
	locked, _, err = l.RecordFailure(ctx, "victim@example.com")
	require.NoError(t, err)
	assert.True(t, locked,
		"within one window the counter accumulates normally and locks at maxFailures+1")
}
