//go:build integration

package integration

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPublicEndpoints_NoAuthRequired is a regression test for the 2026-05-20 incident
// where the monitoring script queried /api/v1/version (wrong path — that path falls
// under the /api/v1 group which has DualAuth middleware, returning 401 for any
// unauthenticated request to an unregistered path).
//
// These endpoints must return non-401 without any Authorization header.
func TestPublicEndpoints_NoAuthRequired(t *testing.T) {
	env := NewTestEnv(t)
	defer env.Cleanup(t)

	publicEndpoints := []struct {
		method string
		path   string
		// expectedStatus is the status code we expect for a public endpoint called
		// without credentials. 200 for health/version, 400 or 401-from-wrong-creds
		// for login (we use empty body so we expect 400 bad request).
		expectedNot401 bool
	}{
		{"GET", "/health", true},
		{"GET", "/api/version", true},
		{"GET", "/api/v1/healthz/version", true},
		// login without body → 400 bad request (not 401 auth required)
		{"POST", "/api/v1/auth/login", true},
		// register without body → 400 bad request (not 401 auth required)
		{"POST", "/api/v1/auth/register", true},
	}

	for _, ep := range publicEndpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			req, err := http.NewRequest(ep.method, env.BaseURL+ep.path, nil)
			assert.NoError(t, err)
			// Deliberately no Authorization header.

			resp, err := env.HTTPClient.Do(req)
			assert.NoError(t, err)
			if err != nil {
				return
			}
			defer resp.Body.Close()

			if ep.expectedNot401 {
				assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode,
					"public endpoint %s %s must not return 401 without auth; got %d",
					ep.method, ep.path, resp.StatusCode)
			}
		})
	}
}

// TestEchoGroupMiddlewareBehavior documents the behavior of Echo's group middleware:
// any unauthenticated request to an UNREGISTERED path under /api/v1/ goes through
// DualAuth and returns 401. This is expected — health checks must use /api/version
// or /api/v1/healthz/version, not /api/v1/version (which has no handler).
func TestEchoGroupMiddlewareBehavior(t *testing.T) {
	env := NewTestEnv(t)
	defer env.Cleanup(t)

	// /api/v1/version does not exist as a route. Echo applies the api group's
	// DualAuth middleware and returns 401. Use /api/version or
	// /api/v1/healthz/version instead.
	req, err := http.NewRequest("GET", env.BaseURL+"/api/v1/version", nil)
	assert.NoError(t, err)

	resp, err := env.HTTPClient.Do(req)
	assert.NoError(t, err)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	// Document that this returns 401 (not 404) due to DualAuth middleware.
	// This is a known limitation, not a bug. Use the correct endpoint paths above.
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"/api/v1/version (unregistered under api group) must return 401 — use /api/version instead")
}
