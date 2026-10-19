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

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/exttest"
	"github.com/entire-vc/evc-mesh/internal/extensions/runner"
	"github.com/entire-vc/evc-mesh/internal/handler"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
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

// runtimeGuardedIntegrations refuses Update on a Runner row the way the
// Postgres repository does: only the versioned runtime API writes it.
type runtimeGuardedIntegrations struct{ *exttest.MemIntegrations }

func (g runtimeGuardedIntegrations) Update(ctx context.Context, id uuid.UUID, in domain.UpdateIntegrationInput) (*domain.IntegrationConfig, error) {
	return nil, apierror.BadRequest("use the versioned runtime API")
}

// A workspace that never had Runner has no integration row, and the generic
// connect refuses a managed-elsewhere extension: enable must create the empty
// row itself, and repeating it must stay a single row.
func TestExtensionWorkspaceHandler_RunnerEnableCreatesMissingRow(t *testing.T) {
	mem := exttest.NewMemIntegrations()
	integ := runtimeGuardedIntegrations{mem}
	consents := &exttest.MemConsents{Integ: mem, ByID: map[uuid.UUID]domain.ExtensionConsent{}}
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(runner.New(extensions.Deps{
		Conns: extensions.IntegrationConnections{Repo: integ},
		Inst:  exttest.InstanceOn{},
	})))
	h := handler.NewExtensionWorkspaceHandler(service.NewExtensionWorkspaceService(reg, integ, consents))

	e := echo.New()
	e.POST("/workspaces/:ws_id/extensions/:ext_id/enable", h.Enable)
	e.POST("/workspaces/:ws_id/extensions/:ext_id/connect", h.Connect)

	ws := uuid.New()
	post := func(action string) int {
		req := httptest.NewRequest(http.MethodPost, "/workspaces/"+ws.String()+"/extensions/runner/"+action, strings.NewReader(`{}`))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}

	assert.Equal(t, http.StatusBadRequest, post("connect"), "generic connect stays refused")
	require.Nil(t, integ.Find(ws, "runner"))

	for i := 0; i < 2; i++ {
		require.Equal(t, http.StatusOK, post("enable"), "enable #%d", i+1)
		row := integ.Find(ws, "runner")
		require.NotNil(t, row)
		assert.True(t, row.IsActive)
		assert.JSONEq(t, `{}`, string(row.Config))
	}
}

// Runtime save leaves the Runner row inactive; enabling it afterwards must work
// although the repository refuses a generic Update on that row.
func TestExtensionWorkspaceHandler_RunnerEnableSwitchesInactiveRowOn(t *testing.T) {
	mem := exttest.NewMemIntegrations()
	integ := runtimeGuardedIntegrations{mem}
	consents := &exttest.MemConsents{Integ: mem, ByID: map[uuid.UUID]domain.ExtensionConsent{}}
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(runner.New(extensions.Deps{
		Conns: extensions.IntegrationConnections{Repo: integ},
		Inst:  exttest.InstanceOn{},
	})))
	h := handler.NewExtensionWorkspaceHandler(service.NewExtensionWorkspaceService(reg, integ, consents))
	e := echo.New()
	e.POST("/workspaces/:ws_id/extensions/:ext_id/enable", h.Enable)

	for _, stored := range []domain.IntegrationProvider{"runner", "agent_runtime"} {
		ws := uuid.New()
		require.NoError(t, mem.Upsert(t.Context(), &domain.IntegrationConfig{
			ID: uuid.New(), WorkspaceID: ws, Provider: stored, Config: []byte(`{}`), IsActive: false,
		}))
		req := httptest.NewRequest(http.MethodPost, "/workspaces/"+ws.String()+"/extensions/runner/enable", strings.NewReader(`{}`))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, string(stored))
		assert.True(t, mem.Find(ws, stored).IsActive, string(stored))
	}
}
