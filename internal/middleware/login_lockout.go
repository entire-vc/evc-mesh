package middleware

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// LoginLockout is the account-level login failure counter that fixes the
// class of defect documented on RateLimitKeyByIP: per-IP brute-force
// protection is only as good as the reverse-proxy chain's willingness to
// relay a trustworthy client IP, and that cannot be assumed for every
// deployment (self-host, a partner instance, or simply a misconfigured hop
// — see #5d759aad). The account identifier sent in the login request body,
// by contrast, is present and meaningful regardless of network topology —
// so LoginLockout keys on THAT, and is the PRIMARY brute-force defense for
// POST /auth/login. Per-IP limiting is layered on top of it only where the
// IP is known to be trustworthy (RateLimitConfig.TrustedProxies), where it
// additionally catches credential stuffing (many different accounts, few
// attempts each) that an account-keyed counter cannot see by construction.
//
// Two properties are load-bearing and must not be "simplified" away:
//
//  1. It locks out FAILED ATTEMPTS, not the account. A request carrying the
//     CORRECT password for an identifier that has exceeded its failure
//     budget is still allowed through — see RecordFailure's caller in
//     internal/handler/auth_handler.go, which only consults this type on
//     the error path of authService.Login, never before it. A naive "N
//     requests per minute per account" limiter would hand an attacker who
//     does NOT know the password a way to lock out the owner who DOES,
//     merely by sending wrong guesses — trading a global DoS (the bug this
//     type fixes) for a targeted one, which is not a fix.
//
//  2. The counter is kept for the identifier STRING, regardless of whether
//     an account with that identifier actually exists. If only real
//     accounts accrued failures, a 429 (locked) vs. 401 (invalid
//     credentials, the existing response for a bad password on a
//     nonexistent account too) would tell an attacker which emails have
//     accounts on this instance — turning the rate limiter itself into an
//     account-enumeration oracle.
type LoginLockout struct {
	client      *redis.Client
	maxFailures int
	window      time.Duration
	// now yields the current time. Injectable ONLY so tests can pin it; nil
	// means time.Now and that is what production always uses. It exists
	// because the failure counter buckets by wall-clock (see windowKey), so
	// a test firing a sequence of requests that happens to straddle a bucket
	// boundary loses the earlier increments and fails — not because the code
	// is wrong, but because the clock moved underneath it. That is not
	// hypothetical: it flaked on CI twice, and the second time it failed the
	// deploy of an already-merged security fix. Widening the window (the
	// earlier mitigation, 1min -> 1h) only makes the boundary rarer; it
	// cannot make it impossible, and a rare flake on the test that guards a
	// security property is worse than a frequent one because it gets
	// dismissed as noise.
	now func() time.Time
}

// LoginLockoutOption configures a LoginLockout at construction.
type LoginLockoutOption func(*LoginLockout)

// WithLoginLockoutClock pins the clock LoginLockout reads. Test-only in
// practice; production calls NewLoginLockout without options and gets
// time.Now.
func WithLoginLockoutClock(now func() time.Time) LoginLockoutOption {
	return func(l *LoginLockout) { l.now = now }
}

