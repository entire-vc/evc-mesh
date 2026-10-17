package main

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/gitlab"
	"github.com/entire-vc/evc-mesh/internal/handler"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// memProjectIntegrations is the database boundary of the project binding: it
// keeps what the repository keeps — one row per (project, extension) — including
// that an empty agent key on a save leaves the stored one alone.
type memProjectIntegrations struct {
	repository.ProjectIntegrationRepository
	rows map[string]*domain.ProjectIntegration
}

func rowKey(p uuid.UUID, intType string) string {
	return p.String() + "/" + domain.ProjectIntegrationExtensionID(intType)
}

func (m *memProjectIntegrations) Get(_ context.Context, p uuid.UUID, t string) (*domain.ProjectIntegration, error) {
	return m.rows[rowKey(p, t)], nil
}

func (m *memProjectIntegrations) Upsert(_ context.Context, pi *domain.ProjectIntegration) error {
	k := rowKey(pi.ProjectID, pi.Type)
	pi.ExtensionID = domain.ProjectIntegrationExtensionID(pi.Type)
	if old := m.rows[k]; old != nil {
		if pi.AgentKey == "" {
			pi.AgentKey = old.AgentKey
		}
		pi.ID, pi.CreatedAt = old.ID, old.CreatedAt
	}
	cp := *pi
	m.rows[k] = &cp
	return nil
}

func (m *memProjectIntegrations) Delete(_ context.Context, p uuid.UUID, t string) error {
	delete(m.rows, rowKey(p, t))
	return nil
}

func (m *memProjectIntegrations) ListByProject(_ context.Context, p uuid.UUID) ([]domain.ProjectIntegration, error) {
	var out []domain.ProjectIntegration
	for _, r := range m.rows {
		if r.ProjectID == p {
			out = append(out, *r)
		}
	}
	return out, nil
}

// levelProvider is a registered extension with chosen levels; the project
// routes only read its manifest.
type levelProvider struct {
	extensions.Provider
	levels extensions.Levels
}

func (p levelProvider) Manifest() extensions.Manifest { return extensions.Manifest{Levels: p.levels} }

type lookup map[string]extensions.Provider

func (l lookup) Get(id string) (extensions.Provider, bool) { p, ok := l[id]; return p, ok }

// deprecatedAliasesInMain returns, for every api.<METHOD>("<path>", ...) registration in
// main.go, the string literals handed to handler.DeprecatedAlias.
func deprecatedAliasesInMain(t *testing.T) map[string]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	require.NoError(t, err)
	out := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if recv, isIdent := sel.X.(*ast.Ident); !isIdent || recv.Name != "api" {
			return true
		}
		path, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			return true
		}
		for _, arg := range call.Args[2:] {
			c, isCall := arg.(*ast.CallExpr)
			if !isCall || calleeName(c.Fun) != "handler.DeprecatedAlias" {
				continue
			}
			lit, isLit := c.Args[0].(*ast.BasicLit)
			require.True(t, isLit, "DeprecatedAlias takes a string literal")
			p, _ := strconv.Unquote(path.Value)
			succ, _ := strconv.Unquote(lit.Value)
			out[sel.Sel.Name+" "+p] = succ
		}
		return true
	})
	return out
}

