package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// MCP-OAuth sec (#266eae6a). RequirePermission clamps an OAuth connector (mot_
// token) to its consenting user's workspace role, but only on routes registered
// behind rbac(). Every other route under agent-auth ran the connector with no
// role check at all — including PATCH /projects/:id, the status routes and the
// memory writes — so a viewer, who holds no write permission, could consent once
// and then write through a connector.
//
// This test is what keeps that from coming back. It reads the real route
// registrations out of main.go (an AST walk, not a grep: a guard that is renamed,
// reordered, or mentioned only in a comment does not count) and fails when a
// route that can change state is neither behind a role guard nor in the reviewed
// allow-list below.
//
// Read routes (GET/HEAD) are out of scope on purpose: what a role may READ is
// the same for a viewer connector as for that viewer in the web app, and the
// tenant boundary on them is enforced group-wide by WorkspaceRLS and
// RequireWorkspaceMemberScoped (see tenancy_wiring_test.go).

// auditGuardCalls are the middleware constructors that put a role bar (or a
// human-only bar) on a route. Keys are the callee as written in main.go.
var auditGuardCalls = map[string]string{
	"rbac":                                "rbac",
	"connectorRBAC":                       "connector-clamp",
	"connectorSelfOrRBAC":                 "connector-clamp",
	"mw.RequirePermission":                "rbac",
	"mw.RequireSelfOrPermission":          "rbac",
	"mw.RequireConnectorPermission":       "connector-clamp",
	"mw.RequireConnectorSelfOrPermission": "connector-clamp",
	"mw.RequireUserAuth":                  "human-only",
}

// auditAllowList holds the state-changing routes that carry no role guard, each
// with the reason that is safe for a connector whose user is a viewer. An entry
// is a claim a reviewer can check against the handler, not a waiver: if the
// reason stops being true, the entry has to go and the route needs a guard.
var auditAllowList = map[string]string{
	"PATCH /auth/me":    "handler resolves the caller with GetUserID; an agent has no user identity and gets 401",
	"POST /auth/logout": "handler resolves the caller with GetUserID; an agent has no user identity",
	"POST /workspaces":  "handler refuses agents outright (403 \"agents cannot create workspaces\")",

	"POST /documents/:doc_id/resolve-anchor": "read-only computation: resolves a text anchor against the markdown and writes nothing",
	"PUT /documents/:doc_id/watch":           "the caller's own watch subscription on a document it can already read; no shared state",
	"DELETE /documents/:doc_id/watch":        "the caller's own watch subscription; removing it affects nobody else",

	"PATCH /agents/me":                "acts on the caller's own agent record only (route has no id); the handler refuses a callback_url from a connector (server-side request to a caller-chosen address), the rest is cosmetic self-description",
	"POST /agents/me/sessions/report": "the caller's own usage telemetry; route has no id and cannot name another agent",
	"POST /agents/heartbeat":          "the caller's own presence; route has no id and cannot name another agent",

	"POST /projects/:proj_id/views": "saved_views.created_by is NOT NULL REFERENCES users(id) and an agent's caller id is the nil UUID, so an agent cannot create one",
	"PATCH /views/:view_id":         "service refuses unless view.CreatedBy == caller; an agent's caller id is the nil UUID and matches no view",
	"DELETE /views/:view_id":        "service refuses unless view.CreatedBy == caller; an agent's caller id is the nil UUID and matches no view",

	"POST /rules/evaluate": "dry-run: returns the violations a change would raise and applies nothing",

	"POST /notifications/mark-read":              "keyed on the caller's user_id, which an agent does not have; touches no one else's notifications",
	"PUT /notifications/preferences":             "keyed on the caller's user_id, which an agent does not have",
	"DELETE /notifications/preferences/:pref_id": "keyed on the caller's user_id, which an agent does not have",
	"POST /me/push-subscriptions":                "keyed on the caller's user_id, which an agent does not have",
	"DELETE /me/push-subscriptions":              "keyed on the caller's user_id, which an agent does not have",
	"POST /me/mentions/:comment_id/seen":         "marks the caller's own mention row as seen; another actor's rows are not addressable",
	"POST /me/document-mentions/:dcom_id/seen":   "marks the caller's own mention row as seen; another actor's rows are not addressable",

	"POST /spark/agents/:agent_id/install": "handler calls requireRegisterAgent for the target workspace; PermRegisterAgent is absent from agentPerms, so no agent identity passes",
}

