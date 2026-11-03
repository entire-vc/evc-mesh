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

// TestStatusResolveGateParity pins the invariant this endpoint exists to enforce:
//
//	the gate on the read that resolves a status slug must never be stricter than
//	the gate on the transition that read serves.
//
// Both routes sit behind the same project-membership rule (RequireTaskProjectMember
// / projectAccessDenial): GET /tasks/:task_id/statuses resolves "which status is
// 'done' in this task's project", POST /tasks/:task_id/move uses the answer. An
// agent that is a member of the task's project reaches both; a workspace agent
// that is NOT a project member is refused both; an agent of another workspace is
// refused both; a human workspace owner reaches both without a project_members
// row. An agent never gets the owner/admin bypass.
//
// The membership of the allowed agent is created explicitly through the API and
// asserted in the database. Assignment auto-enrols the assignee (#497), so the
// task is deliberately assigned to nobody.
func TestStatusResolveGateParity(t *testing.T) {
	env := NewTestEnv(t)
	defer env.Cleanup(t)

	env.Register(t, uniqueEmail("gate-parity"), "TestPass123", "Gate Parity Owner")

	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]interface{}
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces, "registration must create a workspace")
	wsID := workspaces[0]["id"].(string)

	resp = env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/projects", wsID), map[string]interface{}{
		"name": "Gate Parity Project",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var project map[string]interface{}
	env.DecodeJSON(t, resp, &project)
	projectID := project["id"].(string)

	newTask := func(t *testing.T, title string) string {
		t.Helper()
		resp := env.Post(t, fmt.Sprintf("/api/v1/projects/%s/tasks", projectID), map[string]interface{}{"title": title})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		var task map[string]interface{}
		env.DecodeJSON(t, resp, &task)
		return task["id"].(string)
	}
	isMember := func(t *testing.T, column, id string) bool {
		t.Helper()
		var ok bool
		require.NoError(t, env.DB.QueryRowContext(context.Background(),
			"SELECT EXISTS(SELECT 1 FROM project_members WHERE project_id = $1 AND "+column+" = $2)",
			projectID, id).Scan(&ok))
		return ok
	}
	taskStatus := func(t *testing.T, taskID string) string {
		t.Helper()
		var statusID string
		require.NoError(t, env.DB.QueryRowContext(context.Background(),
			"SELECT status_id FROM tasks WHERE id = $1", taskID).Scan(&statusID))
		return statusID
	}

	// Explicit project member: enrolled through the API, then verified in the DB.
	memberID, memberKey := env.CreateAgent(t, wsID, "Project Member Agent")
	enrol := env.Post(t, fmt.Sprintf("/api/v1/projects/%s/members/agents", projectID),
		map[string]interface{}{"agent_id": memberID, "role": "member"})
	require.Equal(t, http.StatusCreated, enrol.StatusCode, string(env.ReadBody(t, enrol)))
	require.True(t, isMember(t, "agent_id", memberID), "premise: member agent must have a project_members row")

	// Workspace agent left out of the project (never assigned, so never auto-enrolled).
	outsiderID, outsiderKey := env.CreateAgent(t, wsID, "Workspace Agent Not Project Member")
	require.False(t, isMember(t, "agent_id", outsiderID),
		"premise broken: the agent is a project member, so the deny cases prove nothing")

	// Agent of an unrelated workspace.
	foreignEnv := NewTestEnv(t)
	defer foreignEnv.Cleanup(t)
	foreignEnv.Register(t, uniqueEmail("gate-parity-foreign"), "TestPass123", "Foreign Owner")
	resp = foreignEnv.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var foreignWorkspaces []map[string]interface{}
	foreignEnv.DecodeJSON(t, resp, &foreignWorkspaces)
	require.NotEmpty(t, foreignWorkspaces)
	_, foreignKey := foreignEnv.CreateAgent(t, foreignWorkspaces[0]["id"].(string), "Foreign Workspace Agent")

	// Resolve the project's done status once (as the human owner) for the move calls.
	var doneStatusID string
	statusesPath := func(taskID string) string { return fmt.Sprintf("/api/v1/tasks/%s/statuses", taskID) }
	movePath := func(taskID string) string { return fmt.Sprintf("/api/v1/tasks/%s/move", taskID) }

	t.Run("ProjectMemberAgentResolvesAndMoves", func(t *testing.T) {
		taskID := newTask(t, "Task a project member agent resolves and moves")

		resp := env.GetWithAgentKey(t, statusesPath(taskID), memberKey)
		require.Equal(t, http.StatusOK, resp.StatusCode, "a project member must be able to list where the task can move")
		var statuses []map[string]interface{}
		env.DecodeJSON(t, resp, &statuses)
		require.NotEmpty(t, statuses, "project must have default statuses")
		var resolved string
		for _, s := range statuses {
			assert.NotEmpty(t, s["id"], "status must expose id")
			assert.NotEmpty(t, s["slug"], "status must expose slug")
			assert.Equal(t, projectID, s["project_id"], "statuses must be the task's own project's")
			if s["category"] == "done" {
				resolved, _ = s["id"].(string)
			}
		}
		require.NotEmpty(t, resolved, "project must expose a done-category status")
		doneStatusID = resolved

		before := taskStatus(t, taskID)
		require.NotEqual(t, doneStatusID, before)

		// The done-evidence governance rule requires a comment before a done move.
		resp = env.PostWithAgentKey(t, fmt.Sprintf("/api/v1/tasks/%s/comments", taskID), memberKey,
			map[string]string{"body": "Gate parity integration test — closing evidence."})
		require.Contains(t, []int{http.StatusOK, http.StatusCreated}, resp.StatusCode)
		_ = resp.Body.Close()

		resp = env.PostWithAgentKey(t, movePath(taskID), memberKey, map[string]string{"status_id": doneStatusID})
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, http.StatusOK, resp.StatusCode, "the same caller that resolved the status must be able to move")
		assert.Equal(t, doneStatusID, taskStatus(t, taskID), "the move must actually change the task's status")
	})
	require.NotEmpty(t, doneStatusID, "member subtest must have resolved the done status")

	t.Run("WorkspaceAgentNotInProjectIsRefusedBothRoutes", func(t *testing.T) {
		taskID := newTask(t, "Task a non-member workspace agent must not reach")
		before := taskStatus(t, taskID)

		resp := env.GetWithAgentKey(t, statusesPath(taskID), outsiderKey)
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusForbidden, resp.StatusCode, "task statuses must stay behind project membership")

		resp2 := env.PostWithAgentKey(t, movePath(taskID), outsiderKey, map[string]string{"status_id": doneStatusID})
		defer func() { _ = resp2.Body.Close() }()
		assert.Equal(t, http.StatusForbidden, resp2.StatusCode, "task move must stay behind project membership")
		assert.Equal(t, before, taskStatus(t, taskID), "a refused move must not change the status")
	})

	t.Run("ProjectScopedStatusRouteRequiresProjectMembership", func(t *testing.T) {
		resp := env.GetWithAgentKey(t, fmt.Sprintf("/api/v1/projects/%s/statuses", projectID), outsiderKey)
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)

		resp2 := env.GetWithAgentKey(t, fmt.Sprintf("/api/v1/projects/%s/statuses", projectID), memberKey)
		defer func() { _ = resp2.Body.Close() }()
		assert.Equal(t, http.StatusOK, resp2.StatusCode, "positive control: the member reads the project route")
	})

	t.Run("AgentOutsideTheWorkspaceIsRefusedBothRoutes", func(t *testing.T) {
		taskID := newTask(t, "Task a foreign-workspace agent must not reach")
		before := taskStatus(t, taskID)

		resp := env.GetWithAgentKey(t, statusesPath(taskID), foreignKey)
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)

		resp2 := env.PostWithAgentKey(t, movePath(taskID), foreignKey, map[string]string{"status_id": doneStatusID})
		defer func() { _ = resp2.Body.Close() }()
		assert.Equal(t, http.StatusForbidden, resp2.StatusCode)
		assert.Equal(t, before, taskStatus(t, taskID))
	})

	t.Run("HumanWorkspaceOwnerBypassesWithoutProjectMembership", func(t *testing.T) {
		// Remove any membership row the project creator may have received, so the
		// owner bypass is what grants access, not a project_members row.
		_, err := env.DB.ExecContext(context.Background(),
			"DELETE FROM project_members WHERE project_id = $1 AND user_id = $2", projectID, env.UserID)
		require.NoError(t, err)
		require.False(t, isMember(t, "user_id", env.UserID), "premise: owner has no project_members row")

		taskID := newTask(t, "Task the workspace owner reaches by role")
		resp := env.Get(t, statusesPath(taskID))
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusOK, resp.StatusCode, "human owner/admin reaches every project of the workspace")

		resp2 := env.MoveTaskToDone(t, taskID, doneStatusID)
		defer func() { _ = resp2.Body.Close() }()
		assert.Equal(t, http.StatusOK, resp2.StatusCode)
		assert.Equal(t, doneStatusID, taskStatus(t, taskID))
	})
}
