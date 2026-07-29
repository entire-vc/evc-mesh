//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/middleware"
)

// scopedParams are the route parameters WorkspaceRLS resolves a tenant from. Any
// route carrying one of them names another tenant's data and must refuse a
// stranger.
//
// It is the middleware's own list rather than a copy of it: a copy is how this
// test would keep passing while the thing it tests moved on.
var scopedParams = middleware.WorkspaceScopedParams

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
	shortID   string
	fieldID   string
	agentID   string
	initID    string

	// The "flat" objects: routes naming them carry no workspace or project in
	// the path, which is why the guard used to miss them entirely.
	eventID     string
	commentID   string
	viewID      string
	webhookID   string
	intID       string
	tmplID      string
	ruleID      string
	linkID      string
	recurringID string
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

	f.shortID = strings.ReplaceAll(f.taskID, "-", "")[:8]
	f.createFlatObjects(t)

	return f
}

// createFlatObjects populates one of each object reachable by its own id with no
// workspace or project in the path — the routes that answered a stranger until
// WorkspaceRLS learned to resolve them.
//
// Every id here is required, not best-effort: an object that failed to be created
// would turn its route's assertion into "403 because the id does not exist",
// which is the shape of a test that passes whether or not the hole is closed.
func (f *victimFixture) createFlatObjects(t *testing.T) {
	t.Helper()

	f.eventID = f.create(t, "POST", "/api/v1/projects/"+f.projectID+"/events", map[string]any{
		"event_type": "custom",
		"subject":    "victim.confidential.event",
		"payload":    map[string]any{"title": "Victim confidential task"},
	})

	f.commentID = f.create(t, "POST", "/api/v1/tasks/"+f.taskID+"/comments", map[string]any{
		"body": "Victim confidential task — comment body",
	})

	f.viewID = f.create(t, "POST", "/api/v1/projects/"+f.projectID+"/views", map[string]any{
		"name": "Victim View",
	})

	// The webhook URL must be a public hostname: the SSRF guard in
	// webhookService.validateWebhookURL rejects anything resolving to a private
	// or loopback address, so localhost would fail with 400 here.
	f.webhookID = f.create(t, "POST", "/api/v1/workspaces/"+f.wsID+"/webhooks", map[string]any{
		"name":   "victim-hook",
		"url":    "https://example.com/hooks/victim",
		"events": []string{"task.created"},
	})

	f.intID = f.create(t, "POST", "/api/v1/workspaces/"+f.wsID+"/integrations", map[string]any{
		"provider": "slack",
		"config":   map[string]any{"token": "victim-secret-token"},
	})

	f.tmplID = f.create(t, "POST", "/api/v1/projects/"+f.projectID+"/templates", map[string]any{
		"name": "Victim Template",
	})

	f.ruleID = f.create(t, "POST", "/api/v1/workspaces/"+f.wsID+"/rules", map[string]any{
		"rule_type": "transition_gate.require_comment",
		"name":      "Victim Rule",
	})

	f.linkID = f.create(t, "POST", "/api/v1/tasks/"+f.taskID+"/vcs-links", map[string]any{
		"link_type":   "pr",
		"external_id": fmt.Sprintf("%d", time.Now().UnixNano()),
		"url":         "https://github.com/victim/repo/pull/1",
	})

	f.recurringID = f.create(t, "POST", "/api/v1/projects/"+f.projectID+"/recurring", map[string]any{
		"title_template": "Victim recurring task",
		"frequency":      "weekly",
	})
}

// create posts body to path as the victim and returns the new object's id.
func (f *victimFixture) create(t *testing.T, method, path string, body map[string]any) string {
	t.Helper()
	resp := f.env.doRequest(t, method, path, body)
	raw := f.env.ReadBody(t, resp)
	require.Equal(t, http.StatusCreated, resp.StatusCode,
		"fixture setup failed: %s %s returned %d: %s", method, path, resp.StatusCode, string(raw))

	var created map[string]any
	require.NoError(t, json.Unmarshal(raw, &created), "cannot decode %s response: %s", path, string(raw))
	id, _ := created["id"].(string)
	require.NotEmpty(t, id, "%s returned no id: %s", path, string(raw))
	return id
}