// auditConnectorGuards pins WHICH guard each connector-clamped route carries.
// The presence check above cannot tell connectorRBAC(PermManageProject) from
// connectorRBAC(PermAddComment); swapping a bar for a weaker one would pass it.
// Changing an entry here is the review moment for that decision.
var auditConnectorGuards = map[string]string{
	"PATCH /projects/:proj_id":                     "connectorRBAC(mw.PermManageProject)",
	"POST /projects/:proj_id/statuses":             "connectorRBAC(mw.PermManageProject)",
	"PATCH /projects/:proj_id/statuses/:status_id": "connectorRBAC(mw.PermManageProject)",
	"PUT /projects/:proj_id/statuses/reorder":      "connectorRBAC(mw.PermManageProject)",
	"POST /tasks/:task_id/checkout":                "connectorRBAC(mw.PermUpdateTask)",
	"DELETE /tasks/:task_id/checkout":              "connectorRBAC(mw.PermUpdateTask)",
	"PATCH /tasks/:task_id/checkout":               "connectorRBAC(mw.PermUpdateTask)",
	"POST /projects/:proj_id/updates":              "connectorRBAC(mw.PermAddComment)",
	"POST /projects/:proj_id/knowledge":            "connectorRBAC(mw.PermWriteMemory)",
	"POST /memories":                               "connectorRBAC(mw.PermWriteMemory)",
	"POST /memories/import":                        "connectorRBAC(mw.PermWriteMemory)",
	"DELETE /memories/:id":                         "connectorRBAC(mw.PermWriteMemory)",
	// /memories/reindex, /backfill-chunks, /rechunk-stale, /backfill-doc-index
	// moved to rbac(mw.PermMemoryIndex) (task 440358b6): connectorRBAC left a
	// plain human JWT, including a viewer, untouched on these routes.
	"POST /agents/:agent_id/activity": "connectorSelfOrRBAC(\"agent_id\", mw.PermDeleteAgent)",
}

// auditPublicWrites are the state-changing routes registered OUTSIDE the
// agent-auth group `api`. None of them runs behind DualAuth, so a connector token
// is not what authenticates them; the list exists so that a new write route added
// to `e`, `v1` or a sub-group cannot go unnoticed just because the walk only
// looks at `api`. Key: "<receiver> <METHOD> <path literal>".
var auditPublicWrites = map[string]string{
	"registerGroup POST /register":               "public sign-up: creates a new user, no existing identity involved",
	"loginGroup POST /login":                     "public login: authenticated by the credentials in the body",
	"refreshGroup POST /refresh":                 "public: authenticated by the refresh token itself",
	"invitePublicGroup POST /:token/accept":      "public: authenticated by the single-use invite token in the path",
	"e POST /webhooks/github":                    "authenticated by the GitHub HMAC signature, not a session",
	"e POST /api/v1/integrations/github/webhook": "authenticated by the GitHub HMAC signature, not a session",
	"e POST /webhooks/gitlab":                    "authenticated by the GitLab secret token header, not a session",
	"e POST /internal/secrets/materialize":       "DirectOnly plus SpawnAuth: reachable only by the spawner, never with a bearer token",
}

// auditRoute is one api.<METHOD>("<path>", handler, middleware...) registration.
type auditRoute struct {
	method string
	path   string
	// guard is "" when the route has no role guard, otherwise the kind of guard
	// (rbac, connector-clamp, human-only).
	guard string
	// mws are the middleware arguments as written, for the printed table.
	mws []string
}

func (r auditRoute) key() string { return r.method + " " + r.path }

func calleeName(e ast.Expr) string {
	switch f := e.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if x, ok := f.X.(*ast.Ident); ok {
			return x.Name + "." + f.Sel.Name
		}
	}
	return ""
}

