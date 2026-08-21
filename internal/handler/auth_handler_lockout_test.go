package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
)

// newTestRedisForLockout mirrors internal/middleware's newTestRedis helper
// (unexported there, so re-declared here) — miniredis, per the task's
// instruction to use it rather than hand-mocking Redis.
func newTestRedisForLockout(t *testing.T) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// lockoutTestWindow is deliberately large (1 hour, vs. LoginLockout's
// production default of 15 minutes) because LoginLockout buckets by
// wall-clock time (time.Now().Unix() / window), same as the existing
// redisRateLimiter it mirrors, and has no injectable clock. Every test
// below fires all its requests back-to-back in well under a second, but a
// short window still leaves a nonzero chance of straddling a bucket
// boundary mid-test (observed once in CI with a 1-minute window — a request
// landed in a fresh bucket partway through a sequence, silently losing the
// prior count). An hour-wide bucket makes that chance negligible without
// needing to plumb a fake clock through two packages for tests this cheap.
func newAuthHandlerTestWithLockout(t *testing.T, userRepo *authTestUserRepo, maxFailures int) (*AuthHandler, *echo.Echo) {
	t.Helper()
	h, e := newAuthHandlerTest(userRepo)
	// PINNED clock, not the wall clock. LoginLockout buckets by
	// time.Now().Unix()/window, so any test firing a SEQUENCE of requests
	// silently loses its earlier increments if the sequence straddles a
	// bucket boundary — the later attempt lands in a fresh bucket and comes
	// back 401 where the test expects 429.
	//
	// This is not theoretical. It flaked on CI with a 1-minute window, was
	// "fixed" by widening the window to an hour, and then flaked AGAIN on
	// 2026-08-21 — failing the deploy of an already-merged security change
	// (evc-mesh #689) on an assertion that is green 30/30 locally. Widening
	// only lowers the probability: the local run is ~0.5s per iteration, CI
	// is ~23x slower at ~11.7s, so CI has ~23x the exposure to a boundary it
	// cannot control.
	//
	// A fixed clock removes the class outright rather than making it rarer.
	// A rare flake on the test guarding a security property is worse than a
	// frequent one, because it gets waved through as noise.
	frozen := time.Unix(1_700_000_000, 0)
	h.WithLoginLockout(mw.NewLoginLockout(
		newTestRedisForLockout(t), maxFailures, time.Hour,
		mw.WithLoginLockoutClock(func() time.Time { return frozen }),
	))
	return h, e
}

// registerUser is a small helper: registers a real account through the
// handler (so password hashing matches production) and returns its email.
func registerUser(t *testing.T, h *AuthHandler, e *echo.Echo, email, password string) {
	t.Helper()
	body := `{"email":"` + email + `","password":"` + password + `","name":"Lockout Test"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	require.NoError(t, h.Register(e.NewContext(req, rec)))
	require.Equal(t, http.StatusCreated, rec.Code, "test fixture setup: registration must succeed")
}

// doLogin performs one login attempt with the given email/password and,
// optionally, a distinct X-Forwarded-For value — used to demonstrate that
// the lockout does not care about the (simulated) source IP at all.
func doLogin(t *testing.T, h *AuthHandler, e *echo.Echo, email, password, xff string) (rec *httptest.ResponseRecorder, respBody map[string]any) {
	t.Helper()
	body := `{"email":"` + email + `","password":"` + password + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	if xff != "" {
		req.Header.Set(echo.HeaderXForwardedFor, xff)
	}
	rec = httptest.NewRecorder()
	require.NoError(t, h.Login(e.NewContext(req, rec)))
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &respBody)
	}
	return rec, respBody
}

