//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scopedParams are the route parameters WorkspaceRLS resolves a tenant from
// (internal/middleware.WorkspaceScopedParams). Any route carrying one of them
// names another tenant's data and must refuse a stranger.
var scopedParams = []string{"ws_id", "proj_id", "project_id", "task_id", "artifact_id", "agent_id", "field_id", "init_id"}

// exemptRoutes must match middleware.workspaceScopeExemptRoutes. Spark's
// :agent_id is a catalog id in an external marketplace, so no local workspace can
// be resolved from it; POST .../install takes its workspace from the body and
// checks membership in the handler instead.
var exemptRoutes = map[string]bool{
	"/spark/agents/:agent_id":         true,
	"/spark/agents/:agent_id/install": true,
}

// victimFixture is one tenant's data, created by its legitimate owner, for an
// unrelated stranger to fail to reach.
type victimFixture struct {
	env       *TestEnv
	wsID      string
	wsSlug    string
	projectID string
	taskID    string
	fieldID   string
	agentID   string
	initID    string
}

// newVictimFixture registers an ordinary user and populates their workspace with
// one of each object the scoped routes are keyed on.
func newVictimFixture(t *testing.T, prefix string) *victimFixture {
	t.Helper()
	env := NewTestEnv(t)
	t.Cleanup(func() { env.Cleanup(t) })
	env.Register(t, uniqueEmail(prefix), "TestPass123", "Victim Owner")

	f := &victimFixture{env: env}

	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]any
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces)
	f.wsID = workspaces[0]["id"].(string)
	f.wsSlug, _ = workspaces[0]["slug"].(string)
	require.NotEmpty(t, f.wsSlug, "workspace slug is what the WebSocket is keyed on")

	resp = env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/projects", f.wsID), map[string]any{
		"name": "Victim Project",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var project map[string]any
	env.DecodeJSON(t, resp, &project)
	f.projectID = project["id"].(string)
	env.OnCleanup(func() {
		ctx := context.Background()
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", f.projectID)
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM custom_field_definitions WHERE project_id = $1", f.projectID)
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", f.projectID)
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", f.projectID)
	})

	resp = env.Post(t, fmt.Sprintf("/api/v1/projects/%s/tasks", f.projectID), map[string]any{
		"title":       "Victim confidential task",
		"description": "Only the victim's workspace may see this",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var task map[string]any
	env.DecodeJSON(t, resp, &task)
	f.taskID = task["id"].(string)

	resp = env.Post(t, fmt.Sprintf("/api/v1/projects/%s/custom-fields", f.projectID), map[string]any{
		"name":       "Victim Field",
		"slug":       "victim_field",
		"field_type": "number",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var field map[string]any
	env.DecodeJSON(t, resp, &field)
	f.fieldID = field["id"].(string)

	resp = env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/agents", f.wsID), map[string]any{
		"name":       "victim-agent",
		"agent_type": "claude_code",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var registered map[string]any
	env.DecodeJSON(t, resp, &registered)
	f.agentID = registered["agent"].(map[string]any)["id"].(string)
	env.OnCleanup(func() {
		_, _ = env.DB.ExecContext(context.Background(), "DELETE FROM agents WHERE id = $1", f.agentID)
	})

	resp = env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/initiatives", f.wsID), map[string]any{
		"name": "Victim Initiative",
	})
	if resp.StatusCode == http.StatusCreated {
		var initiative map[string]any
		env.DecodeJSON(t, resp, &initiative)
		f.initID = initiative["id"].(string)
		env.OnCleanup(func() {
			_, _ = env.DB.ExecContext(context.Background(), "DELETE FROM initiatives WHERE id = $1", f.initID)
		})
	} else {
		_ = resp.Body.Close()
	}

	return f
}

// concreteURL turns a registered route pattern into a URL pointing at this
// victim's real objects. Parameters that are not workspace-scoping (:user_id,
// :status_id, ...) get a syntactically valid dummy: the guard runs before the
// handler, so what they point at is never looked up.
func (f *victimFixture) concreteURL(pattern string) string {
	replacements := map[string]string{
		":ws_id":    f.wsID,
		":proj_id":  f.projectID,
		":task_id":  f.taskID,
		":field_id": f.fieldID,
		":agent_id": f.agentID,
		":init_id":  f.initID,
	}
	segments := strings.Split(pattern, "/")
	for i, seg := range segments {
		if !strings.HasPrefix(seg, ":") {
			continue
		}
		if v, ok := replacements[seg]; ok && v != "" {
			segments[i] = v
			continue
		}
		segments[i] = dummyUUID
	}
	return "/api/v1" + strings.Join(segments, "/")
}

// discoverScopedRoutes reads the real route registrations out of cmd/api/main.go
// and returns every route keyed on a workspace-scoping parameter other than
// :ws_id. Reading them rather than listing them is the point: the hole this test
// covers was a hand-maintained list of guarded routes falling behind the router.
func discoverScopedRoutes(t *testing.T) []struct{ Method, Path string } {
	t.Helper()
	src, err := os.ReadFile("../../cmd/api/main.go")
	require.NoError(t, err, "cannot read route registrations")

	re := regexp.MustCompile(`api\.(GET|POST|PUT|PATCH|DELETE)\("(/[^"]*)"`)
	var routes []struct{ Method, Path string }
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		method, path := m[1], m[2]
		// /workspaces/:ws_id routes have their own table in cross_tenant_test.go.
		if strings.HasPrefix(path, "/workspaces/:ws_id") || exemptRoutes[path] {
			continue
		}
		scoped := false
		for _, p := range scopedParams {
			if strings.Contains(path, ":"+p) {
				scoped = true
				break
			}
		}
		if !scoped || seen[method+" "+path] {
			continue
		}
		seen[method+" "+path] = true
		routes = append(routes, struct{ Method, Path string }{method, path})
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path == routes[j].Path {
			return routes[i].Method < routes[j].Method
		}
		return routes[i].Path < routes[j].Path
	})
	return routes
}

