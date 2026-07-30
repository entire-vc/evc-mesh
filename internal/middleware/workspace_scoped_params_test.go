package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// paramCtx builds an Echo context for a route carrying one workspace-scoping
// parameter, in the state WorkspaceRLS leaves behind: the workspace it resolved
// plus how it resolved it.
func paramCtx(param, value, path string, wsID uuid.UUID, source, authType string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath(path)
	c.SetParamNames(param)
	c.SetParamValues(value)
	c.Set(ContextKeyAuthType, authType)
	if source != "" {
		c.Set(ContextKeyWorkspaceID, wsID)
		c.Set(ContextKeyWorkspaceSource, source)
	}
	return c, rec
}

// TestRequireWorkspaceMemberScoped_NonWorkspaceParams_Guarded is the regression
// test for the second half of the cross-tenant hole. The first version of this
// guard keyed on the literal :ws_id, so /agents/:agent_id, /custom-fields/:field_id
// and /tasks/:task_id/activity — all of which WorkspaceRLS resolves a tenant from —
// went unchecked, and a stranger could read them (and POST to the agent activity
// log) in another tenant's workspace.
func TestRequireWorkspaceMemberScoped_NonWorkspaceParams_Guarded(t *testing.T) {
	for _, param := range []string{"agent_id", "field_id", "task_id", "artifact_id", "init_id", "proj_id"} {
		t.Run(param, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()

			victimWS := uuid.New()
			stranger := uuid.New()
			c, rec := paramCtx(param, uuid.New().String(), "/api/v1/x/:"+param, victimWS, WorkspaceSourceParam, AuthTypeUser)
			c.Set(ContextKeyUserID, stranger)

			// No workspace role in context: WorkspaceRLS found no membership row.
			// The owner fallback runs and finds somebody else.
			mock.ExpectQuery("SELECT owner_id FROM workspaces").
				WithArgs(victimWS).
				WillReturnRows(sqlmock.NewRows([]string{"owner_id"}).AddRow(uuid.New()))

			guard := RequireWorkspaceMemberScoped(sqlx.NewDb(db, "postgres"))
			require.NoError(t, guard(nopHandler)(c))
			assert.Equal(t, http.StatusForbidden, rec.Code,
				"a stranger reached a route keyed on :%s", param)
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// TestRequireWorkspaceMemberScoped_WorkspaceFromAuthIsNotPermission covers the
// bypass the source tag exists to close. On an agent-key request WorkspaceRLS
// falls back to the agent's *own* workspace whenever the path parameter resolves
// to nothing. Treating that as "the workspace was resolved, and it is mine" would
// wave the request through on a route that named an object the agent cannot see.
func TestRequireWorkspaceMemberScoped_WorkspaceFromAuthIsNotPermission(t *testing.T) {
	ownWS := uuid.New()
	c, rec := paramCtx("task_id", uuid.New().String(), "/api/v1/tasks/:task_id", ownWS, WorkspaceSourceAuth, AuthTypeAgent)
	c.Set(ContextKeyAgentID, uuid.New())

	guard := RequireWorkspaceMemberScoped(nil)
	require.NoError(t, guard(nopHandler)(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestRequireWorkspaceMemberScoped_UnresolvedParamIsRefused: a workspace-scoping
// parameter that resolved to nothing must not fall through as "no workspace in
// context, nothing to check". This is how the per-route guard already behaved on
// /tasks/:task_id.
func TestRequireWorkspaceMemberScoped_UnresolvedParamIsRefused(t *testing.T) {
	c, rec := paramCtx("agent_id", uuid.New().String(), "/api/v1/agents/:agent_id", uuid.Nil, "", AuthTypeUser)

	guard := RequireWorkspaceMemberScoped(nil)
	require.NoError(t, guard(nopHandler)(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestRequireWorkspaceMemberScoped_ExemptRoutePassesThrough: Spark's :agent_id is
// a catalog id in an external marketplace, not an agents.id here, so no workspace
// can ever resolve from it. Guarding it would 403 the whole Spark integration.
func TestRequireWorkspaceMemberScoped_ExemptRoutePassesThrough(t *testing.T) {
	for path := range workspaceScopeExemptRoutes {
		t.Run(path, func(t *testing.T) {
			c, rec := paramCtx("agent_id", "spark-catalog-id", path, uuid.Nil, "", AuthTypeUser)

			guard := RequireWorkspaceMemberScoped(nil)
			require.NoError(t, guard(nopHandler)(c))
			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

// TestWorkspaceScopedParams_CoversEveryResolver keeps WorkspaceScopedParams honest.
//
// The guard fires on the parameter names in that list; WorkspaceRLS resolves a
// tenant from the parameter names it reads. Those two sets are now the same set by
// construction — WorkspaceScopedParams is derived from the resolver tables — so
// what is left to check is that the derivation is the only way in. A literal
// c.Param("...") inside WorkspaceRLS would be a parameter the resolver reads and
// the list does not know about, which is how a route becomes reachable across
// tenants again.
func TestWorkspaceScopedParams_CoversEveryResolver(t *testing.T) {
	src, err := os.ReadFile("workspace.go")
	require.NoError(t, err)

	body := string(src)
	start := regexp.MustCompile(`func WorkspaceRLS\(`).FindStringIndex(body)
	require.NotNil(t, start, "WorkspaceRLS not found — has the resolver been renamed?")
	end := regexp.MustCompile(`\nfunc RequireWorkspaceMember\(`).FindStringIndex(body)
	require.NotNil(t, end, "RequireWorkspaceMember not found")
	resolver := body[start[0]:end[0]]

	adHoc := regexp.MustCompile(`c\.Param\("([a-z_]+)"\)`).FindAllStringSubmatch(resolver, -1)
	var names []string
	for _, m := range adHoc {
		names = append(names, m[1])
	}
	sort.Strings(names)
	assert.Empty(t, names,
		"WorkspaceRLS reads these parameters directly instead of through workspaceParamResolvers, "+
			"so WorkspaceScopedParams — which is derived from that table — does not know about them "+
			"and RequireWorkspaceMemberScoped will not fire on them: %v", names)

	require.NotEmpty(t, WorkspaceScopedParams, "the derived list is empty — has the table been renamed?")
	listed := make(map[string]bool, len(WorkspaceScopedParams))
	for _, p := range WorkspaceScopedParams {
		listed[p] = true
	}
	for _, r := range workspaceParamResolvers {
		assert.True(t, listed[r.param], ":%s is resolved but not guarded", r.param)
	}
}

// TestWorkspaceRLS_CompositeRouteMismatchIsFlagged is the unit-level repro for the
// composite hole. The route names two objects; they belong to two tenants; the
// parent is the caller's own. Resolving stopped at the parent, so the guard
// checked the caller against themselves and the write went through against a
// stranger's row.
func TestWorkspaceRLS_CompositeRouteMismatchIsFlagged(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	callerWS := uuid.New()
	victimWS := uuid.New()
	statusID := uuid.New()

	// :proj_id resolves through the project repository (stubbed below); :status_id
	// resolves to somebody else's workspace.
	mock.ExpectQuery("FROM task_statuses").
		WithArgs(statusID).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(victimWS))
	mock.ExpectQuery("set_config").
		WithArgs(callerWS.String()).
		WillReturnRows(sqlmock.NewRows([]string{"set_config"}).AddRow(callerWS.String()))

	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodPatch, "/", http.NoBody), rec)
	c.SetPath("/api/v1/projects/:proj_id/statuses/:status_id")
	c.SetParamNames("proj_id", "status_id")
	c.SetParamValues(uuid.New().String(), statusID.String())
	c.Set(ContextKeyAuthType, AuthTypeAgent)

	sqlxDB := sqlx.NewDb(db, "postgres")
	require.NoError(t, WorkspaceRLS(sqlxDB, stubProjectRepo{wsID: callerWS})(nopHandler)(c))

	assert.Equal(t, callerWS, c.Get(ContextKeyWorkspaceID),
		"the session workspace should still be the one the parent id named")
	assert.Equal(t, true, c.Get(ContextKeyWorkspaceParamMismatch),
		"a path naming two tenants was not flagged")

	// And the guard refuses it, even though the caller is a member of the workspace
	// the parent id resolved to — which is exactly the request that used to succeed.
	rec2 := httptest.NewRecorder()
	c2 := e.NewContext(httptest.NewRequest(http.MethodPatch, "/", http.NoBody), rec2)
	c2.SetPath("/api/v1/projects/:proj_id/statuses/:status_id")
	c2.SetParamNames("proj_id", "status_id")
	c2.SetParamValues(uuid.New().String(), statusID.String())
	c2.Set(ContextKeyAuthType, AuthTypeAgent)
	c2.Set(ContextKeyWorkspaceID, callerWS)
	c2.Set(ContextKeyWorkspaceSource, WorkspaceSourceParam)
	c2.Set(ContextKeyWorkspaceParamMismatch, true)

	require.NoError(t, RequireWorkspaceMemberScoped(sqlxDB)(nopHandler)(c2))
	assert.Equal(t, http.StatusForbidden, rec2.Code,
		"the guard allowed a request whose path named two tenants")
}

// TestWorkspaceRLS_CompositeRouteAgreementPasses is the other half: a composite
// route whose two ids belong to the same tenant is the normal case and must not be
// flagged, or the fix reads as an outage on every status edit in the product.
func TestWorkspaceRLS_CompositeRouteAgreementPasses(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	wsID := uuid.New()
	statusID := uuid.New()

	mock.ExpectQuery("FROM task_statuses").
		WithArgs(statusID).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(wsID))
	mock.ExpectQuery("set_config").
		WithArgs(wsID.String()).
		WillReturnRows(sqlmock.NewRows([]string{"set_config"}).AddRow(wsID.String()))

	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodPatch, "/", http.NoBody), httptest.NewRecorder())
	c.SetPath("/api/v1/projects/:proj_id/statuses/:status_id")
	c.SetParamNames("proj_id", "status_id")
	c.SetParamValues(uuid.New().String(), statusID.String())
	c.Set(ContextKeyAuthType, AuthTypeAgent)

	require.NoError(t, WorkspaceRLS(sqlx.NewDb(db, "postgres"), stubProjectRepo{wsID: wsID})(nopHandler)(c))
	assert.Equal(t, wsID, c.Get(ContextKeyWorkspaceID))
	assert.Nil(t, c.Get(ContextKeyWorkspaceParamMismatch),
		"a composite route whose ids agree was refused")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestWorkspaceRLS_UnknownChildIDIsFlagged: a child id that names no row at all is
// the same refusal. Resolving it to nothing and carrying on with the parent's
// workspace is how "does this id exist in some other tenant" gets answered.
func TestWorkspaceRLS_UnknownChildIDIsFlagged(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	wsID := uuid.New()
	mock.ExpectQuery("FROM task_dependencies").
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}))
	mock.ExpectQuery("FROM tasks t JOIN projects p").
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(wsID))
	mock.ExpectQuery("set_config").
		WillReturnRows(sqlmock.NewRows([]string{"set_config"}).AddRow(wsID.String()))

	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodDelete, "/", http.NoBody), httptest.NewRecorder())
	c.SetPath("/api/v1/tasks/:task_id/dependencies/:dep_id")
	c.SetParamNames("task_id", "dep_id")
	c.SetParamValues(uuid.New().String(), uuid.New().String())
	c.Set(ContextKeyAuthType, AuthTypeAgent)

	require.NoError(t, WorkspaceRLS(sqlx.NewDb(db, "postgres"), nil)(nopHandler)(c))
	assert.Equal(t, true, c.Get(ContextKeyWorkspaceParamMismatch))
}

// stubProjectRepo answers every project lookup with one workspace, which is all
// the resolver asks of it.
type stubProjectRepo struct {
	repository.ProjectRepository
	wsID uuid.UUID
}

func (s stubProjectRepo) GetByID(context.Context, uuid.UUID) (*domain.Project, error) {
	return &domain.Project{WorkspaceID: s.wsID}, nil
}
