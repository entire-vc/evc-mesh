package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
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

type instanceOffHTTPStore struct{}

func (instanceOffHTTPStore) InstanceSetting(context.Context, string) (*extensions.InstanceSetting, error) {
	return &extensions.InstanceSetting{Enabled: false}, nil
}

// The list endpoint carries status.instance_disabled in its JSON: the UI reads
// it to say "disabled at instance level" while the workspace row is enabled.
func TestExtensionWorkspaceHandler_ListJSONReportsInstanceDisabled(t *testing.T) {
	list := func(instance extensions.InstanceStore, connect bool) map[string]any {
		fx, err := exttest.NewFixture(instance, false)
		require.NoError(t, err)
		h := handler.NewExtensionWorkspaceHandler(service.NewExtensionWorkspaceService(fx.Registry, fx.Integ, fx.Consents))
		e := echo.New()
		e.GET("/workspaces/:ws_id/extensions", h.List)
		e.POST("/workspaces/:ws_id/extensions/:ext_id/connect", h.Connect)
		root := "/workspaces/" + uuid.NewString() + "/extensions"
		if connect {
			req := httptest.NewRequest(http.MethodPost, root+"/demo/connect",
				strings.NewReader(`{"settings":{"base_url":"https://x.example"},"consent":{"capabilities":["api:tasks.read","events:task.updated"]}}`))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		}
		req := httptest.NewRequest(http.MethodGet, root, http.NoBody)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		var out struct {
			Extensions []struct {
				ID     string         `json:"id"`
				Status map[string]any `json:"status"`
			} `json:"extensions"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
		for _, it := range out.Extensions {
			if it.ID == "demo" {
				return it.Status
			}
		}
		t.Fatalf("demo not in list: %s", rec.Body.String())
		return nil
	}

	off := list(instanceOffHTTPStore{}, true)
	assert.Equal(t, true, off["enabled"], "workspace row is enabled")
	assert.Equal(t, true, off["instance_disabled"])

	on := list(exttest.InstanceOn{}, true)
	assert.NotContains(t, on, "instance_disabled", "an enabled instance reports nothing")
}
