package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise resolveWorkspaceFromParams directly, without a database.
//
// It is the whole of the fix: the old resolver stopped at the first parameter
// that produced a workspace, so on a route naming a parent and a child the parent
// answered and the child was never looked at. What is asserted here is the new
// rule — every parameter present must resolve, and they must all name the same
// workspace — and the two ways it refuses.

// routeParamCtx builds an Echo context carrying the given route parameters.
func routeParamCtx(params map[string]string) echo.Context {
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), httptest.NewRecorder())
	names := make([]string, 0, len(params))
	values := make([]string, 0, len(params))
	for k, v := range params {
		names = append(names, k)
		values = append(values, v)
	}
	c.SetParamNames(names...)
	c.SetParamValues(values...)
	return c
}

// withResolvers swaps the resolver table for the duration of a test, so a case
// can be stated without a database behind it.
func withResolvers(t *testing.T, resolvers []workspaceParamResolver) {
	t.Helper()
	original := workspaceParamResolvers
	workspaceParamResolvers = resolvers
	t.Cleanup(func() { workspaceParamResolvers = original })
}

// staticResolver answers with a fixed workspace for any value, or refuses.
func staticResolver(param string, ws uuid.UUID, ok bool) workspaceParamResolver {
	return workspaceParamResolver{
		params: []string{param},
		resolve: func(context.Context, workspaceResolverDeps, string) (uuid.UUID, bool) {
			return ws, ok
		},
	}
}

func TestResolveWorkspaceFromParams(t *testing.T) {
	wsA := uuid.New()
	wsB := uuid.New()

	t.Run("no scoped parameter at all", func(t *testing.T) {
		ws, named, agreed := resolveWorkspaceFromParams(
			routeParamCtx(map[string]string{"token": "abc"}), workspaceResolverDeps{})
		assert.False(t, named, "a route with no scoped parameter must not claim to name a tenant")
		assert.Equal(t, uuid.Nil, ws)
		// agreed is vacuously true; the caller gates on named && agreed.
		assert.True(t, agreed)
	})

	t.Run("one parameter resolves", func(t *testing.T) {
		withResolvers(t, []workspaceParamResolver{staticResolver("proj_id", wsA, true)})
		ws, named, agreed := resolveWorkspaceFromParams(
			routeParamCtx(map[string]string{"proj_id": uuid.NewString()}), workspaceResolverDeps{})
		assert.True(t, named)
		assert.True(t, agreed)
		assert.Equal(t, wsA, ws)
	})

	t.Run("one parameter resolves to nothing — refuse, do not skip", func(t *testing.T) {
		withResolvers(t, []workspaceParamResolver{staticResolver("proj_id", uuid.Nil, false)})
		ws, named, agreed := resolveWorkspaceFromParams(
			routeParamCtx(map[string]string{"proj_id": uuid.NewString()}), workspaceResolverDeps{})
		assert.True(t, named, "the parameter was present, so the route is scoped")
		assert.False(t, agreed, "an unresolvable id must not be treated as nothing to check")
		assert.Equal(t, uuid.Nil, ws)
	})

	t.Run("parent and child agree", func(t *testing.T) {
		withResolvers(t, []workspaceParamResolver{
			staticResolver("proj_id", wsA, true),
			staticResolver("status_id", wsA, true),
		})
		ws, named, agreed := resolveWorkspaceFromParams(routeParamCtx(map[string]string{
			"proj_id":   uuid.NewString(),
			"status_id": uuid.NewString(),
		}), workspaceResolverDeps{})
		assert.True(t, named)
		assert.True(t, agreed, "a parent and child in the same tenant must be allowed through")
		assert.Equal(t, wsA, ws)
	})

	// This is the bug. Before the fix the parent answered first and the child was
	// never consulted, so this case resolved to wsA and the caller was checked
	// against a workspace they legitimately own.
	t.Run("parent and child name different tenants", func(t *testing.T) {
		withResolvers(t, []workspaceParamResolver{
			staticResolver("proj_id", wsA, true),
			staticResolver("status_id", wsB, true),
		})
		ws, named, agreed := resolveWorkspaceFromParams(routeParamCtx(map[string]string{
			"proj_id":   uuid.NewString(),
			"status_id": uuid.NewString(),
		}), workspaceResolverDeps{})
		assert.True(t, named)
		assert.False(t, agreed, "a request naming two tenants must not resolve to either of them")
		assert.NotEqual(t, wsA, ws, "resolving to the parent is exactly the hole this closes")
		assert.Equal(t, uuid.Nil, ws)
	})

	t.Run("child resolves to nothing while the parent is fine", func(t *testing.T) {
		withResolvers(t, []workspaceParamResolver{
			staticResolver("proj_id", wsA, true),
			staticResolver("status_id", uuid.Nil, false),
		})
		_, named, agreed := resolveWorkspaceFromParams(routeParamCtx(map[string]string{
			"proj_id":   uuid.NewString(),
			"status_id": uuid.NewString(),
		}), workspaceResolverDeps{})
		assert.True(t, named)
		assert.False(t, agreed,
			"an unknown child id is what a probe looks like; the parent must not carry the request")
	})

	t.Run("alternate spellings are one parameter", func(t *testing.T) {
		// :proj_id and :project_id are two names for the same thing. Only the
		// first present is read, so a route cannot be made to resolve twice.
		withResolvers(t, []workspaceParamResolver{{
			params: []string{"proj_id", "project_id"},
			resolve: func(_ context.Context, _ workspaceResolverDeps, raw string) (uuid.UUID, bool) {
				assert.NotEmpty(t, raw)
				return wsA, true
			},
		}})
		ws, named, agreed := resolveWorkspaceFromParams(
			routeParamCtx(map[string]string{"project_id": uuid.NewString()}), workspaceResolverDeps{})
		assert.True(t, named)
		assert.True(t, agreed)
		assert.Equal(t, wsA, ws)
	})
}

