//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentFlow(t *testing.T) {
	env := NewTestEnv(t)
	defer env.Cleanup(t)

	email := uniqueEmail("agent-flow")
	env.Register(t, email, "TestPass123", "Agent Tester")

	// Get workspace.
	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]interface{}
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces)
	wsID := workspaces[0]["id"].(string)

	// --- Step 1: Register agent ---
	var agentID, apiKey string
	t.Run("RegisterAgent", func(t *testing.T) {
		resp := env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/agents", wsID), map[string]interface{}{
			"name":       "Test Agent",
			"agent_type": "claude_code",
			"capabilities": map[string]interface{}{
				"languages": []string{"go", "python"},
			},
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		var result map[string]interface{}
		env.DecodeJSON(t, resp, &result)

		// The response should contain the agent and the plain API key.
		if agent, ok := result["agent"].(map[string]interface{}); ok {
			agentID = agent["id"].(string)
		}
		if key, ok := result["api_key"].(string); ok {
			apiKey = key
		}

		assert.NotEmpty(t, agentID, "agent ID must be present")
		assert.NotEmpty(t, apiKey, "API key must be returned on registration")
		assert.True(t, strings.HasPrefix(apiKey, "agk_"), "API key must start with agk_")

		env.OnCleanup(func() {
			env.DB.ExecContext(context.Background(),
				"DELETE FROM agents WHERE id = $1", agentID)
		})
	})

	// --- Step 2: Get agent by ID ---
	t.Run("GetAgent", func(t *testing.T) {
		if agentID == "" {
			t.Skip("agent not created")
		}
		resp := env.Get(t, fmt.Sprintf("/api/v1/agents/%s", agentID))
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var agent map[string]interface{}
		env.DecodeJSON(t, resp, &agent)
		assert.Equal(t, "Test Agent", agent["name"])
		assert.Equal(t, "claude_code", agent["agent_type"])

		// API key hash should NOT be exposed.
		_, hasKeyHash := agent["api_key_hash"]
		assert.False(t, hasKeyHash, "api_key_hash must not be exposed in API response")
	})

	// --- Step 3: List agents ---
	t.Run("ListAgents", func(t *testing.T) {
		resp := env.Get(t, fmt.Sprintf("/api/v1/workspaces/%s/agents", wsID))
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var page map[string]interface{}
		env.DecodeJSON(t, resp, &page)
		items, ok := page["items"].([]interface{})
		require.True(t, ok)
		assert.GreaterOrEqual(t, len(items), 1)
	})

	// --- Step 4: Update agent ---
	t.Run("UpdateAgent", func(t *testing.T) {
		if agentID == "" {
			t.Skip("agent not created")
		}
		resp := env.Patch(t, fmt.Sprintf("/api/v1/agents/%s", agentID), map[string]interface{}{
			"name": "Updated Agent Name",
		})
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var agent map[string]interface{}
		env.DecodeJSON(t, resp, &agent)
		assert.Equal(t, "Updated Agent Name", agent["name"])
	})

	// --- Step 5: Regenerate API key ---
	t.Run("RegenerateKey", func(t *testing.T) {
		if agentID == "" {
			t.Skip("agent not created")
		}
		resp := env.Post(t, fmt.Sprintf("/api/v1/agents/%s/regenerate-key", agentID), nil)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var result map[string]interface{}
		env.DecodeJSON(t, resp, &result)
		newKey, ok := result["api_key"].(string)
		require.True(t, ok)
		assert.NotEmpty(t, newKey)
		assert.True(t, strings.HasPrefix(newKey, "agk_"))
		assert.NotEqual(t, apiKey, newKey, "new key must differ from old key")
	})

	// --- Step 6: Delete agent (soft delete) ---
	t.Run("DeleteAgent", func(t *testing.T) {
		if agentID == "" {
			t.Skip("agent not created")
		}
		resp := env.Delete(t, fmt.Sprintf("/api/v1/agents/%s", agentID))
		require.Equal(t, http.StatusNoContent, resp.StatusCode)
		resp.Body.Close()

		// Verify agent is not returned.
		resp = env.Get(t, fmt.Sprintf("/api/v1/agents/%s", agentID))
		assert.Contains(t, []int{http.StatusNotFound, http.StatusInternalServerError}, resp.StatusCode,
			"deleted agent should not be accessible")
		resp.Body.Close()
	})
}

func TestAgentFlow_Validation(t *testing.T) {
	env := NewTestEnv(t)
	defer env.Cleanup(t)

	email := uniqueEmail("agent-validation")
	env.Register(t, email, "TestPass123", "Agent Validator")

	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]interface{}
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces)
	wsID := workspaces[0]["id"].(string)

	// --- Name required ---
	t.Run("NameRequired", func(t *testing.T) {
		resp := env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/agents", wsID), map[string]interface{}{
			"agent_type": "claude_code",
		})
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		resp.Body.Close()
	})

	// --- Invalid workspace ID ---
	t.Run("InvalidWorkspaceID", func(t *testing.T) {
		resp := env.Post(t, "/api/v1/workspaces/not-a-uuid/agents", map[string]interface{}{
			"name":       "Test",
			"agent_type": "claude_code",
		})
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		resp.Body.Close()
	})
}
