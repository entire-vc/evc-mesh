//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAgentHierarchy validates that agents can be organized in a parent/child
// tree (parent agent → sub-agent → sub-sub-agent) and that each node
// authenticates independently with its own API key.
//
// Scenario:
//  1. Register a parent agent in the workspace.
//  2. Register a sub-agent that references the parent via parent_agent_id.
//  3. Register a sub-sub-agent that references the sub-agent.
//  4. GET /agents/:parent_id/sub-agents — verify the tree returns the correct
//     direct children.
//  5. Assert sub-agent.created_by == parent_agent_id.
//  6. Authenticate as the sub-agent using its own API key.
//  7. Sub-agent creates a task; assert the activity log records the sub-agent
//     as the actor.
func TestAgentHierarchy(t *testing.T) {
	t.Skip("TODO: implement agent hierarchy first")

	env := NewTestEnv(t)
	defer env.Cleanup(t)

	email := uniqueEmail("agent-hierarchy")
	env.Register(t, email, "TestPass123", "Hierarchy Tester")

	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]interface{}
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces)
	wsID := workspaces[0]["id"].(string)

	// --- Step 1: Register parent agent ---
	var parentAgentID, parentAPIKey string
	t.Run("RegisterParentAgent", func(t *testing.T) {
		resp := env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/agents", wsID), map[string]interface{}{
			"name":       "Parent Agent",
			"agent_type": "claude_code",
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		var result map[string]interface{}
		env.DecodeJSON(t, resp, &result)

		if agent, ok := result["agent"].(map[string]interface{}); ok {
			parentAgentID = agent["id"].(string)
		}
		if key, ok := result["api_key"].(string); ok {
			parentAPIKey = key
		}

		assert.NotEmpty(t, parentAgentID)
		assert.True(t, strings.HasPrefix(parentAPIKey, "agk_"))
	})

	// --- Step 2: Register sub-agent under parent ---
	var subAgentID, subAgentAPIKey string
	t.Run("RegisterSubAgent", func(t *testing.T) {
		if parentAgentID == "" {
			t.Skip("parent agent not created")
		}
		resp := env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/agents", wsID), map[string]interface{}{
			"name":            "Sub Agent",
			"agent_type":      "claude_code",
			"parent_agent_id": parentAgentID,
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		var result map[string]interface{}
		env.DecodeJSON(t, resp, &result)

		if agent, ok := result["agent"].(map[string]interface{}); ok {
			subAgentID = agent["id"].(string)
			// Assert created_by records the parent.
			assert.Equal(t, parentAgentID, agent["parent_agent_id"],
				"sub-agent.parent_agent_id must point to parent")
		}
		if key, ok := result["api_key"].(string); ok {
			subAgentAPIKey = key
		}

		assert.NotEmpty(t, subAgentID)
		assert.True(t, strings.HasPrefix(subAgentAPIKey, "agk_"))
	})

	// --- Step 3: Register sub-sub-agent under sub-agent ---
	var subSubAgentID string
	t.Run("RegisterSubSubAgent", func(t *testing.T) {
		if subAgentID == "" {
			t.Skip("sub-agent not created")
		}
		resp := env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/agents", wsID), map[string]interface{}{
			"name":            "Sub-Sub Agent",
			"agent_type":      "claude_code",
			"parent_agent_id": subAgentID,
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		var result map[string]interface{}
		env.DecodeJSON(t, resp, &result)

		if agent, ok := result["agent"].(map[string]interface{}); ok {
			subSubAgentID = agent["id"].(string)
			assert.Equal(t, subAgentID, agent["parent_agent_id"])
		}
		assert.NotEmpty(t, subSubAgentID)
	})

	// --- Step 4: GET /agents/:parent_id/sub-agents — verify direct children ---
	t.Run("ListSubAgents", func(t *testing.T) {
		if parentAgentID == "" {
			t.Skip("parent agent not created")
		}
		resp := env.Get(t, fmt.Sprintf("/api/v1/agents/%s/sub-agents", parentAgentID))
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var children []map[string]interface{}
		env.DecodeJSON(t, resp, &children)

		// Only the direct sub-agent should be listed (not the sub-sub-agent).
		require.Len(t, children, 1, "parent should have exactly one direct sub-agent")
		assert.Equal(t, subAgentID, children[0]["id"])
	})

	// --- Step 5: Sub-agent authenticates with its own API key ---
	t.Run("SubAgentAuthentication", func(t *testing.T) {
		if subAgentAPIKey == "" {
			t.Skip("sub-agent API key not available")
		}

		// Temporarily switch to agent key auth.
		savedToken := env.AuthToken
		env.AuthToken = ""

		req := map[string]interface{}{} // empty body for heartbeat
		_ = req

		// Heartbeat endpoint accepts X-Agent-Key.
		// Using doRequest directly here is not possible without modifying
		// TestEnv; instead verify via GET /agents/:id with agent key header.
		// This step is scaffolded for when the sub-agent auth is implemented.
		_ = subAgentAPIKey

		env.AuthToken = savedToken
	})

	// --- Step 6: Sub-agent creates a task; actor recorded correctly ---
	t.Run("SubAgentCreateTask", func(t *testing.T) {
		if subAgentID == "" || subAgentAPIKey == "" {
			t.Skip("sub-agent not available")
		}

		// Create a project first (as the human user).
		resp := env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/projects", wsID), map[string]interface{}{
			"name": "Hierarchy Test Project",
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		var project map[string]interface{}
		env.DecodeJSON(t, resp, &project)
		projectID := project["id"].(string)

		// Task created by sub-agent (via X-Agent-Key header).
		// The activity log entry for this task should record the sub-agent as actor.
		// Scaffolded — full assertion requires activity log API.
		_ = projectID
	})
}
