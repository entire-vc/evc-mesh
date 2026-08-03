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
// POST /tasks/:task_id/move is workspace-gated. Resolving "which status is 'done'
// in this task's project" is a precondition of that move. While the only way to
// resolve it was GET /projects/:proj_id/statuses — project-gated — a caller who was
// allowed to move a task could not discover where to move it, and the refusal it got
// back named the project, not the move. The move was unreachable and the 403 pointed
// at the wrong resource.
//
// The test therefore asserts three things together; any one alone is not evidence:
//  1. positive — a workspace member who is NOT a project member reads the new endpoint;
//  2. negative (a check exists) — an agent outside the workspace is refused it;
//  3. negative (nothing was weakened) — that same non-project-member key is still
//     refused the project-scoped status route.
//
// Without (2) a passing (1) cannot be distinguished from "the endpoint has no gate
// at all", and without (3) it cannot be distinguished from "we relaxed the project
// route and broke project scoping for every other consumer, including the UI".
func TestStatusResolveGateParity(t *testing.T) {
	env := NewTestEnv(t)
	defer env.Cleanup(t)

	// --- Workspace A: the task, the project, and an agent that is NOT a project member.
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

	resp = env.Post(t, fmt.Sprintf("/api/v1/projects/%s/tasks", projectID), map[string]interface{}{
		"title": "Task whose status the outsider must be able to resolve",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var task map[string]interface{}
	env.DecodeJSON(t, resp, &task)
	taskID := task["id"].(string)

	// The agent is created in the workspace and deliberately left out of the project.
	// It is also NOT assigned the task: assignment auto-enrols the assignee into
	// project_members (#497), which would quietly destroy the condition under test.
	outsiderID, outsiderKey := env.CreateAgent(t, wsID, "Workspace Member Not Project Member")

	// Guard the premise rather than assume it. If a future change enrols agents into
	// projects on creation, this test would otherwise keep passing while testing nothing.
	var isProjectMember bool
	require.NoError(t, env.DB.QueryRowContext(context.Background(),
		"SELECT EXISTS(SELECT 1 FROM project_members WHERE project_id = $1 AND agent_id = $2)",
		projectID, outsiderID,
	).Scan(&isProjectMember))
	require.False(t, isProjectMember,
		"premise broken: the agent is a project member, so this test proves nothing")

	// --- Workspace B: an agent with no relationship to workspace A at all.
	outsiderEnv := NewTestEnv(t)
	defer outsiderEnv.Cleanup(t)
	outsiderEnv.Register(t, uniqueEmail("gate-parity-foreign"), "TestPass123", "Foreign Owner")

	resp = outsiderEnv.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var foreignWorkspaces []map[string]interface{}
	outsiderEnv.DecodeJSON(t, resp, &foreignWorkspaces)
	require.NotEmpty(t, foreignWorkspaces)
	foreignWsID := foreignWorkspaces[0]["id"].(string)
	_, foreignAgentKey := outsiderEnv.CreateAgent(t, foreignWsID, "Foreign Workspace Agent")

	var doneStatusID string

	// --- 1. POSITIVE: workspace member, not project member, reads the task's statuses.
	t.Run("WorkspaceMemberCanResolveStatusesForATaskItMayMove", func(t *testing.T) {
		resp := env.GetWithAgentKey(t, fmt.Sprintf("/api/v1/tasks/%s/statuses", taskID), outsiderKey)
		require.Equal(t, http.StatusOK, resp.StatusCode,
			"a caller allowed to move this task must be allowed to list where it can move")

		var statuses []map[string]interface{}
		env.DecodeJSON(t, resp, &statuses)
		require.NotEmpty(t, statuses, "project must have default statuses")

		// The response must carry the fields the slug resolver reads, or the endpoint
		// is reachable but useless — a 200 that resolves nothing.
		for _, s := range statuses {
			assert.NotEmpty(t, s["id"], "status must expose id")
			assert.NotEmpty(t, s["slug"], "status must expose slug")
			assert.Equal(t, projectID, s["project_id"], "statuses must be the task's own project's")
			if s["category"] == "done" {
				doneStatusID, _ = s["id"].(string)
			}
		}
		require.NotEmpty(t, doneStatusID, "project must expose a done-category status")
	})

	// --- 2. NEGATIVE CONTROL: a check exists at all.
	t.Run("AgentOutsideTheWorkspaceIsRefused", func(t *testing.T) {
		resp := env.GetWithAgentKey(t, fmt.Sprintf("/api/v1/tasks/%s/statuses", taskID), foreignAgentKey)
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"without this, a passing positive case cannot be told apart from an ungated endpoint")
	})

	// --- 3. NEGATIVE CONTROL: the project-scoped route was not weakened.
	t.Run("ProjectScopedStatusRouteStillRequiresProjectMembership", func(t *testing.T) {
		resp := env.GetWithAgentKey(t, fmt.Sprintf("/api/v1/projects/%s/statuses", projectID), outsiderKey)
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"the fix must be additive: project config stays behind project membership "+
				"for every existing consumer, including the UI")
	})

	// --- 4. The loop closes: the read the write needed now permits the write.
	t.Run("SameCallerCanCompleteTheMoveTheResolveServed", func(t *testing.T) {
		if doneStatusID == "" {
			t.Skip("done status not resolved")
		}

		// The done-evidence governance rule requires a comment before a done move.
		resp := env.PostWithAgentKey(t, fmt.Sprintf("/api/v1/tasks/%s/comments", taskID), outsiderKey,
			map[string]string{"body": "Gate parity integration test — closing evidence."})
		require.Contains(t, []int{http.StatusOK, http.StatusCreated}, resp.StatusCode)
		_ = resp.Body.Close()

		resp = env.PostWithAgentKey(t, fmt.Sprintf("/api/v1/tasks/%s/move", taskID), outsiderKey,
			map[string]string{"status_id": doneStatusID})
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"resolve and move must be reachable by the same caller — that is the whole invariant")
	})
}