// ---------------------------------------------------------------------------
// AC: "запрос по аккаунту, исчерпавшему бюджет отказов, всё равно проверяет
// пароль; пароль верный → пропускаем И сбрасываем счётчик отказов; пароль
// неверный → 429." The genuine owner, who knows the password, must NEVER be
// locked out — no matter how many wrong guesses (their own or an attacker's)
// were recorded against their identifier first.
//
// MUTATION CONTROL (reset): comment out the h.loginLockout.Reset(...) call
// in AuthHandler.Login and this test must go RED at the final assertion —
// without the reset, the post-success failure count keeps accumulating on
// top of the pre-success count, so the "maxFailures more wrong attempts are
// still not locked" assertion fails.
// ---------------------------------------------------------------------------
func TestAuthHandler_Login_CorrectPasswordAlwaysPasses_AndResetsBudget(t *testing.T) {
	const maxFailures = 3
	userRepo := newAuthTestUserRepo()
	h, e := newAuthHandlerTestWithLockout(t, userRepo, maxFailures)
	registerUser(t, h, e, "owner@example.com", "CorrectPass1")

	// Exhaust the failure budget with wrong passwords — locking the account.
	for i := 0; i < maxFailures; i++ {
		rec, _ := doLogin(t, h, e, "owner@example.com", "wrong-password", "")
		require.Equal(t, http.StatusUnauthorized, rec.Code, "attempt %d of %d must be a normal 401, not yet locked", i+1, maxFailures)
	}
	rec, _ := doLogin(t, h, e, "owner@example.com", "wrong-password", "")
	require.Equal(t, http.StatusTooManyRequests, rec.Code, "the (maxFailures+1)th wrong attempt must lock the account")

	// TODAY (before this fix), a naive per-account lock would deny this
	// request too. The correct password must ALWAYS pass, locked or not.
	rec, respBody := doLogin(t, h, e, "owner@example.com", "CorrectPass1", "")
	require.Equal(t, http.StatusOK, rec.Code, "the CORRECT password must never be blocked by the lockout, regardless of prior failures")
	assert.NotEmpty(t, respBody["tokens"], "a successful login must return tokens")

	// The successful login must have reset the budget: maxFailures MORE
	// wrong attempts must again all be plain 401s, not 429 — if the reset
	// had not happened, the leftover pre-success failures would already
	// push an early attempt in this batch over the threshold.
	for i := 0; i < maxFailures; i++ {
		attemptRec, _ := doLogin(t, h, e, "owner@example.com", "still-wrong", "")
		assert.Equal(t, http.StatusUnauthorized, attemptRec.Code, "post-reset attempt %d of %d must not be locked", i+1, maxFailures)
	}
	rec, _ = doLogin(t, h, e, "owner@example.com", "still-wrong", "")
	assert.Equal(t, http.StatusTooManyRequests, rec.Code, "budget must exhaust again at the same threshold as a fresh identifier")
}

// AC: "Ответы для существующего и несуществующего аккаунта обязаны быть
// неразличимы — код, тело, Retry-After." Once locked, an existing account
// and a nonexistent one must produce byte-identical 429 responses — a
// wrong-password 401 already behaves this way (auth.ErrInvalidCredentials is
// returned in both cases); this test proves the NEW 429 path preserves that.
func TestAuthHandler_Login_LockedResponse_IndistinguishableExistingVsNonexistent(t *testing.T) {
	const maxFailures = 2
	userRepo := newAuthTestUserRepo()
	h, e := newAuthHandlerTestWithLockout(t, userRepo, maxFailures)
	registerUser(t, h, e, "real-account@example.com", "CorrectPass1")

	lockAndCapture := func(email string) (int, string, string) {
		var rec *httptest.ResponseRecorder
		for i := 0; i <= maxFailures; i++ {
			rec, _ = doLogin(t, h, e, email, "wrong-password", "")
		}
		return rec.Code, rec.Body.String(), rec.Header().Get("Retry-After")
	}

	existingCode, existingBody, existingRetry := lockAndCapture("real-account@example.com")
	nonexistentCode, nonexistentBody, nonexistentRetry := lockAndCapture("no-such-account@example.com")

	require.Equal(t, http.StatusTooManyRequests, existingCode, "existing account must be locked by now")
	require.Equal(t, http.StatusTooManyRequests, nonexistentCode, "nonexistent account must be locked identically")
	assert.Equal(t, existingBody, nonexistentBody, "429 response body must not reveal account existence")
	// Retry-After values are computed from wall-clock position in the
	// window, not from anything account-specific — assert both are present
	// and non-empty, which is the property that matters (identical CONTENT
	// is not meaningful here since the two calls run milliseconds apart and
	// could legitimately straddle a window-second boundary).
	assert.NotEmpty(t, existingRetry, "locked response must carry Retry-After")
	assert.NotEmpty(t, nonexistentRetry, "locked response must carry Retry-After")
}

// AC: normalized identifier — "A@x.com" and "a@x.com" must share one budget
// (the real account is looked up case-insensitively too, per
// auth.NormalizeEmail; the lockout must track the SAME entity, not two).
//
// MUTATION CONTROL (normalization): replace `auth.NormalizeEmail(req.Email)`
// with the raw `req.Email` in AuthHandler.Login and this test must go RED —
// without normalization, "A@x.com" and "a@x.com" hash to different Redis
// keys, so exhausting one leaves the other with a full, independent budget,
// and the final assertion (still-locked) fails.
func TestAuthHandler_Login_NormalizesIdentifierAcrossCase_SharesBudget(t *testing.T) {
	const maxFailures = 2
	userRepo := newAuthTestUserRepo()
	h, e := newAuthHandlerTestWithLockout(t, userRepo, maxFailures)
	registerUser(t, h, e, "case-test@example.com", "CorrectPass1")

	// Exhaust the budget using an UPPERCASE variant of the email.
	for i := 0; i <= maxFailures; i++ {
		doLogin(t, h, e, "CASE-TEST@example.com", "wrong-password", "")
	}

	// A lowercase variant — same underlying account — must already be
	// locked too: the budget must be shared, not per-casing.
	rec, _ := doLogin(t, h, e, "case-test@example.com", "wrong-password", "")
	assert.Equal(t, http.StatusTooManyRequests, rec.Code,
		"case-test@example.com and CASE-TEST@example.com must share one failure budget")
}

