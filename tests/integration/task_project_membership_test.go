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

// projectMembershipFixture is one workspace with two projects, a task in each,
// and a plain workspace member who belongs to only one of them.
type projectMembershipFixture struct {
	owner, member *TestEnv
	wsID          string
	// closedProject has no project_members row for the member; openProject has one.
	closedProject, openProject string
	closedTask, openTask       string
	closedShort                string
	closedComment              string
	memberUserID               string
}

func newProjectMembershipFixture(t *testing.T) *projectMembershipFixture {
	t.Helper()
	f := &projectMembershipFixture{owner: NewTestEnv(t)}
	t.Cleanup(func() { f.owner.Cleanup(t) })
	f.owner.Register(t, uniqueEmail("proj-member-owner"), "TestPass123", "Project Owner")

	resp := f.owner.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]any
	f.owner.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces)
	f.wsID = workspaces[0]["id"].(string)

	newProject := func(name string) (projectID, taskID string) {
		resp := f.owner.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/projects", f.wsID), map[string]any{"name": name})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		var project map[string]any
		f.owner.DecodeJSON(t, resp, &project)
		projectID = project["id"].(string)
		f.owner.OnCleanup(func() {
			ctx := context.Background()
			_, _ = f.owner.DB.ExecContext(ctx, "DELETE FROM project_members WHERE project_id = $1", projectID)
			_, _ = f.owner.DB.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", projectID)
			_, _ = f.owner.DB.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", projectID)
			_, _ = f.owner.DB.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", projectID)
		})
		resp = f.owner.Post(t, fmt.Sprintf("/api/v1/projects/%s/tasks", projectID), map[string]any{
			"title": name + " task",
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		var task map[string]any
		f.owner.DecodeJSON(t, resp, &task)
		return projectID, task["id"].(string)
	}
	f.closedProject, f.closedTask = newProject("Closed Project")
	f.openProject, f.openTask = newProject("Open Project")
	f.closedShort = f.closedTask[:8]

	resp = f.owner.Post(t, fmt.Sprintf("/api/v1/tasks/%s/comments", f.closedTask), map[string]any{"body": "closed project comment"})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var comment map[string]any
	f.owner.DecodeJSON(t, resp, &comment)
	f.closedComment = comment["id"].(string)

	memberEmail := uniqueEmail("proj-member-plain")
	resp = f.owner.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/members", f.wsID), map[string]any{
		"email": memberEmail, "role": "member", "password": "TestPass123",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(f.owner.ReadBody(t, resp)))
	f.owner.OnCleanup(func() {
		ctx := context.Background()
		_, _ = f.owner.DB.ExecContext(ctx, "DELETE FROM project_members WHERE user_id IN (SELECT id FROM users WHERE email = $1)", memberEmail)
		_, _ = f.owner.DB.ExecContext(ctx, "DELETE FROM workspace_members WHERE user_id IN (SELECT id FROM users WHERE email = $1)", memberEmail)
		_, _ = f.owner.DB.ExecContext(ctx, "DELETE FROM refresh_tokens WHERE user_id IN (SELECT id FROM users WHERE email = $1)", memberEmail)
		_, _ = f.owner.DB.ExecContext(ctx, "DELETE FROM users WHERE email = $1", memberEmail)
	})
	require.NoError(t, f.owner.DB.GetContext(context.Background(), &f.memberUserID,
		"SELECT id::text FROM users WHERE email = $1", memberEmail))

	// The member joins the open project only. Whatever the server does on its own
	// when a project is created or a member is added, the closed project must
	// not list them, or the test below proves nothing.
	resp = f.owner.Post(t, fmt.Sprintf("/api/v1/projects/%s/members", f.openProject), map[string]any{
		"user_id": f.memberUserID, "role": "member",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(f.owner.ReadBody(t, resp)))
	_, err := f.owner.DB.ExecContext(context.Background(),
		"DELETE FROM project_members WHERE project_id = $1 AND user_id = $2", f.closedProject, f.memberUserID)
	require.NoError(t, err)

	f.member = NewTestEnv(t)
	t.Cleanup(func() { f.member.Cleanup(t) })
	f.member.Login(t, memberEmail, "TestPass123")
	require.NotEmpty(t, f.member.AuthToken)
	return f
}

// taskRouteProbes are the task-by-id routes the member is probed on, reads and
// writes. Writes carry a body the handler would accept, so a 4xx can only come
// from the guard and not from validation.
func (f *projectMembershipFixture) taskRouteProbes(taskID, short, commentID string) []struct{ method, path string } {
	return []struct{ method, path string }{
		{http.MethodGet, "/api/v1/tasks/" + taskID},
		{http.MethodGet, "/api/v1/tasks/by-short-id/" + short},
		{http.MethodGet, "/api/v1/tasks/" + short},
		{http.MethodGet, "/api/v1/tasks/" + taskID + "/comments"},
		{http.MethodGet, "/api/v1/tasks/" + taskID + "/artifacts"},
		{http.MethodGet, "/api/v1/tasks/" + taskID + "/dependencies"},
		{http.MethodGet, "/api/v1/tasks/" + taskID + "/subtasks"},
		{http.MethodGet, "/api/v1/tasks/" + taskID + "/statuses"},
		{http.MethodGet, "/api/v1/tasks/" + taskID + "/context"},
		{http.MethodGet, "/api/v1/tasks/" + taskID + "/activity"},
		{http.MethodGet, "/api/v1/tasks/" + taskID + "/vcs-links"},
		{http.MethodPatch, "/api/v1/tasks/" + taskID},
		{http.MethodPost, "/api/v1/tasks/" + taskID + "/comments"},
		{http.MethodPatch, "/api/v1/comments/" + commentID},
	}
}

func (f *projectMembershipFixture) do(t *testing.T, env *TestEnv, method, path string) int {
	t.Helper()
	var resp *http.Response
	switch method {
	case http.MethodGet:
		resp = env.Get(t, path)
	case http.MethodPatch:
		resp = env.Patch(t, path, map[string]any{"title": "renamed by probe", "body": "edited by probe"})
	case http.MethodPost:
		resp = env.Post(t, path, map[string]any{"body": "written by probe"})
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestTaskRoutes_RequireProjectMembership: a workspace member who is not in a
// project gets nothing from that project's tasks by id — not the task, not its
// comments, not a write. Before the guard every one of these answered 2xx, while
// GET /projects/:proj_id/tasks for the same project already refused them.
func TestTaskRoutes_RequireProjectMembership(t *testing.T) {
	f := newProjectMembershipFixture(t)

	// Positive control first: the list route refuses the member, so the
	// fixture really does leave them outside the closed project.
	resp := f.member.Get(t, fmt.Sprintf("/api/v1/projects/%s/tasks", f.closedProject))
	_ = resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode, "fixture: the member must be outside the closed project")

	for _, p := range f.taskRouteProbes(f.closedTask, f.closedShort, f.closedComment) {
		t.Run("stranger "+p.method+" "+p.path, func(t *testing.T) {
			got := f.do(t, f.member, p.method, p.path)
			assert.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, got,
				"a workspace member outside the project reached its task")
		})
	}

	// The project they are in still works.
	for _, path := range []string{
		"/api/v1/tasks/" + f.openTask,
		"/api/v1/tasks/by-short-id/" + f.openTask[:8],
		"/api/v1/tasks/" + f.openTask + "/comments",
	} {
		resp := f.member.Get(t, path)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, "project member lost access: %s", path)
	}

	// A task named in a body is checked the same way: an edge from the member's
	// own task to a task in the closed project answers like a missing task.
	resp = f.member.Post(t, fmt.Sprintf("/api/v1/tasks/%s/dependencies", f.openTask), map[string]any{
		"depends_on_task_id": f.closedTask,
	})
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "a body-named task outside the member's projects was reachable")

	// And the workspace owner reaches every project without a membership row.
	_, err := f.owner.DB.ExecContext(context.Background(),
		"DELETE FROM project_members WHERE project_id = $1 AND user_id IS NOT NULL", f.closedProject)
	require.NoError(t, err)
	resp = f.owner.Get(t, "/api/v1/tasks/"+f.closedTask)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "workspace owner lost access to a task")
}

