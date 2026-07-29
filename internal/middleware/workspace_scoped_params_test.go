package middleware

import (
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
// The guard fires on the parameter names in that list; WorkspaceRLS resolves a
// tenant from the parameter names it reads. If those two sets ever diverge, a
// route becomes reachable across tenants again — exactly the failure this file
// was written for.
//
// The list is now derived from workspaceParamResolvers, so the two cannot diverge
// by omission. What can still diverge is a parameter read by a hard-coded name
// somewhere in this file instead of through the resolver table — that would be a
// tenant the guard never learns about, which is how :status_id and :dep_id came to
// be resolved by nobody. So the file's own source is scanned for a literal
// c.Param("...") and every name found has to be one the guard knows.
//
// Zero matches is the expected result and the strongest one: it means every
// parameter reaches the resolver through workspaceParamResolvers. The table
// itself is checked for emptiness by TestWorkspaceScopedParams_MatchesResolverTable.
func TestWorkspaceScopedParams_CoversEveryResolver(t *testing.T) {
	src, err := os.ReadFile("workspace.go")
	require.NoError(t, err)

	re := regexp.MustCompile(`c\.Param\("([a-z_]+)"\)`)
	matches := re.FindAllStringSubmatch(string(src), -1)

	listed := make(map[string]bool, len(WorkspaceScopedParams))
	for _, p := range WorkspaceScopedParams {
		listed[p] = true
	}

	var missing []string
	for _, m := range matches {
		if !listed[m[1]] && !workspaceUnscopedParams[m[1]] {
			missing = append(missing, m[1])
		}
	}
	sort.Strings(missing)
	assert.Empty(t, missing,
		"WorkspaceRLS reads these route parameters but RequireWorkspaceMemberScoped "+
			"does not guard them — add a resolver to workspaceParamResolvers: %v", missing)
}

// TestWorkspaceScopedParams_MatchesResolverTable states the property the
// derivation guarantees, so that replacing it with a hand-written list — the shape
// this file's original bug had — fails loudly instead of quietly narrowing the
// guard.
func TestWorkspaceScopedParams_MatchesResolverTable(t *testing.T) {
	require.NotEmpty(t, workspaceParamResolvers, "no resolvers — nothing resolves a tenant at all")
	require.NotEmpty(t, WorkspaceScopedParams, "the guard fires on no parameter at all")

	listed := make(map[string]bool, len(WorkspaceScopedParams))
	for _, p := range WorkspaceScopedParams {
		listed[p] = true
	}
	for _, r := range workspaceParamResolvers {
		require.NotNil(t, r.resolve, "resolver for %v has no lookup", r.params)
		require.NotEmpty(t, r.params, "a resolver declares no parameter name")
		for _, name := range r.params {
			assert.True(t, listed[name],
				"WorkspaceRLS resolves a workspace from :%s but the guard does not fire on it", name)
		}
	}

	// A parameter cannot be both resolvable and excused from resolving.
	for name := range workspaceUnscopedParams {
		assert.False(t, listed[name],
			":%s is in workspaceUnscopedParams but a resolver also claims it", name)
	}
}
