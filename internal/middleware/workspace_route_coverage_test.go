package middleware

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
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
// It requires EVERY parameter of a route to be one the middleware checks, not just
// one of them, and that is the whole difference between this version and the one
// that shipped with the previous fix. "At least one resolves" is satisfied by the
// parent id of any composite route, and the parent is always the caller's own:
// PATCH /projects/:proj_id/statuses/:status_id resolved :proj_id, the guard
// checked the caller against their own project, and the id that decided which row
// got written — :status_id — was never looked at by anything. So a member of any
// workspace renamed another tenant's workflow status and got 200 for it, with this
// test green the entire time. The same silence covered
// DELETE /projects/:proj_id/auto-transition-rules/:rule_id,
// DELETE /tasks/:task_id/dependencies/:dep_id and
// DELETE|POST /workspaces/:ws_id/invites/:invite_id.
//
// A route that genuinely cannot resolve a parameter — /memories/:id, checked in
// its handler — has to be written down in workspaceScopeHandlerCheckedRoutes with
// the reason, which makes adding one a decision somebody reviews instead of an
// omission nobody sees.
func TestEveryIdentifiedRouteIsWorkspaceScoped(t *testing.T) {
	routes := authenticatedRoutes(t)
	require.Greater(t, len(routes), 100,
		"only %d routes discovered in cmd/api/main.go — the registration style has probably "+
			"changed and this test is no longer reading the real router", len(routes))

	scoped := make(map[string]bool, len(WorkspaceScopedParams))
	for _, p := range WorkspaceScopedParams {
		scoped[p] = true
	}

	unguarded := routesWithUncheckedParams(routes, scoped)

	assert.Empty(t, unguarded,
		"these routes name an object by an id that nothing in the middleware resolves or contains, "+
			"so RequireWorkspaceMemberScoped never checks it and a caller can pair their own parent id "+
			"with another tenant's child id. Add a resolver to workspaceParamResolvers, a containment "+
			"query to workspaceScopedPairParams, or — if the handler really does check the tenant "+
			"itself — record the route and the reason in workspaceScopeHandlerCheckedRoutes:\n  %s",
		strings.Join(unguarded, "\n  "))
}

var paramCollectionAliases = map[string]bool{
	// POST /me/mentions/:comment_id/seen marks the caller's own mention row for a
	// comment, so :comment_id there is a comments.id like everywhere else — the
	// collection segment is "mentions" only because the row being written is the
	// mention, not the comment. The resolver is correct for it.
	"/me/mentions/:comment_id/seen": true,
}

// TestScopedParamNamesAreUnambiguous keeps one parameter name from meaning two
// different objects.
//
// Resolvers are keyed on the parameter name alone, so a name shared by two route
// families sends one family's ids to the other's table. That was live:
// /projects/:proj_id/auto-transition-rules/:rule_id spelled its id :rule_id, the
// same as /rules/:rule_id, whose resolver reads the unrelated `rules` table. The
// collision was invisible while the guard stopped at the first parameter that
// resolved — and would have refused every legitimate auto-transition request the
// moment it stopped. The route now spells it :atr_id.
//
// The property checked is that a parameter is always preceded by the same literal
// path segment, which is what "names the same kind of object" looks like in a REST
// path.
func TestScopedParamNamesAreUnambiguous(t *testing.T) {
	scoped := make(map[string]bool, len(WorkspaceScopedParams))
	for _, p := range WorkspaceScopedParams {
		scoped[p] = true
	}

	owners := map[string]map[string]bool{}
	for _, path := range authenticatedRoutes(t) {
		segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
		for i, seg := range segs {
			name := strings.TrimPrefix(seg, ":")
			if !strings.HasPrefix(seg, ":") || !scoped[name] || i == 0 {
				continue
			}
			if paramCollectionAliases[path] {
				continue
			}
			if owners[name] == nil {
				owners[name] = map[string]bool{}
			}
			owners[name][segs[i-1]] = true
		}
	}

	for name, collections := range owners {
		if len(collections) <= 1 {
			continue
		}
		var got []string
		for c := range collections {
			got = append(got, c)
		}
		sort.Strings(got)
		assert.Failf(t, "ambiguous route parameter",
			":%s names an object under %v — one resolver cannot answer for both, so ids of one "+
				"kind are looked up in the other's table. Give one of them its own parameter name.",
			name, got)
	}
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
	for _, r := range workspaceParamResolvers {
		assert.True(t, scoped[r.param],
			"WorkspaceRLS resolves a workspace from :%s but the guard does not fire on it", r.param)
		assert.NotNil(t, r.resolve, "resolver for :%s has no lookup", r.param)
	}
	for _, p := range workspaceScopedPairParams {
		assert.True(t, scoped[p.param],
			"the guard contains :%s to a workspace but does not fire on it", p.param)
		assert.NotEmpty(t, p.query, "containment check for :%s has no query", p.param)
	}
	assert.True(t, scoped["short"], "the short task id resolver is not guarded")
}

