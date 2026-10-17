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

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/exttest"
	"github.com/entire-vc/evc-mesh/internal/extensions/runner"
	"github.com/entire-vc/evc-mesh/internal/handler"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// The strictness of the old agent_runtime special branches survives the move:
// a broken catalog in PUT/PATCH settings on the Runner extension is a 400, also
// when the caller (the dispatcher) still uses the legacy id and the row is
// still stored under it.
func TestExtensionWorkspaceHandler_RunnerRefusesInvalidPools(t *testing.T) {
	integ := exttest.NewMemIntegrations()
	consents := &exttest.MemConsents{Integ: integ, ByID: map[uuid.UUID]domain.ExtensionConsent{}}
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(runner.New(extensions.Deps{
		Conns: extensions.IntegrationConnections{Repo: integ},
		Inst:  exttest.InstanceOn{},
	})))
	h := handler.NewExtensionWorkspaceHandler(service.NewExtensionWorkspaceService(reg, integ, consents))

	e := echo.New()
	e.PATCH("/workspaces/:ws_id/extensions/:ext_id/settings", h.PatchSettings)

	ws := uuid.New()
	require.NoError(t, integ.Upsert(t.Context(), &domain.IntegrationConfig{
		ID: uuid.New(), WorkspaceID: ws, Provider: "agent_runtime", Config: []byte(`{}`), IsActive: true,
	}))

	patch := func(ext, body string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPatch, "/workspaces/"+ws.String()+"/extensions/"+ext+"/settings", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	for _, ext := range []string{"runner", "agent_runtime"} {
		code, out := patch(ext, `{"settings":{"pools":"not-an-object"}}`)
		assert.Equal(t, http.StatusBadRequest, code, ext)
		assert.Equal(t, "validation_failed", out["error_code"], ext)
		// refused before the catalog is even looked at: the versioned runtime API
		// owns every write to Runner's configuration
		fields, _ := out["validation"].(map[string]any)
		assert.Contains(t, fields["extension"], "managed elsewhere", ext)
	}
}