// TestCrossTenant_NonMemberIsRefusedOnEveryScopedRoute is the regression test for
// the second cross-tenant hole. The first fix guarded routes carrying :ws_id, but
// WorkspaceRLS resolves the tenant from :proj_id, :task_id, :artifact_id,
// :agent_id, :field_id and :init_id as well, and those went unguarded: a stranger
// could read any agent by id, read another tenant's task activity and VCS links,
// read any custom field, and — via POST /agents/:agent_id/activity, which had no
// RBAC bar either — write a forged entry into another tenant's agent activity log.
//
// The route list is read out of cmd/api/main.go rather than typed here, so a route
// added later is proven, not assumed, to be covered.
func TestCrossTenant_NonMemberIsRefusedOnEveryScopedRoute(t *testing.T) {
	victim := newVictimFixture(t, "xtp-victim")

	intruder := NewTestEnv(t)
	defer intruder.Cleanup(t)
	intruder.Register(t, uniqueEmail("xtp-intruder"), "TestPass123", "Intruder")

	routes := discoverScopedRoutes(t)
	require.Greater(t, len(routes), 40,
		"only %d workspace-scoped routes discovered — the registration style has probably changed "+
			"and this test is no longer reading the real routes", len(routes))

	for _, r := range routes {
		t.Run(r.Method+" "+r.Path, func(t *testing.T) {
			if strings.Contains(r.Path, ":init_id") && victim.initID == "" {
				t.Skip("no initiative fixture")
			}
			resp := intruder.doRequest(t, r.Method, victim.concreteURL(r.Path), map[string]any{})
			body := string(intruder.ReadBody(t, resp))

			assert.Equal(t, http.StatusForbidden, resp.StatusCode,
				"a non-member reached %s %s (status %d, body %s)", r.Method, r.Path, resp.StatusCode, body)
			assert.NotContains(t, body, "Victim confidential task",
				"%s %s leaked another tenant's task title", r.Method, r.Path)
			assert.NotContains(t, strings.ToLower(body), "@test.mesh.local",
				"%s %s leaked an email address to a non-member", r.Method, r.Path)
		})
	}
}

// TestCrossTenant_StrangerCannotWriteAgentActivity pins the one finding that was
// a write, not a read: POST /api/v1/agents/:agent_id/activity carried neither the
// membership guard nor an RBAC bar, so an unrelated stranger could inject entries
// into another tenant's agent activity log and got 201 for it. A 403 is not enough
// on its own here — the row must not exist afterwards.
func TestCrossTenant_StrangerCannotWriteAgentActivity(t *testing.T) {
	victim := newVictimFixture(t, "xta-victim")

	intruder := NewTestEnv(t)
	defer intruder.Cleanup(t)
	intruder.Register(t, uniqueEmail("xta-intruder"), "TestPass123", "Intruder")

	const forged = "forged-by-an-unrelated-stranger"
	resp := intruder.Post(t, fmt.Sprintf("/api/v1/agents/%s/activity", victim.agentID), map[string]any{
		"event_type": "task_started",
		"message":    forged,
	})
	body := string(intruder.ReadBody(t, resp))
	require.Equal(t, http.StatusForbidden, resp.StatusCode,
		"a stranger wrote to another tenant's agent activity log (status %d, body %s)", resp.StatusCode, body)

	var count int
	require.NoError(t, victim.env.DB.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM agent_activity_log WHERE agent_id = $1", victim.agentID).Scan(&count))
	assert.Zero(t, count, "the forged activity row reached another tenant's log")
}

// TestCrossTenant_ScopedRoutesStillWorkForTheirOwner is the other half: widening
// the guard from :ws_id to every workspace-scoping parameter must not lock out the
// people the objects belong to.
func TestCrossTenant_ScopedRoutesStillWorkForTheirOwner(t *testing.T) {
	victim := newVictimFixture(t, "xtok-owner")

	readable := []string{
		"/api/v1/projects/" + victim.projectID,
		"/api/v1/projects/" + victim.projectID + "/tasks",
		"/api/v1/tasks/" + victim.taskID,
		"/api/v1/tasks/" + victim.taskID + "/activity",
		"/api/v1/tasks/" + victim.taskID + "/vcs-links",
		"/api/v1/tasks/" + victim.taskID + "/comments",
		"/api/v1/custom-fields/" + victim.fieldID,
		"/api/v1/agents/" + victim.agentID,
		"/api/v1/agents/" + victim.agentID + "/sub-agents",
		"/api/v1/agents/" + victim.agentID + "/heartbeat",
		"/api/v1/agents/" + victim.agentID + "/activity",
	}
	for _, path := range readable {
		t.Run("GET "+path, func(t *testing.T) {
			resp := victim.env.Get(t, path)
			body := string(victim.env.ReadBody(t, resp))
			assert.Less(t, resp.StatusCode, http.StatusBadRequest,
				"the owner was refused %s (status %d, body %s)", path, resp.StatusCode, body)
		})
	}

	t.Run("POST /agents/:agent_id/activity", func(t *testing.T) {
		resp := victim.env.Post(t, fmt.Sprintf("/api/v1/agents/%s/activity", victim.agentID), map[string]any{
			"event_type": "task_started",
			"message":    "owner writing to their own agent's log",
		})
		body := string(victim.env.ReadBody(t, resp))
		assert.Less(t, resp.StatusCode, http.StatusBadRequest,
			"the owner was refused their own agent activity log (status %d, body %s)", resp.StatusCode, body)
	})
}