// TestBodyTenantFieldsAreDeclared closes the half of the class no test that reads
// the router can see.
//
// A handler that binds workspace_id or project_id out of the JSON body names a
// tenant the router never mentions. RequireWorkspaceMemberScoped resolves nothing
// from such a path and passes it through as if it were /auth/me — which is how
// POST /rules/evaluate shipped returning another tenant's private rule names to
// any authenticated caller who pasted their workspace id into the body.
//
// So the fields themselves are enumerated out of the handler sources and each one
// has to appear below with a verdict. A new tenant-shaped field in a request
// struct fails this test until somebody writes down which of the three it is:
// guarded by middleware.RequireBodyWorkspace, checked inside the handler, or
// harmless because the query it feeds is already scoped to the caller.
func TestBodyTenantFieldsAreDeclared(t *testing.T) {
	found := bodyTenantFields(t)
	require.NotEmpty(t, found, "no request-body tenant fields discovered — has the struct style changed?")

	var undeclared []string
	for _, f := range found {
		if _, ok := declaredBodyTenantFields[f]; !ok {
			undeclared = append(undeclared, f)
		}
	}
	sort.Strings(undeclared)
	assert.Empty(t, undeclared,
		"these request structs take a tenant-identifying id from the request body, where the "+
			"route parameters the workspace guard reads cannot see it. Guard the route with "+
			"middleware.RequireBodyWorkspace, or check the caller against it in the handler, then "+
			"record it in declaredBodyTenantFields with which one you did:\n  %s",
		strings.Join(undeclared, "\n  "))

	// And the other direction: a declaration that no longer matches a field would
	// silently excuse the next struct that took its name.
	present := make(map[string]bool, len(found))
	for _, f := range found {
		present[f] = true
	}
	for f := range declaredBodyTenantFields {
		assert.True(t, present[f], "%s is declared but no longer exists", f)
	}
}

// declaredBodyTenantFields is the reviewed verdict on every tenant-identifying id
// that arrives in a request body. The key is "<file>:<struct>.<json field>".
var declaredBodyTenantFields = map[string]string{
	// Guarded by middleware.RequireBodyWorkspace on the route.
	"rule_handler.go:evaluateRuleRequest.workspace_id":              "guarded: bodyWS on POST /rules/evaluate",
	"notification_handler.go:updatePreferencesRequest.workspace_id": "guarded: bodyWS on PUT /notifications/preferences",
	"rule_handler.go:evaluateRuleRequest.project_id":                "guarded: bodyWS resolves it and requires it to agree with workspace_id",
	"rule_handler.go:evaluateRuleRequest.task_id":                   "benign: only narrows an evaluation already scoped to the guarded workspace",

	// Checked in the handler, or in the service it calls.
	"memory_handler.go:rememberRequest.workspace_id":                   "checked: MemoryHandler.requireWorkspaceID -> workspaceAllowed",
	"memory_handler.go:rememberRequest.project_id":                     "checked: same call, the project is narrowed inside the allowed workspace",
	"spark_handler.go:installRequest.workspace_id":                     "checked: SparkHandler.Install -> memberRepo.GetRole + PermRegisterAgent",
	"dependency_handler.go:createDependencyRequest.depends_on_task_id": "checked: taskDependencyService.Create requires the same project as :task_id",
	"initiative_handler.go:linkProjectRequest.project_id":              "checked: initiativeService.LinkProject requires the initiative's own workspace",
	"task_handler.go:moveToProjectRequest.project_id":                  "checked: taskService.MoveToProject requires the task's own workspace",
	"project_member_handler.go:addAgentMemberRequest.agent_id":         "checked: projectMemberService.AddAgentMember requires the project's own workspace",

	// Scoped by something other than the id in the body: the row these end up on is
	// already pinned to a tenant the guard checked, and the id is stored as an
	// opaque reference that is never resolved across the boundary.
	"event_handler.go:createEventRequest.task_id":                 "benign: a reference on an event already scoped to the guarded :proj_id",
	"agent_handler.go:CreateAgentActivity.task_id":                "benign: a reference on a log row whose workspace_id is the guarded :agent_id's own",
	"agent_handler.go:reportSessionRequest.task_id":               "benign: a reference on the calling agent's own session",
	"rule_handler.go:createRuleRequest.agent_id":                  "benign: narrows who a rule applies to, inside the guarded :ws_id/:proj_id it is created in",
	"project_integration_handler.go:teamRelayResponse.project_id": "benign: a response struct, never bound from a request",
}

// bodyTenantFieldNames are the json field names that identify a tenant, or an
// object that belongs to one, when they arrive in a request body.
var bodyTenantFieldNames = map[string]bool{
	"workspace_id":       true,
	"project_id":         true,
	"task_id":            true,
	"depends_on_task_id": true,
	"initiative_id":      true,
	"agent_id":           true,
}