// TestProjectExtensionRoutes_Contract calls the project routes through a router
// built from main.go's own registrations and checks docs/extensions/api.md.
func TestProjectExtensionRoutes_Contract(t *testing.T) {
	repo := &memProjectIntegrations{rows: map[string]*domain.ProjectIntegration{}}
	h := handler.NewExtensionProjectHandler(lookup{
		"team-relay": levelProvider{levels: extensions.Levels{Workspace: true, Project: true}},
		"gitlab":     gitlab.New(extensions.Deps{}),
		"github":     levelProvider{levels: extensions.Levels{Workspace: true}},
	}, service.NewProjectIntegrationService(repo)).
		WithActions("team-relay",
			handler.ProjectAction{Name: "search", Method: http.MethodGet, Run: func(c echo.Context) error { return c.NoContent(http.StatusOK) }},
			handler.ProjectAction{Name: "mount", Method: http.MethodPost, Run: func(c echo.Context) error { return c.NoContent(http.StatusOK) }},
		)
	handlers := map[string]echo.HandlerFunc{
		"GET /projects/:proj_id/extensions":                          h.List,
		"GET /projects/:proj_id/extensions/:ext_id":                  h.Get,
		"PUT /projects/:proj_id/extensions/:ext_id":                  h.Put,
		"DELETE /projects/:proj_id/extensions/:ext_id":               h.Delete,
		"GET /projects/:proj_id/extensions/:ext_id/actions":          h.ListActions,
		"GET /projects/:proj_id/extensions/:ext_id/actions/:action":  h.Action,
		"POST /projects/:proj_id/extensions/:ext_id/actions/:action": h.Action,
	}
	registered := map[string]auditRoute{}
	for _, r := range parseAuditRoutes(t) {
		registered[r.key()] = r
	}
	e := echo.New()
	for key, fn := range handlers {
		r, ok := registered[key]
		require.True(t, ok, "route %q is not registered in main.go", key)
		assert.Contains(t, r.mws, "projAccess", "%s must carry the project access guard", key)
		if r.method != http.MethodGet {
			require.Equal(t, "rbac", r.guard, "%s must carry rbac()", key)
		}
		e.Add(r.method, r.path, fn)
	}

	project := uuid.New().String()
	call := func(method, tail, body string) (*httptest.ResponseRecorder, map[string]any) {
		req := httptest.NewRequest(method, "/projects/"+project+"/extensions"+tail, strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec, out
	}
	const key = "tr_agent_0123456789abcdef0123456789abcdef0123456789abcdef"

	rec, out := call(http.MethodGet, "", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, out["extensions"], "no bindings yet")

	rec, _ = call(http.MethodGet, "/team-relay", "")
	assert.Equal(t, http.StatusNotFound, rec.Code, "never set")

	// An id with no project level (or no registration) is a plain 404 and writes nothing.
	for _, id := range []string{"github", "nope"} {
		for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
			rec, _ = call(m, "/"+id, `{"enabled":true}`)
			assert.Equal(t, http.StatusNotFound, rec.Code, m+" "+id)
		}
	}
	assert.Empty(t, repo.rows, "a refused request writes nothing")

	// A manifest that lost its project level (or a registry without the
	// extension) is refused by the handler itself, before anything is read or written.
	for name, reg := range map[string]lookup{
		"no project level": {"team-relay": levelProvider{levels: extensions.Levels{Workspace: true}}},
		"not registered":   {},
	} {
		off := echo.New()
		oh := handler.NewExtensionProjectHandler(reg, service.NewProjectIntegrationService(repo))
		off.GET("/projects/:proj_id/extensions/:ext_id", oh.Get)
		off.PUT("/projects/:proj_id/extensions/:ext_id", oh.Put)
		off.DELETE("/projects/:proj_id/extensions/:ext_id", oh.Delete)
		for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
			rr := httptest.NewRecorder()
			off.ServeHTTP(rr, httptest.NewRequest(m, "/projects/"+project+"/extensions/team-relay", strings.NewReader(`{"enabled":true,"settings":{"share_slug":"x","agent_key":"`+key+`"}}`)))
			assert.Equal(t, http.StatusNotFound, rr.Code, name+" "+m)
		}
		assert.Empty(t, repo.rows, name+": nothing written")
	}

	// Team Relay: share, secret and the mount settings the old block carried.
	rec, body := call(http.MethodPut, "/team-relay", `{"enabled":true,"settings":{"share_slug":"docs","agent_key":"`+key+`","subfolder":"sub","include_project_slug":true,"docs_mount_path":"Team Relay","excluded_paths":["tmp"],"sync_interval_minutes":30}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "team-relay", body["extension_id"])
	assert.Equal(t, true, body["enabled"])
	settings := body["settings"].(map[string]any)
	assert.Equal(t, "docs", settings["share_slug"])
	assert.Equal(t, "sub", settings["subfolder"])
	assert.Equal(t, true, settings["include_project_slug"])
	assert.Equal(t, "Team Relay", settings["docs_mount_path"])
	assert.Equal(t, []any{"tmp"}, settings["excluded_paths"])
	assert.EqualValues(t, 30, settings["sync_interval_minutes"])
	assert.Equal(t, "***", settings["agent_key"], "a secret is reported as set")
	assert.NotContains(t, rec.Body.String(), key, "the secret value never leaves the server")
	assert.NotEmpty(t, body["created_at"])
	assert.NotEmpty(t, body["updated_at"])
	assert.Equal(t, false, body["key_expiring_soon"])

	rec, body = call(http.MethodGet, "/team-relay", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "docs", body["settings"].(map[string]any)["share_slug"])
	assert.Equal(t, "Team Relay", body["settings"].(map[string]any)["docs_mount_path"])
	assert.NotContains(t, rec.Body.String(), key)

	rec, body = call(http.MethodPut, "/team-relay", `{"enabled":true,"settings":{"share_slug":"docs2","agent_key":"***"}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "docs2", body["settings"].(map[string]any)["share_slug"], "PUT replaces the settings")
	assert.Equal(t, key, repo.rows[rowKey(uuid.MustParse(project), "team-relay")].AgentKey, `"***" keeps the stored secret`)

	rec, _ = call(http.MethodPut, "/team-relay", `{"enabled":true,"settings":{"bogus":1}}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "a key outside the project schema is refused")
	assert.Contains(t, rec.Body.String(), "bogus")
	rec, _ = call(http.MethodPut, "/team-relay", `{"enabled":true,"settings":{"agent_key":"`+key+`"}}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "enabled needs a share")
	assert.Contains(t, string(repo.rows[rowKey(uuid.MustParse(project), "team-relay")].Settings), `"share_slug":"docs2"`, "refused saves change nothing")

	// GitLab: the generic path, on the same project.
	rec, _ = call(http.MethodGet, "/gitlab", "")
	assert.Equal(t, http.StatusNotFound, rec.Code, "gitlab never bound")
	rec, body = call(http.MethodPut, "/gitlab", `{"enabled":true,"settings":{"repo_path":"entire-vc/evc-mesh"}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "gitlab", body["extension_id"])
	assert.Equal(t, "entire-vc/evc-mesh", body["settings"].(map[string]any)["repo_path"])
	rec, _ = call(http.MethodPut, "/gitlab", `{"enabled":true,"settings":{"repo_path":"entire-vc/evc-mesh","bogus":1}}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "a key outside the schema: 400")
	rec, body = call(http.MethodGet, "/gitlab", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "entire-vc/evc-mesh", body["settings"].(map[string]any)["repo_path"], "refused save changed nothing")

	rec, body = call(http.MethodGet, "", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, body["extensions"], 2, "both bindings listed")

	// Actions: Team Relay's own, plus check for everyone, each only on its method.
	rec, _ = call(http.MethodGet, "/team-relay/actions/search", "")
	assert.Equal(t, http.StatusOK, rec.Code)
	rec, _ = call(http.MethodPost, "/team-relay/actions/mount", "")
	assert.Equal(t, http.StatusOK, rec.Code)
	rec, _ = call(http.MethodGet, "/team-relay/actions/mount", "")
	assert.Equal(t, http.StatusNotFound, rec.Code, "a write action is not reachable by GET")
	rec, _ = call(http.MethodPost, "/gitlab/actions/mount", "")
	assert.Equal(t, http.StatusNotFound, rec.Code, "gitlab has no mount")

	rec, _ = call(http.MethodDelete, "/team-relay", "")
	assert.Equal(t, http.StatusNoContent, rec.Code)
	rec, _ = call(http.MethodGet, "/team-relay", "")
	assert.Equal(t, http.StatusNotFound, rec.Code, "deleted")
	rec, _ = call(http.MethodDelete, "/gitlab", "")
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, repo.rows)
}

