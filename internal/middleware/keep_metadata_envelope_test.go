package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The envelope is a pure response rewriter, so it is tested with the real Echo
// router and stand-in guards that only choose a status; the real guards behind
// it are covered over Postgres in internal/handler/keep_metadata_db_test.go.

func kmEnvelopeServer(guard echo.HandlerFunc) *echo.Echo {
	e := echo.New()
	g := e.Group("/api/v1")
	g.Use(KeepMetadataEnvelope())
	g.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if guard != nil {
				return guard(c)
			}
			return next(c)
		}
	})
	g.GET("/workspaces/:ws_id/secrets/:secret_id/keep-metadata", func(c echo.Context) error {
		return KeepMetadataSuccess(c, "5", 7)
	})
	g.GET("/other", func(c echo.Context) error { return c.JSON(http.StatusForbidden, map[string]string{"error": "generic"}) })
	return e
}

func kmDo(e *echo.Echo, path string) (rec *httptest.ResponseRecorder, body map[string]any) {
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, http.NoBody))
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec, body
}

func kmURL() string {
	return "/api/v1/workspaces/" + uuid.NewString() + "/secrets/" + uuid.NewString() + "/keep-metadata"
}

func TestKeepMetadataEnvelope_GuardAnswersBecomeTheClosedEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name        string
		guardStatus int
		code        int
		class, msg  string
	}{
		{"401", 401, 401, "unauthenticated", "authentication failed"},
		{"403", 403, 404, "not_found", "not found"},
		{"404", 404, 404, "not_found", "not found"},
		{"429", 429, 429, "rate_limited", "request rate limited"},
		{"413", 413, 400, "invalid_argument", "invalid metadata request"},
		{"500", 500, 503, "not_ready", "metadata unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := kmEnvelopeServer(func(c echo.Context) error {
				c.Response().Header().Set("Retry-After", "9")
				return c.JSON(tc.guardStatus, map[string]string{"error": "raw guard reason uuid " + uuid.NewString()})
			})
			rec, body := kmDo(e, kmURL())
			assert.Equal(t, tc.code, rec.Code)
			assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
			assert.Equal(t, map[string]any{"error_class": tc.class, "message": tc.msg, "request_id": body["request_id"]}, body)
			assert.NotContains(t, rec.Body.String(), "raw guard reason")
		})
	}
}

func TestKeepMetadataEnvelope_ReturnedHTTPErrorIsRewrittenToo(t *testing.T) {
	e := kmEnvelopeServer(func(echo.Context) error { return echo.NewHTTPError(http.StatusForbidden, "no") })
	rec, body := kmDo(e, kmURL())
	assert.Equal(t, 404, rec.Code)
	assert.Equal(t, "not_found", body["error_class"])
}

func TestKeepMetadataEnvelope_OwnSuccessPassesThroughOnce(t *testing.T) {
	rec, body := kmDo(kmEnvelopeServer(nil), kmURL())
	require.Equal(t, 200, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, map[string]any{"version": "5", "length": float64(7), "request_id": body["request_id"]}, body)
	assert.Equal(t, 1, countJSONObjects(rec.Body.String()), "exactly one body")
}

func countJSONObjects(s string) int {
	dec := json.NewDecoder(strings.NewReader(s))
	n := 0
	for dec.More() {
		var v any
		if dec.Decode(&v) != nil {
			break
		}
		n++
	}
	return n
}

func TestKeepMetadataEnvelope_ShapeIsJudgedBeforeTheGuards(t *testing.T) {
	guardRan := false
	e := kmEnvelopeServer(func(c echo.Context) error { guardRan = true; return c.NoContent(403) })
	for name, path := range map[string]string{
		"query":     kmURL() + "?x=1",
		"empty q":   kmURL() + "?",
		"upper":     "/api/v1/workspaces/" + uuid.NewString() + "/secrets/ABCDEFAB-0000-4000-8000-000000000000/keep-metadata",
		"not uuid":  "/api/v1/workspaces/" + uuid.NewString() + "/secrets/nope/keep-metadata",
		"ws broken": "/api/v1/workspaces/1/secrets/" + uuid.NewString() + "/keep-metadata",
	} {
		guardRan = false
		rec, body := kmDo(e, path)
		assert.Equal(t, 400, rec.Code, name)
		assert.Equal(t, "invalid_argument", body["error_class"], name)
		assert.False(t, guardRan, name+": a guard ran before the shape check")
	}
}

func TestKeepMetadataEnvelope_OtherRoutesAreUntouched(t *testing.T) {
	rec, body := kmDo(kmEnvelopeServer(nil), "/api/v1/other")
	assert.Equal(t, 403, rec.Code)
	assert.Equal(t, map[string]any{"error": "generic"}, body)
	assert.Empty(t, rec.Header().Get("Cache-Control"))
}