// TestTaskRoutes_AgentProjectMembership: an agent reaches the tasks of the
// projects it is a member of and no others — the rule /projects/:proj_id/*
// already applied to it, now applied to the task routes as well.
func TestTaskRoutes_AgentProjectMembership(t *testing.T) {
	f := newProjectMembershipFixture(t)
	agentID, key := f.owner.CreateAgent(t, f.wsID, "proj-member-agent")

	resp := f.owner.Post(t, fmt.Sprintf("/api/v1/projects/%s/members/agents", f.openProject), map[string]any{
		"agent_id": agentID, "role": "member",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(f.owner.ReadBody(t, resp)))
	_, err := f.owner.DB.ExecContext(context.Background(),
		"DELETE FROM project_members WHERE project_id = $1 AND agent_id = $2", f.closedProject, agentID)
	require.NoError(t, err)

	resp = f.owner.GetWithAgentKey(t, "/api/v1/tasks/"+f.openTask, key)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "an agent lost access to a task of its own project")

	resp = f.owner.PostWithAgentKey(t, fmt.Sprintf("/api/v1/tasks/%s/comments", f.openTask), key, map[string]any{"body": "agent note"})
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusCreated, resp.StatusCode, "an agent lost write access to a task of its own project")

	for _, path := range []string{
		"/api/v1/tasks/" + f.closedTask,
		"/api/v1/tasks/by-short-id/" + f.closedShort,
		"/api/v1/tasks/" + f.closedTask + "/comments",
	} {
		resp := f.owner.GetWithAgentKey(t, path, key)
		_ = resp.Body.Close()
		assert.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, resp.StatusCode,
			"an agent reached a task of a project it is not a member of: %s", path)
	}
}