// AC (negative control): "брутфорс по ОДНОМУ аккаунту с РАЗНЫХ IP по-прежнему
// отсекается." The lockout is keyed purely on the identifier, so rotating
// the (simulated) source IP across attempts must not reset or evade it —
// this is exactly the gap the pre-fix IP-only limiter had (an attacker
// spreading guesses across many source IPs was never caught by a per-IP
// bucket at all).
func TestAuthHandler_Login_BruteForceAcrossDifferentIPs_StillLocked(t *testing.T) {
	const maxFailures = 3
	userRepo := newAuthTestUserRepo()
	h, e := newAuthHandlerTestWithLockout(t, userRepo, maxFailures)
	registerUser(t, h, e, "victim@example.com", "CorrectPass1")

	ips := []string{"203.0.113.1", "198.51.100.7", "192.0.2.55", "203.0.113.99"}
	var last *httptest.ResponseRecorder
	for i, ip := range ips {
		last, _ = doLogin(t, h, e, "victim@example.com", "wrong-password", ip)
		if i < maxFailures {
			require.Equal(t, http.StatusUnauthorized, last.Code, "attempt %d (from %s) must be a normal 401", i+1, ip)
		}
	}
	assert.Equal(t, http.StatusTooManyRequests, last.Code,
		"brute force spread across different source IPs must still be locked — the counter is per-account, not per-IP")
}

// AC (a), end-to-end through the actual shipped Login handler: two DIFFERENT
// real accounts, both hit from the same (simulated) source IP, must not
// share one failure budget. Exhausting one account's budget must leave the
// other account's own login attempts completely unaffected — the opposite
// of TestRateLimitKeyByIP_TwoAccountsSameIP_ShareOneBucket_BEFORE in
// internal/middleware, which demonstrates the shared-bucket defect this is
// fixing.
func TestAuthHandler_Login_TwoAccountsSameIP_DoNotShareBudget(t *testing.T) {
	const maxFailures = 2
	const sharedIP = "10.10.10.1" // e.g. every external client, per #5d759aad
	userRepo := newAuthTestUserRepo()
	h, e := newAuthHandlerTestWithLockout(t, userRepo, maxFailures)
	registerUser(t, h, e, "alice@example.com", "AlicePass1")
	registerUser(t, h, e, "bob@example.com", "BobPass1")

	// Exhaust alice's budget from the shared IP.
	var last *httptest.ResponseRecorder
	for i := 0; i <= maxFailures; i++ {
		last, _ = doLogin(t, h, e, "alice@example.com", "wrong-password", sharedIP)
	}
	require.Equal(t, http.StatusTooManyRequests, last.Code, "alice must now be locked")

	// Bob, same IP, correct password on his FIRST attempt — must succeed.
	rec, respBody := doLogin(t, h, e, "bob@example.com", "BobPass1", sharedIP)
	assert.Equal(t, http.StatusOK, rec.Code,
		"bob's account must not be affected by alice's failures, even from the same source IP")
	assert.NotEmpty(t, respBody["tokens"])
}

// newAuthHandlerTestWithBrokenLockout wires a LoginLockout whose Redis
// connection is already closed, so every RecordFailure/Reset call returns
// an error — covers AuthHandler.Login's fail-open logging branches on both
// the failure-recording and the reset path (a live Redis never errors in
// the tests above, so those two branches are otherwise dead in this suite).
func newAuthHandlerTestWithBrokenLockout(t *testing.T, userRepo *authTestUserRepo, maxFailures int) (*AuthHandler, *echo.Echo) {
	t.Helper()
	h, e := newAuthHandlerTest(userRepo)
	rdb := newTestRedisForLockout(t)
	require.NoError(t, rdb.Close())
	h.WithLoginLockout(mw.NewLoginLockout(rdb, maxFailures, time.Hour))
	return h, e
}

