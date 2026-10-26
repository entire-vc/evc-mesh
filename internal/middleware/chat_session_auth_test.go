package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// authSvc is the ChatSessionService boundary of the middleware: only Authenticate
// is reachable from it.
type authSvc struct {
	service.ChatSessionService
	p   *service.ChatSessionPrincipal
	err error
}

func (s authSvc) Authenticate(context.Context, string) (*service.ChatSessionPrincipal, error) {
	return s.p, s.err
}

func chatPrincipal() *service.ChatSessionPrincipal {
	return &service.ChatSessionPrincipal{
		Agent: &domain.Agent{ID: uuid.New()},
		Token: &domain.ChatSessionToken{
			UserID: uuid.New(), WorkspaceID: uuid.New(), ProjectID: uuid.New(),
			ChatThreadID: uuid.New(), ChatSessionID: uuid.New(), ExecutionEpoch: 3,
		},
	}
}

func TestChatSessionAuth(t *testing.T) {
	p := chatPrincipal()
	do := func(svc service.ChatSessionService, authz string) (int, echo.Context) {
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		h := ChatSessionAuth(svc)(func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })
		require.NoError(t, h(c))
		return rec.Code, c
	}

	code, c := do(authSvc{p: p}, "Bearer cst_abc")
	assert.Equal(t, http.StatusNoContent, code)
	assert.Equal(t, p, GetChatSession(c))
	assert.True(t, IsChatSession(c))
	attr, ok := actorctx.ChatAttributionFromContext(c.Request().Context())
	require.True(t, ok)
	assert.Equal(t, p.Token.UserID, attr.InitiatedByUserID)
	assert.Equal(t, p.Token.ChatSessionID, attr.ChatSessionID)
	assert.Equal(t, int64(3), attr.ExecutionEpoch)

	code, c = do(authSvc{}, "Bearer agk_notachattoken")
	assert.Equal(t, http.StatusNoContent, code, "a non-cst_ bearer is left to the next auth")
	assert.False(t, IsChatSession(c))
	code, _ = do(authSvc{}, "")
	assert.Equal(t, http.StatusNoContent, code)

	code, _ = do(nil, "Bearer cst_abc")
	assert.Equal(t, http.StatusUnauthorized, code, "service not configured")
	code, _ = do(authSvc{err: apierror.Unauthorized("expired")}, "Bearer cst_abc")
	assert.Equal(t, http.StatusUnauthorized, code, "invalid token: no fall-through")
	code, _ = do(authSvc{err: errors.New("db down")}, "Bearer cst_abc")
	assert.Equal(t, http.StatusServiceUnavailable, code, "cannot verify is never an allow")
}

func TestChatSessionProjectScope(t *testing.T) {
	p := chatPrincipal()
	ws, proj, other := p.Token.WorkspaceID.String(), p.Token.ProjectID.String(), uuid.NewString()

	// run executes the scope middleware on a route with the given param names/values.
	run := func(principal *service.ChatSessionPrincipal, method, route, query string, names, values []string) int {
		e := echo.New()
		req := httptest.NewRequest(method, "/x"+query, http.NoBody)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetPath(route)
		c.SetParamNames(names...)
		c.SetParamValues(values...)
		if principal != nil {
			c.Set(ContextKeyChatSession, principal)
		}
		h := ChatSessionProjectScope(nil)(func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })
		require.NoError(t, h(c))
		return rec.Code
	}
	const wsList = "/api/v1/workspaces/:ws_id/projects"
	const projRoute = "/api/v1/projects/:proj_id/tasks"

	assert.Equal(t, http.StatusNoContent, run(nil, "GET", wsList, "", []string{"ws_id"}, []string{other}), "inert without a chat principal")

	// The workspace-level list names no project: refused whatever the query says.
	assert.Equal(t, http.StatusForbidden, run(p, "GET", wsList, "", []string{"ws_id"}, []string{ws}))
	assert.Equal(t, http.StatusForbidden, run(p, "GET", wsList, "?project_id="+other, []string{"ws_id"}, []string{ws}))
	assert.Equal(t, http.StatusForbidden, run(p, "GET", wsList, "?project_id="+proj, []string{"ws_id"}, []string{ws}), "matching query is not proof of scope")
	assert.Equal(t, http.StatusForbidden, run(p, "GET", wsList, "", []string{"ws_id"}, []string{other}), "foreign workspace")
	assert.Equal(t, http.StatusForbidden, run(p, "GET", wsList, "", []string{"ws_id"}, []string{"not-a-uuid"}))

	// Project-bearing routes.
	assert.Equal(t, http.StatusNoContent, run(p, "GET", projRoute, "", []string{"proj_id"}, []string{proj}))
	assert.Equal(t, http.StatusNoContent, run(p, "GET", projRoute, "?project_id="+proj, []string{"proj_id"}, []string{proj}))
	assert.Equal(t, http.StatusForbidden, run(p, "GET", projRoute, "", []string{"proj_id"}, []string{other}))
	assert.Equal(t, http.StatusForbidden, run(p, "GET", projRoute, "", []string{"proj_id"}, []string{"bad"}))
	assert.Equal(t, http.StatusForbidden, run(p, "GET", projRoute, "?project_id="+other, []string{"proj_id"}, []string{proj}), "query must agree with the token")
	assert.Equal(t, http.StatusForbidden, run(p, "GET", projRoute, "?project_id=bad", []string{"proj_id"}, []string{proj}))
	assert.Equal(t, http.StatusNoContent, run(p, "GET", "/api/v1/workspaces/:ws_id/projects/:project_id", "", []string{"ws_id", "project_id"}, []string{ws, proj}))

	// A task is resolved through the DB; without one it cannot be placed, so it is refused.
	assert.Equal(t, http.StatusForbidden, run(p, "POST", "/api/v1/tasks/:task_id/move", "", []string{"task_id"}, []string{uuid.NewString()}))
	// A route whose body names a second project (the move destination) is refused
	// even when the task is the token's own.
	assert.Equal(t, http.StatusForbidden, run(p, "POST", "/api/v1/tasks/:task_id/move-to-project", "", []string{"task_id"}, []string{uuid.NewString()}))
	assert.Equal(t, http.StatusForbidden, run(p, "POST", "/api/v1/tasks/move-to-project-batch", "", nil, nil))
	// Unknown parameter: fail closed.
	assert.Equal(t, http.StatusForbidden, run(p, "GET", "/api/v1/things/:thing_id", "", []string{"thing_id"}, []string{"1"}))

	// The explicit allowlist: the agent itself, nothing else.
	assert.Equal(t, http.StatusNoContent, run(p, "GET", "/api/v1/agents/me", "", nil, nil))
	assert.Equal(t, http.StatusForbidden, run(p, "GET", "/api/v1/agents/me/tasks", "", nil, nil))
	assert.Equal(t, http.StatusForbidden, run(p, "DELETE", "/api/v1/agents/me", "", nil, nil))
}

