//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// End-to-end cover for the fourth tenant channel: the caller names a PRINCIPAL
// from another workspace, and the system both accepts the assignment and then
// delivers the task to that principal.
//
// The unit tests in internal/service prove the guard refuses. These prove it over
// real HTTP against a real database, which is where three things can differ:
//
//   - the agent-key path. rbac() short-circuits to a static capability map for an
//     agent key and never looks at the target object's workspace, so "the route
//     carries rbac(...)" has never been a tenancy check. Only an end-to-end run
//     under a real key shows which guard actually fired.
//   - the read side. ListByAssignee's workspace predicate is SQL; a mock cannot
//     prove a WHERE clause reaches Postgres.
//   - RLS. The policies on `tasks` are real but inert for this process — the
//     backend connects as the table owner and FORCE ROW LEVEL SECURITY is
//     deliberately off. Running against the actual database is what distinguishes
//     "the application refused" from "something else would have caught it".

// twoWorkspaceFixture is one operator owning two workspaces, with a project and
// an agent in each. HOME is where the task lives; FOREIGN is where the principal
// the caller must not be able to name lives.
type twoWorkspaceFixture struct {
	env *TestEnv

	homeWS, foreignWS       string
	homeProject             string
	homeTodoStatus          string
	homeAgentID, homeKey    string
	foreignAgentID, foreign string
}

func newTwoWorkspaceFixture(t *testing.T) *twoWorkspaceFixture {
	t.Helper()

	env := NewTestEnv(t)
	t.Cleanup(func() { env.Cleanup(t) })
	env.Register(t, uniqueEmail("xwa-operator"), "TestPass123", "Operator")

	mkWorkspace := func(name string) string {
		resp := env.Post(t, "/api/v1/workspaces", map[string]interface{}{"name": name})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		var created map[string]interface{}
		env.DecodeJSON(t, resp, &created)
		id, ok := created["id"].(string)
		require.True(t, ok)
		env.OnCleanup(func() {
			ctx := context.Background()
			_, _ = env.DB.ExecContext(ctx, "DELETE FROM workspace_members WHERE workspace_id = $1", id)
			_, _ = env.DB.ExecContext(ctx, "DELETE FROM workspaces WHERE id = $1", id)
		})
		return id
	}

	f := &twoWorkspaceFixture{env: env}
	f.homeWS = mkWorkspace("Home Tenant")
	f.foreignWS = mkWorkspace("Foreign Tenant")

	// A project in HOME, with its default statuses.
	resp := env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/projects", f.homeWS),
		map[string]interface{}{"name": "Home Project"})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var project map[string]interface{}
	env.DecodeJSON(t, resp, &project)
	f.homeProject = project["id"].(string)
	env.OnCleanup(func() {
		ctx := context.Background()
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", f.homeProject)
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM project_members WHERE project_id = $1", f.homeProject)
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", f.homeProject)
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", f.homeProject)
	})

	resp = env.Get(t, fmt.Sprintf("/api/v1/projects/%s/statuses", f.homeProject))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var statuses []map[string]interface{}
	env.DecodeJSON(t, resp, &statuses)
	for _, s := range statuses {
		if s["category"] == "todo" {
			f.homeTodoStatus = s["id"].(string)
		}
	}
	require.NotEmpty(t, f.homeTodoStatus, "project must have a todo status")

	f.homeAgentID, f.homeKey = env.CreateAgent(t, f.homeWS, "home-agent")
	f.foreignAgentID, f.foreign = env.CreateAgent(t, f.foreignWS, "foreign-agent")

	return f
}

// createHomeTask makes a task in the home project and returns its id.
func (f *twoWorkspaceFixture) createHomeTask(t *testing.T, title string) string {
	t.Helper()
	resp := f.env.Post(t, fmt.Sprintf("/api/v1/projects/%s/tasks", f.homeProject),
		map[string]interface{}{"title": title, "status_id": f.homeTodoStatus})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var task map[string]interface{}
	f.env.DecodeJSON(t, resp, &task)
	return task["id"].(string)
}

// assertRefused checks the response is the tenancy refusal rather than any other
// 4xx: a 404 or a validation error would also be "not 200" while meaning the test
// never reached the guard.
func assertAssigneeRefused(t *testing.T, env *TestEnv, resp *http.Response, what string) {
	t.Helper()
	body := string(env.ReadBody(t, resp))
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode,
		"%s must be refused with 422, got %d: %s", what, resp.StatusCode, body)
	assert.Contains(t, body, "assignee_not_in_workspace",
		"%s must be refused by the assignee tenancy guard specifically, not incidentally: %s", what, body)
}

