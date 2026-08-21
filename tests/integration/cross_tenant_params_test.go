//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
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
	env        *TestEnv
	wsID       string
	wsSlug     string
	projectID  string
	taskID     string
	shortID    string
	fieldID    string
	agentID    string
	initID     string
	artifactID string

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
	secretID    string
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

	// Required, not best-effort, for the same reason as createFlatObjects: a
	// missing fixture turns the route's assertion into "403 because the id
	// does not exist", which passes whether or not cross-tenant access is
	// actually blocked — see #a49500c5, which is exactly this failure mode
	// (concreteURL had no :artifact_id fixture and fell through to a dummy
	// UUID, so the test never exercised "real artifact, foreign workspace"
	// at all).
	f.artifactID = f.createArtifact(t)
	env.OnCleanup(func() {
		_, _ = env.DB.ExecContext(context.Background(), "DELETE FROM artifacts WHERE id = $1", f.artifactID)
	})

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

	// A real secret, so /secrets/:secret_id is exercised against an object that
	// actually exists. Without this the fixture has no :secret_id and
	// concreteURL falls back to dummyUUID — the intruder would be refused a
	// secret that does not exist anywhere, which passes while proving nothing
	// about reaching a REAL one. The value never appears in any response, so
	// unlike the other fixtures there is no marker string to assert on; the
	// status code and the survival check in
	// TestCrossTenant_StrangerCannotRotateOrDeleteASecret carry the assertion.
	f.secretID = f.create(t, "POST", "/api/v1/workspaces/"+f.wsID+"/secrets", map[string]any{
		"name":  "VICTIM_TOKEN",
		"scope": "workspace",
		"value": "victim-secret-token",
	})
}

// createArtifact uploads a real file to the victim's task and returns the
// artifact's id. Artifact upload is multipart/form-data, unlike every other
// fixture here, so it can't go through the shared JSON create() helper.
func (f *victimFixture) createArtifact(t *testing.T) string {
	t.Helper()
	resp := uploadArtifactRaw(t, f.env, f.taskID, "victim-artifact.txt",
		[]byte("Victim confidential artifact content"))
	raw := f.env.ReadBody(t, resp)
	require.Equal(t, http.StatusCreated, resp.StatusCode,
		"fixture setup failed: artifact upload returned %d: %s", resp.StatusCode, string(raw))

	var created map[string]any
	require.NoError(t, json.Unmarshal(raw, &created), "cannot decode artifact upload response: %s", string(raw))
	id, _ := created["id"].(string)
	require.NotEmpty(t, id, "artifact upload returned no id: %s", string(raw))
	return id
}

// uploadArtifactRaw performs the multipart artifact upload and returns the raw
// response — the shared TestEnv.Post helper only speaks JSON, and artifact
// upload takes name/artifact_type/file as multipart form fields.
func uploadArtifactRaw(t *testing.T, env *TestEnv, taskID, filename string, content []byte) *http.Response {
	t.Helper()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	require.NoError(t, w.WriteField("name", filename))
	require.NoError(t, w.WriteField("artifact_type", "file"))
	part, err := w.CreateFormFile("file", filename)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	req, err := http.NewRequest(http.MethodPost,
		env.BaseURL+"/api/v1/tasks/"+taskID+"/artifacts", &buf)
	require.NoError(t, err)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if env.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+env.AuthToken)
	}

	resp, err := env.HTTPClient.Do(req)
	require.NoError(t, err)
	return resp
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
		":artifact_id":  f.artifactID,
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
		":secret_id":    f.secretID,
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

// TestCrossTenant_StrangerCannotRotateOrDeleteASecret pins the secret store's
// write routes the way TestCrossTenant_StrangerCannotWriteAgentActivity pins the
// activity log: for a write, a 403 alone is not the whole assertion — the row
// must still be there, unchanged, afterwards.
//
// Rotate and Delete both work by stamping rotated_at on the CURRENT row, so a
// refusal that leaked through would not look like corruption on the next read.
// It would look like the secret quietly ceasing to exist: the materializer only
// resolves rows with rotated_at IS NULL, so the victim's lanes would stop
// receiving the value on their next spawn and the failure would surface as a
// missing environment variable, far from here. Hence the survival check.
func TestCrossTenant_StrangerCannotRotateOrDeleteASecret(t *testing.T) {
	victim := newVictimFixture(t, "xts-victim")
	require.NotEmpty(t, victim.secretID, "fixture has no secret — the test below would prove nothing")

	intruder := NewTestEnv(t)
	defer intruder.Cleanup(t)
	intruder.Register(t, uniqueEmail("xts-intruder"), "TestPass123", "Intruder")

	stillCurrent := func(t *testing.T) bool {
		t.Helper()
		var rotated *time.Time
		require.NoError(t, victim.env.DB.QueryRowContext(context.Background(),
			"SELECT rotated_at FROM secrets WHERE id = $1", victim.secretID).Scan(&rotated))
		return rotated == nil
	}
	require.True(t, stillCurrent(t), "fixture secret is not current before the test even starts")

	t.Run("rotate", func(t *testing.T) {
		resp := intruder.Post(t, "/api/v1/secrets/"+victim.secretID+"/rotate", map[string]any{
			"value": "planted-by-an-unrelated-stranger",
		})
		body := string(intruder.ReadBody(t, resp))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a stranger rotated another tenant's secret (status %d, body %s)", resp.StatusCode, body)
		assert.NotContains(t, body, "victim-secret-token", "the refusal echoed the secret's value")
		assert.True(t, stillCurrent(t), "a stranger's rotate superseded another tenant's secret")
	})

	t.Run("delete", func(t *testing.T) {
		resp := intruder.doRequest(t, http.MethodDelete, "/api/v1/secrets/"+victim.secretID, nil)
		body := string(intruder.ReadBody(t, resp))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a stranger deleted another tenant's secret (status %d, body %s)", resp.StatusCode, body)
		assert.True(t, stillCurrent(t), "a stranger's delete ended another tenant's secret")
	})
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
		"/api/v1/artifacts/" + victim.artifactID,
		"/api/v1/artifacts/" + victim.artifactID + "/download",
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