// bodyTenantFields reads the handler package with the Go parser and returns every
// "<file>:<struct>.<json field>" that carries a tenant-identifying id.
//
// It parses rather than greps because the shape that hides best from a grep is the
// one that hides best from review: an anonymous `var req struct { ... }` declared
// inside the handler function, which has no type name to search for. Those are
// named here after the function that declares them.
func bodyTenantFields(t *testing.T) []string {
	t.Helper()

	handlerDir := filepath.Join("..", "handler")

	entries, err := os.ReadDir(handlerDir)
	require.NoError(t, err, "cannot read the handler package")

	fset := token.NewFileSet()
	var found []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, filepath.Join(handlerDir, name), nil, 0)
		require.NoError(t, parseErr, "cannot parse %s", name)

		// Innermost enclosing name for whatever struct we are looking at.
		var enclosing []string
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.TypeSpec:
				enclosing = append(enclosing, node.Name.Name)
			case *ast.FuncDecl:
				enclosing = append(enclosing, node.Name.Name)
			case *ast.StructType:
				owner := "anonymous"
				if len(enclosing) > 0 {
					owner = enclosing[len(enclosing)-1]
				}
				for _, f := range node.Fields.List {
					if f.Tag == nil {
						continue
					}
					tag, unquoteErr := strconv.Unquote(f.Tag.Value)
					if unquoteErr != nil {
						continue
					}
					jsonName, _, _ := strings.Cut(reflect.StructTag(tag).Get("json"), ",")
					if bodyTenantFieldNames[jsonName] {
						found = append(found, fmt.Sprintf("%s:%s.%s", name, owner, jsonName))
					}
				}
			}
			return true
		})
	}
	sort.Strings(found)
	return found
}

// routesWithUncheckedParams returns every authenticated route carrying a path
// parameter that scoped does not cover, formatted for the failure message.
func routesWithUncheckedParams(routes []string, scoped map[string]bool) []string {
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
		var unchecked []string
		for _, p := range params {
			if !scoped[p] {
				unchecked = append(unchecked, ":"+p)
			}
		}
		if len(unchecked) > 0 {
			unguarded = append(unguarded, fmt.Sprintf("%s (unchecked: %s)", path, strings.Join(unchecked, ", ")))
		}
	}
	sort.Strings(unguarded)
	return unguarded
}

// childParamsAddedByThisFix are the parameters that had no resolver and no
// containment check when the composite holes were live.
var childParamsAddedByThisFix = map[string]bool{
	"status_id":       true,
	"atr_id":          true,
	"dep_id":          true,
	"invite_id":       true,
	"member_agent_id": true,
	"user_id":         true,
}

// TestEveryIdentifiedRouteIsWorkspaceScoped_CatchesTheRoutesThatShipped is the
// test for the test.
//
// The previous version of the invariant asked whether ANY parameter of a route
// resolved a workspace, and every composite route satisfied it through its parent
// id while its child id addressed any tenant's row. So "the invariant test passes"
// meant nothing for exactly the routes that were broken, and there is no way to
// tell a strengthened invariant from a decorative one except by running it against
// the state it was supposed to catch.
//
// This runs the current check with the parameters the fix added taken back out —
// the middleware as it stood in production — and requires it to name the routes
// that were reported. If someone weakens the check back to "any parameter", this
// stops finding them and fails.
func TestEveryIdentifiedRouteIsWorkspaceScoped_CatchesTheRoutesThatShipped(t *testing.T) {
	beforeTheFix := map[string]bool{}
	for _, p := range WorkspaceScopedParams {
		if !childParamsAddedByThisFix[p] {
			beforeTheFix[p] = true
		}
	}
	// :rule_id covered the auto-transition route by name collision alone — the
	// resolver behind it reads the unrelated `rules` table. Renaming that route's
	// parameter to :atr_id is what turned it into a real check.
	beforeTheFix["rule_id"] = true

	flagged := routesWithUncheckedParams(authenticatedRoutes(t), beforeTheFix)
	joined := strings.Join(flagged, "\n  ")

	for _, reported := range []string{
		"/projects/:proj_id/statuses/:status_id",
		"/projects/:proj_id/auto-transition-rules/:atr_id",
		"/tasks/:task_id/dependencies/:dep_id",
		"/workspaces/:ws_id/invites/:invite_id",
		"/workspaces/:ws_id/invites/:invite_id/resend",
		"/workspaces/:ws_id/members/:user_id",
		"/projects/:proj_id/members/:user_id",
		"/projects/:proj_id/members/agents/:member_agent_id",
	} {
		assert.Contains(t, joined, reported,
			"the invariant does not catch %s, which was reachable across tenants in production — "+
				"it has been weakened back to \"at least one parameter resolves\"", reported)
	}
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
