package main

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
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// roleRepo answers GetRole from a fixed table; everything else on the member
// repository is out of reach of rbac().
type roleRepo struct {
	repository.WorkspaceMemberRepository
	roles map[uuid.UUID]string
}

func (r roleRepo) GetRole(_ context.Context, _, userID uuid.UUID) (string, error) {
	if role, ok := r.roles[userID]; ok {
		return role, nil
	}
	return "", apierror.NotFound("member")
}

// runtimeGuarded refuses a generic Update on a Runner row, like the Postgres
// repository: only the versioned runtime API writes it.
type runtimeGuarded struct{ *exttest.MemIntegrations }

func (g runtimeGuarded) Update(context.Context, uuid.UUID, domain.UpdateIntegrationInput) (*domain.IntegrationConfig, error) {
	return nil, apierror.BadRequest("use the versioned runtime API")
}

// TestRunnerEnableRoute_CatalogAndRBAC drives enable on a workspace that never
// had Runner through the real RequirePermission guard that main.go puts on the
// route: a member is refused with 403 and nothing is written; owner and admin
// (the positive control) get 200, and the catalog GET then reports
// runner.status.enabled=true. Before the fix the same enable answered 404
// (no integration row) and the catalog kept enabled=false.
func TestRunnerEnableRoute_CatalogAndRBAC(t *testing.T) {
	var enableRoute auditRoute
	for _, r := range parseAuditRoutes(t) {
		if r.key() == "POST /workspaces/:ws_id/extensions/:ext_id/enable" {
			enableRoute = r
		}
	}
	require.Equal(t, "rbac", enableRoute.guard, "enable must carry rbac() in main.go")
	require.Contains(t, strings.Join(enableRoute.mws, " "), "PermManageWebhooks", "the guard main.go registers")

	mem := exttest.NewMemIntegrations()
	integ := runtimeGuarded{mem}
	consents := &exttest.MemConsents{Integ: mem, ByID: map[uuid.UUID]domain.ExtensionConsent{}}
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(runner.New(extensions.Deps{
		Conns: extensions.IntegrationConnections{Repo: integ},
	})))
	h := handler.NewExtensionWorkspaceHandler(service.NewExtensionWorkspaceService(reg, integ, consents))

	ws := uuid.New()
	owner, admin, member := uuid.New(), uuid.New(), uuid.New()
	members := roleRepo{roles: map[uuid.UUID]string{owner: domain.RoleOwner, admin: domain.RoleAdmin, member: domain.RoleMember}}

	var caller uuid.UUID
	asCaller := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(mw.ContextKeyAuthType, mw.AuthTypeUser)
			c.Set(mw.ContextKeyUserID, caller)
			c.Set(mw.ContextKeyWorkspaceID, ws)
			return next(c)
		}
	}
	e := echo.New()
	e.GET("/workspaces/:ws_id/extensions", h.List, asCaller)
	e.POST("/workspaces/:ws_id/extensions/:ext_id/enable", h.Enable, asCaller, mw.RequirePermission(mw.PermManageWebhooks, members))

	base := "/workspaces/" + ws.String() + "/extensions"
	call := func(method, path string, who uuid.UUID) (int, map[string]any) {
		caller = who
		req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	runnerEnabled := func() any {
		code, out := call(http.MethodGet, base, owner)
		require.Equal(t, http.StatusOK, code)
		for _, it := range out["extensions"].([]any) {
			item := it.(map[string]any)
			if item["id"] == "runner" {
				return item["status"].(map[string]any)["enabled"]
			}
		}
		t.Fatal("runner is missing from the catalog")
		return nil
	}

	assert.Equal(t, false, runnerEnabled(), "catalog before enable")

	code, _ := call(http.MethodPost, base+"/runner/enable", member)
	assert.Equal(t, http.StatusForbidden, code, "member is refused")
	assert.Nil(t, mem.Find(ws, "runner"), "a refused member writes nothing")
	assert.Equal(t, false, runnerEnabled(), "catalog after the refusal")

	for _, who := range []uuid.UUID{admin, owner} {
		code, out := call(http.MethodPost, base+"/runner/enable", who)
		require.Equal(t, http.StatusOK, code, "%v", out)
		assert.Equal(t, true, runnerEnabled())
		row := mem.Find(ws, "runner")
		require.NotNil(t, row)
		assert.True(t, row.IsActive)
	}
}
