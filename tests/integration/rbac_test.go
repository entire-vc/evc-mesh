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

// TestRBACLite_OwnerFullAccess verifies that the workspace owner has
// unrestricted access to all project and member management operations.
//
// Scenario:
//  1. Register a user (becomes workspace owner).
//  2. Create a project — expect 201.
//  3. Invite a second user as a workspace member — expect 201.
//  4. Remove that member — expect 204.
//  5. Register an agent — expect 201.
//  6. Delete the project — expect 204.
func TestRBACLite_OwnerFullAccess(t *testing.T) {
	t.Skip("TODO: implement RBAC lite first")

	env := NewTestEnv(t)
	defer env.Cleanup(t)

	email := uniqueEmail("rbac-owner")
	env.Register(t, email, "TestPass123", "Owner User")

	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]interface{}
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces)
	wsID := workspaces[0]["id"].(string)

	// --- Step 1: Owner creates a project ---
	var projectID string
	t.Run("OwnerCanCreateProject", func(t *testing.T) {
		resp := env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/projects", wsID), map[string]interface{}{
			"name": "RBAC Owner Project",
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		var project map[string]interface{}
		env.DecodeJSON(t, resp, &project)
		projectID = project["id"].(string)
		assert.NotEmpty(t, projectID)

		env.OnCleanup(func() {
			ctx := context.Background()
			env.DB.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", projectID)
			env.DB.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", projectID)
			env.DB.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", projectID)
		})
	})

	// --- Step 2: Owner invites a member ---
	var memberUserID string
	t.Run("OwnerCanInviteMember", func(t *testing.T) {
		// Register the second user (no workspace yet).
		memberEmail := uniqueEmail("rbac-member")
		memberEnv := NewTestEnv(t)
		defer memberEnv.Cleanup(t)
		result := memberEnv.Register(t, memberEmail, "TestPass123", "Member User")
		if user, ok := result["user"].(map[string]interface{}); ok {
			memberUserID = user["id"].(string)
		}

		// Owner invites the member to the owner's workspace.
		resp := env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/members", wsID), map[string]interface{}{
			"user_id": memberUserID,
			"role":    "member",
		})
		assert.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	})

	// --- Step 3: Owner removes the member ---
	t.Run("OwnerCanRemoveMember", func(t *testing.T) {
		if memberUserID == "" {
			t.Skip("member not created")
		}
		resp := env.Delete(t, fmt.Sprintf("/api/v1/workspaces/%s/members/%s", wsID, memberUserID))
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
		resp.Body.Close()
	})

	// --- Step 4: Owner registers an agent ---
	t.Run("OwnerCanManageAgents", func(t *testing.T) {
		resp := env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/agents", wsID), map[string]interface{}{
			"name":       "RBAC Owner Agent",
			"agent_type": "claude_code",
		})
		assert.Equal(t, http.StatusCreated, resp.StatusCode)
		if resp.StatusCode == http.StatusCreated {
			var result map[string]interface{}
			env.DecodeJSON(t, resp, &result)
			if agent, ok := result["agent"].(map[string]interface{}); ok {
				agentID := agent["id"].(string)
				env.OnCleanup(func() {
					env.DB.ExecContext(context.Background(), "DELETE FROM agents WHERE id = $1", agentID)
				})
			}
		} else {
			resp.Body.Close()
		}
	})

	// --- Step 5: Owner deletes the project ---
	t.Run("OwnerCanDeleteProject", func(t *testing.T) {
		if projectID == "" {
			t.Skip("project not created")
		}
		resp := env.Delete(t, fmt.Sprintf("/api/v1/projects/%s", projectID))
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
		resp.Body.Close()
	})
}

