package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// The same chain cmd/api builds for the protected group, against a real database:
// ExtensionAuth -> DualAuth (steps aside) -> ExtensionCapabilityGuard ->
// WorkspaceRLS -> RequireWorkspaceMemberScoped, then the per-route guards. What it
// pins is the tenant boundary: a token is scoped to ONE workspace, and no route
// can be steered at another one through the path.

func newChainFixture(t *testing.T, consented []string) (f *extFixture, otherWS *domain.Workspace, ownProject, foreignProject uuid.UUID) {
	t.Helper()
	f = newExtFixture(t, consented)
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	owner := &domain.User{ID: uuid.New(), Email: "chain-" + suffix + "@example.com", PasswordHash: "x", Name: "Chain", Username: "chain-" + suffix, IsActive: true}
	require.NoError(t, postgres.NewUserRepo(f.db).Create(ctx, owner))
	otherWS = &domain.Workspace{ID: uuid.New(), Name: "other", Slug: "other-" + suffix, OwnerID: owner.ID}
	require.NoError(t, postgres.NewWorkspaceRepo(f.db).Create(ctx, otherWS))

	projRepo := postgres.NewProjectRepo(f.db)
	mk := func(ws uuid.UUID, slug string) uuid.UUID {
		p := &domain.Project{ID: uuid.New(), WorkspaceID: ws, Name: slug, Slug: slug + "-" + suffix, DefaultAssigneeType: domain.DefaultAssigneeNone, Settings: []byte("{}"), CreatedAt: time.Now(), UpdatedAt: time.Now()}
		require.NoError(t, projRepo.Create(ctx, p))
		return p.ID
	}
	ownProject, foreignProject = mk(f.ws.ID, "own"), mk(otherWS.ID, "foreign")

	// Rebuild the router with the full chain.
	f.e = echo.New()
	api := f.e.Group("/api/v1")
	api.Use(ExtensionAuth(f.svc))
	api.Use(DualAuth(nil, nil, nil))
	api.Use(ExtensionCapabilityGuard(f.svc))
	api.Use(WorkspaceRLS(f.db, projRepo))
	api.Use(RequireWorkspaceMemberScoped(f.db))
	reach := func(name string) echo.HandlerFunc {
		return func(c echo.Context) error {
			f.reached[name]++
			return c.NoContent(http.StatusOK)
		}
	}
	api.GET("/workspaces/:ws_id/projects", reach("ws-projects"))
	api.GET("/projects/:proj_id", reach("project"), RequireProjectMember(f.db))
	api.PATCH("/projects/:proj_id", reach("project-write"), RequirePermission(PermManageWebhooks, nil))
	api.GET("/ext/me", reach("me"))
	return f, otherWS, ownProject, foreignProject
}

func TestExtensionChain_TokenIsBoundToItsWorkspace(t *testing.T) {
	f, otherWS, ownProject, foreignProject := newChainFixture(t, []string{"api:projects:read", "api:workspaces:read"})

	assert.Equal(t, http.StatusOK, f.do(http.MethodGet, "/api/v1/workspaces/"+f.ws.ID.String()+"/projects", f.token))
	assert.Equal(t, http.StatusOK, f.do(http.MethodGet, "/api/v1/projects/"+ownProject.String(), f.token))
	assert.Equal(t, 1, f.reached["project"])

	// Same capability, another tenant: refused before the handler.
	assert.Equal(t, http.StatusForbidden, f.do(http.MethodGet, "/api/v1/workspaces/"+otherWS.ID.String()+"/projects", f.token))
	assert.Equal(t, http.StatusForbidden, f.do(http.MethodGet, "/api/v1/projects/"+foreignProject.String(), f.token))
	assert.Equal(t, 0, f.reached["ws-projects"]-1, "only the own-workspace call reached the handler")
	assert.Equal(t, 1, f.reached["project"], "the foreign project never reached the handler")
}

func TestExtensionChain_WriteNeedsTheCapabilityNotAWorkspaceRole(t *testing.T) {
	f, _, ownProject, _ := newChainFixture(t, []string{"api:projects:read"})
	p := "/api/v1/projects/" + ownProject.String()

	assert.Equal(t, http.StatusForbidden, f.do(http.MethodPatch, p, f.token))
	assert.Equal(t, int64(1), f.violations(t))
	assert.Zero(t, f.reached["project-write"])

	// With the write capability RequirePermission steps aside: the capability
	// guard, not a role, authorizes an extension.
	g := newExtFixtureWithChainCaps(t, []string{"api:projects:read", "api:projects:write"})
	assert.Equal(t, http.StatusOK, g.do(http.MethodPatch, "/api/v1/projects/"+g.ownProject.String(), g.token))
	assert.Equal(t, 1, g.reached["project-write"])
}

type chainCaps struct {
	*extFixture
	ownProject uuid.UUID
}

