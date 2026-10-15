package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions/exttest"
	"github.com/entire-vc/evc-mesh/internal/handler"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// TestWorkspaceExtensionRoutes_Contract calls every workspace extension route
// through a router built from main.go's own registrations (paths and guards are
// read from main.go) and checks the statuses and bodies of docs/extensions/api.md.
func TestWorkspaceExtensionRoutes_Contract(t *testing.T) {
	fx, err := exttest.NewFixture(exttest.InstanceOn{}, false)
	require.NoError(t, err)
	h := handler.NewExtensionWorkspaceHandler(service.NewExtensionWorkspaceService(fx.Registry, fx.Integ, fx.Consents))
	handlers := map[string]echo.HandlerFunc{
		"GET /workspaces/:ws_id/extensions":                    h.List,
		"POST /workspaces/:ws_id/extensions/:ext_id/connect":   h.Connect,
		"PATCH /workspaces/:ws_id/extensions/:ext_id/settings": h.PatchSettings,
		"POST /workspaces/:ws_id/extensions/:ext_id/enable":    h.Enable,
		"POST /workspaces/:ws_id/extensions/:ext_id/disable":   h.Disable,
		"POST /workspaces/:ws_id/extensions/:ext_id/test":      h.Test,
		"DELETE /workspaces/:ws_id/extensions/:ext_id":         h.Disconnect,
	}
	registered := map[string]auditRoute{}
	for _, r := range parseAuditRoutes(t) {
		registered[r.key()] = r
	}

	e := echo.New()
	for key, fn := range handlers {
		r, ok := registered[key]
		require.True(t, ok, "route %q is not registered in main.go", key)
		var mws []echo.MiddlewareFunc
		if r.method != http.MethodGet {
			require.Equal(t, "rbac", r.guard, "%s must carry rbac()", key)
			mws = append(mws, func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error {
					c.Response().Header().Set("X-Guarded", "1")
					return next(c)
				}
			})
		}
		e.Add(r.method, r.path, fn, mws...)
	}

	ws := uuid.New().String()
	base := "/workspaces/" + ws + "/extensions"
	call := func(method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec, out
	}
	status := func(m map[string]any) map[string]any { return m["status"].(map[string]any) }
	const consent = `{"settings":{"base_url":"https://x","token":"s3cret"},"consent":{"capabilities":["api:tasks.read","events:task.updated"]}}`

	rec, body := call(http.MethodGet, base, "")
	require.Equal(t, http.StatusOK, rec.Code)
	item := body["extensions"].([]any)[0].(map[string]any)
	assert.Equal(t, "demo", item["id"])
	assert.Equal(t, "instance", status(item)["source"])
	assert.Nil(t, item["connection"])

	rec, body = call(http.MethodPost, base+"/demo/disable", "")
	require.Equal(t, http.StatusConflict, rec.Code, "instance-on needs confirm")
	assert.Equal(t, "confirmation_required", body["error_code"])
	assert.Contains(t, fmt.Sprint(body["message"]), "send confirm=true to proceed", "the 409 says what to do")
	assert.Equal(t, "1", rec.Header().Get("X-Guarded"))

	rec, body = call(http.MethodPost, base+"/demo/connect", `{"settings":{},"consent":{"capabilities":[]}}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "validation_failed", body["error_code"])

	rec, body = call(http.MethodPost, base+"/demo/connect", consent)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "workspace", status(body)["source"])
	assert.Equal(t, "***", body["connection"].(map[string]any)["settings"].(map[string]any)["token"])
	assert.NotContains(t, rec.Body.String(), "s3cret")

	rec, body = call(http.MethodPost, base+"/demo/connect", consent)
	require.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "already_connected", body["error_code"])

	rec, body = call(http.MethodPatch, base+"/demo/settings", `{"settings":{"base_url":"https://y","token":"***"}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "https://y", body["connection"].(map[string]any)["settings"].(map[string]any)["base_url"])
	rec, _ = call(http.MethodPatch, base+"/demo/settings", `{}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec, body = call(http.MethodPost, base+"/demo/disable", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, false, status(body)["enabled"])
	rec, body = call(http.MethodPost, base+"/demo/enable", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, true, status(body)["enabled"])

	rec, body = call(http.MethodPost, base+"/demo/test", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, true, body["ok"])
	assert.Equal(t, "1", rec.Header().Get("X-Guarded"))

	rec, _ = call(http.MethodDelete, base+"/demo?data=bogus", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec, _ = call(http.MethodDelete, base+"/demo?data=delete", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code, "demo has no data purge")
	rec, _ = call(http.MethodDelete, base+"/demo", "")
	require.Equal(t, http.StatusNoContent, rec.Code)
	rec, body = call(http.MethodDelete, base+"/demo", "")
	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "not_found", body["error_code"])

	rec, _ = call(http.MethodGet, "/workspaces/not-a-uuid/extensions", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec, _ = call(http.MethodPost, base+"/nope/connect", consent)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
