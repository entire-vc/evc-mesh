package middleware

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

// The shared spawn token this route accepted before an internal ticket, named here
// only to prove it no longer opens anything.
const (
	legacySpawnTokenEnv    = "MESH_SPAWN_TOKEN"
	legacySpawnTokenHeader = "X-Spawn-Token"
)

// runSpawnerAuth drives SpawnerAuth with no agent service: every case here is
// refused before a key would be looked up, so a nil service is never touched
// (a request that did reach it would panic and fail the test).
func runSpawnerAuth(t *testing.T, headers map[string]string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/internal/secrets/materialize", http.NoBody)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	reached := false
	_ = SpawnerAuth(nil)(func(c echo.Context) error {
		reached = true
		return c.NoContent(http.StatusOK)
	})(c)
	return rec, reached
}

// The old token, sent with exactly the value the old env var holds, is
// refused: there is no token path left to match it against.
func TestSpawnerAuth_LegacySpawnTokenRefused(t *testing.T) {
	t.Setenv(legacySpawnTokenEnv, "the-real-token")
	t.Setenv(EnvSpawnerAgentIDs, "2b7f0a4e-6c1d-4f43-9e55-0d3b8a1c7e90")
	for _, v := range []string{"the-real-token", "anything-else", ""} {
		rec, reached := runSpawnerAuth(t, map[string]string{legacySpawnTokenHeader: v})
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "token %q", v)
		assert.False(t, reached, "token %q must not reach the handler", v)
	}
}

func TestSpawnerAuth_LegacySpawnTokenRefusedWithEmptyEnv(t *testing.T) {
	t.Setenv(legacySpawnTokenEnv, "")
	rec, reached := runSpawnerAuth(t, map[string]string{legacySpawnTokenHeader: ""})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, reached)
}

func TestSpawnerAuth_MalformedKeyRejected(t *testing.T) {
	rec, reached := runSpawnerAuth(t, map[string]string{"X-Agent-Key": "not-an-agent-key"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, reached)
}

// captureLog returns what fn wrote through the standard logger.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	fn()
	return buf.String()
}

func TestLogSpawnerAllowlist_EmptySaysEndpointRefusesAll(t *testing.T) {
	t.Setenv(EnvSpawnerAgentIDs, "")
	out := captureLog(t, LogSpawnerAllowlist)
	assert.Contains(t, out, EnvSpawnerAgentIDs+" not set")
	assert.Contains(t, out, "refuse every request")
}

func TestLogSpawnerAllowlist_CountsValidAgents(t *testing.T) {
	// The garbage entry is skipped, so the count is the two valid ids.
	t.Setenv(EnvSpawnerAgentIDs, "2b7f0a4e-6c1d-4f43-9e55-0d3b8a1c7e90, not-a-uuid ,7d0c1e2f-3a4b-4c5d-8e6f-9a0b1c2d3e4f")
	out := captureLog(t, LogSpawnerAllowlist)
	assert.Contains(t, out, EnvSpawnerAgentIDs+" lists 2 agent(s)")
	assert.Contains(t, out, "agent-key path active")
	assert.NotContains(t, out, "refuse every request")
}