func TestCrossWorkspaceAssignee_RefusedOnEveryWritePath_JWT(t *testing.T) {
	f := newTwoWorkspaceFixture(t)
	env := f.env

	t.Run("POST /projects/:proj_id/tasks", func(t *testing.T) {
		resp := env.Post(t, fmt.Sprintf("/api/v1/projects/%s/tasks", f.homeProject), map[string]interface{}{
			"title": "planted at create", "status_id": f.homeTodoStatus,
			"assignee_id": f.foreignAgentID, "assignee_type": "agent",
		})
		assertAssigneeRefused(t, env, resp, "create_task with a foreign assignee")
	})

	t.Run("POST /tasks/:task_id/assign", func(t *testing.T) {
		taskID := f.createHomeTask(t, "assign target")
		resp := env.Post(t, fmt.Sprintf("/api/v1/tasks/%s/assign", taskID), map[string]interface{}{
			"assignee_id": f.foreignAgentID, "assignee_type": "agent",
		})
		assertAssigneeRefused(t, env, resp, "assign_task to a foreign agent")
	})

	t.Run("PATCH /tasks/:task_id", func(t *testing.T) {
		taskID := f.createHomeTask(t, "patch target")
		resp := env.Patch(t, fmt.Sprintf("/api/v1/tasks/%s", taskID), map[string]interface{}{
			"assignee_id": f.foreignAgentID, "assignee_type": "agent",
		})
		assertAssigneeRefused(t, env, resp, "PATCH task with a foreign assignee")
	})

	t.Run("POST /tasks/:task_id/subtasks", func(t *testing.T) {
		parentID := f.createHomeTask(t, "subtask parent")
		resp := env.Post(t, fmt.Sprintf("/api/v1/tasks/%s/subtasks", parentID), map[string]interface{}{
			"title": "planted child", "assignee_id": f.foreignAgentID, "assignee_type": "agent",
		})
		assertAssigneeRefused(t, env, resp, "create_subtask with a foreign assignee")
	})

	t.Run("no enrolment row survives any of it", func(t *testing.T) {
		var n int
		require.NoError(t, env.DB.QueryRowContext(context.Background(),
			"SELECT count(*) FROM project_members WHERE project_id = $1 AND agent_id = $2",
			f.homeProject, f.foreignAgentID).Scan(&n))
		assert.Zero(t, n,
			"a refused assignment must not leave the foreign agent enrolled in the project — "+
				"that row is what makes it look native to every check that runs later")
	})
}

// TestCrossWorkspaceAssignee_RefusedUnderAnAgentKey is the arm that the route's
// rbac() cannot cover.
func TestCrossWorkspaceAssignee_RefusedUnderAnAgentKey(t *testing.T) {
	f := newTwoWorkspaceFixture(t)
	taskID := f.createHomeTask(t, "agent-key assign target")

	resp := f.env.PostWithAgentKey(t, fmt.Sprintf("/api/v1/tasks/%s/assign", taskID), f.homeKey,
		map[string]interface{}{"assignee_id": f.foreignAgentID, "assignee_type": "agent"})

	assertAssigneeRefused(t, f.env, resp,
		"assign_task under an agent key (rbac() resolves an agent key from a static map "+
			"and never reads the target's workspace)")
}

// TestCrossWorkspaceAssignee_NativeAssignmentStillWorks is the positive control.
//
// Without it every assertion above is satisfied by a guard that refuses all
// assignment, which would look green here and break the whole fleet's task
// routing in production.
func TestCrossWorkspaceAssignee_NativeAssignmentStillWorks(t *testing.T) {
	f := newTwoWorkspaceFixture(t)
	taskID := f.createHomeTask(t, "native assign target")

	resp := f.env.Post(t, fmt.Sprintf("/api/v1/tasks/%s/assign", taskID),
		map[string]interface{}{"assignee_id": f.homeAgentID, "assignee_type": "agent"})
	body := string(f.env.ReadBody(t, resp))
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"an agent of this workspace must still be assignable: %s", body)

	var n int
	require.NoError(t, f.env.DB.QueryRowContext(context.Background(),
		"SELECT count(*) FROM project_members WHERE project_id = $1 AND agent_id = $2",
		f.homeProject, f.homeAgentID).Scan(&n))
	assert.Equal(t, 1, n, "and must still be auto-enrolled in the project")
}

// TestAgentTaskFeedIsWorkspaceScoped covers the read half.
//
// Scoping the write path alone would have left every row already written still
// readable: the feed query matched on assignee_id and assignee_type with no
// workspace predicate at all, so any task carrying the agent's id appeared in its
// own feed — and the notify path posts that task to the agent's callback_url.
//
// The cross-workspace row is planted with a direct INSERT on purpose. The API now
// refuses to create one, so going through the API would test nothing; what needs
// proving is that the rows the defect ALREADY produced are no longer served.
func TestAgentTaskFeedIsWorkspaceScoped(t *testing.T) {
	f := newTwoWorkspaceFixture(t)
	ctx := context.Background()

	nativeTaskID := f.createHomeTask(t, "native feed task")
	resp := f.env.Post(t, fmt.Sprintf("/api/v1/tasks/%s/assign", nativeTaskID),
		map[string]interface{}{"assignee_id": f.homeAgentID, "assignee_type": "agent"})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// The legacy row: a home-project task pointing at the FOREIGN agent.
	plantedID := f.createHomeTask(t, "planted cross-workspace task")
	_, err := f.env.DB.ExecContext(ctx,
		"UPDATE tasks SET assignee_id = $1, assignee_type = 'agent' WHERE id = $2",
		f.foreignAgentID, plantedID)
	require.NoError(t, err)

	// Positive control first: without it, an empty feed for the foreign agent
	// could just mean the request failed, and the test would pass while proving
	// nothing.
	resp = f.env.GetWithAgentKey(t, "/api/v1/agents/me/tasks", f.homeKey)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	homeBody := string(f.env.ReadBody(t, resp))
	assert.Contains(t, homeBody, nativeTaskID,
		"the home agent must still see its own task — the feed has to work, not just be empty")

	resp = f.env.GetWithAgentKey(t, "/api/v1/agents/me/tasks", f.foreign)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	foreignBody := string(f.env.ReadBody(t, resp))
	assert.NotContains(t, foreignBody, plantedID,
		"a task in another workspace must not appear in this agent's feed even when the row "+
			"names it as assignee: this feed is what drives the agent to pick the task up, and "+
			"the notify path posts the task's contents to its callback_url")
}
