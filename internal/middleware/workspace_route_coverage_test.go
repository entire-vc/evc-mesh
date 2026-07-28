package middleware

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// routeRegistration matches the authenticated API group's route registrations in
// cmd/api/main.go. Only api.* is read: the public groups (/auth, /invites) and the
// bare e.* routes are unauthenticated by design and have their own tests.
var routeRegistration = regexp.MustCompile(`api\.(GET|POST|PUT|PATCH|DELETE)\("(/[^"]*)"`)

// TestEveryIdentifiedRouteIsWorkspaceScoped closes the class of bug that
// WorkspaceScopedParams alone cannot see.
//
// TestWorkspaceScopedParams_CoversEveryResolver checks the guard against the
// resolver — that every parameter WorkspaceRLS resolves is one the guard fires on.
// It cannot see the opposite failure: a route naming tenant-owned data through a
// parameter that no resolver reads. That route is not "unguarded" in any way the
// middleware can detect; it simply looks like /auth/me, a route with nothing to
// check. That is exactly how GET /events/:event_id shipped returning another
// tenant's task titles to any logged-in stranger, and it was not alone —
// /comments/:comment_id, /views/:view_id, /webhooks/:webhook_id,
// /integrations/:int_id, /templates/:tmpl_id, /rules/:rule_id, /vcs-links/:link_id,
// /recurring/:id and /tasks/by-short-id/:short were the same shape.
//
// So this test reads the router rather than the middleware, and requires of every
// authenticated route carrying a path parameter that at least one of its
// parameters resolves a workspace. A route that genuinely cannot — /memories/:id,
// checked in its handler — has to be written down in
// workspaceScopeHandlerCheckedRoutes with the reason, which makes adding one a
// decision somebody reviews instead of an omission nobody sees.
func TestEveryIdentifiedRouteIsWorkspaceScoped(t *testing.T) {
	routes := authenticatedRoutes(t)
	require.Greater(t, len(routes), 100,
		"only %d routes discovered in cmd/api/main.go — the registration style has probably "+
			"changed and this test is no longer reading the real router", len(routes))

	scoped := make(map[string]bool, len(WorkspaceScopedParams))
	for _, p := range WorkspaceScopedParams {
		scoped[p] = true
	}

	var unguarded []string
	for _, path := range routes {
		params := pathParams(path)
		if len(params) == 0 {
			// No object is named, so there is no other tenant's object to reach.
			continue
		}
		if workspaceScopeHandlerCheckedRoutes["/api/v1"+path] {
			continue
		}
		covered := false
		for _, p := range params {
			if scoped[p] {
				covered = true
				break
			}
		}
		if !covered {
			unguarded = append(unguarded, fmt.Sprintf("%s (params: %s)", path, strings.Join(params, ", ")))
		}
	}
	sort.Strings(unguarded)

	assert.Empty(t, unguarded,
		"these routes name an object by an id that WorkspaceRLS cannot resolve a workspace from, "+
			"so RequireWorkspaceMemberScoped never runs on them and any logged-in stranger reaches "+
			"another tenant's data. Add a resolver to workspaceObjectResolvers, or — if the handler "+
			"really does check the tenant itself — record the route and the reason in "+
			"workspaceScopeHandlerCheckedRoutes:\n  %s", strings.Join(unguarded, "\n  "))
}

// TestWorkspaceScopeHandlerCheckedRoutesAreReal keeps the exception list from
// outliving the routes it excuses: a stale entry would silently excuse a future
// route registered at the same path.
func TestWorkspaceScopeHandlerCheckedRoutesAreReal(t *testing.T) {
	registered := make(map[string]bool)
	for _, path := range authenticatedRoutes(t) {
		registered["/api/v1"+path] = true
	}
	for path := range workspaceScopeHandlerCheckedRoutes {
		assert.True(t, registered[path],
			"%s is excused from the workspace guard but is no longer a registered route", path)
	}
}

// TestWorkspaceObjectResolverParamsAreScoped states the property the derivation in
// WorkspaceScopedParams is there to guarantee, so that replacing the derivation
// with a hand-written list would fail rather than quietly reopen the hole.
func TestWorkspaceObjectResolverParamsAreScoped(t *testing.T) {
	scoped := make(map[string]bool, len(WorkspaceScopedParams))
	for _, p := range WorkspaceScopedParams {
		scoped[p] = true
	}
	for _, r := range workspaceObjectResolvers {
		assert.True(t, scoped[r.param],
			"WorkspaceRLS resolves a workspace from :%s but the guard does not fire on it", r.param)
		assert.NotEmpty(t, r.query, "resolver for :%s has no query", r.param)
	}
	assert.True(t, scoped["short"], "the short task id resolver is not guarded")
}

// authenticatedRoutes returns the deduplicated paths registered on the
// authenticated API group, read from the router itself so that a route added
// later is proven — not assumed — to be covered.
func authenticatedRoutes(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("../../cmd/api/main.go")
	require.NoError(t, err, "cannot read route registrations")

	seen := map[string]bool{}
	var paths []string
	for _, m := range routeRegistration.FindAllStringSubmatch(string(src), -1) {
		if seen[m[2]] {
			continue
		}
		seen[m[2]] = true
		paths = append(paths, m[2])
	}
	sort.Strings(paths)
	return paths
}

// pathParams returns the names of the path parameters in an Echo route pattern.
func pathParams(path string) []string {
	var params []string
	for _, seg := range strings.Split(path, "/") {
		if strings.HasPrefix(seg, ":") {
			params = append(params, strings.TrimPrefix(seg, ":"))
		}
	}
	return params
}
