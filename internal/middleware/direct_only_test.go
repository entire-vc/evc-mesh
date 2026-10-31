package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runInternalRoute drives DirectOnly -> auth -> handler in the order
// cmd/api/main.go wires them, so the tests see what a caller sees. auth is
// passed in: the passthrough isolates DirectOnly, SpawnerAuth(nil) shows the
// guard answers before auth (a request reaching a nil service would panic).
func runInternalRoute(t *testing.T, auth echo.MiddlewareFunc, headers map[string]string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	e := echo.New()
	reached := false
	e.POST("/internal/secrets/materialize", func(c echo.Context) error {
		reached = true
		return c.NoContent(http.StatusOK)
	}, DirectOnly(), auth)

	req := httptest.NewRequest(http.MethodPost, "/internal/secrets/materialize", http.NoBody)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec, reached
}

func passAuth(next echo.HandlerFunc) echo.HandlerFunc { return next }

// Positive control: a direct call gets through, so a 404 below is the guard
// and not a broken harness.
func TestDirectOnly_DirectCallPasses(t *testing.T) {
	rec, reached := runInternalRoute(t, passAuth, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, reached)
}

func TestDirectOnly_ProxiedCallIsNotFoundEvenWhenAuthPasses(t *testing.T) {
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded", "X-Real-IP", "x-forwarded-for"} {
		t.Run(h, func(t *testing.T) {
			rec, reached := runInternalRoute(t, passAuth, map[string]string{h: "203.0.113.7"})
			assert.Equal(t, http.StatusNotFound, rec.Code)
			assert.False(t, reached, "handler must not run for a proxied request")
		})
	}
}

// The guard answers before auth, so going through the edge does not reveal
// whether a key was right (401 vs 404 would be an oracle).
func TestDirectOnly_ProxiedCallWithBadKeyIsAlsoNotFound(t *testing.T) {
	rec, _ := runInternalRoute(t, SpawnerAuth(nil), map[string]string{"X-Agent-Key": "wrong", "X-Forwarded-For": "203.0.113.7"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	require.NotContains(t, rec.Body.String(), "agent key")
}

// An EMPTY forwarding header is still a proxy hop — presence, not content.
func TestDirectOnly_EmptyForwardingHeaderStillRefused(t *testing.T) {
	e := echo.New()
	e.POST("/x", func(c echo.Context) error { return c.NoContent(http.StatusOK) }, DirectOnly(), passAuth)
	req := httptest.NewRequest(http.MethodPost, "/x", http.NoBody)
	req.Header["X-Forwarded-For"] = []string{""}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
