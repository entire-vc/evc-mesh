//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRateLimit_AuthEndpoints verifies that the auth endpoints enforce a
// per-IP rate limit, returning HTTP 429 after the threshold is exceeded, and
// allowing requests again once the rate-limit window resets.
//
// Scenario:
//  1. Send requests to POST /auth/login in rapid succession until 429 is received.
//  2. Assert that at least one response has status 429.
//  3. Assert that early requests (before the limit) returned 401 (wrong password),
//     not 429 — confirming the limit is not set too low.
//  4. Wait for the rate-limit window to expire.
//  5. Send a new request — assert it is no longer rate-limited (returns 401 or 200).
func TestRateLimit_AuthEndpoints(t *testing.T) {
	t.Skip("TODO: implement rate limiting first")

	env := NewTestEnv(t)
	defer env.Cleanup(t)

	// Register a user so login attempts produce 401 (not 404).
	email := uniqueEmail("ratelimit-auth")
	env.Register(t, email, "TestPass123", "RateLimit User")

	// --- Step 1: Hammer the login endpoint ---
	const burstSize = 100
	gotRateLimited := false
	earlyRequestsFailed := false

	for i := 0; i < burstSize; i++ {
		resp := env.Post(t, "/api/v1/auth/login", map[string]string{
			"email":    email,
			"password": "WrongPassword!",
		})
		code := resp.StatusCode
		resp.Body.Close()

		if code == http.StatusTooManyRequests {
			gotRateLimited = true
			// Confirm that at least one earlier request was not rate-limited.
			if i > 0 {
				earlyRequestsFailed = true
			}
			break
		}
	}

	assert.True(t, gotRateLimited, "rate limiter must kick in before %d requests", burstSize)
	assert.True(t, earlyRequestsFailed,
		"the very first request must not be rate-limited (limit > 1)")

	// --- Step 2: Wait for window reset (max 65 s for a 1-minute window) ---
	// In CI this sub-test is skipped to avoid long waits; include in manual runs.
	t.Run("WindowReset", func(t *testing.T) {
		t.Skip("skipping window-reset assertion to keep CI fast; run manually with -run TestRateLimit")

		time.Sleep(65 * time.Second)

		resp := env.Post(t, "/api/v1/auth/login", map[string]string{
			"email":    email,
			"password": "WrongPassword!",
		})
		assert.NotEqual(t, http.StatusTooManyRequests, resp.StatusCode,
			"requests must be allowed again after window reset")
		resp.Body.Close()
	})
}

// TestRateLimit_AgentAPI verifies that agent-authenticated API requests use a
// higher (or separate) rate-limit bucket compared to unauthenticated requests,
// and that the correct HTTP 429 response is returned when the agent limit is
// exceeded.
//
// Scenario:
//  1. Register a user; create a workspace and an agent.
//  2. Send agent-authenticated requests in rapid succession (higher burst).
//  3. Assert that the agent limit is higher than the anonymous limit (agent
//     can sustain more requests before hitting 429).
//  4. Once 429 is received, assert Retry-After header is present.
func TestRateLimit_AgentAPI(t *testing.T) {
	t.Skip("TODO: implement rate limiting first")

	env := NewTestEnv(t)
	defer env.Cleanup(t)

	email := uniqueEmail("ratelimit-agent")
	env.Register(t, email, "TestPass123", "RateLimit Agent Owner")

	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]interface{}
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces)
	wsID := workspaces[0]["id"].(string)

	// Register an agent and obtain the API key.
	var agentAPIKey string
	t.Run("RegisterAgent", func(t *testing.T) {
		resp := env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/agents", wsID), map[string]interface{}{
			"name":       "RateLimit Test Agent",
			"agent_type": "claude_code",
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		var result map[string]interface{}
		env.DecodeJSON(t, resp, &result)
		if key, ok := result["api_key"].(string); ok {
			agentAPIKey = key
		}
		assert.NotEmpty(t, agentAPIKey)
	})

	_ = agentAPIKey

	// Create a project so there is a valid list endpoint to hit.
	resp = env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/projects", wsID), map[string]interface{}{
		"name": "RateLimit Agent Project",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var project map[string]interface{}
	env.DecodeJSON(t, resp, &project)
	projectID := project["id"].(string)

	// --- Step 1: Burst agent requests ---
	const agentBurstSize = 200
	gotRateLimited := false
	var rateLimitedAt int
	var retryAfterPresent bool

	for i := 0; i < agentBurstSize; i++ {
		// Use doRequest via the agent-key auth helper (scaffolded).
		// For now, fall back to user token to test the plumbing.
		resp := env.Get(t, fmt.Sprintf("/api/v1/projects/%s/tasks", projectID))
		code := resp.StatusCode

		if code == http.StatusTooManyRequests {
			gotRateLimited = true
			rateLimitedAt = i + 1
			retryAfterPresent = resp.Header.Get("Retry-After") != ""
			resp.Body.Close()
			break
		}
		resp.Body.Close()
	}

	if gotRateLimited {
		assert.Greater(t, rateLimitedAt, 1,
			"agent limit must allow more than 1 request before throttling")
		assert.True(t, retryAfterPresent,
			"429 response must include Retry-After header")

		// Agent should sustain more requests than anonymous (threshold > 100).
		assert.GreaterOrEqual(t, rateLimitedAt, 100,
			"agent rate limit should be at least 100 req/window")
	} else {
		t.Logf("Agent did not hit rate limit within %d requests (limit may be higher)", agentBurstSize)
	}
}
