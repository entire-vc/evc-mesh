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
	"github.com/entire-vc/evc-mesh/internal/extensions/external"
	"github.com/entire-vc/evc-mesh/internal/extensions/exttest"
	"github.com/entire-vc/evc-mesh/internal/handler"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
)

const connectRBACManifest = `{"schema":1,"id":"keepish","name":"Keepish","version":"0.1.0","kind":"plugin","runtime":"external",
 "mesh_api":">=1","publisher":"example","category":"development",
 "levels":{"instance":true,"workspace":true,"project":false},
 "capabilities":{"api":["tasks:read"],"events":["task.updated"],"ui":[]},
 "external":{"base_url_setting":"keep_url","health_path":"/healthz","install_path":"/mesh/install","events_path":"/mesh/events"}}`

type connectInstance struct{ set *extensions.InstanceSetting }

func (i connectInstance) InstanceSetting(context.Context, string) (*extensions.InstanceSetting, error) {
	return i.set, nil
}

// TestExtensionConnectRoute_RBACAndInheritance drives the instance-configured
// external Connect through the real RequirePermission guard that main.go puts
// on POST /workspaces/:ws_id/extensions/:ext_id/connect: a member is refused
// with 403 and no connection row is written; admin and owner (the positive
// control) get 201 and an active connection that inherited the instance address.
func TestExtensionConnectRoute_RBACAndInheritance(t *testing.T) {
	var connectRoute auditRoute
	for _, r := range parseAuditRoutes(t) {
		if r.key() == "POST /workspaces/:ws_id/extensions/:ext_id/connect" {
			connectRoute = r
		}
	}
	require.Equal(t, "rbac", connectRoute.guard, "connect must carry rbac() in main.go")
	require.Contains(t, strings.Join(connectRoute.mws, " "), "PermManageWebhooks", "the guard main.go registers")

	f, err := exttest.NewFixture(nil, false)
	require.NoError(t, err)
	man, err := extensions.ParseManifest([]byte(connectRBACManifest))
	require.NoError(t, err)
	inst := &extensions.InstanceSetting{Enabled: true, Settings: map[string]any{"keep_url": "https://keep.example.test"}}
	p, err := external.NewProvider(man, extensions.Deps{
		Conns: extensions.IntegrationConnections{Repo: f.Integ}, Inst: connectInstance{set: inst}, Allow: f.Allow,
	}, &http.Client{}, nil)
	require.NoError(t, err)
	require.NoError(t, f.Registry.Register(p))
	h := handler.NewExtensionWorkspaceHandler(service.NewExtensionWorkspaceService(f.Registry, f.Integ, f.Consents))

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
	e.POST("/workspaces/:ws_id/extensions/:ext_id/connect", h.Connect, asCaller, mw.RequirePermission(mw.PermManageWebhooks, members))

	body := `{"consent":{"capabilities":["api:tasks:read","events:task.updated"]}}`
	connect := func(who uuid.UUID) (int, map[string]any) {
		caller = who
		req := httptest.NewRequest(http.MethodPost, "/workspaces/"+ws.String()+"/extensions/keepish/connect", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, _ := connect(member)
	assert.Equal(t, http.StatusForbidden, code, "member is refused")
	assert.Nil(t, f.Integ.Find(ws, "keepish"), "a refused member writes nothing")

	for _, who := range []uuid.UUID{admin, owner} {
		ws = uuid.New() // a fresh workspace: a second connect on the same one is 409
		code, out := connect(who)
		require.Equal(t, http.StatusCreated, code, "%v", out)
		row := f.Integ.Find(ws, "keepish")
		require.NotNil(t, row)
		assert.True(t, row.IsActive)
		assert.Contains(t, string(row.Config), "https://keep.example.test", "inherited instance address")
	}
}
