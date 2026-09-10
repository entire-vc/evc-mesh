package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// TestNewHTTPErrorHandler_MapsApierrorToItsOwnStatusCode is the regression
// test for the defect a verifier found live: a handler returning
// apierror.ValidationError (400, with field detail) was rendered as a bare
// 500 by Echo's default handler, because that handler only recognizes
// *echo.HTTPError. GET /me/mentions with no workspace_id reproduced this
// exactly.
func TestNewHTTPErrorHandler_MapsApierrorToItsOwnStatusCode(t *testing.T) {
	e := echo.New()
	h := NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)

	req := httptest.NewRequest(http.MethodGet, "/me/mentions", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	h(apierror.ValidationError(map[string]string{"workspace_id": "required UUID"}), c)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var body apierror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "required UUID", body.Validation["workspace_id"])
}

// TestNewHTTPErrorHandler_EveryFactoryStatusSurvives guards every status this
// codebase's handlers actually return through apierror — a future factory
// (or a typo'd Code) should fail this test, not silently reintroduce a 500.
func TestNewHTTPErrorHandler_EveryFactoryStatusSurvives(t *testing.T) {
	cases := []*apierror.Error{
		apierror.BadRequest("bad"),
		apierror.ValidationError(map[string]string{"f": "required"}),
		apierror.Unauthorized(""),
		apierror.Forbidden(""),
		apierror.NotFound("Task"),
		apierror.Conflict("already exists"),
		apierror.TooManyRequests(""),
		apierror.ServiceUnavailable(""),
		apierror.InternalError(""),
	}

	e := echo.New()
	h := NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)

	for _, apiErr := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		h(apiErr, c)

		assert.Equal(t, apiErr.StatusCode(), rec.Code, apiErr.Message)
	}
}

// TestNewHTTPErrorHandler_FallsBackForNonApierrorTypes proves the fix is
// additive: an error this handler does not recognize (here, echo's own
// HTTPError) is left to the fallback exactly as before this handler existed.
func TestNewHTTPErrorHandler_FallsBackForNonApierrorTypes(t *testing.T) {
	e := echo.New()
	var calledWith error
	fallback := func(err error, _ echo.Context) { calledWith = err }
	h := NewHTTPErrorHandler(fallback)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	boom := errors.New("not an apierror")
	h(boom, c)

	assert.Same(t, boom, calledWith)
}

// TestNewHTTPErrorHandler_NoopsOnceTheResponseIsCommitted matches Echo's own
// DefaultHTTPErrorHandler contract: a handler that already wrote (and
// committed) a response must not get a second, conflicting write attempt.
func TestNewHTTPErrorHandler_NoopsOnceTheResponseIsCommitted(t *testing.T) {
	e := echo.New()
	h := NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, c.String(http.StatusOK, "already sent"))

	h(apierror.NotFound("Task"), c)

	assert.Equal(t, http.StatusOK, rec.Code, "the committed response must not be overwritten")
}
