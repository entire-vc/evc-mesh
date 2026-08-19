package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

// TestDocumentByPathWildcardDoesNotSwallowSiblingRoutes pins the one thing the
// by-path route could plausibly break.
//
// /projects/:proj_id/documents/by-path/* is the only trailing wildcard under the
// documents collection, and a wildcard that matched too eagerly would quietly
// take over its siblings — /documents/search most of all, which would then answer
// "no such document at path search" instead of searching. Echo resolves static
// segments before wildcards, so it does not; that is a property of the router,
// which is exactly the kind of thing worth asserting rather than assuming.
//
// It builds the routes rather than reading them from the real router, because
// what is being tested is the shape of the patterns, and constructing the real
// one needs a database.
func TestDocumentByPathWildcardDoesNotSwallowSiblingRoutes(t *testing.T) {
	e := echo.New()

	handled := func(name string) echo.HandlerFunc {
		return func(c echo.Context) error { return c.String(http.StatusOK, name) }
	}

	e.GET("/api/v1/projects/:proj_id/documents", handled("list"))
	e.GET("/api/v1/projects/:proj_id/documents/search", handled("search"))
	e.GET("/api/v1/projects/:proj_id/documents/by-path/*", handled("by-path"))

	cases := []struct {
		url  string
		want string
	}{
		{"/api/v1/projects/p1/documents", "list"},
		{"/api/v1/projects/p1/documents/search?q=deploy", "search"},
		{"/api/v1/projects/p1/documents/by-path/adr-004", "by-path"},
		{"/api/v1/projects/p1/documents/by-path/architecture/adr/adr-004", "by-path"},
	}

	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.url, http.NoBody)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code, tc.url)
		assert.Equal(t, tc.want, rec.Body.String(), "%s went to the wrong handler", tc.url)
	}
}

// And the wildcard reaches the handler whole, slashes included — a path that
// arrived as only its first segment would resolve to the wrong document rather
// than fail.
func TestDocumentByPathWildcardCarriesEverySegment(t *testing.T) {
	e := echo.New()
	var got string
	e.GET("/api/v1/projects/:proj_id/documents/by-path/*", func(c echo.Context) error {
		got = c.Param("*")
		return c.NoContent(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/projects/p1/documents/by-path/architecture/adr/adr-004", http.NoBody)
	e.ServeHTTP(httptest.NewRecorder(), req)

	assert.Equal(t, "architecture/adr/adr-004", got)
}