func newExtFixtureWithChainCaps(t *testing.T, caps []string) *chainCaps {
	t.Helper()
	f, _, own, _ := newChainFixture(t, caps)
	return &chainCaps{extFixture: f, ownProject: own}
}

func TestExtensionChain_ActorMayAccessWorkspace(t *testing.T) {
	f, otherWS, _, _ := newChainFixture(t, []string{"api:tasks:read"})
	e := echo.New()

	p := &service.ExtensionPrincipal{WorkspaceID: f.ws.ID, ActorID: uuid.New(), ExtensionID: "gitlab"}
	c := e.NewContext(httpRequest(), nil)
	c.Set(ContextKeyAuthType, AuthTypeExtension)
	c.Set(ContextKeyExtensionPrincipal, p)
	c.Set(ContextKeyExtensionWorkspaceID, f.ws.ID)

	assert.True(t, ActorMayAccessWorkspace(c, f.db, f.ws.ID))
	assert.False(t, ActorMayAccessWorkspace(c, f.db, otherWS.ID), "body names another tenant")

	// An extension without a recorded workspace is never let through.
	c2 := e.NewContext(httpRequest(), nil)
	c2.Set(ContextKeyAuthType, AuthTypeExtension)
	assert.False(t, ActorMayAccessWorkspace(c2, f.db, f.ws.ID))
}

func TestExtensionChain_RequireWorkspaceMemberWithoutWorkspaceInToken(t *testing.T) {
	f, _, _, _ := newChainFixture(t, []string{"api:tasks:read"})
	e := echo.New()
	c := e.NewContext(httpRequest(), nil)
	c.Set(ContextKeyAuthType, AuthTypeExtension)
	c.Set(ContextKeyWorkspaceID, f.ws.ID) // route names a workspace, token carries none
	rec := runMiddleware(c, RequireWorkspaceMember(f.db))
	assert.Equal(t, http.StatusForbidden, rec)
}

func TestExtensionRateLimitKey(t *testing.T) {
	e := echo.New()
	c := e.NewContext(httpRequest(), nil)
	p := &service.ExtensionPrincipal{ActorID: uuid.New(), WorkspaceID: uuid.New(), ExtensionID: "gitlab"}
	c.Set(ContextKeyExtensionPrincipal, p)
	key := RateLimitKeyByActor(c)
	assert.Equal(t, "ext:"+p.ActorID.String(), key)
	assert.Equal(t, "extension", keyTypeFromKey(key))
}

func TestExtensionAuth_NilServiceIsInertAndBadPrincipalIsRefused(t *testing.T) {
	e := echo.New()
	reached := 0
	h := func(c echo.Context) error { reached++; return c.NoContent(http.StatusOK) }

	// svc == nil: ext_ tokens are simply not recognised here.
	req := httpRequest()
	req.Header.Set("Authorization", "Bearer ext_whatever")
	rec := serve(e, req, ExtensionAuth(nil), h)
	assert.Equal(t, http.StatusOK, rec)
	assert.Equal(t, 1, reached)

	// Authenticated-as-extension flag without a principal: never allowed.
	c := e.NewContext(httpRequest(), nil)
	c.Set(ContextKeyAuthType, AuthTypeExtension)
	assert.Equal(t, http.StatusForbidden, runMiddleware(c, ExtensionCapabilityGuard(nil)))

	// A plain (non-extension) request passes the guard untouched.
	c = e.NewContext(httpRequest(), nil)
	assert.Equal(t, http.StatusOK, runMiddleware(c, ExtensionCapabilityGuard(nil)))
}

func TestExtensionCapabilityGuard_HeadFollowsGet(t *testing.T) {
	e := echo.New()
	req := httpRequest()
	req.Method = http.MethodHead
	rec := newRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/api/v1/ext/me")
	c.Set(ContextKeyAuthType, AuthTypeExtension)
	c.Set(ContextKeyExtensionPrincipal, &service.ExtensionPrincipal{ActorID: uuid.New(), WorkspaceID: uuid.New()})
	err := ExtensionCapabilityGuard(nil)(func(c echo.Context) error { return c.NoContent(http.StatusOK) })(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code, "HEAD of an open GET route")
}

func httpRequest() *http.Request { return httptest.NewRequest(http.MethodGet, "/", http.NoBody) }

func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

// runMiddleware runs mw in front of a handler that answers 200 and returns the
// status written.
func runMiddleware(c echo.Context, mw echo.MiddlewareFunc) int {
	rec := httptest.NewRecorder()
	c.SetResponse(echo.NewResponse(rec, c.Echo()))
	_ = mw(func(c echo.Context) error { return c.NoContent(http.StatusOK) })(c)
	return rec.Code
}

func serve(e *echo.Echo, req *http.Request, mw echo.MiddlewareFunc, h echo.HandlerFunc) int {
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	_ = mw(h)(c)
	return rec.Code
}
