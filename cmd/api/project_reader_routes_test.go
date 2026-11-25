package main

import (
	"context"
	"database/sql"
	"errors"
	"go/ast"
	"go/token"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/apisource"
	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
)

// A project reader (agents.reader_project_id, docs/project-reader.md) reaches
// exactly the pinned allowlist on its own project and nothing else, whatever
// role its grant carries. The routes are the real registrations read by
// parseAuditRoutes; the guard is the one routes_protected_group.go mounts.

// projectParams are the parameters mw.RequestProjects resolves.
var projectParams = map[string]bool{
	"proj_id": true, "task_id": true, "short": true, "doc_id": true,
	"artifact_id": true, "event_id": true, "link_id": true,
}

// stubResolver stands in for mw.RequestProjects: every project parameter of the
// matched route resolves to project (or to err), found=false when there is none.
func stubResolver(project uuid.UUID, err error) mw.ProjectResolver {
	return func(_ context.Context, c echo.Context) ([]uuid.UUID, bool, error) {
		var out []uuid.UUID
		found := false
		for _, n := range c.ParamNames() {
			if projectParams[n] {
				found = true
				out = append(out, project)
			}
		}
		if found && err != nil {
			return nil, true, err
		}
		return out, found, nil
	}
}

// serveAsReader registers every route behind a stand-in for DualAuth that
// authenticates a project reader bound to readerProject with the given grant
// role, then the same two guards main mounts, in the same order.
func serveAsReader(routes []auditRoute, role string, readerProject uuid.UUID, resolve mw.ProjectResolver) *echo.Echo {
	e := echo.New()
	g := e.Group("/api/v1")
	g.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(mw.ContextKeyAuthType, mw.AuthTypeAgent)
			c.Set(mw.ContextKeyAgentID, uuid.New())
			c.Set(mw.ContextKeyWorkspaceRole, role)
			if readerProject != uuid.Nil {
				c.Set(mw.ContextKeyReaderProjectID, readerProject)
			}
			return next(c)
		}
	})
	g.Use(mw.AgentReadOnlyGuard(), mw.ProjectReaderGuard(resolve))
	reached := func(c echo.Context) error { return c.String(http.StatusOK, "handler-reached") }
	for _, r := range routes {
		g.Add(r.method, r.path, reached)
	}
	return e
}

func allRoutes(t *testing.T) []auditRoute {
	t.Helper()
	routes := parseAuditRoutes(t)
	if len(routes) < 300 {
		t.Fatalf("only %d routes read; the walk has stopped seeing the registrations", len(routes))
	}
	return routes
}

var wantReaderAllowlist = []string{
	"GET /api/v1/agents/me",
	"GET /api/v1/tasks/:task_id/comments",
}

// The allowlist is exactly these entries and each is a registered route. A new
// entry is a security change (see projectReaderRoutes) and must be pinned here.
func TestProjectReader_AllowlistIsPinned(t *testing.T) {
	var got []string
	for k := range mw.ProjectReaderRoutes() {
		got = append(got, k)
	}
	sort.Strings(got)
	if strings.Join(got, "|") != strings.Join(wantReaderAllowlist, "|") {
		t.Fatalf("project reader allowlist = %v, pinned %v", got, wantReaderAllowlist)
	}
	byKey := map[string]bool{}
	for _, r := range allRoutes(t) {
		byKey["/api/v1"+r.path] = true
		byKey[r.method+" /api/v1"+r.path] = true
	}
	for _, k := range wantReaderAllowlist {
		if !byKey[k] {
			t.Errorf("allowlisted %s is not a registered route", k)
		}
	}
}

