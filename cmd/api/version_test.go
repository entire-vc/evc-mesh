package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
)

func TestVersionHandler_ReportsExtensionsAPI(t *testing.T) {
	e := echo.New()
	h := newVersionHandler(nil)
	for _, path := range []string{"/api/version", "/api/v1/version", "/api/v1/healthz/version"} {
		e.GET(path, h)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, http.NoBody))
		require.Equal(t, http.StatusOK, rec.Code, path)

		var body map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), path)
		v, ok := body["extensions_api"]
		require.True(t, ok, "%s: extensions_api field missing", path)
		assert.EqualValues(t, extensions.ExtensionsAPIVersion, v, path)
		assert.EqualValues(t, 1, v, "%s: API version is 1 until a breaking change is recorded in an ADR", path)
	}
}

func TestVersionHandler_SparkFollowsInstanceLevel(t *testing.T) {
	e := echo.New()
	e.GET("/v", newVersionHandler(func(context.Context) (string, bool) { return "https://spark.example", true }))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v", http.NoBody))
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, true, body["spark_enabled"])
	assert.Equal(t, "https://spark.example", body["spark_url"])

	e.GET("/off", newVersionHandler(nil))
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/off", http.NoBody))
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, false, body["spark_enabled"])
	assert.Equal(t, "", body["spark_url"])
}