// parseAuditRoutes returns every registration on the agent-auth group `api`.
// It fails the test if main.go registers routes on that group in a form this
// walk cannot read (Any, Add, Match, Group, Static, File): such a route would be
// invisible to the guard below, which is worse than a failing test.
func parseAuditRoutes(t *testing.T) []auditRoute {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	var routes []auditRoute
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok || recv.Name != "api" {
			return true
		}
		switch sel.Sel.Name {
		case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD":
		case "Any", "Add", "Match", "Group", "Static", "File", "RouteNotFound", "CONNECT", "OPTIONS", "TRACE", "PROPFIND", "REPORT":
			t.Errorf("main.go:%d registers a route on the agent-auth group with api.%s(...), which the RBAC route audit cannot read; "+
				"use api.GET/POST/PUT/PATCH/DELETE or extend parseAuditRoutes",
				fset.Position(call.Pos()).Line, sel.Sel.Name)
			return true
		default:
			return true
		}
		if len(call.Args) < 2 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			t.Errorf("main.go:%d: api.%s path is not a string literal; the RBAC route audit cannot read it",
				fset.Position(call.Pos()).Line, sel.Sel.Name)
			return true
		}
		path, uerr := strconv.Unquote(lit.Value)
		if uerr != nil {
			t.Fatalf("unquote %s: %v", lit.Value, uerr)
		}

		r := auditRoute{method: sel.Sel.Name, path: path}
		for _, arg := range call.Args[2:] {
			r.mws = append(r.mws, exprString(arg))
			if c, isCall := arg.(*ast.CallExpr); isCall {
				if kind, isGuard := auditGuardCalls[calleeName(c.Fun)]; isGuard && r.guard == "" {
					r.guard = kind
				}
			}
		}
		routes = append(routes, r)
		return true
	})
	return routes
}

func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	case *ast.CallExpr:
		args := make([]string, len(v.Args))
		for i, a := range v.Args {
			args[i] = exprString(a)
		}
		return exprString(v.Fun) + "(" + strings.Join(args, ", ") + ")"
	case *ast.BasicLit:
		return v.Value
	}
	return "?"
}

func isWrite(method string) bool {
	switch method {
	case "POST", "PUT", "PATCH", "DELETE":
		return true
	}
	return false
}

// TestRBACRouteAudit_EveryWriteRouteHasARoleGuardOrAReviewedReason is the guard
// test: a new state-changing route under agent-auth with no rbac(), no connector
// clamp, no human-only bar and no allow-list entry fails here.
func TestRBACRouteAudit_EveryWriteRouteHasARoleGuardOrAReviewedReason(t *testing.T) {
	routes := parseAuditRoutes(t)

	// If the registration style changed and the walk reads almost nothing, every
	// assertion below would pass vacuously. main.go has ~270 routes on this group.
	if len(routes) < 200 {
		t.Fatalf("only %d routes read from main.go; the walk has stopped seeing the real registrations", len(routes))
	}
	guarded := 0
	for _, r := range routes {
		if r.guard == "rbac" {
			guarded++
		}
	}
	if guarded < 80 {
		t.Fatalf("only %d routes read as rbac()-guarded; the guard detection has stopped matching main.go", guarded)
	}

	var unguarded []string
	for _, r := range routes {
		if !isWrite(r.method) || r.guard != "" {
			continue
		}
		if _, ok := auditAllowList[r.key()]; ok {
			continue
		}
		unguarded = append(unguarded, r.key())
	}
	sort.Strings(unguarded)
	if len(unguarded) > 0 {
		t.Errorf("%d state-changing route(s) under agent-auth have no role guard and no allow-list entry:\n  %s\n\n"+
			"An OAuth connector reaches these with whatever the route itself enforces, so a viewer who consents once can write through it.\n"+
			"Add rbac(<perm>) if humans and trusted agents should be held to the same bar, connectorRBAC(<perm>) to clamp only connectors,\n"+
			"mw.RequireUserAuth() if the route is human-only, or — only if the handler already makes it safe for a connector\n"+
			"whose user is a viewer — an entry in auditAllowList with the reason.",
			len(unguarded), strings.Join(unguarded, "\n  "))
	}
}