func TestResolveTaskProject_NoDB(t *testing.T) {
	for _, raw := range []string{uuid.NewString(), "abcdef", "xyz", "abc", "abcdef0123456789"} {
		_, ok := resolveTaskProject(context.Background(), nil, raw)
		assert.False(t, ok, raw)
	}
}

// The far end of a dependency edge is read from the body; everything but a
// resolvable task of the token's project is refused (the allow path needs a DB:
// see the handler e2e test).
func TestBodyTaskInProject_FailsClosed(t *testing.T) {
	project := uuid.New()
	check := func(body io.Reader) bool {
		req := httptest.NewRequest(http.MethodPost, "/x", http.NoBody)
		if body == nil {
			req.Body = nil
		} else {
			req.Body = io.NopCloser(body)
		}
		return bodyTaskInProject(echo.New().NewContext(req, httptest.NewRecorder()), nil, project)
	}
	assert.False(t, check(nil), "no body")
	assert.False(t, check(strings.NewReader("nope")), "not json")
	assert.False(t, check(strings.NewReader(`{}`)), "no far end")
	assert.False(t, check(strings.NewReader(`{"depends_on_task_id":"zzz"}`)), "malformed far end")
	assert.False(t, check(strings.NewReader(`{"depends_on_task_id":"`+uuid.NewString()+`"}`)), "unresolvable far end")
}

// Every mapped child parameter has a resolver query, and without a database (or
// with a value that is not a UUID) it cannot place the resource, so it is refused.
func TestChatSessionChildResolvers_FailClosedWithoutProject(t *testing.T) {
	p := chatPrincipal()
	require.NotEmpty(t, chatSessionChildResolvers)
	for name, query := range chatSessionChildResolvers {
		assert.Contains(t, query, "project_id", name)
		for _, raw := range []string{uuid.NewString(), "not-a-uuid"} {
			_, ok := resolveChildProject(context.Background(), nil, query, raw)
			assert.False(t, ok, "%s %s", name, raw)
		}

		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/x", http.NoBody), rec)
		c.SetPath("/api/v1/projects/:proj_id/things/:" + name)
		c.SetParamNames("proj_id", name)
		c.SetParamValues(p.Token.ProjectID.String(), uuid.NewString())
		c.Set(ContextKeyChatSession, p)
		h := ChatSessionProjectScope(nil)(func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })
		require.NoError(t, h(c))
		assert.Equal(t, http.StatusForbidden, rec.Code, "%s resource that cannot be resolved", name)
	}
}

// Against real Postgres: a resolver query that yields a project is trusted as is,
// one that yields no row (or NULL) is refused. "SELECT $1::uuid" stands in for a
// resolver row owned by the given project.
func TestResolveChildProject_RealDB(t *testing.T) {
	db := extTestDB(t)
	ctx := context.Background()
	project := uuid.New()

	got, ok := resolveChildProject(ctx, db, `SELECT $1::uuid`, project.String())
	require.True(t, ok)
	assert.Equal(t, project, got)

	_, ok = resolveChildProject(ctx, db, `SELECT $1::uuid WHERE false`, project.String())
	assert.False(t, ok, "no row means no project")

	_, ok = resolveChildProject(ctx, db, `SELECT NULL::uuid WHERE $1::uuid IS NOT NULL`, project.String())
	assert.False(t, ok, "a NULL project is not a project")

	_, ok = resolveChildProject(ctx, db, `SELECT project_id FROM no_such_table WHERE id = $1`, project.String())
	assert.False(t, ok, "a failing query is refused, never allowed")
}