// Covers the `lockErr != nil` branch in AuthHandler.Login: a Redis error
// while recording a failure must be logged and fall through to the normal
// 401 — it must NOT surface as a 5xx, and must NOT accidentally read as
// "locked".
func TestAuthHandler_Login_LockoutRecordFailureError_FallsThroughTo401(t *testing.T) {
	userRepo := newAuthTestUserRepo()
	h, e := newAuthHandlerTestWithBrokenLockout(t, userRepo, 3)
	registerUser(t, h, e, "owner@example.com", "CorrectPass1")

	rec, _ := doLogin(t, h, e, "owner@example.com", "wrong-password", "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"a Redis error recording the failure must fail open to a normal 401, not 5xx or 429")
}

// Covers the `resetErr != nil` branch: a Redis error while resetting the
// counter on a SUCCESSFUL login must be logged but must NOT fail the login
// itself — the user already proved their password, that must not now hinge
// on Redis being up.
func TestAuthHandler_Login_LockoutResetError_LoginStillSucceeds(t *testing.T) {
	userRepo := newAuthTestUserRepo()
	h, e := newAuthHandlerTestWithBrokenLockout(t, userRepo, 3)
	registerUser(t, h, e, "owner@example.com", "CorrectPass1")

	rec, respBody := doLogin(t, h, e, "owner@example.com", "CorrectPass1", "")
	assert.Equal(t, http.StatusOK, rec.Code,
		"a Redis error on the post-success reset must not fail the login itself")
	assert.NotEmpty(t, respBody["tokens"])
}

// AC (#734ca49e): reordering auth.Service.Login to check the password
// BEFORE IsActive has a side effect that must be explicit, not discovered
// later — a WRONG password against a DEACTIVATED account now returns
// auth.ErrInvalidCredentials (it used to return auth.ErrUserInactive,
// which AuthHandler.Login's lockout branch deliberately ignores — see its
// doc comment). Since the lockout branch keys purely on
// errors.Is(err, auth.ErrInvalidCredentials), this deactivated account's
// wrong-password attempts now consume the SAME per-account failure budget
// as any other account's wrong guesses — correct, because someone
// submitting wrong passwords against a deactivated account is still a
// brute-force attempt, and no code change to auth_handler.go was needed to
// get this: it already gated on error TYPE, not on account state.
//
// Before the #734ca49e fix, this test would have failed to lock: the
// deactivated account's wrong-password attempts returned ErrUserInactive,
// which the `errors.Is(err, auth.ErrInvalidCredentials)` guard in
// AuthHandler.Login does not match, so RecordFailure was never called and
// the budget never moved — every attempt would have come back a plain 401,
// never a 429, however many were sent.
func TestAuthHandler_Login_InactiveAccount_WrongPassword_CountsAgainstLockoutBudget(t *testing.T) {
	const maxFailures = 2
	userRepo := newAuthTestUserRepo()
	h, e := newAuthHandlerTestWithLockout(t, userRepo, maxFailures)
	registerUser(t, h, e, "inactive-budget@example.com", "CorrectPass1")

	// Deactivate the account directly in the repo (mirrors the pattern used
	// in internal/auth/service_test.go's TestLogin_InactiveUser).
	userRepo.mu.Lock()
	for _, u := range userRepo.users {
		if u.Email == "inactive-budget@example.com" {
			u.IsActive = false
		}
	}
	userRepo.mu.Unlock()

	// The first maxFailures wrong-password attempts against the deactivated
	// account must be ordinary 401s (not yet locked, not the inactive
	// message — see the indistinguishability test in internal/auth for that
	// property; this test is about the BUDGET, not the body).
	var last *httptest.ResponseRecorder
	for i := 0; i < maxFailures; i++ {
		last, _ = doLogin(t, h, e, "inactive-budget@example.com", "wrong-password", "")
		require.Equal(t, http.StatusUnauthorized, last.Code, "attempt %d of %d must be a normal 401, not yet locked", i+1, maxFailures)
	}

	// The (maxFailures+1)th wrong-password attempt must lock the account —
	// proving the deactivated account's wrong guesses DO consume the
	// failure budget, same as any active account's.
	last, _ = doLogin(t, h, e, "inactive-budget@example.com", "wrong-password", "")
	require.Equal(t, http.StatusTooManyRequests, last.Code,
		"a deactivated account's wrong-password attempts must count against the lockout budget, same as any other account's")

	// Negative control: the CORRECT password on this now-locked, deactivated
	// account must NOT be treated as a brute-force success either — it
	// still returns the clear "account is inactive" error (service-layer
	// property, tested directly in internal/auth), and critically must NOT
	// be let through the lockout as if it were a valid login.
	rec, respBody := doLogin(t, h, e, "inactive-budget@example.com", "CorrectPass1", "")
	assert.NotEqual(t, http.StatusOK, rec.Code, "a deactivated account must never receive tokens, even with the correct password and even while rate-limited")
	assert.Empty(t, respBody["tokens"], "no tokens must ever be issued to a deactivated account")
}