// TestRBACRouteAudit_AllowListHasNoStaleOrRedundantEntries stops the allow-list
// from rotting into a list of waivers: an entry for a route that no longer exists,
// or that has since been given a guard, is dead weight that makes the next real
// entry look routine.
func TestRBACRouteAudit_AllowListHasNoStaleOrRedundantEntries(t *testing.T) {
	byKey := map[string]auditRoute{}
	for _, r := range parseAuditRoutes(t) {
		byKey[r.key()] = r
	}
	for key, reason := range auditAllowList {
		r, ok := byKey[key]
		switch {
		case !ok:
			t.Errorf("allow-list entry %q matches no route in main.go — remove it", key)
		case !isWrite(r.method):
			t.Errorf("allow-list entry %q is a read route; only state-changing routes are audited — remove it", key)
		case r.guard != "":
			t.Errorf("allow-list entry %q is redundant: the route already has a %s guard — remove it", key, r.guard)
		}
		if len(strings.TrimSpace(reason)) < 30 {
			t.Errorf("allow-list entry %q has no real reason (%q); state why it is safe for a connector whose user is a viewer", key, reason)
		}
	}
}

// TestRBACRouteAudit_PrintTable prints the route table the MR carries. Run with
//
//	MESH_ROUTE_AUDIT_TABLE=1 go test ./cmd/api -run RouteAudit_PrintTable -v
//
// It asserts nothing; it exists so the table in the MR is generated, not typed.
func TestRBACRouteAudit_PrintTable(t *testing.T) {
	if os.Getenv("MESH_ROUTE_AUDIT_TABLE") == "" {
		t.Skip("set MESH_ROUTE_AUDIT_TABLE=1 to print the table")
	}
	routes := parseAuditRoutes(t)
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].path == routes[j].path {
			return routes[i].method < routes[j].method
		}
		return routes[i].path < routes[j].path
	})
	var b strings.Builder
	fmt.Fprintln(&b, "\n| method | path | treatment | note |")
	fmt.Fprintln(&b, "|---|---|---|---|")
	for _, r := range routes {
		treatment, note := r.guard, ""
		switch {
		case !isWrite(r.method):
			treatment = "read"
		case r.guard == "":
			treatment, note = "allow-list", auditAllowList[r.key()]
		default:
			note = strings.Join(r.mws, " ")
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", r.method, r.path, treatment, note)
	}
	t.Log(b.String())
}

// TestRBACRouteAudit_ConnectorClampsCarryTheReviewedPermission fails when a
// clamped route loses its guard, gets a different one, or a new clamped route is
// added without being recorded in auditConnectorGuards.
func TestRBACRouteAudit_ConnectorClampsCarryTheReviewedPermission(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range parseAuditRoutes(t) {
		if r.guard != "connector-clamp" {
			continue
		}
		seen[r.key()] = true
		want, ok := auditConnectorGuards[r.key()]
		if !ok {
			t.Errorf("%s is connector-clamped but not recorded in auditConnectorGuards — record the permission it should carry", r.key())
			continue
		}
		found := false
		for _, m := range r.mws {
			if m == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s should carry %s, has middleware %v", r.key(), want, r.mws)
		}
	}
	for key := range auditConnectorGuards {
		if !seen[key] {
			t.Errorf("auditConnectorGuards lists %s, but that route is not connector-clamped in main.go", key)
		}
	}
}

// TestRBACRouteAudit_NoUnreviewedWriteOutsideTheAuthenticatedGroup closes the
// blind spot of reading only `api`: a write registered on `e`, `v1` or a
// sub-group with its own auth would never be seen by the tests above.
func TestRBACRouteAudit_NoUnreviewedWriteOutsideTheAuthenticatedGroup(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	found := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok || recv.Name == "api" || !isWrite(sel.Sel.Name) || len(call.Args) < 2 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		path, _ := strconv.Unquote(lit.Value)
		found[recv.Name+" "+sel.Sel.Name+" "+path] = true
		return true
	})
	if len(found) < len(auditPublicWrites) {
		t.Fatalf("read %d non-api write routes but %d are expected — the walk has stopped seeing them", len(found), len(auditPublicWrites))
	}
	for key := range found {
		if _, ok := auditPublicWrites[key]; !ok {
			t.Errorf("%q is a state-changing route outside the agent-auth group and is not in auditPublicWrites — "+
				"say how it is authenticated, or move it under `api` so the role audit covers it", key)
		}
	}
	for key := range auditPublicWrites {
		if !found[key] {
			t.Errorf("auditPublicWrites lists %q but main.go no longer registers it — remove it", key)
		}
	}
}
