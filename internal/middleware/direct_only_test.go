package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runInternalRoute drives DirectOnly -> SpawnAuth -> handler in the order
// cmd/api/main.go wires them, so the tests see what a caller sees.
func runInternalRoute(t *testing.T, headers map[string]string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	e := echo.New()
	reached := false
	e.POST("/internal/secrets/materialize", func(c echo.Context) error {
		reached = true
		return c.NoContent(http.StatusOK)
	}, DirectOnly(), SpawnAuth())

	req := httptest.NewRequest(http.MethodPost, "/internal/secrets/materialize", http.NoBody)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec, reached
}

// Positive control: a direct call with the right token gets through, so a
// 404 below is the guard and not a broken harness.
func TestDirectOnly_DirectCallWithTokenPasses(t *testing.T) {
	withSpawnToken(t, "spawn-test-token")
	rec, reached := runInternalRoute(t, map[string]string{SpawnTokenHeader: "spawn-test-token"})
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, reached)
}

func TestDirectOnly_ProxiedCallIsNotFoundEvenWithValidToken(t *testing.T) {
	withSpawnToken(t, "spawn-test-token")
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded", "X-Real-IP", "x-forwarded-for"} {
		t.Run(h, func(t *testing.T) {
			rec, reached := runInternalRoute(t, map[string]string{
				SpawnTokenHeader: "spawn-test-token",
				h:                "203.0.113.7",
			})
			assert.Equal(t, http.StatusNotFound, rec.Code)
			assert.False(t, reached, "handler must not run for a proxied request")
		})
	}
}

// The guard answers before auth, so going through the edge does not reveal
// whether a token was right (401 vs 404 would be an oracle).
func TestDirectOnly_ProxiedCallWithBadTokenIsAlsoNotFound(t *testing.T) {
	withSpawnToken(t, "spawn-test-token")
	rec, _ := runInternalRoute(t, map[string]string{SpawnTokenHeader: "wrong", "X-Forwarded-For": "203.0.113.7"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	require.NotContains(t, rec.Body.String(), "spawn token")
}

// An EMPTY forwarding header is still a proxy hop — presence, not content.
func TestDirectOnly_EmptyForwardingHeaderStillRefused(t *testing.T) {
	withSpawnToken(t, "spawn-test-token")
	e := echo.New()
	e.POST("/x", func(c echo.Context) error { return c.NoContent(http.StatusOK) }, DirectOnly(), SpawnAuth())
	req := httptest.NewRequest(http.MethodPost, "/x", http.NoBody)
	req.Header.Set(SpawnTokenHeader, "spawn-test-token")
	req.Header["X-Forwarded-For"] = []string{""}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
