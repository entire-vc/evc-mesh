package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// A user or agent token never reaches the decision: the endpoint answers only
// an extension actor, before it reads the body or the database.
func TestExtAuthorize_NonExtensionCallerIs403(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ext/me/authorize", strings.NewReader(`{"user_id":"x"}`))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, NewExtensionAuthorizeHandler(nil, nil, nil).Authorize(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// A strict body is exactly one JSON object: a second value or trailing garbage
// after a valid request is refused before any lookup (the nil DB would panic).
func TestExtAuthorize_TrailingDataIs400(t *testing.T) {
	valid := `{"user_id":"` + uuid.NewString() + `","action":"read"}`
	for _, body := range []string{valid + ` {}`, valid + `garbage`, valid + `{"action":"manage"}`} {
		e := echo.New()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ext/me/authorize", strings.NewReader(body))
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.Set(middleware.ContextKeyExtensionPrincipal, &service.ExtensionPrincipal{WorkspaceID: uuid.New()})

		require.NoError(t, NewExtensionAuthorizeHandler(nil, nil, nil).Authorize(c), body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, body)
	}
}