// AC: every route but the allowlist is 403 from ProjectReaderGuard — with a
// member grant too, so the refusal does not lean on AgentReadOnlyGuard — and
// the allowlist reaches its handler on the reader's own project.
func TestProjectReader_EveryRouteClassified(t *testing.T) {
	own := uuid.New()
	allow := mw.ProjectReaderRoutes()
	for _, role := range []string{domain.RoleViewer, domain.RoleMember, domain.RoleAdmin} {
		routes := allRoutes(t)
		e := serveAsReader(routes, role, own, stubResolver(own, nil))
		reachedN := 0
		for _, r := range routes {
			rec := send(e, r.method, r.path)
			if allow[r.method+" /api/v1"+r.path] {
				if rec.Body.String() != "handler-reached" {
					t.Errorf("[%s] %s: reader on its own project got %d %q, want the handler", role, r.key(), rec.Code, rec.Body.String())
				}
				reachedN++
				continue
			}
			body := rec.Body.String()
			refused := rec.Code == http.StatusForbidden &&
				(strings.Contains(body, mw.ProjectReaderMessage) ||
					(role == domain.RoleViewer && strings.Contains(body, mw.ReadOnlyAgentMessage)))
			if !refused {
				t.Errorf("[%s] %s: reader got %d %q, want 403", role, r.key(), rec.Code, body)
			}
		}
		if reachedN != len(wantReaderAllowlist) {
			t.Errorf("[%s] %d allowlisted routes reached, want %d", role, reachedN, len(wantReaderAllowlist))
		}
	}
}

// The leak of the source card: memory and knowledge paths, with and without a
// project_id, are refused outright — named here so a regression reads plainly.
func TestProjectReader_MemoryAndKnowledgeRefused(t *testing.T) {
	own := uuid.New()
	e := serveAsReader(allRoutes(t), domain.RoleViewer, own, stubResolver(own, nil))
	for _, p := range []string{
		"/memories", "/memories/search", "/memories/by-key", "/memories/export",
		"/memories/recall-final/v1", "/memories/recall_graph", "/memories/:id",
		"/memories/:id/related", "/memories/:id/revisions",
		"/projects/:proj_id/knowledge", "/canonical_updates",
		"/tasks/:task_id", "/projects/:proj_id/documents", "/documents/:doc_id",
		"/workspaces/:ws_id/agents", "/agents/me/events/stream",
	} {
		rec := send(e, http.MethodGet, p)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), mw.ProjectReaderMessage) {
			t.Errorf("GET %s: reader got %d %q, want 403 project reader", p, rec.Code, rec.Body.String())
		}
	}
	if rec := send(e, http.MethodPost, "/tasks/:task_id/comments"); rec.Code != http.StatusForbidden {
		t.Errorf("POST comment: reader got %d, want 403", rec.Code)
	}
}

// A foreign or unresolvable project on an allowlisted route is the same 403;
// the self route does not depend on the project.
func TestProjectReader_ForeignOrUnknownProjectRefused(t *testing.T) {
	own := uuid.New()
	routes := allRoutes(t)
	for name, resolve := range map[string]mw.ProjectResolver{
		"foreign": stubResolver(uuid.New(), nil),
		"unknown": stubResolver(uuid.Nil, sql.ErrNoRows),
	} {
		e := serveAsReader(routes, domain.RoleViewer, own, resolve)
		rec := send(e, http.MethodGet, "/tasks/:task_id/comments")
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), mw.ProjectReaderMessage) {
			t.Errorf("%s: GET comments got %d %q, want 403 project reader", name, rec.Code, rec.Body.String())
		}
		if rec := send(e, http.MethodGet, "/agents/me"); rec.Body.String() != "handler-reached" {
			t.Errorf("%s: GET /agents/me got %d %q, want the handler", name, rec.Code, rec.Body.String())
		}
	}
	e := serveAsReader(routes, domain.RoleViewer, own, stubResolver(own, errors.New("db down")))
	if rec := send(e, http.MethodGet, "/tasks/:task_id/comments"); rec.Code != http.StatusInternalServerError {
		t.Errorf("resolver failure: got %d, want 500 (fail closed, not open)", rec.Code)
	}
}