// POST /memories is the one memory route a chat token reaches: the body is pinned
// to the chat's project before the handler sees it, anything wider is refused, and
// the other memory routes stay closed.
func TestChatSessionProjectScope_MemoryWrite(t *testing.T) {
	p := chatPrincipal()
	proj, ws := p.Token.ProjectID.String(), p.Token.WorkspaceID.String()

	run := func(method, route, body string) (int, map[string]any) {
		e := echo.New()
		req := httptest.NewRequest(method, "/x", strings.NewReader(body))
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetPath(route)
		c.Set(ContextKeyChatSession, p)
		var seen map[string]any
		h := ChatSessionProjectScope(nil)(func(c echo.Context) error {
			raw, err := io.ReadAll(c.Request().Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &seen))
			assert.Equal(t, int64(len(raw)), c.Request().ContentLength)
			return c.NoContent(http.StatusNoContent)
		})
		require.NoError(t, h(c))
		return rec.Code, seen
	}

	for name, body := range map[string]string{
		"defaults":         `{"key":"k","content":"v"}`,
		"explicit-project": `{"key":"k","content":"v","scope":"project","project_id":"` + proj + `","workspace_id":"` + ws + `"}`,
		"empty-scope":      `{"key":"k","content":"v","scope":""}`,
		"null-project":     `{"key":"k","content":"v","project_id":null}`,
	} {
		code, seen := run(http.MethodPost, ChatSessionMemoryWriteRoute, body)
		require.Equal(t, http.StatusNoContent, code, name)
		assert.Equal(t, "project", seen["scope"], name)
		assert.Equal(t, proj, seen["project_id"], name)
		assert.Equal(t, "v", seen["content"], name+": the rest of the body is kept")
	}

	for name, body := range map[string]string{
		"workspace-scope":   `{"key":"k","scope":"workspace"}`,
		"agent-scope":       `{"key":"k","scope":"agent"}`,
		"foreign-project":   `{"key":"k","project_id":"` + uuid.NewString() + `"}`,
		"bad-project":       `{"key":"k","project_id":"nope"}`,
		"foreign-workspace": `{"key":"k","workspace_id":"` + uuid.NewString() + `"}`,
		"bad-workspace":     `{"key":"k","workspace_id":"nope"}`,
		"wrong-type":        `{"key":"k","scope":1}`,
		"not-json":          `nope`,
		"not-object":        `[1]`,
		"null":              `null`,
	} {
		code, _ := run(http.MethodPost, ChatSessionMemoryWriteRoute, body)
		assert.Equal(t, http.StatusForbidden, code, name)
	}

	code, _ := run(http.MethodGet, ChatSessionMemoryWriteRoute, "")
	assert.Equal(t, http.StatusForbidden, code, "reading memory stays refused")
	code, _ = run(http.MethodPost, "/api/v1/memories/import", `{}`)
	assert.Equal(t, http.StatusForbidden, code, "import stays refused")
}

func TestPinMemoryWriteToProject_NoBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/x", http.NoBody)
	req.Body = nil
	assert.False(t, pinMemoryWriteToProject(echo.New().NewContext(req, httptest.NewRecorder()), uuid.New(), uuid.New()))
}

// The agent ceiling of a chat token admits write_memory on POST /memories only;
// the user's role still decides.
func TestClampConnector_ChatSessionMemoryWrite(t *testing.T) {
	require.False(t, agentPerms[PermWriteMemory], "precondition: write_memory is outside the agent ceiling")
	run := func(role, method, route string) int {
		c, rec := newRBACOAuthConnectorContext(uuid.New(), uuid.New(), uuid.New())
		c.Request().Method = method
		c.SetPath(route)
		c.Set(ContextKeyChatSession, &service.ChatSessionPrincipal{UserRole: role})
		h := RequireConnectorPermission(PermWriteMemory, newRBACMockMemberRepo(), nil)(func(c echo.Context) error {
			return c.NoContent(http.StatusNoContent)
		})
		require.NoError(t, h(c))
		return rec.Code
	}
	assert.Equal(t, http.StatusNoContent, run(domain.RoleMember, http.MethodPost, ChatSessionMemoryWriteRoute))
	assert.Equal(t, http.StatusForbidden, run(domain.RoleViewer, http.MethodPost, ChatSessionMemoryWriteRoute), "viewer holds no write_memory")
	assert.Equal(t, http.StatusForbidden, run(domain.RoleMember, http.MethodPost, "/api/v1/memories/import"))
	assert.Equal(t, http.StatusForbidden, run(domain.RoleMember, http.MethodDelete, "/api/v1/memories/:id"))
	assert.Equal(t, http.StatusForbidden, run(domain.RoleMember, http.MethodPost, "/api/v1/projects/:proj_id/knowledge"))
}
