package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// A metadata write that changes nothing must leave no activity row: an empty
// task.updated, an X→X pointer diff or a none→none reassignment is noise that
// the activity feed renders as a change. A real change still logs exactly the
// changed field (positive control).
func TestTaskNoOpMetadataWritesNoActivity(t *testing.T) {
	f, repo, svc, id, _ := newCheckoutM1Fixture(t)
	ctx := actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)
	due := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	after := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	hours := 3.5
	_, err := f.db.Exec(`UPDATE tasks SET due_date=$2,start_after=$3,estimated_hours=$4 WHERE id=$1`, id, due, after, hours)
	require.NoError(t, err)
	_, err = f.db.Exec(`DELETE FROM activity_log WHERE entity_id=$1`, id)
	require.NoError(t, err)
	rows := func(action string) []map[string]any {
		var raw [][]byte
		require.NoError(t, f.db.Select(&raw, `SELECT changes FROM activity_log WHERE entity_id=$1 AND action=$2 ORDER BY created_at`, id, action))
		out := make([]map[string]any, 0, len(raw))
		for _, r := range raw {
			var m map[string]any
			require.NoError(t, json.Unmarshal(r, &m))
			out = append(out, m)
		}
		return out
	}

	task, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	require.NoError(t, svc.Update(ctx, task))
	require.Empty(t, rows("task.updated"), "an unchanged PATCH (incl. set due_date/start_after/estimated_hours) logs nothing")

	require.NoError(t, svc.AssignTask(ctx, id, AssignTaskInput{AssigneeID: nil, AssigneeType: domain.AssigneeTypeUnassigned}))
	require.Empty(t, rows("task.assigned"), "unassigning an unassigned task is none→none")

	task, err = repo.GetByID(ctx, id)
	require.NoError(t, err)
	task.Title = "renamed"
	require.NoError(t, svc.Update(ctx, task))
	updated := rows("task.updated")
	require.Len(t, updated, 1, "positive control: a real change is logged")
	require.Contains(t, updated[0], "title")
	require.Len(t, updated[0], 1, "only the changed field is in the diff: %v", updated[0])
}