// TestOldIntegrationRoutes_AnswerWithDeprecation checks that every old route the
// API document lists as an alias is wired in main.go with the successor named in
// the document, and that the wired middleware sets both headers without touching
// the body.
func TestOldIntegrationRoutes_AnswerWithDeprecation(t *testing.T) {
	want := map[string]string{
		"GET /workspaces/:ws_id/integrations":               "/api/v1/workspaces/{ws}/extensions",
		"POST /workspaces/:ws_id/integrations":              "/api/v1/workspaces/{ws}/extensions/{id}/connect",
		"PATCH /integrations/:int_id":                       "/api/v1/workspaces/{ws}/extensions/{id}/settings",
		"DELETE /integrations/:int_id":                      "/api/v1/workspaces/{ws}/extensions/{id}",
		"GET /projects/:proj_id/integrations/team-relay":    "/api/v1/projects/{p}/extensions/team-relay",
		"PATCH /projects/:proj_id/integrations/team-relay":  "/api/v1/projects/{p}/extensions/team-relay",
		"DELETE /projects/:proj_id/integrations/team-relay": "/api/v1/projects/{p}/extensions/team-relay",
	}
	got := deprecatedAliasesInMain(t)
	assert.Equal(t, want, got, "the deprecated aliases wired in main.go")

	e := echo.New()
	for key, successor := range got {
		method, path, _ := strings.Cut(key, " ")
		e.Add(method, path, func(c echo.Context) error { return c.String(http.StatusTeapot, "old body") }, handler.DeprecatedAlias(successor))
	}
	for key, successor := range got {
		method, path, _ := strings.Cut(key, " ")
		path = strings.NewReplacer(":ws_id", "w", ":int_id", "i", ":proj_id", "p").Replace(path)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(method, path, http.NoBody))
		assert.Equal(t, "true", rec.Header().Get("Deprecation"), key)
		assert.Equal(t, "<"+successor+`>; rel="successor-version"`, rec.Header().Get("Link"), key)
		assert.Equal(t, "old body", rec.Body.String(), "%s: body is unchanged", key)
	}

	// The new routes are not deprecated.
	for _, r := range parseAuditRoutes(t) {
		if strings.Contains(r.path, "/extensions") {
			assert.NotContains(t, strings.Join(r.mws, " "), "DeprecatedAlias", r.key())
		}
	}
}
