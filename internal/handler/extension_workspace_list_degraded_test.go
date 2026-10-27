package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
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

// degradedInstance is an instance store whose secret store is down, as prod
// saw on 2026-10-27 when Keep answered 502.
type degradedInstance struct{}

func (degradedInstance) InstanceSetting(context.Context, string) (*extensions.InstanceSetting, error) {
	return nil, errors.New(`open "token": secret store degraded: keep unavailable: status 502`)
}

func listFixture(t *testing.T) (e *echo.Echo, path string) {
	t.Helper()
	fx, err := exttest.NewFixture(exttest.InstanceOn{}, false)
	require.NoError(t, err)
	man, err := extensions.ParseManifest([]byte(strings.Replace(exttest.Manifest, `"id":"demo","name":"Demo"`, `"id":"broken","name":"Broken"`, 1)))
	require.NoError(t, err)
	broken := exttest.DemoProvider{BaseProvider: extensions.BaseProvider{Man: man, Conns: extensions.IntegrationConnections{Repo: fx.Integ}, Inst: degradedInstance{}, Allow: fx.Allow}, Healthy: true}
	require.NoError(t, fx.Registry.Register(broken))
	h := handler.NewExtensionWorkspaceHandler(service.NewExtensionWorkspaceService(fx.Registry, fx.Integ, fx.Consents))
	e = echo.New()
	e.GET("/workspaces/:ws_id/extensions", h.List)
	return e, "/workspaces/" + uuid.NewString() + "/extensions"
}

// One extension whose status cannot be resolved must not take the whole list
// down: the list answers 200 and only that extension is marked degraded.
func TestExtensionWorkspaceList_SecretStoreDownDegradesOneExtension(t *testing.T) {
	e, path := listFixture(t)
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, http.NoBody))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, logs.String(), `extension "broken": secret store degraded`)
	assert.NotContains(t, logs.String(), "keep unavailable", "the raw store error never reaches the log")

	var out struct {
		Extensions []struct {
			ID     string         `json:"id"`
			Status map[string]any `json:"status"`
		} `json:"extensions"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	byID := map[string]map[string]any{}
	for _, x := range out.Extensions {
		byID[x.ID] = x.Status
	}
	require.Contains(t, byID, "demo")
	require.Contains(t, byID, "broken")
	assert.Equal(t, true, byID["demo"]["enabled"], "the healthy extension is unaffected")
	assert.Nil(t, byID["demo"]["degraded"])
	assert.Equal(t, true, byID["broken"]["degraded"])
	assert.Equal(t, false, byID["broken"]["enabled"])
	assert.NotContains(t, byID["broken"]["last_error"], "keep", "the secret store error stays in the server log")
}

// A request the client already gave up on is not a server error: 499, never 5xx.
func TestExtensionWorkspaceList_ClientCanceled(t *testing.T) {
	e, path := listFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, http.NoBody).WithContext(ctx))
	assert.Less(t, rec.Code, 500, rec.Body.String())
	assert.Equal(t, 499, rec.Code)
}