// concreteURL turns a registered route pattern into a URL pointing at this
// victim's real objects. Parameters that are not workspace-scoping (:user_id,
// :status_id, ...) get a syntactically valid dummy: the guard runs before the
// handler, so what they point at is never looked up.
func (f *victimFixture) concreteURL(pattern string) string {
	replacements := map[string]string{
		":ws_id":        f.wsID,
		":proj_id":      f.projectID,
		":task_id":      f.taskID,
		":field_id":     f.fieldID,
		":agent_id":     f.agentID,
		":init_id":      f.initID,
		":short":        f.shortID,
		":event_id":     f.eventID,
		":comment_id":   f.commentID,
		":view_id":      f.viewID,
		":webhook_id":   f.webhookID,
		":int_id":       f.intID,
		":tmpl_id":      f.tmplID,
		":rule_id":      f.ruleID,
		":link_id":      f.linkID,
		":recurring_id": f.recurringID,
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
			assert.NotContains(t, body, "victim-secret-token",
				"%s %s leaked another tenant's integration credentials", r.Method, r.Path)
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
		// The flat object routes: closing them must not close them on their owner.
		"/api/v1/events/" + victim.eventID,
		"/api/v1/views/" + victim.viewID,
		"/api/v1/webhooks/" + victim.webhookID,
		"/api/v1/webhooks/" + victim.webhookID + "/deliveries",
		"/api/v1/templates/" + victim.tmplID,
		"/api/v1/rules/" + victim.ruleID,
		"/api/v1/recurring/" + victim.recurringID,
		"/api/v1/recurring/" + victim.recurringID + "/history",
		"/api/v1/tasks/by-short-id/" + victim.shortID,
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

// TestCrossTenant_StrangerCannotReadEventByID is the reported repro, written out
// on its own rather than left to the table above.
//
// GET /api/v1/events/:event_id answered 200 to any logged-in stranger with the
// victim's task title, workspace_id, project_id and the acting agent's uuid in the
// body. It was invisible to the two guards that shipped before it: the route names
// the event directly, so there is no :ws_id and no :proj_id for WorkspaceRLS to
// resolve a tenant from, and with no tenant resolved RequireWorkspaceMemberScoped
// had nothing to compare the caller against and waved the request through as if it
// were /auth/me.
//
// The assertion is deliberately on the body as well as the status: a 403 that
// still returned the payload would be the same leak with a different number on it.
func TestCrossTenant_StrangerCannotReadEventByID(t *testing.T) {
	victim := newVictimFixture(t, "xtev-victim")

	intruder := NewTestEnv(t)
	defer intruder.Cleanup(t)
	intruder.Register(t, uniqueEmail("xtev-intruder"), "TestPass123", "Intruder")

	resp := intruder.Get(t, "/api/v1/events/"+victim.eventID)
	body := string(intruder.ReadBody(t, resp))

	require.Equal(t, http.StatusForbidden, resp.StatusCode,
		"a stranger read another tenant's event (status %d, body %s)", resp.StatusCode, body)
	assert.NotContains(t, body, "Victim confidential task", "the event payload leaked")
	assert.NotContains(t, body, victim.wsID, "the event leaked another tenant's workspace id")
	assert.NotContains(t, body, victim.projectID, "the event leaked another tenant's project id")

	// The owner still reads their own event, and it is the one we created.
	ownerResp := victim.env.Get(t, "/api/v1/events/"+victim.eventID)
	ownerBody := string(victim.env.ReadBody(t, ownerResp))
	require.Equal(t, http.StatusOK, ownerResp.StatusCode,
		"the owner was refused their own event (status %d, body %s)", ownerResp.StatusCode, ownerBody)
	assert.Contains(t, ownerBody, "victim.confidential.event",
		"the owner got something other than their event back")
}

// TestCrossTenant_StrangerCannotResolveShortTaskID covers the enumeration half of
// the same class. GET /api/v1/tasks/by-short-id/:short matches a 6-12 hex prefix
// of a task uuid with LIKE across every tenant's tasks and carried no guard at
// all — so it both returned another tenant's task and, by answering "several tasks
// match" separately from "no such task", confirmed which prefixes exist.
func TestCrossTenant_StrangerCannotResolveShortTaskID(t *testing.T) {
	victim := newVictimFixture(t, "xtsh-victim")

	intruder := NewTestEnv(t)
	defer intruder.Cleanup(t)
	intruder.Register(t, uniqueEmail("xtsh-intruder"), "TestPass123", "Intruder")

	resp := intruder.Get(t, "/api/v1/tasks/by-short-id/"+victim.shortID)
	body := string(intruder.ReadBody(t, resp))
	require.Equal(t, http.StatusForbidden, resp.StatusCode,
		"a stranger resolved another tenant's task by short id (status %d, body %s)", resp.StatusCode, body)
	assert.NotContains(t, body, "Victim confidential task", "the task leaked")

	ownerResp := victim.env.Get(t, "/api/v1/tasks/by-short-id/"+victim.shortID)
	ownerBody := string(victim.env.ReadBody(t, ownerResp))
	require.Equal(t, http.StatusOK, ownerResp.StatusCode,
		"the owner was refused their own task by short id (status %d, body %s)", ownerResp.StatusCode, ownerBody)
	assert.Contains(t, ownerBody, "Victim confidential task")
}

// TestCrossTenant_StrangerCannotWriteFlatObjects pins the writes. A 403 on a read
// is the whole story; on a write it is not — the row must be unchanged afterwards.
// Every one of these was reachable: PATCH /integrations/:int_id echoed the target
// tenant's stored credentials back in its response, POST /recurring/:id/trigger
// created a real task in their project, and DELETE /vcs-links/:link_id removed
// their data outright.
// The intruder here is an AGENT, not a user, and that is the whole point. Most of
// these routes carry rbac(...), which looks like a second line of defence and is
// not one: for a user JWT it 403s only because no route parameter had resolved a
// workspace for it to read, and for an agent key it short-circuits to a static
// capability map (middleware/rbac.go) and never looks at the target object's
// workspace at all. So an agent key — which every one of our agents holds, and
// which any OSS self-hoster's users can mint in their own workspace — walked
// straight through. A user-JWT intruder would make this test pass against the
// unfixed binary and prove nothing.
func TestCrossTenant_StrangerCannotWriteFlatObjects(t *testing.T) {
	victim := newVictimFixture(t, "xtw-victim")

	intruder := NewTestEnv(t)
	defer intruder.Cleanup(t)
	intruder.Register(t, uniqueEmail("xtw-intruder"), "TestPass123", "Intruder")
	intruderKey := intruder.registerOwnAgent(t, "xtw-intruder-agent")

	writes := []struct {
		name   string
		method string
		path   string
		body   map[string]any
	}{
		{"rename another tenant's integration", http.MethodPatch,
			"/api/v1/integrations/" + victim.intID, map[string]any{"is_active": true}},
		{"rewrite another tenant's webhook", http.MethodPatch,
			"/api/v1/webhooks/" + victim.webhookID, map[string]any{"name": "hijacked"}},
		{"rewrite another tenant's rule", http.MethodPatch,
			"/api/v1/rules/" + victim.ruleID, map[string]any{"name": "hijacked"}},
		{"rewrite another tenant's saved view", http.MethodPatch,
			"/api/v1/views/" + victim.viewID, map[string]any{"name": "hijacked"}},
		{"rewrite another tenant's template", http.MethodPatch,
			"/api/v1/templates/" + victim.tmplID, map[string]any{"name": "hijacked"}},
		{"rewrite another tenant's schedule", http.MethodPatch,
			"/api/v1/recurring/" + victim.recurringID, map[string]any{"title_template": "hijacked"}},
		{"trigger another tenant's schedule", http.MethodPost,
			"/api/v1/recurring/" + victim.recurringID + "/trigger", map[string]any{}},
		{"delete another tenant's vcs link", http.MethodDelete,
			"/api/v1/vcs-links/" + victim.linkID, nil},
		{"delete another tenant's comment", http.MethodDelete,
			"/api/v1/comments/" + victim.commentID, nil},
	}

	for _, w := range writes {
		t.Run(w.name, func(t *testing.T) {
			resp := intruder.doRequestAsAgent(t, w.method, w.path, w.body, intruderKey)
			body := string(intruder.ReadBody(t, resp))
			assert.Equal(t, http.StatusForbidden, resp.StatusCode,
				"%s %s was allowed (status %d, body %s)", w.method, w.path, resp.StatusCode, body)
			assert.NotContains(t, body, "victim-secret-token",
				"%s %s echoed another tenant's credentials back", w.method, w.path)
		})
	}

	// Nothing named "hijacked" reached the victim's tenant, and what should still
	// exist does.
	ctx := context.Background()
	for _, check := range []struct {
		what  string
		query string
		arg   string
	}{
		{"webhook", `SELECT COUNT(*) FROM webhook_configs WHERE id = $1 AND name = 'hijacked'`, victim.webhookID},
		{"rule", `SELECT COUNT(*) FROM rules WHERE id = $1 AND name = 'hijacked'`, victim.ruleID},
		{"saved view", `SELECT COUNT(*) FROM saved_views WHERE id = $1 AND name = 'hijacked'`, victim.viewID},
		{"template", `SELECT COUNT(*) FROM task_templates WHERE id = $1 AND name = 'hijacked'`, victim.tmplID},
		{"schedule", `SELECT COUNT(*) FROM recurring_schedules WHERE id = $1 AND title_template = 'hijacked'`, victim.recurringID},
	} {
		var n int
		require.NoError(t, victim.env.DB.QueryRowContext(ctx, check.query, check.arg).Scan(&n))
		assert.Zero(t, n, "a stranger rewrote another tenant's %s", check.what)
	}

	for _, check := range []struct {
		what  string
		query string
		arg   string
	}{
		{"vcs link", `SELECT COUNT(*) FROM vcs_links WHERE id = $1`, victim.linkID},
		{"comment", `SELECT COUNT(*) FROM comments WHERE id = $1`, victim.commentID},
	} {
		var n int
		require.NoError(t, victim.env.DB.QueryRowContext(ctx, check.query, check.arg).Scan(&n))
		assert.Equal(t, 1, n, "a stranger deleted another tenant's %s", check.what)
	}

	// The forged trigger must not have created a task in the victim's project.
	var hijacked int
	require.NoError(t, victim.env.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks WHERE project_id = $1 AND title = 'Victim recurring task'`,
		victim.projectID).Scan(&hijacked))
	assert.Zero(t, hijacked, "a stranger triggered another tenant's recurring schedule")
}

// registerOwnAgent registers an agent in this environment's own workspace and
// returns its api key. It is how a test gets hold of the credential that rbac()
// waves through — see TestCrossTenant_StrangerCannotWriteFlatObjects.
func (e *TestEnv) registerOwnAgent(t *testing.T, name string) string {
	t.Helper()

	resp := e.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]any
	e.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces, "the intruder has no workspace of their own")
	ownWS := workspaces[0]["id"].(string)

	resp = e.Post(t, "/api/v1/workspaces/"+ownWS+"/agents", map[string]any{
		"name":       name,
		"agent_type": "claude_code",
	})
	raw := e.ReadBody(t, resp)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "agent registration failed: %s", string(raw))

	var registered map[string]any
	require.NoError(t, json.Unmarshal(raw, &registered))
	key, _ := registered["api_key"].(string)
	require.NotEmpty(t, key, "no api_key in the registration response: %s", string(raw))

	if agent, ok := registered["agent"].(map[string]any); ok {
		if id, ok := agent["id"].(string); ok {
			e.OnCleanup(func() {
				_, _ = e.DB.ExecContext(context.Background(), "DELETE FROM agents WHERE id = $1", id)
			})
		}
	}
	return key
}

// doRequestAsAgent issues a request authenticated with an agent key rather than a
// user JWT.
func (e *TestEnv) doRequestAsAgent(t *testing.T, method, path string, body any, agentKey string) *http.Response {
	t.Helper()

	var bodyReader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.BaseURL+path, bodyReader)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Agent-Key", agentKey)

	resp, err := e.HTTPClient.Do(req)
	require.NoError(t, err, "%s %s", method, path)
	return resp
}

// --- Composite routes: a parent id you own plus a child id you do not ---------

// compositeFixture holds a victim tenant carrying one of each object that hangs
// off a parent in the path, and an intruder tenant with parents of their own to
// name in its place.
//
// The intruder acts with their own user token rather than an agent key, and that
// is deliberate: they are the OWNER of their workspace, and a workspace owner
// bypasses the project-membership middleware (RequireProjectMember) on any project
// in their own workspace. An agent key would be stopped there before ever reaching
// the handler — the request would be refused for a reason that has nothing to do
// with the hole, and the test would pass against the unfixed binary and prove
// nothing. /rules/evaluate is additionally exercised with an agent key below,
// where rbac() genuinely is the only thing in the way.
type compositeFixture struct {
	victim *victimFixture

	victimStatusID   string
	victimAutoRuleID string
	victimDepID      string
	victimInviteID   string

	intruder       *TestEnv
	intruderKey    string
	intruderWsID   string
	intruderProjID string
	intruderTaskID string
	intruderInitID string
}

// A new project is seeded with default statuses, so the fixture's own status
// needs a name that will not collide with one of them on (project_id, slug).
const victimStatusName = "Victim Backlog"

// newCompositeFixture builds both tenants.
//
// Every id here is required rather than best-effort. An object that failed to be
// created would turn its assertion into "refused because the id does not exist",
// which looks identical to a working guard and holds whether or not one is
// present.
func newCompositeFixture(t *testing.T, prefix string) *compositeFixture {
	t.Helper()

	f := &compositeFixture{victim: newVictimFixture(t, prefix+"-victim")}
	v := f.victim

	f.victimStatusID = v.create(t, "POST", "/api/v1/projects/"+v.projectID+"/statuses", map[string]any{
		"name":     victimStatusName,
		"color":    "#6B7280",
		"category": "backlog",
	})

	f.victimAutoRuleID = f.victimAutoTransitionRule(t)

	// A dependency needs a second task to point at.
	otherTaskID := v.create(t, "POST", "/api/v1/projects/"+v.projectID+"/tasks", map[string]any{
		"title": "Victim blocking task",
	})
	f.victimDepID = v.create(t, "POST", "/api/v1/tasks/"+v.taskID+"/dependencies", map[string]any{
		"depends_on_task_id": otherTaskID,
		"dependency_type":    "blocks",
	})

	f.victimInviteID = v.create(t, "POST", "/api/v1/workspaces/"+v.wsID+"/invites", map[string]any{
		"email": uniqueEmail(prefix + "-invitee"),
		"role":  "member",
	})

	// The intruder: an ordinary account, owner of its own workspace.
	f.intruder = NewTestEnv(t)
	t.Cleanup(func() { f.intruder.Cleanup(t) })
	f.intruder.Register(t, uniqueEmail(prefix+"-intruder"), "TestPass123", "Intruder")

	resp := f.intruder.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]any
	f.intruder.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces, "the intruder has no workspace of their own")
	f.intruderWsID = workspaces[0]["id"].(string)

	f.intruderProjID = f.createAs(t, f.intruder, "POST", "/api/v1/workspaces/"+f.intruderWsID+"/projects", map[string]any{
		"name": "Intruder Project",
	})
	f.intruder.OnCleanup(func() {
		ctx := context.Background()
		_, _ = f.intruder.DB.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", f.intruderProjID)
		_, _ = f.intruder.DB.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", f.intruderProjID)
		_, _ = f.intruder.DB.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", f.intruderProjID)
	})

	f.intruderTaskID = f.createAs(t, f.intruder, "POST", "/api/v1/projects/"+f.intruderProjID+"/tasks", map[string]any{
		"title": "Intruder own task",
	})

	resp = f.intruder.Post(t, "/api/v1/workspaces/"+f.intruderWsID+"/initiatives", map[string]any{
		"name": "Intruder Initiative",
	})
	if resp.StatusCode == http.StatusCreated {
		var initiative map[string]any
		f.intruder.DecodeJSON(t, resp, &initiative)
		f.intruderInitID = initiative["id"].(string)
		f.intruder.OnCleanup(func() {
			_, _ = f.intruder.DB.ExecContext(context.Background(),
				"DELETE FROM initiatives WHERE id = $1", f.intruderInitID)
		})
	} else {
		_ = resp.Body.Close()
	}

	f.intruderKey = f.intruder.registerOwnAgent(t, prefix+"-intruder-agent")

	return f
}

// victimAutoTransitionRule returns the id of an auto-transition rule in the
// victim's project.
//
// A new project is seeded with a rule per trigger, and (project_id, trigger) is
// unique, so creating one is not reliably possible. The seeded rule is the
// victim's own object either way, which is all the test needs.
func (f *compositeFixture) victimAutoTransitionRule(t *testing.T) string {
	t.Helper()
	v := f.victim

	resp := v.env.Get(t, "/api/v1/projects/"+v.projectID+"/auto-transition-rules")
	raw := v.env.ReadBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"cannot list the victim's auto-transition rules: %s", string(raw))

	var rules []map[string]any
	require.NoError(t, json.Unmarshal(raw, &rules), "cannot decode rules: %s", string(raw))
	if len(rules) > 0 {
		id, _ := rules[0]["id"].(string)
		require.NotEmpty(t, id, "an auto-transition rule with no id: %s", string(raw))
		return id
	}

	return v.create(t, "POST", "/api/v1/projects/"+v.projectID+"/auto-transition-rules", map[string]any{
		"trigger":          "all_subtasks_done",
		"target_status_id": f.victimStatusID,
	})
}

// createAs posts body to path as env and returns the new object's id.
func (f *compositeFixture) createAs(t *testing.T, env *TestEnv, method, path string, body map[string]any) string {
	t.Helper()
	resp := env.doRequest(t, method, path, body)
	raw := env.ReadBody(t, resp)
	require.Equal(t, http.StatusCreated, resp.StatusCode,
		"fixture setup failed: %s %s returned %d: %s", method, path, resp.StatusCode, string(raw))
	var created map[string]any
	require.NoError(t, json.Unmarshal(raw, &created), "cannot decode %s response: %s", path, string(raw))
	id, _ := created["id"].(string)
	require.NotEmpty(t, id, "%s returned no id: %s", path, string(raw))
	return id
}

// TestCrossTenant_CompositeRouteChildIsChecked is the regression test for the
// fourth cross-tenant hole, and the one the previous three fixes walked straight
// past.
//
// The guard resolved the tenant from the FIRST route parameter that produced one
// and checked membership of that. On a route naming a parent and a child, the
// parent comes first — so a caller supplied a parent they legitimately own
// together with a child id belonging to another tenant, the guard checked them
// against their own parent and passed, and the handler then acted on the child by
// its id alone. The child was never resolved and never checked by anything.
//
// Every assertion here is on the database as well as the status code. A 403
// printed over a completed write is indistinguishable from a refusal if you only
// read the response: what proves the write did not happen is that the victim's row
// is still what it was.
func TestCrossTenant_CompositeRouteChildIsChecked(t *testing.T) {
	f := newCompositeFixture(t, "xtc")
	ctx := context.Background()

	t.Run("PATCH /projects/:proj_id/statuses/:status_id", func(t *testing.T) {
		resp := f.intruder.Patch(t,
			fmt.Sprintf("/api/v1/projects/%s/statuses/%s", f.intruderProjID, f.victimStatusID),
			map[string]any{"name": "PWNED-BY-B"})
		body := string(f.intruder.ReadBody(t, resp))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a stranger renamed another tenant's status (status %d, body %s)", resp.StatusCode, body)

		var name string
		require.NoError(t, f.victim.env.DB.QueryRowContext(ctx,
			"SELECT name FROM task_statuses WHERE id = $1", f.victimStatusID).Scan(&name))
		assert.Equal(t, victimStatusName, name, "another tenant's status was renamed")
	})

	t.Run("DELETE /projects/:proj_id/auto-transition-rules/:auto_rule_id", func(t *testing.T) {
		resp := f.intruder.Delete(t,
			fmt.Sprintf("/api/v1/projects/%s/auto-transition-rules/%s", f.intruderProjID, f.victimAutoRuleID))
		body := string(f.intruder.ReadBody(t, resp))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a stranger deleted another tenant's auto-transition rule (status %d, body %s)", resp.StatusCode, body)

		var n int
		require.NoError(t, f.victim.env.DB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM auto_transition_rules WHERE id = $1", f.victimAutoRuleID).Scan(&n))
		assert.Equal(t, 1, n, "another tenant's auto-transition rule was deleted")
	})

	t.Run("DELETE /tasks/:task_id/dependencies/:dep_id", func(t *testing.T) {
		resp := f.intruder.Delete(t,
			fmt.Sprintf("/api/v1/tasks/%s/dependencies/%s", f.intruderTaskID, f.victimDepID))
		body := string(f.intruder.ReadBody(t, resp))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a stranger deleted another tenant's dependency (status %d, body %s)", resp.StatusCode, body)

		var n int
		require.NoError(t, f.victim.env.DB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM task_dependencies WHERE id = $1", f.victimDepID).Scan(&n))
		assert.Equal(t, 1, n, "another tenant's dependency edge was deleted")
	})

	t.Run("POST /workspaces/:ws_id/invites/:invite_id/resend", func(t *testing.T) {
		resp := f.intruder.Post(t,
			fmt.Sprintf("/api/v1/workspaces/%s/invites/%s/resend", f.intruderWsID, f.victimInviteID),
			map[string]any{})
		body := string(f.intruder.ReadBody(t, resp))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a stranger re-sent another tenant's invite email (status %d, body %s)", resp.StatusCode, body)
	})

	t.Run("DELETE /workspaces/:ws_id/invites/:invite_id", func(t *testing.T) {
		resp := f.intruder.Delete(t,
			fmt.Sprintf("/api/v1/workspaces/%s/invites/%s", f.intruderWsID, f.victimInviteID))
		body := string(f.intruder.ReadBody(t, resp))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a stranger revoked another tenant's invite (status %d, body %s)", resp.StatusCode, body)

		var n int
		require.NoError(t, f.victim.env.DB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM user_invites WHERE id = $1", f.victimInviteID).Scan(&n))
		assert.Equal(t, 1, n, "another tenant's pending invite was revoked")
	})

	// This route is the one composite route that was NOT reachable across tenants:
	// ArtifactHandler.Download already re-checks the artifact against the resolved
	// workspace itself (GetByIDInWorkspace). What is asserted here is the guard's
	// fail-closed half — a child id that resolves to nothing is refused before the
	// handler rather than waved through — because that is the property the other
	// five routes had no handler check to fall back on.
	t.Run("GET /tasks/:task_id/artifacts/:artifact_id/download", func(t *testing.T) {
		resp := f.intruder.Get(t,
			fmt.Sprintf("/api/v1/tasks/%s/artifacts/%s/download", f.intruderTaskID, dummyUUID))
		body := string(f.intruder.ReadBody(t, resp))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"an unresolvable artifact id was not refused (status %d, body %s)", resp.StatusCode, body)
	})
}

// TestCrossTenant_BodySuppliedWorkspaceIsChecked covers the other half of the same
// class: routes whose tenant does not come from the path at all.
//
// RequireWorkspaceMemberScoped can only check a workspace it can see, and it reads
// the route path. A handler that instead takes its workspace from a JSON body
// field is invisible to it — the route looks like /auth/me, a route with nothing
// to check — so the caller's claim about which tenant they are acting on went
// unexamined. POST /rules/evaluate has no path parameter at all.
func TestCrossTenant_BodySuppliedWorkspaceIsChecked(t *testing.T) {
	f := newCompositeFixture(t, "xtb")
	ctx := context.Background()

	// /rules/evaluate is a dry run and writes nothing, so the status code and the
	// absence of the victim's rule in the response body are the whole assertion.
	t.Run("POST /rules/evaluate as a user", func(t *testing.T) {
		resp := f.intruder.Post(t, "/api/v1/rules/evaluate", map[string]any{
			"action":       "move_task",
			"workspace_id": f.victim.wsID,
			"task_id":      f.victim.taskID,
		})
		body := string(f.intruder.ReadBody(t, resp))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a stranger evaluated another tenant's rules (status %d, body %s)", resp.StatusCode, body)
		assert.NotContains(t, body, "Victim Rule", "the victim's rule name leaked")
	})

	// The agent-key case is the one rbac() does not cover: on an agent key it
	// short-circuits to a static capability map and never looks at the target
	// workspace.
	t.Run("POST /rules/evaluate as an agent", func(t *testing.T) {
		resp := f.intruder.doRequestAsAgent(t, http.MethodPost, "/api/v1/rules/evaluate", map[string]any{
			"action":       "move_task",
			"workspace_id": f.victim.wsID,
			"task_id":      f.victim.taskID,
		}, f.intruderKey)
		body := string(f.intruder.ReadBody(t, resp))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"an agent key evaluated another tenant's rules (status %d, body %s)", resp.StatusCode, body)
		assert.NotContains(t, body, "Victim Rule", "the victim's rule name leaked")
	})

	t.Run("PUT /notifications/preferences", func(t *testing.T) {
		resp := f.intruder.Put(t, "/api/v1/notifications/preferences", map[string]any{
			"workspace_id": f.victim.wsID,
			"channel":      "web_push",
			"events":       []string{"task.assigned"},
		})
		body := string(f.intruder.ReadBody(t, resp))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a stranger wrote preferences into another tenant's workspace (status %d, body %s)",
			resp.StatusCode, body)

		var n int
		require.NoError(t, f.victim.env.DB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM notification_preferences WHERE workspace_id = $1", f.victim.wsID).Scan(&n))
		assert.Zero(t, n, "a preferences row reached another tenant's workspace")
	})

	t.Run("POST /initiatives/:init_id/projects", func(t *testing.T) {
		if f.intruderInitID == "" {
			t.Skip("no initiative fixture")
		}
		resp := f.intruder.Post(t, "/api/v1/initiatives/"+f.intruderInitID+"/projects", map[string]any{
			"project_id": f.victim.projectID,
		})
		body := string(f.intruder.ReadBody(t, resp))
		assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
			"a stranger linked another tenant's project into their initiative (status %d, body %s)",
			resp.StatusCode, body)

		var n int
		require.NoError(t, f.victim.env.DB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM initiative_projects WHERE project_id = $1", f.victim.projectID).Scan(&n))
		assert.Zero(t, n, "another tenant's project was linked into a foreign initiative")
	})
}

// TestCrossTenant_CompositeRoutesStillWorkForTheirOwner is the half that matters
// most here, and the reason :rule_id had to be renamed to :auto_rule_id on the
// auto-transition routes.
//
// One parameter spelling cannot name two tables. /rules/:rule_id is a row in
// `rules`; /projects/:proj_id/auto-transition-rules/:rule_id was a row in
// `auto_transition_rules`. Had both kept the same spelling, the guard would have
// looked an auto-transition rule's id up in `rules`, found nothing, and refused —
// blocking the owner exactly as firmly as the intruder, while a test that only
// asserted "the intruder gets 403" stayed green over a completely broken feature.
// So every route touched by this change is exercised here by the person it belongs
// to, and the assertion is that the effect actually happened.
func TestCrossTenant_CompositeRoutesStillWorkForTheirOwner(t *testing.T) {
	f := newCompositeFixture(t, "xtco")
	v := f.victim
	ctx := context.Background()

	t.Run("PATCH own status", func(t *testing.T) {
		resp := v.env.Patch(t,
			fmt.Sprintf("/api/v1/projects/%s/statuses/%s", v.projectID, f.victimStatusID),
			map[string]any{"name": "Owner Renamed"})
		body := string(v.env.ReadBody(t, resp))
		require.Equal(t, http.StatusOK, resp.StatusCode,
			"the owner was refused their own status (status %d, body %s)", resp.StatusCode, body)

		var name string
		require.NoError(t, v.env.DB.QueryRowContext(ctx,
			"SELECT name FROM task_statuses WHERE id = $1", f.victimStatusID).Scan(&name))
		assert.Equal(t, "Owner Renamed", name, "the owner's rename did not take effect")
	})

	t.Run("PUT own auto-transition rule", func(t *testing.T) {
		enabled := false
		resp := v.env.Put(t,
			fmt.Sprintf("/api/v1/projects/%s/auto-transition-rules/%s", v.projectID, f.victimAutoRuleID),
			map[string]any{"is_enabled": enabled})
		body := string(v.env.ReadBody(t, resp))
		require.Equal(t, http.StatusOK, resp.StatusCode,
			"the owner was refused their own auto-transition rule (status %d, body %s)", resp.StatusCode, body)

		var isEnabled bool
		require.NoError(t, v.env.DB.QueryRowContext(ctx,
			"SELECT is_enabled FROM auto_transition_rules WHERE id = $1", f.victimAutoRuleID).Scan(&isEnabled))
		assert.False(t, isEnabled, "the owner's update did not take effect")
	})

	t.Run("DELETE own auto-transition rule", func(t *testing.T) {
		resp := v.env.Delete(t,
			fmt.Sprintf("/api/v1/projects/%s/auto-transition-rules/%s", v.projectID, f.victimAutoRuleID))
		body := string(v.env.ReadBody(t, resp))
		require.Equal(t, http.StatusNoContent, resp.StatusCode,
			"the owner was refused deleting their own auto-transition rule (status %d, body %s)",
			resp.StatusCode, body)

		var n int
		require.NoError(t, v.env.DB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM auto_transition_rules WHERE id = $1", f.victimAutoRuleID).Scan(&n))
		assert.Zero(t, n, "the owner's delete did not take effect")
	})

	t.Run("DELETE own dependency", func(t *testing.T) {
		resp := v.env.Delete(t,
			fmt.Sprintf("/api/v1/tasks/%s/dependencies/%s", v.taskID, f.victimDepID))
		body := string(v.env.ReadBody(t, resp))
		require.Equal(t, http.StatusNoContent, resp.StatusCode,
			"the owner was refused deleting their own dependency (status %d, body %s)", resp.StatusCode, body)

		var n int
		require.NoError(t, v.env.DB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM task_dependencies WHERE id = $1", f.victimDepID).Scan(&n))
		assert.Zero(t, n, "the owner's delete did not take effect")
	})

	t.Run("POST /rules/evaluate on own workspace", func(t *testing.T) {
		resp := v.env.Post(t, "/api/v1/rules/evaluate", map[string]any{
			"action":       "move_task",
			"workspace_id": v.wsID,
			"task_id":      v.taskID,
		})
		body := string(v.env.ReadBody(t, resp))
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"the owner was refused evaluating their own rules (status %d, body %s)", resp.StatusCode, body)
	})

	t.Run("POST /rules/evaluate with an agent key and no workspace_id", func(t *testing.T) {
		// An agent key carries its own workspace, so omitting workspace_id has to
		// keep working: the guard falls back to the credential's own tenant, which
		// needs no check.
		key := v.env.registerOwnAgent(t, "xtco-victim-agent")
		resp := v.env.doRequestAsAgent(t, http.MethodPost, "/api/v1/rules/evaluate", map[string]any{
			"action":  "move_task",
			"task_id": v.taskID,
		}, key)
		body := string(v.env.ReadBody(t, resp))
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"an agent key was refused its own workspace (status %d, body %s)", resp.StatusCode, body)
	})

	t.Run("PUT own notification preferences", func(t *testing.T) {
		resp := v.env.Put(t, "/api/v1/notifications/preferences", map[string]any{
			"workspace_id": v.wsID,
			"channel":      "web_push",
			"events":       []string{"task.assigned"},
		})
		body := string(v.env.ReadBody(t, resp))
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"the owner was refused their own preferences (status %d, body %s)", resp.StatusCode, body)
	})

	t.Run("DELETE own invite", func(t *testing.T) {
		resp := v.env.Delete(t,
			fmt.Sprintf("/api/v1/workspaces/%s/invites/%s", v.wsID, f.victimInviteID))
		body := string(v.env.ReadBody(t, resp))
		require.Equal(t, http.StatusNoContent, resp.StatusCode,
			"the owner was refused revoking their own invite (status %d, body %s)", resp.StatusCode, body)

		var n int
		require.NoError(t, v.env.DB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM user_invites WHERE id = $1", f.victimInviteID).Scan(&n))
		assert.Zero(t, n, "the owner's revoke did not take effect")
	})

	t.Run("DELETE own project member", func(t *testing.T) {
		// :user_id names a global user account, not a tenant-owned row, so it is
		// excused from resolving a workspace. This pins that the route still works
		// and that the composite guard did not start refusing it.
		resp := v.env.Delete(t,
			fmt.Sprintf("/api/v1/projects/%s/members/%s", v.projectID, v.env.UserID))
		body := string(v.env.ReadBody(t, resp))
		assert.Less(t, resp.StatusCode, http.StatusInternalServerError,
			"the owner hit a server error on their own project membership (status %d, body %s)",
			resp.StatusCode, body)
		assert.NotEqual(t, http.StatusForbidden, resp.StatusCode,
			"the owner was refused their own project membership route (body %s)", body)
	})
}
