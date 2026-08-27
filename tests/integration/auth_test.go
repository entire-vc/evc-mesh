//go:build integration

package integration

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthFlow_RegisterLoginLogout(t *testing.T) {
	env := NewTestEnv(t)
	defer env.Cleanup(t)

	email := uniqueEmail("auth-flow")
	password := "TestPass123"

	// --- Step 1: Register ---
	t.Run("Register", func(t *testing.T) {
		result := env.Register(t, email, password, "Auth Test User")

		user, ok := result["user"].(map[string]interface{})
		require.True(t, ok, "response must contain user")
		assert.Equal(t, email, user["email"])
		assert.Equal(t, "Auth Test User", user["name"])
		assert.NotEmpty(t, user["id"])

		// Password hash must NOT be in the response (json:"-" tag).
		_, hasPasswordHash := user["password_hash"]
		assert.False(t, hasPasswordHash, "password_hash must not be exposed in API response")

		tokens, ok := result["tokens"].(map[string]interface{})
		require.True(t, ok, "response must contain tokens")
		assert.NotEmpty(t, tokens["access_token"])
		assert.NotZero(t, tokens["expires_in"])
		_, hasRefreshInBody := tokens["refresh_token"]
		assert.False(t, hasRefreshInBody, "refresh_token must not be in the response body — it belongs only in the httpOnly cookie")
		assert.NotEmpty(t, env.RefreshCookieValue(t), "refresh token must have arrived as a cookie")
	})

	// --- Step 2: Duplicate registration should fail ---
	t.Run("DuplicateRegister", func(t *testing.T) {
		resp := env.Post(t, "/api/v1/auth/register", map[string]string{
			"email":    email,
			"password": password,
			"name":     "Duplicate",
		})
		assert.Equal(t, http.StatusConflict, resp.StatusCode)
		resp.Body.Close()
	})

	// --- Step 3: Login ---
	t.Run("Login", func(t *testing.T) {
		result := env.Login(t, email, password)

		tokens, ok := result["tokens"].(map[string]interface{})
		require.True(t, ok)
		assert.NotEmpty(t, tokens["access_token"])
		_, hasRefreshInBody := tokens["refresh_token"]
		assert.False(t, hasRefreshInBody, "refresh_token must not be in the response body")
		assert.NotEmpty(t, env.RefreshCookieValue(t))
	})

	// --- Step 4: /auth/me ---
	t.Run("Me", func(t *testing.T) {
		resp := env.Get(t, "/api/v1/auth/me")
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var user map[string]interface{}
		env.DecodeJSON(t, resp, &user)
		assert.Equal(t, email, user["email"])
		assert.Equal(t, "Auth Test User", user["name"])
	})

	// --- Step 5: Token refresh (refresh token rides the jar's cookie now,
	// not a body field) ---
	var preRefreshToken string
	t.Run("RefreshToken", func(t *testing.T) {
		preRefreshToken = env.RefreshCookieValue(t)

		resp := env.Post(t, "/api/v1/auth/refresh", nil)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var result map[string]interface{}
		env.DecodeJSON(t, resp, &result)
		tokens, ok := result["tokens"].(map[string]interface{})
		require.True(t, ok)
		assert.NotEmpty(t, tokens["access_token"])
		_, hasRefreshInBody := tokens["refresh_token"]
		assert.False(t, hasRefreshInBody)

		rotated := env.RefreshCookieValue(t)
		assert.NotEqual(t, preRefreshToken, rotated, "refresh must rotate the cookie, not reissue the same token")

		// Update auth token for subsequent requests.
		env.AuthToken = tokens["access_token"].(string)
	})

	// --- Step 6: Reuse old refresh token (theft detection) ---
	// The jar now holds the ROTATED cookie, so replay the pre-rotation value
	// explicitly via a raw request — this is exactly the scenario the task's
	// tab-race concern is about: an old, already-revoked token showing up.
	t.Run("RefreshTokenReuse", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, env.BaseURL+"/api/v1/auth/refresh", nil)
		require.NoError(t, err)
		req.AddCookie(&http.Cookie{Name: "refresh_token", Value: preRefreshToken}) // nosemgrep: go.lang.security.audit.net.cookie-missing-httponly.cookie-missing-httponly,go.lang.security.audit.net.cookie-missing-secure.cookie-missing-secure -- AddCookie on an http.Request only serializes Name=Value into the Cookie request header; Secure/HttpOnly are response-cookie attributes and meaningless here

		// A plain, jar-less client — env.HTTPClient's own jar already holds
		// the ROTATED cookie, and a Jar-backed Do() would silently append
		// its current cookie alongside the one we set here.
		resp, err := (&http.Client{}).Do(req)
		require.NoError(t, err)
		// Should be 401 because token was already rotated.
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		resp.Body.Close()
	})

	// --- Step 7: Logout ---
	t.Run("Logout", func(t *testing.T) {
		resp := env.Post(t, "/api/v1/auth/logout", nil)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()
	})

	// --- Step 8: Access token still works (short-lived, not blacklisted) ---
	t.Run("AccessTokenAfterLogout", func(t *testing.T) {
		resp := env.Get(t, "/api/v1/auth/me")
		// Access token is still valid until expiry (15 min).
		// Logout only revokes refresh tokens.
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()
	})
}

func TestAuthFlow_InvalidCredentials(t *testing.T) {
	env := NewTestEnv(t)
	defer env.Cleanup(t)

	// --- Wrong password ---
	t.Run("WrongPassword", func(t *testing.T) {
		email := uniqueEmail("wrong-pass")
		env.Register(t, email, "TestPass123", "Test User")

		resp := env.Post(t, "/api/v1/auth/login", map[string]string{
			"email":    email,
			"password": "WrongPassword123",
		})
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		resp.Body.Close()
	})

	// --- Nonexistent email ---
	t.Run("NonexistentEmail", func(t *testing.T) {
		resp := env.Post(t, "/api/v1/auth/login", map[string]string{
			"email":    "nonexistent@test.local",
			"password": "TestPass123",
		})
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		resp.Body.Close()
	})
}

func TestAuthFlow_PasswordValidation(t *testing.T) {
	env := NewTestEnv(t)
	defer env.Cleanup(t)

	tests := []struct {
		name     string
		password string
		wantCode int
	}{
		{"TooShort", "Ab1", http.StatusBadRequest},
		{"NoUppercase", "testpass123", http.StatusBadRequest},
		{"NoLowercase", "TESTPASS123", http.StatusBadRequest},
		{"NoDigit", "TestPassword", http.StatusBadRequest},
		{"Valid", "TestPass123", http.StatusCreated},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := env.Post(t, "/api/v1/auth/register", map[string]string{
				"email":    uniqueEmail("pw-" + tt.name),
				"password": tt.password,
				"name":     "Password Test",
			})
			assert.Equal(t, tt.wantCode, resp.StatusCode)
			resp.Body.Close()
		})
	}
}

func TestAuthFlow_UnauthenticatedAccess(t *testing.T) {
	env := NewTestEnv(t)
	defer env.Cleanup(t)

	// No auth token set -- requests to protected endpoints should fail.
	env.AuthToken = ""

	resp := env.Get(t, "/api/v1/auth/me")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()

	resp = env.Get(t, "/api/v1/workspaces")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()
}
