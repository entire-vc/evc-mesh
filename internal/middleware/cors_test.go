package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// CORS middleware tests.
//
// Contract for Phase 5-OSS CORS configuration:
//   - Default (development): AllowOrigins = ["*"] — permits any origin.
//   - Production-ready:      AllowOrigins = configured allowlist only.
//   - Preflight OPTIONS requests return 204 No Content with CORS headers.
//   - Unknown origins in restricted mode receive no Access-Control-Allow-Origin header.
//   - Listed origins in restricted mode receive the correct CORS headers.
//
// The production implementation should expose a CORSConfig struct and a
// NewCORSMiddleware(cfg CORSConfig) echo.MiddlewareFunc factory.
// Until then, tests use Echo's built-in echomw.CORS() to validate the contract.
// ---------------------------------------------------------------------------

// corsConfig documents the expected Phase 5-OSS CORS configuration type.
// The real implementation will live in internal/middleware/cors.go.
type corsConfig struct {
	// AllowOrigins is the list of allowed origins.
	// ["*"] means all origins are allowed (development default).
	AllowOrigins []string
	// AllowMethods are the HTTP methods allowed cross-origin.
	AllowMethods []string
	// AllowHeaders are request headers allowed cross-origin.
	AllowHeaders []string
}

// buildCORSMiddleware creates an Echo CORS middleware from corsConfig.
// This is a bridge to the Echo built-in — the real implementation will
// wrap this with application-level defaults.
func buildCORSMiddleware(cfg corsConfig) echo.MiddlewareFunc {
	return echomw.CORSWithConfig(echomw.CORSConfig{
		AllowOrigins: cfg.AllowOrigins,
		AllowMethods: cfg.AllowMethods,
		AllowHeaders: cfg.AllowHeaders,
	})
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestCORS_DefaultAllowsAll(t *testing.T) {
	// With AllowOrigins = ["*"], any origin should receive the header.
	e := echo.New()
	mw := buildCORSMiddleware(corsConfig{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch},
		AllowHeaders: []string{"Content-Type", "Authorization", "X-Agent-Key"},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", http.NoBody)
	req.Header.Set("Origin", "https://totally-unknown-domain.example.com")
	rec := httptest.NewRecorder()

	handler := mw(func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	c := e.NewContext(req, rec)
	err := handler(c)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, rec.Code)
	// With wildcard, the ACAO header must be present.
	acao := rec.Header().Get("Access-Control-Allow-Origin")
	assert.NotEmpty(t, acao, "Access-Control-Allow-Origin should be set for wildcard config")
}

func TestCORS_AllowlistBlocksUnknownOrigin(t *testing.T) {
	// When a specific allowlist is configured, unknown origins must not receive
	// the Access-Control-Allow-Origin header (or receive an empty/mismatched value).
	e := echo.New()
	mw := buildCORSMiddleware(corsConfig{
		AllowOrigins: []string{"https://app.entire.vc", "https://staging.entire.vc"},
		AllowMethods: []string{http.MethodGet, http.MethodPost},
		AllowHeaders: []string{"Content-Type", "Authorization"},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", http.NoBody)
	req.Header.Set("Origin", "https://evil.attacker.com")
	rec := httptest.NewRecorder()

	handler := mw(func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	c := e.NewContext(req, rec)
	err := handler(c)
	require.NoError(t, err)

	// The ACAO header must NOT reflect the attacker's origin.
	acao := rec.Header().Get("Access-Control-Allow-Origin")
	assert.NotEqual(t, "https://evil.attacker.com", acao,
		"unknown origin must not be reflected in Access-Control-Allow-Origin")
}

func TestCORS_AllowlistPermitsListedOrigin(t *testing.T) {
	e := echo.New()
	allowedOrigin := "https://app.entire.vc"
	mw := buildCORSMiddleware(corsConfig{
		AllowOrigins: []string{allowedOrigin, "https://staging.entire.vc"},
		AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete},
		AllowHeaders: []string{"Content-Type", "Authorization", "X-Agent-Key"},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", http.NoBody)
	req.Header.Set("Origin", allowedOrigin)
	rec := httptest.NewRecorder()

	handler := mw(func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	c := e.NewContext(req, rec)
	err := handler(c)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, rec.Code)
	acao := rec.Header().Get("Access-Control-Allow-Origin")
	assert.Equal(t, allowedOrigin, acao,
		"listed origin must be reflected in Access-Control-Allow-Origin")
}

func TestCORS_PreflightOPTIONS_ReturnsNoContent(t *testing.T) {
	// Browsers send OPTIONS preflight before cross-origin requests with
	// non-simple methods or headers. The server must respond 204 with CORS headers.
	e := echo.New()
	mw := buildCORSMiddleware(corsConfig{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodOptions},
		AllowHeaders: []string{"Content-Type", "Authorization", "X-Agent-Key"},
	})

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/tasks", http.NoBody)
	req.Header.Set("Origin", "https://app.entire.vc")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
	rec := httptest.NewRecorder()

	// Echo's CORS middleware short-circuits OPTIONS preflight.
	handler := mw(func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	c := e.NewContext(req, rec)
	err := handler(c)
	require.NoError(t, err)

	// Preflight must respond 2xx (204 or 200).
	assert.True(t, rec.Code == http.StatusNoContent || rec.Code == http.StatusOK,
		"preflight OPTIONS must return 2xx, got %d", rec.Code)

	// Must include CORS headers in the preflight response.
	acam := rec.Header().Get("Access-Control-Allow-Methods")
	assert.NotEmpty(t, acam, "preflight must return Access-Control-Allow-Methods")

	acao := rec.Header().Get("Access-Control-Allow-Origin")
	assert.NotEmpty(t, acao, "preflight must return Access-Control-Allow-Origin")
}

func TestCORS_AgentKeyHeaderAllowed(t *testing.T) {
	// The X-Agent-Key header must be explicitly allowed so agents can make
	// cross-origin requests from browser-based clients.
	e := echo.New()
	mw := buildCORSMiddleware(corsConfig{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodOptions},
		AllowHeaders: []string{"Content-Type", "Authorization", "X-Agent-Key"},
	})

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/tasks", http.NoBody)
	req.Header.Set("Origin", "https://app.entire.vc")
	req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	req.Header.Set("Access-Control-Request-Headers", "X-Agent-Key")
	rec := httptest.NewRecorder()

	handler := mw(func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	c := e.NewContext(req, rec)
	err := handler(c)
	require.NoError(t, err)

	// Preflight must not be rejected (must be 2xx).
	assert.True(t, rec.Code < 400,
		"X-Agent-Key preflight must not be rejected, got %d", rec.Code)
}

func TestCORS_NoOriginHeader_PassesThrough(t *testing.T) {
	// Requests without Origin header (e.g. server-to-server, curl) must pass
	// without CORS headers in the response.
	e := echo.New()
	mw := buildCORSMiddleware(corsConfig{
		AllowOrigins: []string{"https://app.entire.vc"},
		AllowMethods: []string{http.MethodGet},
		AllowHeaders: []string{"Content-Type"},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", http.NoBody)
	// No Origin header — direct API call.
	rec := httptest.NewRecorder()

	var handlerCalled bool
	handler := mw(func(c echo.Context) error {
		handlerCalled = true
		return c.NoContent(http.StatusOK)
	})

	c := e.NewContext(req, rec)
	err := handler(c)
	require.NoError(t, err)

	assert.True(t, handlerCalled, "handler must be called for non-CORS requests")
	assert.Equal(t, http.StatusOK, rec.Code)
}