// Control: the same guard is inert for an ordinary agent (no reader marker) —
// a plain viewer agent keeps every read it had, a member agent every route.
func TestProjectReader_InertForOrdinaryAgents(t *testing.T) {
	routes := allRoutes(t)
	foreign := stubResolver(uuid.New(), nil)
	member := serveAsReader(routes, domain.RoleMember, uuid.Nil, foreign)
	viewer := serveAsReader(routes, domain.RoleViewer, uuid.Nil, foreign)
	for _, r := range routes {
		if rec := send(member, r.method, r.path); rec.Body.String() != "handler-reached" {
			t.Errorf("member agent %s: got %d %q, want the handler", r.key(), rec.Code, rec.Body.String())
		}
		if r.method == http.MethodGet {
			if rec := send(viewer, r.method, r.path); rec.Body.String() != "handler-reached" {
				t.Errorf("viewer agent %s: got %d %q, want the handler", r.key(), rec.Code, rec.Body.String())
			}
		}
	}
}

// Red run: without ProjectReaderGuard a reader reaches the memory handler —
// the defect of the source card (foreign memory by id → 200).
func TestProjectReader_WithoutGuardReaderReadsMemory(t *testing.T) {
	e := echo.New()
	g := e.Group("/api/v1")
	g.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(mw.ContextKeyAuthType, mw.AuthTypeAgent)
			c.Set(mw.ContextKeyWorkspaceRole, domain.RoleViewer)
			c.Set(mw.ContextKeyReaderProjectID, uuid.New())
			return next(c)
		}
	})
	g.Use(mw.AgentReadOnlyGuard())
	g.GET("/memories/:id", func(c echo.Context) error { return c.String(http.StatusOK, "handler-reached") })
	if rec := send(e, http.MethodGet, "/memories/:id"); rec.Body.String() != "handler-reached" {
		t.Fatalf("negative control broken: without the guard got %d %q", rec.Code, rec.Body.String())
	}
}

// Issuance is human-admin only, and the guard is mounted right after
// AgentReadOnlyGuard, before the first route.
func TestProjectReader_IssuanceHumanOnlyAndGuardMounted(t *testing.T) {
	var issue *auditRoute
	for _, r := range allRoutes(t) {
		if r.key() == "POST /workspaces/:ws_id/project-readers" {
			r := r
			issue = &r
		}
	}
	if issue == nil {
		t.Fatal("POST /workspaces/:ws_id/project-readers is not registered")
	}
	has := map[string]bool{}
	for _, m := range issue.mws {
		has[m] = true
	}
	for _, want := range []string{"mw.RequireUserAuth()", "rbac(mw.PermManageMembers)"} {
		if !has[want] {
			t.Errorf("issuance route must carry %s, has %v", want, issue.mws)
		}
	}
	for m := range has {
		if strings.Contains(m, "idempotent") {
			t.Errorf("issuance route must not replay its answer (it carries the key): %v", issue.mws)
		}
	}

	fset := token.NewFileSet()
	file, err := apisource.Parse(fset, ".", 0)
	if err != nil {
		t.Fatalf("parse sources: %v", err)
	}
	var uses []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Use" {
			return true
		}
		if recv, ok := sel.X.(*ast.Ident); !ok || recv.Name != "api" {
			return true
		}
		for _, a := range call.Args {
			if c, ok := a.(*ast.CallExpr); ok {
				uses = append(uses, calleeName(c.Fun))
			}
		}
		return true
	})
	for i, u := range uses {
		if u == "mw.AgentReadOnlyGuard" {
			if i+1 >= len(uses) || uses[i+1] != "mw.ProjectReaderGuard" {
				t.Fatalf("mw.ProjectReaderGuard must follow mw.AgentReadOnlyGuard in the api.Use chain, got %v", uses)
			}
			return
		}
	}
	t.Fatalf("mw.AgentReadOnlyGuard not found in api.Use chain %v", uses)
}