// TestWorkspaceIDParamResolvesWithoutALookup pins the one resolver that answers
// from the request alone: a workspace names itself. An id that does not exist
// still "resolves", because refusing it here would answer 404-by-timing while the
// membership check refuses it anyway — and refuses it the same way as one that
// exists but is not the caller's.
func TestWorkspaceIDParamResolvesWithoutALookup(t *testing.T) {
	wsID := uuid.New()

	ws, named, agreed := resolveWorkspaceFromParams(
		routeParamCtx(map[string]string{"ws_id": wsID.String()}), workspaceResolverDeps{})
	assert.True(t, named)
	assert.True(t, agreed)
	assert.Equal(t, wsID, ws)

	t.Run("a malformed workspace id resolves to nothing", func(t *testing.T) {
		_, named, agreed := resolveWorkspaceFromParams(
			routeParamCtx(map[string]string{"ws_id": "not-a-uuid"}), workspaceResolverDeps{})
		assert.True(t, named)
		assert.False(t, agreed)
	})
}

// TestParamResolversFailClosedWithoutADatabase covers the wiring guard: a resolver
// that needs the database and does not have one must refuse rather than return a
// zero workspace that compares equal to another zero.
func TestParamResolversFailClosedWithoutADatabase(t *testing.T) {
	deps := workspaceResolverDeps{} // no db, no projectRepo

	for _, r := range workspaceParamResolvers {
		param := r.params[0]
		if param == "ws_id" {
			continue // answers from the request alone, by design (see above)
		}
		t.Run(param, func(t *testing.T) {
			_, ok := r.resolve(context.Background(), deps, uuid.NewString())
			assert.False(t, ok, ":%s resolved a workspace with no database behind it", param)
		})
	}
}

// TestWorkspaceScopedParamsIncludesTheCompositeChildren states the concrete
// outcome of this change in terms a reader can check against the router: the
// child of every composite route is now a parameter the guard fires on.
func TestWorkspaceScopedParamsIncludesTheCompositeChildren(t *testing.T) {
	listed := make(map[string]bool, len(WorkspaceScopedParams))
	for _, p := range WorkspaceScopedParams {
		listed[p] = true
	}

	for _, param := range []string{
		"status_id",       // PATCH  /projects/:proj_id/statuses/:status_id
		"auto_rule_id",    // DELETE /projects/:proj_id/auto-transition-rules/:auto_rule_id
		"dep_id",          // DELETE /tasks/:task_id/dependencies/:dep_id
		"invite_id",       // DELETE /workspaces/:ws_id/invites/:invite_id
		"member_agent_id", // DELETE /projects/:proj_id/members/agents/:member_agent_id
		"artifact_id",     // GET    /tasks/:task_id/artifacts/:artifact_id/download
	} {
		assert.True(t, listed[param],
			":%s is the child of a composite route but the guard does not fire on it", param)
	}

	// :rule_id must still be the governance rule and nothing else — the rename to
	// :auto_rule_id exists precisely so one spelling does not name two tables.
	require.True(t, listed["rule_id"])
	var ruleQuery string
	for _, r := range workspaceObjectResolvers {
		if r.param == "rule_id" {
			ruleQuery = r.query
		}
	}
	assert.Contains(t, ruleQuery, "FROM rules",
		":rule_id must resolve against the governance rules table")
	assert.NotContains(t, ruleQuery, "auto_transition_rules")
}