// TestRBACLite_MemberLimitedAccess verifies that a workspace member (non-owner)
// can create tasks and comments inside assigned projects but cannot perform
// administrative operations such as deleting projects or managing members.
//
// Scenario:
//  1. Register owner; register member; owner invites member to workspace.
//  2. Owner creates a project.
//  3. Member creates a task in that project — expect 201.
//  4. Member adds a comment to the task — expect 201.
//  5. Member attempts to delete the project — expect 403.
//  6. Member attempts to invite another user — expect 403.
func TestRBACLite_MemberLimitedAccess(t *testing.T) {
	t.Skip("TODO: implement RBAC lite first")

	ownerEnv := NewTestEnv(t)
	defer ownerEnv.Cleanup(t)
	memberEnv := NewTestEnv(t)
	defer memberEnv.Cleanup(t)

	ownerEmail := uniqueEmail("rbac-owner2")
	ownerEnv.Register(t, ownerEmail, "TestPass123", "Owner 2")

	memberEmail := uniqueEmail("rbac-member2")
	memberResult := memberEnv.Register(t, memberEmail, "TestPass123", "Member 2")

	var memberUserID string
	if user, ok := memberResult["user"].(map[string]interface{}); ok {
		memberUserID = user["id"].(string)
	}

	// Get owner workspace.
	resp := ownerEnv.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]interface{}
	ownerEnv.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces)
	wsID := workspaces[0]["id"].(string)

	// Owner creates a project.
	resp = ownerEnv.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/projects", wsID), map[string]interface{}{
		"name": "RBAC Member Project",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var project map[string]interface{}
	ownerEnv.DecodeJSON(t, resp, &project)
	projectID := project["id"].(string)

	ownerEnv.OnCleanup(func() {
		ctx := context.Background()
		ownerEnv.DB.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", projectID)
		ownerEnv.DB.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", projectID)
		ownerEnv.DB.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", projectID)
	})

	// Owner invites the member.
	resp = ownerEnv.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/members", wsID), map[string]interface{}{
		"user_id": memberUserID,
		"role":    "member",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// memberEnv must target the same workspace.
	_ = wsID

	// --- Step 1: Member creates a task ---
	var taskID string
	t.Run("MemberCanCreateTask", func(t *testing.T) {
		resp := memberEnv.Post(t, fmt.Sprintf("/api/v1/projects/%s/tasks", projectID), map[string]interface{}{
			"title":    "Member Task",
			"priority": "medium",
		})
		assert.Equal(t, http.StatusCreated, resp.StatusCode)
		if resp.StatusCode == http.StatusCreated {
			var task map[string]interface{}
			memberEnv.DecodeJSON(t, resp, &task)
			taskID = task["id"].(string)
		} else {
			resp.Body.Close()
		}
	})

	// --- Step 2: Member adds a comment ---
	t.Run("MemberCanAddComment", func(t *testing.T) {
		if taskID == "" {
			t.Skip("task not created")
		}
		resp := memberEnv.Post(t, fmt.Sprintf("/api/v1/tasks/%s/comments", taskID), map[string]interface{}{
			"body": "Member comment",
		})
		assert.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	})

	// --- Step 3: Member cannot delete the project ---
	t.Run("MemberCannotDeleteProject", func(t *testing.T) {
		resp := memberEnv.Delete(t, fmt.Sprintf("/api/v1/projects/%s", projectID))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"member must not be able to delete a project")
		resp.Body.Close()
	})

	// --- Step 4: Member cannot invite other users ---
	t.Run("MemberCannotInviteUsers", func(t *testing.T) {
		thirdEmail := uniqueEmail("rbac-third")
		resp := memberEnv.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/members", wsID), map[string]interface{}{
			"user_id": thirdEmail, // intentional: member should be blocked before lookup
			"role":    "member",
		})
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"member must not be able to invite other users")
		resp.Body.Close()
	})
}

// TestRBACLite_AgentScopedAccess verifies that an agent authenticated with
// X-Agent-Key can manage tasks, comments, and artifacts within the workspace
// but is blocked from administrative operations.
//
// Scenario:
//  1. Register a user (owner); register an agent.
//  2. Create a project; create a task via the owner.
//  3. Authenticate as the agent (X-Agent-Key header).
//  4. Agent updates the task — expect 200.
//  5. Agent adds a comment — expect 201.
//  6. Agent attempts to delete the project — expect 403.
//  7. Agent attempts to invite a workspace member — expect 403.
func TestRBACLite_AgentScopedAccess(t *testing.T) {
	t.Skip("TODO: implement RBAC lite first")

	env := NewTestEnv(t)
	defer env.Cleanup(t)

	email := uniqueEmail("rbac-agent")
	env.Register(t, email, "TestPass123", "Agent RBAC Owner")

	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]interface{}
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces)
	wsID := workspaces[0]["id"].(string)

	// Register agent.
	var agentAPIKey string
	t.Run("RegisterAgent", func(t *testing.T) {
		resp := env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/agents", wsID), map[string]interface{}{
			"name":       "RBAC Test Agent",
			"agent_type": "claude_code",
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		var result map[string]interface{}
		env.DecodeJSON(t, resp, &result)
		if key, ok := result["api_key"].(string); ok {
			agentAPIKey = key
		}
		assert.NotEmpty(t, agentAPIKey)
	})

	// Owner creates project and task.
	resp = env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/projects", wsID), map[string]interface{}{
		"name": "RBAC Agent Project",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var project map[string]interface{}
	env.DecodeJSON(t, resp, &project)
	projectID := project["id"].(string)

	env.OnCleanup(func() {
		ctx := context.Background()
		env.DB.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", projectID)
		env.DB.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", projectID)
		env.DB.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", projectID)
	})

	resp = env.Post(t, fmt.Sprintf("/api/v1/projects/%s/tasks", projectID), map[string]interface{}{
		"title":    "Agent RBAC Task",
		"priority": "medium",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var task map[string]interface{}
	env.DecodeJSON(t, resp, &task)
	taskID := task["id"].(string)

	// --- Agent task operations (scaffolded; requires agent-key HTTP helper) ---
	// When agent-key auth helper is added to TestEnv, replace the skips below.
	_ = agentAPIKey
	_ = taskID

	// --- Step 1: Agent updates the task ---
	t.Run("AgentCanUpdateTask", func(t *testing.T) {
		// Requires doRequestWithAgentKey helper — scaffolded.
	})

	// --- Step 2: Agent adds a comment ---
	t.Run("AgentCanAddComment", func(t *testing.T) {
		// Requires doRequestWithAgentKey helper — scaffolded.
	})

	// --- Step 3: Agent cannot delete the project ---
	t.Run("AgentCannotDeleteProject", func(t *testing.T) {
		// Expect 403 when agent attempts DELETE /projects/:id.
	})

	// --- Step 4: Agent cannot invite workspace members ---
	t.Run("AgentCannotInviteMembers", func(t *testing.T) {
		// Expect 403 when agent attempts POST /workspaces/:id/members.
	})
}
