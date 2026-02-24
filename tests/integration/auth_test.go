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
		assert.NotEmpty(t, tokens["refresh_token"])
		assert.NotZero(t, tokens["expires_in"])
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
	var refreshToken string
	t.Run("Login", func(t *testing.T) {
		result := env.Login(t, email, password)

		tokens, ok := result["tokens"].(map[string]interface{})
		require.True(t, ok)
		assert.NotEmpty(t, tokens["access_token"])
		refreshToken = tokens["refresh_token"].(string)
		assert.NotEmpty(t, refreshToken)
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

	// --- Step 5: Token refresh ---
	t.Run("RefreshToken", func(t *testing.T) {
		resp := env.Post(t, "/api/v1/auth/refresh", map[string]string{
			"refresh_token": refreshToken,
		})
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var result map[string]interface{}
		env.DecodeJSON(t, resp, &result)
		tokens, ok := result["tokens"].(map[string]interface{})
		require.True(t, ok)
		assert.NotEmpty(t, tokens["access_token"])
		assert.NotEmpty(t, tokens["refresh_token"])

		// Update auth token for subsequent requests.
		env.AuthToken = tokens["access_token"].(string)
	})

	// --- Step 6: Reuse old refresh token (theft detection) ---
	t.Run("RefreshTokenReuse", func(t *testing.T) {
		resp := env.Post(t, "/api/v1/auth/refresh", map[string]string{
			"refresh_token": refreshToken,
		})
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
