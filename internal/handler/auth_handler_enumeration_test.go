package handler

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// AC (#c9055b2c "hvost", from the #734ca49e / PR #721 review): criterion 1 of
// that task required indistinguishability at the HTTP layer — status code,
// body, AND headers. The suite only ever proved this at the SERVICE layer
// (internal/auth.TestLogin_InactiveUser_WrongPassword_IndistinguishableFromNonexistent)
// plus a throwaway test the reviewer ran once by hand and never committed:
//
//	inactive:    code=401 headers=map[Content-Type:[application/json]] body={"code":401,"message":"invalid email or password"}
//	nonexistent: code=401 headers=map[Content-Type:[application/json]] body={"code":401,"message":"invalid email or password"}
//	--- PASS
//
// This is that test, landed permanently, comparing the actual echo.Context
// response objects (not their JSON re-decoded and re-compared field by
// field, which would silently ignore an extra header or a body byte outside
// the fields being checked).
//
// MUTATION CONTROL — two mutations were run against this test (full
// commands/output in the task report), with an honest finding on the first:
//
//   - Mutation A, the literal example the task's acceptance criteria named
//     ("give ErrUserInactive its own distinct body in handleError" — adding
//     an `if errors.Is(err, auth.ErrUserInactive) { return c.JSON(401,
//     distinctBody) }` branch at the top of handleError in task_handler.go):
//     stayed GREEN. Not a hole in this test — a fact about why it's safe:
//     neither scenario compared here ever returns auth.ErrUserInactive.
//     Login checks the password BEFORE IsActive (#734ca49e), so a WRONG
//     password against a deactivated account returns
//     auth.ErrInvalidCredentials — the literal same package-level error
//     value returned for a nonexistent account. A handleError branch keyed
//     on ErrUserInactive is structurally unreachable from this pair, so it
//     cannot turn this test red — verified, not assumed.
//   - Mutation B, a real regression of the property this test exists to
//     protect (temporarily reverting internal/auth/service.go's Login to
//     the pre-#734ca49e ordering — IsActive checked before the password):
//     went RED, exactly as expected — the deactivated-account body became
//     `{"code":401,"message":"user account is inactive"}` while the
//     nonexistent-account body stayed `{"code":401,"message":"invalid email
//     or password"}`. This is the mutation that actually exercises this
//     test's core assertion.
//
// ---------------------------------------------------------------------------
func TestAuthHandler_Login_InactiveWrongPassword_IndistinguishableFromNonexistent_HTTP(t *testing.T) {
	userRepo := newAuthTestUserRepo()
	h, e := newAuthHandlerTest(userRepo)
	registerUser(t, h, e, "deactivated-http@example.com", "CorrectPass1")

	userRepo.mu.Lock()
	for _, u := range userRepo.users {
		if u.Email == "deactivated-http@example.com" {
			u.IsActive = false
		}
	}
	userRepo.mu.Unlock()

	inactiveRec, _ := doLogin(t, h, e, "deactivated-http@example.com", "TotallyWrongPassword1", "")
	nonexistentRec, _ := doLogin(t, h, e, "no-such-account-http@example.com", "TotallyWrongPassword1", "")

	require.Equal(t, http.StatusUnauthorized, inactiveRec.Code, "deactivated account + wrong password must be a plain 401, not the inactive-specific error")
	require.Equal(t, http.StatusUnauthorized, nonexistentRec.Code, "nonexistent account must be a plain 401")

	// Compare the response TRIPLE — code, body, headers — not just the code:
	// a test asserting only the 401 would go green on the exact defect class
	// this guards against (a future handleError change that keeps the status
	// but adds a distinguishing body field or header for one of the two).
	assert.Equal(t, inactiveRec.Code, nonexistentRec.Code,
		"status codes must be identical")
	assert.Equal(t, inactiveRec.Body.String(), nonexistentRec.Body.String(),
		"response BODIES must be byte-identical — an attacker must not be able to tell these two cases apart from the JSON")
	assert.Equal(t, inactiveRec.Header(), nonexistentRec.Header(),
		"response HEADERS must be identical — no extra/differing header (e.g. a distinguishing debug header) may leak which case this was")

	// Pin the content too, not just its equality to itself, so a future
	// handleError change that makes BOTH responses drift together (still
	// "equal to each other" but no longer the documented invalid-credentials
	// 401 apierror shape) still fails loudly here rather than passing this
	// test vacuously.
	assert.JSONEq(t, `{"code":401,"message":"invalid email or password"}`, inactiveRec.Body.String())
	assert.Equal(t, "application/json", inactiveRec.Header().Get("Content-Type"))
}
