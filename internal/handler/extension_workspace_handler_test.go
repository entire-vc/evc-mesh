package handler_test

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

	"github.com/entire-vc/evc-mesh/internal/extensions/exttest"
	"github.com/entire-vc/evc-mesh/internal/handler"
	"github.com/entire-vc/evc-mesh/internal/service"
)

func TestExtensionWorkspaceHandler(t *testing.T) {
	fx, err := exttest.NewFixture(exttest.InstanceOn{}, true)
	require.NoError(t, err)
	h := handler.NewExtensionWorkspaceHandler(service.NewExtensionWorkspaceService(fx.Registry, fx.Integ, fx.Consents))

	e := echo.New()
	const base = "/workspaces/:ws_id/extensions"
	e.GET(base, h.List)
	e.POST(base+"/:ext_id/connect", h.Connect)
	e.PATCH(base+"/:ext_id/settings", h.PatchSettings)
	e.POST(base+"/:ext_id/enable", h.Enable)
	e.POST(base+"/:ext_id/disable", h.Disable)
	e.POST(base+"/:ext_id/test", h.Test)
	e.DELETE(base+"/:ext_id", h.Disconnect)

	root := "/workspaces/" + uuid.NewString() + "/extensions"
	call := func(method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, _ := call(http.MethodGet, root, "")
	assert.Equal(t, http.StatusOK, code)
	code, _ = call(http.MethodGet, "/workspaces/x/extensions", "")
	assert.Equal(t, http.StatusBadRequest, code)

	code, out := call(http.MethodPost, root+"/demo/disable", "")
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "confirmation_required", out["error_code"])
	code, out = call(http.MethodPost, root+"/demo/disable", `{"confirm":true}`)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, false, out["status"].(map[string]any)["enabled"])

	code, _ = call(http.MethodPost, root+"/demo/connect", `{`)
	assert.Equal(t, http.StatusBadRequest, code)
	code, out = call(http.MethodPost, root+"/demo/connect", `{"consent":{"capabilities":["api:tasks.read","events:task.updated"]}}`)
	assert.Equal(t, http.StatusConflict, code, "the disable above already created the workspace row")
	assert.Equal(t, "already_connected", out["error_code"])

	code, _ = call(http.MethodPatch, root+"/demo/settings", `{`)
	assert.Equal(t, http.StatusBadRequest, code)
	code, _ = call(http.MethodPatch, root+"/demo/settings", `{"settings":{"base_url":"u"}}`)
	assert.Equal(t, http.StatusOK, code)
	code, _ = call(http.MethodPost, root+"/demo/enable", "")
	assert.Equal(t, http.StatusOK, code)
	code, out = call(http.MethodPost, root+"/demo/test", "")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, true, out["ok"])

	code, _ = call(http.MethodDelete, root+"/demo?data=x", "")
	assert.Equal(t, http.StatusBadRequest, code)
	code, _ = call(http.MethodDelete, root+"/demo?data=delete", "")
	assert.Equal(t, http.StatusNoContent, code)
	assert.Equal(t, 1, fx.Purged)
	code, _ = call(http.MethodDelete, root+"/demo", "")
	assert.Equal(t, http.StatusNotFound, code)

	code, out = call(http.MethodPost, root+"/demo/connect", `{"consent":{"capabilities":["api:tasks.read","events:task.updated"]}}`)
	assert.Equal(t, http.StatusCreated, code)
	assert.Equal(t, "workspace", out["status"].(map[string]any)["source"])
}
