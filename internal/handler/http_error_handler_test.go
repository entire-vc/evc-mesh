package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
func TestNewHTTPErrorHandler_UnclassifiedErrorIsInternalErrorWithCode(t *testing.T) {
	e := echo.New()
	called := false
	h := NewHTTPErrorHandler(func(error, echo.Context) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	var logged bytes.Buffer
	e.Logger.SetOutput(&logged)

	cause := errors.New("db password is hunter2")
	h(fmt.Errorf("load: %w", cause), c)

	// The cause is logged by class only: error text can carry credentials (a
	// DSN, a value from a database error), and even a short digest of it can be
	// brute-forced offline from a leaked log, so neither is written.
	full := sha256.Sum256([]byte(fmt.Errorf("load: %w", cause).Error()))
	secret := sha256.Sum256([]byte("hunter2"))
	assert.NotContains(t, logged.String(), "hunter2", "error text must not reach the server log")
	assert.NotContains(t, logged.String(), hex.EncodeToString(full[:])[:8], "no digest of the error text in the log")
	assert.NotContains(t, logged.String(), hex.EncodeToString(secret[:])[:8], "no digest of the secret in the log")
	assert.NotContains(t, logged.String(), "fingerprint", "no text-derived field in the log line")
	assert.Contains(t, logged.String(), "*fmt.wrapError -> *errors.errorString", "the error type chain is logged")
	assert.Contains(t, logged.String(), "GET", "method and path stay in the log line")

	assert.False(t, called, "an unclassified error is answered here, not by the fallback")
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, apierror.CodeInternalError, body["error_code"])
	assert.NotContains(t, rec.Body.String(), "hunter2", "the error's own text must not reach the client")
}

// The path in the 500 log line is the matched route template, not the raw URL:
// public routes such as /api/v1/invites/:token carry a bearer credential in
// the path segment, and a raw path would write it to the log.
func TestNewHTTPErrorHandler_UnclassifiedErrorLogsRouteNotRawPath(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)
	var logged bytes.Buffer
	e.Logger.SetOutput(&logged)
	e.GET("/api/v1/invites/:token", func(echo.Context) error { return errors.New("boom") })

	const token = "inv_s3cr3t_token_value"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/invites/"+token+"?x=1", http.NoBody))

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, logged.String(), token, "the invite token must not reach the server log")
	assert.Contains(t, logged.String(), "/api/v1/invites/:token", "the route template identifies the endpoint")
}

// A non-error HTTPError (a 3xx written through the error path) is not ours to
// reshape: it still goes to Echo's own handler.
func TestNewHTTPErrorHandler_NonErrorStatusStillFallsBack(t *testing.T) {
	e := echo.New()
	var calledWith error
	h := NewHTTPErrorHandler(func(err error, _ echo.Context) { calledWith = err })
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)

	redirect := echo.NewHTTPError(http.StatusFound, "moved")
	h(redirect, c)

	assert.Same(t, redirect, calledWith)
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

// Echo's own errors (no route, wrong method) used to answer a bare
// {"message": ...}; they now carry the generic error_code of their status.
func TestNewHTTPErrorHandler_EchoHTTPErrorsCarryErrorCode(t *testing.T) {
	cases := []struct {
		err  *echo.HTTPError
		code string
	}{
		{echo.ErrNotFound, apierror.CodeNotFound},
		{echo.ErrUnauthorized, apierror.CodeUnauthorized},
		{echo.NewHTTPError(http.StatusMethodNotAllowed, "Method Not Allowed"), apierror.CodeInternalError},
	}
	for _, tc := range cases {
		e := echo.New()
		h := NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
		h(tc.err, c)

		assert.Equal(t, tc.err.Code, rec.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, tc.code, body["error_code"])
		assert.NotEmpty(t, body["message"])
	}
}

// The logged route is quoted as defence in depth: the matched route template is
// not attacker-controlled, but a newline in it must still never reach the
// message raw, whatever the logger's output format.
func TestNewHTTPErrorHandler_UnclassifiedErrorLogQuotesRequestPath(t *testing.T) {
	e := echo.New()
	h := NewHTTPErrorHandler(func(error, echo.Context) {})
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	c := e.NewContext(req, httptest.NewRecorder())
	c.SetPath("/a\nFORGED LINE")

	var logged bytes.Buffer
	e.Logger.SetOutput(&logged)
	h(errors.New("boom"), c)

	var entry struct {
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(logged.Bytes(), &entry))
	assert.NotContains(t, entry.Message, "\n", "a decoded newline must not reach the message raw")
	assert.Contains(t, entry.Message, `GET "/a\nFORGED LINE"`, "the path is logged as a quoted string")
	assert.Equal(t, 1, bytes.Count(logged.Bytes(), []byte("\n")), "one log entry")
}
