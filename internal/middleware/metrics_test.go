package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

func newMetricsTestContext(t *testing.T, authHeader string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	e := echo.New()
	return e.NewContext(req, rec), rec
}

// An empty token means the gate is a historical no-op — deployments that
// don't set MESH_METRICS_TOKEN (e.g. internal prod, fronted by Caddy) must
// see no behavior change.
func TestMetricsAuth_EmptyTokenIsNoOp(t *testing.T) {
	called := false
	next := func(c echo.Context) error {
		called = true
		return c.NoContent(http.StatusOK)
	}

	c, rec := newMetricsTestContext(t, "")
	err := MetricsAuth("")(next)(c)

	assert.NoError(t, err)
	assert.True(t, called, "next handler must run when no token is configured")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMetricsAuth_MissingHeaderRejected(t *testing.T) {
	called := false
	next := func(c echo.Context) error {
		called = true
		return c.NoContent(http.StatusOK)
	}

	c, rec := newMetricsTestContext(t, "")
	err := MetricsAuth("s3cr3t")(next)(c)

	assert.NoError(t, err)
	assert.False(t, called, "next handler must not run without a token")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestMetricsAuth_WrongTokenRejected(t *testing.T) {
	called := false
	next := func(c echo.Context) error {
		called = true
		return c.NoContent(http.StatusOK)
	}

	c, rec := newMetricsTestContext(t, "Bearer wrong-token")
	err := MetricsAuth("s3cr3t")(next)(c)

	assert.NoError(t, err)
	assert.False(t, called)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// Not "Bearer <token>" at all — a bare token, wrong scheme, etc. — must be
// rejected the same way as a wrong token, not panic or fall through.
func TestMetricsAuth_MalformedHeaderRejected(t *testing.T) {
	c, rec := newMetricsTestContext(t, "s3cr3t")
	err := MetricsAuth("s3cr3t")(func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})(c)

	assert.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestMetricsAuth_CorrectTokenAllowed(t *testing.T) {
	called := false
	next := func(c echo.Context) error {
		called = true
		return c.NoContent(http.StatusOK)
	}

	c, rec := newMetricsTestContext(t, "Bearer s3cr3t")
	err := MetricsAuth("s3cr3t")(next)(c)

	assert.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// MetricsAuthHTTP carries the same contract as MetricsAuth for plain
// net/http servers (e.g. cmd/mcp's mux, which doesn't run Echo). MetricsAuth
// wraps this — these cases pin the one implementation both stacks share.

func newMetricsHTTPRequest(authHeader string) (*http.Request, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	return req, httptest.NewRecorder()
}

func TestMetricsAuthHTTP_EmptyTokenIsNoOp(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req, rec := newMetricsHTTPRequest("")
	MetricsAuthHTTP("", next).ServeHTTP(rec, req)

	assert.True(t, called, "next handler must run when no token is configured")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMetricsAuthHTTP_MissingHeaderRejected(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	req, rec := newMetricsHTTPRequest("")
	MetricsAuthHTTP("s3cr3t", next).ServeHTTP(rec, req)

	assert.False(t, called, "next handler must not run without a token")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestMetricsAuthHTTP_WrongTokenRejected(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	req, rec := newMetricsHTTPRequest("Bearer wrong-token")
	MetricsAuthHTTP("s3cr3t", next).ServeHTTP(rec, req)

	assert.False(t, called)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// Not "Bearer <token>" at all — a bare token, wrong scheme, etc. — must be
// rejected the same way as a wrong token, not panic or fall through.
func TestMetricsAuthHTTP_MalformedHeaderRejected(t *testing.T) {
	req, rec := newMetricsHTTPRequest("s3cr3t")
	MetricsAuthHTTP("s3cr3t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestMetricsAuthHTTP_CorrectTokenAllowed(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req, rec := newMetricsHTTPRequest("Bearer s3cr3t")
	MetricsAuthHTTP("s3cr3t", next).ServeHTTP(rec, req)

	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rec.Code)
}
