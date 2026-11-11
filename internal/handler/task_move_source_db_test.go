package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// On real Postgres and the real /api/v1 chain: a reserved source is
// a 400 that writes no task.moved row and leaves the task where it was; a
// client producer (mcp, mesh-runner) lands in activity_log.source and in
// changes.source instead of being folded into "api".
func TestMoveTask_SourceRecordedAndReservedRefused_DB(t *testing.T) {
	env := newChatE2EEnv(t)
	agentKey := map[string]string{"X-Agent-Key": env.agentKey}
	moved := func(taskID any) (n int) {
		require.NoError(t, env.db.Get(&n, `SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action='task.moved'`, taskID))
		return n
	}

	forged := env.newTask(t, env.projectID, env.todoID)
	code, body := env.post(t, "/api/v1/tasks/"+forged.String()+"/move", agentKey,
		map[string]any{"status_id": env.doneID, "source": "auto_transition"})
	require.Equal(t, http.StatusBadRequest, code, string(body))
	assert.Zero(t, moved(forged), "a refused move must not write an activity row")
	assert.Equal(t, env.todoID, env.taskStatus(t, forged), "a refused move must not change the task")

	for _, src := range []string{"mcp", "mesh-runner"} {
		task := env.newTask(t, env.projectID, env.todoID)
		code, body = env.post(t, "/api/v1/tasks/"+task.String()+"/move", agentKey,
			map[string]any{"status_id": env.doneID, "source": src})
		require.Equal(t, http.StatusOK, code, string(body))
		var row struct {
			Source  string `db:"source"`
			Changes []byte `db:"changes"`
		}
		require.NoError(t, env.db.Get(&row, `SELECT source, changes FROM activity_log WHERE entity_id=$1 AND action='task.moved'`, task))
		var changes map[string]any
		require.NoError(t, json.Unmarshal(row.Changes, &changes))
		assert.Equal(t, src, row.Source, "activity_log.source")
		assert.Equal(t, src, changes["source"], "changes.source")
	}
}