// NewLoginLockout builds a LoginLockout backed by client. maxFailures <= 0
// disables the lockout: RecordFailure always reports "not locked" and
// Reset is a no-op. window should be the same value on every process
// sharing client — it is baked into the Redis key (see windowKey), so a
// mismatched window across instances would silently split one identifier's
// budget across multiple keys.
func NewLoginLockout(client *redis.Client, maxFailures int, window time.Duration, opts ...LoginLockoutOption) *LoginLockout {
	l := &LoginLockout{client: client, maxFailures: maxFailures, window: window}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// nowUnix is the single place this type reads the clock.
func (l *LoginLockout) nowUnix() int64 {
	if l.now != nil {
		return l.now().Unix()
	}
	return time.Now().Unix()
}

// windowSeconds returns l.window in whole seconds, floored at 1. A caller
// that (mis)configures window <= 0 must not turn into a divide-by-zero in
// windowKey NOR — the sharper failure this floor actually prevents — into
// an Expire(key, 0) in RecordFailure: Redis treats a zero/negative TTL as
// "delete immediately", which would silently wipe the just-incremented
// counter before the next request ever saw it, making the lockout a no-op
// under a misconfigured window instead of degrading to a merely-short one.
func (l *LoginLockout) windowSeconds() int64 {
	if s := int64(l.window.Seconds()); s > 0 {
		return s
	}
	return 1
}

// windowKey returns the Redis key for identifier's current fixed window,
// mirroring redisRateLimiter.windowKey in ratelimit_redis.go (same
// bucket-by-wall-clock approach, same reasoning for why it's good enough:
// the boundary imprecision this trades away is irrelevant at brute-force
// timescales).
func (l *LoginLockout) windowKey(identifier string, nowUnix int64) string {
	bucket := nowUnix / l.windowSeconds()
	return fmt.Sprintf("loginlock:%s:%d", identifier, bucket)
}

// RecordFailure records one failed login attempt for identifier and reports
// whether this attempt has pushed the identifier's failure count within the
// current window PAST maxFailures (i.e. this is the (maxFailures+1)th or
// later failure — the first maxFailures failures are NOT locked, matching
// "brute force is still caught after N attempts" rather than "the very
// first typo locks you out").
//
// On any Redis error it fails OPEN — returns locked=false with err set for
// the caller to log — consistent with rateLimitRedis's fail-open behavior
// elsewhere in this package: a Redis outage must not be able to lock every
// account out, nor take login down entirely.
func (l *LoginLockout) RecordFailure(ctx context.Context, identifier string) (locked bool, retryAfterSecs int, err error) {
	if l.maxFailures <= 0 || l.client == nil {
		return false, 0, nil
	}
	// One clock read for the whole call. Previously windowKey and the
	// Retry-After computation below each called time.Now() separately, so a
	// boundary landing between them produced a Retry-After belonging to a
	// different window than the key that was just incremented.
	nowUnix := l.nowUnix()
	rkey := l.windowKey(identifier, nowUnix)
	windowSecs := l.windowSeconds()

	var incrCmd *redis.IntCmd
	_, err = l.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		incrCmd = pipe.Incr(ctx, rkey)
		// 2x window TTL so a request that lands right at a bucket boundary
		// still sees an accurate count, same reasoning as ratelimit_redis.go.
		// Built from the FLOORED windowSecs, not the raw (possibly zero)
		// l.window — see windowSeconds' doc comment for why that distinction
		// is load-bearing, not cosmetic.
		pipe.Expire(ctx, rkey, 2*time.Duration(windowSecs)*time.Second)
		return nil
	})
	if err != nil {
		return false, 0, fmt.Errorf("login lockout record failure: %w", err)
	}

	count := incrCmd.Val()
	if count <= int64(l.maxFailures) {
		return false, 0, nil
	}

	secondsIntoWindow := nowUnix % windowSecs
	retryAfterSecs = int(windowSecs - secondsIntoWindow)
	return true, retryAfterSecs, nil
}

// Reset clears identifier's failure counter for the current window. Called
// after a SUCCESSFUL login so the genuine owner — who has just proven they
// know the password — starts with a full budget again, undoing whatever
// wrong guesses (their own mistyped attempts, or an attacker's) were
// recorded against this identifier before they logged in correctly.
func (l *LoginLockout) Reset(ctx context.Context, identifier string) error {
	if l.maxFailures <= 0 || l.client == nil {
		return nil
	}
	if err := l.client.Del(ctx, l.windowKey(identifier, l.nowUnix())).Err(); err != nil {
		return fmt.Errorf("login lockout reset: %w", err)
	}
	return nil
}
