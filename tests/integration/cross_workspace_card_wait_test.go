//go:build integration

package integration

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Exercise the real HTTP guards and the source receipt using a HOME key. The
// guest key is never used to wake the source; a human in the source workspace
// must not gain the guest agent's right to read the target.
func TestCrossWorkspace_CardWaitReceiptAndRedaction(t *testing.T) {
	source, target := newTenant(t, "wait-source"), newTenant(t, "wait-target")
	env := source.env
	agentID, homeKey := env.CreateAgent(t, source.wsID, "card-wait-agent")
	_, err := env.DB.Exec(`UPDATE tasks SET checked_out_by=$2,checkout_expires=now()+interval '15 minutes'
		WHERE id=$1`, source.taskID, agentID)
	require.NoError(t, err)
	// A real agent working on its card is a member of the card's project;
	// without the row the project guard answers 403 before the wait handler.
	enrol := env.Post(t, "/api/v1/projects/"+source.projectID+"/members/agents",
		map[string]any{"agent_id": agentID, "role": "member"})
	require.Equal(t, http.StatusCreated, enrol.StatusCode, string(env.ReadBody(t, enrol)))
	base := "/api/v1/tasks/" + source.taskID
	command := func(targetID string) *http.Response {
		return env.PostWithAgentKey(t, base+"/wait", homeKey,
			map[string]any{"on": "card", "target_task_id": targetID, "target_condition": "task_done"})
	}
	denied := command(target.otherTask)
	deniedBody := env.ReadBody(t, denied)
	require.Equal(t, http.StatusUnprocessableEntity, denied.StatusCode, string(deniedBody))
	missing := command(uuid.NewString())
	missingBody := env.ReadBody(t, missing)
	require.Equal(t, http.StatusUnprocessableEntity, missing.StatusCode, string(missingBody))
	assert.JSONEq(t, string(deniedBody), string(missingBody))
	assert.Contains(t, string(deniedBody), `"error_code":"invalid_target"`)
	assert.Contains(t, string(deniedBody), "target card not found")

	grantResp := target.env.Post(t, "/api/v1/workspaces/"+target.wsID+"/agent-grants",
		map[string]any{"agent_id": agentID, "role": "member"})
	require.Equal(t, http.StatusCreated, grantResp.StatusCode)
	var grant struct {
		ID string `json:"id"`
	}
	target.env.DecodeJSON(t, grantResp, &grant)
	// Ordinary dependency writes keep the original tenancy rule even for an
	// agent whose membership permits the typed command below.
	ordinary := env.PostWithAgentKey(t, base+"/dependencies", homeKey,
		map[string]any{"depends_on_task_id": target.otherTask})
	assert.Equal(t, http.StatusNotFound, ordinary.StatusCode)
	_ = ordinary.Body.Close()

	resp := command(target.otherTask)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("typed card wait returned %d: %s", resp.StatusCode, env.ReadBody(t, resp))
	}
	var result struct {
		Wait struct {
			ID             string `json:"id"`
			ServerObserved bool   `json:"server_observed"`
		} `json:"wait"`
	}
	env.DecodeJSON(t, resp, &result)
	require.True(t, result.Wait.ServerObserved)
	require.NotEmpty(t, result.Wait.ID)
	deps := env.Get(t, base+"/dependencies")
	require.Equal(t, http.StatusOK, deps.StatusCode)
	var edges struct {
		Outgoing []map[string]any `json:"outgoing"`
	}
	env.DecodeJSON(t, deps, &edges)
	found := false
	for _, edge := range edges.Outgoing {
		if edge["depends_on_task_id"] == target.otherTask {
			found = true
			assert.Nil(t, edge["related_task_title"])
			assert.Nil(t, edge["related_task_status_id"])
		}
	}
	require.True(t, found, "redact the foreign card, but keep its blocking edge visible")

	// A home key still cannot read the foreign task. Resolving and observing
	// the wait requires no permission change and no target read on that key.
	foreign := env.GetWithAgentKey(t, "/api/v1/tasks/"+target.otherTask, homeKey)
	assert.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, foreign.StatusCode)
	_ = foreign.Body.Close()
	_, err = env.DB.Exec(`UPDATE tasks SET status_id=(SELECT id FROM task_statuses
		WHERE project_id=$2 AND category='done' LIMIT 1) WHERE id=$1`, target.otherTask, target.projectID)
	require.NoError(t, err)
	receiptResp := env.GetWithAgentKey(t, base+"/wait?wait_id="+result.Wait.ID, homeKey)
	require.Equal(t, http.StatusOK, receiptResp.StatusCode)
	var receipt map[string]any
	env.DecodeJSON(t, receiptResp, &receipt)
	assert.Equal(t, "met", receipt["resolution"])
	assert.Equal(t, "target_condition_met", receipt["resolve_cause"])
	assert.NotEmpty(t, receipt["resolved_at"])
	assert.Equal(t, source.taskID, receipt["task_id"])
	assert.Equal(t, agentID, receipt["agent_id"])

	// A wait id cannot be used to read a receipt through a different card.
	wrongSource := env.GetWithAgentKey(t, "/api/v1/tasks/"+source.otherTask+"/wait?wait_id="+result.Wait.ID, homeKey)
	assert.Equal(t, http.StatusNotFound, wrongSource.StatusCode)
	_ = wrongSource.Body.Close()
}
