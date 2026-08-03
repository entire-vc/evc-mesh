package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// routeGate describes one registered route and the guard identifiers applied to it.
type routeGate struct {
	method string
	path   string
	guards []string
}

// parseRouteGates reads main.go and returns every `api.<METHOD>("<path>", handler, guards...)`
// registration with the *identifier names* of the guards passed to it.
//
// This reads the source rather than the running router because Echo does not expose
// per-route middleware at runtime, and the fact under test is a wiring decision that
// lives only in main.go. It is an AST walk, not a grep: a renamed guard, a reordered
// argument list, or a deleted route all surface as a changed/absent entry rather than
// as a silently passing match.
func parseRouteGates(t *testing.T) []routeGate {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	var routes []routeGate
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
		case "GET", "POST", "PUT", "PATCH", "DELETE":
		default:
			return true
		}
		if len(call.Args) < 1 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}

		r := routeGate{method: sel.Sel.Name, path: lit.Value[1 : len(lit.Value)-1]}
		// Args beyond (path, handler) are the guards.
		for _, arg := range call.Args[2:] {
			switch g := arg.(type) {
			case *ast.Ident:
				r.guards = append(r.guards, g.Name)
			case *ast.CallExpr:
				// e.g. rbac(mw.PermUpdateTask) — record the callee name.
				if id, ok := g.Fun.(*ast.Ident); ok {
					r.guards = append(r.guards, id.Name)
				}
			}
		}
		routes = append(routes, r)
		return true
	})

	if len(routes) == 0 {
		t.Fatal("parsed zero routes from main.go — the parser, not the wiring, is broken")
	}
	return routes
}

func findRoute(t *testing.T, routes []routeGate, method, path string) routeGate {
	t.Helper()
	for _, r := range routes {
		if r.method == method && r.path == path {
			return r
		}
	}
	t.Fatalf("route %s %s is not registered in main.go", method, path)
	return routeGate{}
}

func hasGuard(r routeGate, name string) bool {
	for _, g := range r.guards {
		if g == name {
			return true
		}
	}
	return false
}

// TestStatusResolveGateIsNotStricterThanTheMoveItServes pins the wiring invariant:
// the lookup a caller must perform to resolve a status slug may not sit behind a
// stricter gate than the transition that lookup exists to serve.
//
// Concretely: POST /tasks/:task_id/move is workspace-gated, so
// GET /tasks/:task_id/statuses must be workspace-gated too. If it were project-gated,
// a caller entitled to move a task could not discover which statuses it may move to,
// and would be refused with a message naming the project rather than the move.
//
// The integration suite proves this end-to-end against a live server, but that job
// runs in REPORT mode (continue-on-error) and cannot block a merge. This test can,
// which is why the wiring is asserted here as well as exercised there.
func TestStatusResolveGateIsNotStricterThanTheMoveItServes(t *testing.T) {
	routes := parseRouteGates(t)

	move := findRoute(t, routes, "POST", "/tasks/:task_id/move")
	resolve := findRoute(t, routes, "GET", "/tasks/:task_id/statuses")

	// Premise: the move is workspace-gated. If this ever changes, the parity claim
	// itself changes — fail here rather than let the assertion below go vacuous.
	if !hasGuard(move, "wsAccess") || hasGuard(move, "projAccess") {
		t.Fatalf("premise changed: POST /tasks/:task_id/move guards are %v, expected workspace-gated", move.guards)
	}

	if hasGuard(resolve, "projAccess") {
		t.Errorf("GET /tasks/:task_id/statuses is project-gated (%v) while the move it serves is "+
			"workspace-gated (%v) — the read is stricter than the write, which makes the write "+
			"unreachable for a workspace member who is not a project member", resolve.guards, move.guards)
	}
	if !hasGuard(resolve, "wsAccess") {
		t.Errorf("GET /tasks/:task_id/statuses guards are %v — it must carry wsAccess, the same "+
			"workspace gate as the move; an ungated route is not the fix either", resolve.guards)
	}
}

// TestProjectScopedStatusRouteKeepsProjectGate is the negative control for the test
// above. The fix had to be additive: had we instead relaxed GET /projects/:proj_id/statuses
// to the workspace gate, the parity test would pass while project configuration silently
// became readable workspace-wide for every consumer, the UI included.
func TestProjectScopedStatusRouteKeepsProjectGate(t *testing.T) {
	routes := parseRouteGates(t)

	for _, path := range []string{
		"/projects/:proj_id/statuses",
		"/projects/:proj_id/statuses/:status_id",
		"/projects/:proj_id/statuses/reorder",
	} {
		for _, method := range []string{"GET", "POST", "PATCH", "PUT"} {
			for _, r := range routes {
				if r.method != method || r.path != path {
					continue
				}
				if !hasGuard(r, "projAccess") {
					t.Errorf("%s %s lost its projAccess guard (now %v) — project status "+
						"configuration must stay behind project membership", method, path, r.guards)
				}
			}
		}
	}
}
