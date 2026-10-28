package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
)

// an internal ticket: a read-only agent credential (grant role viewer) must be
// refused on EVERY state-changing route of the authenticated group, not on one
// sampled POST. The routes are read from main.go by parseAuditRoutes (the same
// AST walk the RBAC audit uses), each is registered on a test group that runs
// the same guard main.go mounts, and a request is sent as a viewer agent.

var routeParam = regexp.MustCompile(`:[A-Za-z_]+`)

func concretePath(path string) string {
	return routeParam.ReplaceAllString(path, uuid.NewString())
}

// serveAsAgent registers every route behind a stand-in for DualAuth (an agent
// with the given grant role) followed by guards, and returns the server.
func serveAsAgent(routes []auditRoute, role string, guards ...echo.MiddlewareFunc) *echo.Echo {
	e := echo.New()
	g := e.Group("/api/v1")
	g.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(mw.ContextKeyAuthType, mw.AuthTypeAgent)
			c.Set(mw.ContextKeyAgentID, uuid.New())
			c.Set(mw.ContextKeyWorkspaceRole, role)
			return next(c)
		}
	})
	g.Use(guards...)
	reached := func(c echo.Context) error { return c.String(http.StatusOK, "handler-reached") }
	for _, r := range routes {
		g.Add(r.method, r.path, reached)
	}
	return e
}

func send(e *echo.Echo, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(method, "/api/v1"+concretePath(path), strings.NewReader("{}")))
	return rec
}

func writeRoutes(t *testing.T) []auditRoute {
	t.Helper()
	var out []auditRoute
	for _, r := range parseAuditRoutes(t) {
		if isWrite(r.method) {
			out = append(out, r)
		}
	}
	// ~200 write routes today; a walk that reads a handful would make the
	// table below pass vacuously.
	if len(out) < 100 {
		t.Fatalf("only %d write routes read from main.go; the walk has stopped seeing the registrations", len(out))
	}
	return out
}

func TestAgentReadOnly_EveryWriteRouteRefusesViewerAgent(t *testing.T) {
	routes := writeRoutes(t)
	e := serveAsAgent(routes, domain.RoleViewer, mw.AgentReadOnlyGuard())
	for _, r := range routes {
		rec := send(e, r.method, r.path)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "read-only agent credential") {
			t.Errorf("%s: viewer agent got %d %q, want 403 read-only agent credential", r.key(), rec.Code, rec.Body.String())
		}
	}
	t.Logf("%d write routes refused for a viewer agent", len(routes))
}

func TestAgentReadOnly_ViewerAgentReadsTasksCommentsDocs(t *testing.T) {
	var reads []auditRoute
	for _, r := range parseAuditRoutes(t) {
		if r.method == "GET" {
			reads = append(reads, r)
		}
	}
	e := serveAsAgent(reads, domain.RoleViewer, mw.AgentReadOnlyGuard())
	for _, path := range []string{
		"/tasks/:task_id",
		"/tasks/:task_id/comments",
		"/projects/:proj_id/tasks",
		"/documents/:doc_id",
		"/memories/search",
	} {
		rec := send(e, http.MethodGet, path)
		if rec.Code != http.StatusOK || rec.Body.String() != "handler-reached" {
			t.Errorf("GET %s: viewer agent got %d %q, want the handler", path, rec.Code, rec.Body.String())
		}
	}
}

// Control and backward compatibility: the same routes with a member agent (the
// role every existing agent key resolves to) pass the guard.
func TestAgentReadOnly_MemberAgentPassesGuard(t *testing.T) {
	routes := writeRoutes(t)
	e := serveAsAgent(routes, domain.RoleMember, mw.AgentReadOnlyGuard())
	for _, r := range routes {
		if rec := send(e, r.method, r.path); rec.Body.String() != "handler-reached" {
			t.Errorf("%s: member agent got %d %q, want the handler", r.key(), rec.Code, rec.Body.String())
		}
	}
}

// Red run: without the guard a viewer agent reaches the comment handler —
// exactly the defect of the card (POST comment → 200).
func TestAgentReadOnly_WithoutGuardViewerAgentWrites(t *testing.T) {
	e := serveAsAgent(writeRoutes(t), domain.RoleViewer)
	rec := send(e, http.MethodPost, "/tasks/:task_id/comments")
	if rec.Body.String() != "handler-reached" {
		t.Fatalf("negative control broken: without the guard POST comment got %d %q", rec.Code, rec.Body.String())
	}
}

// The table above proves the guard; this proves main.go mounts it on `api`
// right after DualAuth and before any route is registered on the group.
func TestAgentReadOnly_GuardMountedOnAuthenticatedGroup(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	var uses []string
	firstRoute := token.NoPos
	guardPos := token.NoPos
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if recv, ok := sel.X.(*ast.Ident); !ok || recv.Name != "api" {
			return true
		}
		switch sel.Sel.Name {
		case "Use":
			for _, a := range call.Args {
				if c, ok := a.(*ast.CallExpr); ok {
					name := calleeName(c.Fun)
					uses = append(uses, name)
					if name == "mw.AgentReadOnlyGuard" {
						guardPos = call.Pos()
					}
				}
			}
		case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD":
			if firstRoute == token.NoPos || call.Pos() < firstRoute {
				firstRoute = call.Pos()
			}
		}
		return true
	})
	dual := -1
	for i, u := range uses {
		if u == "mw.DualAuth" {
			dual = i
		}
	}
	if dual < 0 || dual+1 >= len(uses) || uses[dual+1] != "mw.AgentReadOnlyGuard" {
		t.Fatalf("api.Use chain must have mw.AgentReadOnlyGuard() directly after mw.DualAuth, got %v", uses)
	}
	if guardPos == token.NoPos || firstRoute == token.NoPos || guardPos > firstRoute {
		t.Fatalf("mw.AgentReadOnlyGuard must be mounted before the first api route (guard at %v, first route at %v)",
			fset.Position(guardPos), fset.Position(firstRoute))
	}
}

// AC4: only a human admin issues or re-grades an agent credential. Pins the
// permission each such route carries; RequirePermission refuses all three
// permissions to every agent role (internal/middleware/agent_readonly_test.go).
func TestAgentReadOnly_CredentialIssuanceIsHumanOnly(t *testing.T) {
	want := map[string]string{
		"POST /workspaces/:ws_id/agent-grants":             "rbac(mw.PermManageMembers)",
		"DELETE /workspaces/:ws_id/agent-grants/:grant_id": "rbac(mw.PermManageMembers)",
		"POST /workspaces/:ws_id/agents":                   "rbac(mw.PermRegisterAgent)",
		"POST /agents/:agent_id/keys":                      "rbac(mw.PermDeleteAgent)",
		"DELETE /agents/:agent_id/keys/:key_id":            "rbac(mw.PermDeleteAgent)",
		"POST /agents/:agent_id/regenerate-key":            "rbac(mw.PermDeleteAgent)",
		"PATCH /agents/:agent_id":                          "rbac(mw.PermDeleteAgent)",
	}
	byKey := map[string]auditRoute{}
	for _, r := range parseAuditRoutes(t) {
		byKey[r.key()] = r
	}
	for key, guard := range want {
		r, ok := byKey[key]
		if !ok {
			t.Errorf("%s is not registered in main.go", key)
			continue
		}
		found := false
		for _, m := range r.mws {
			if m == guard {
				found = true
			}
		}
		if !found {
			t.Errorf("%s must carry %s, has %v", key, guard, r.mws)
		}
	}
}
