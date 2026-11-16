package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func taskNumber(t *testing.T, f *moveFixture, id uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, f.db.Get(&n, `SELECT task_number FROM tasks WHERE id=$1`, id))
	return n
}

// uq_tasks_project_number covers soft-deleted rows too, so a number a deleted task holds in the target project is
// taken. MoveToProject used to pick MAX+1 over live rows only and reused it: 409 "already exists" on Move.
func TestMoveToProject_SkipsNumbersHeldBySoftDeletedTasks(t *testing.T) {
	f := newMoveFixture(t)
	ctx := context.Background()
	repo := NewTaskRepo(f.db)

	first := graphTask(t, f.completionGraphFixture, nil)
	require.NoError(t, repo.MoveToProject(ctx, first, f.dstProject, f.dstTodo))
	held := taskNumber(t, f, first)
	require.NoError(t, repo.Delete(ctx, first))

	// twice: the second move lands on top of the first moved-and-deleted one, as in the live repeat
	prev := held
	for i := 0; i < 2; i++ {
		next := graphTask(t, f.completionGraphFixture, nil)
		require.NoError(t, repo.MoveToProject(ctx, next, f.dstProject, f.dstTodo), "move %d", i+1)
		require.Equal(t, f.dstProject, mustProject(t, f, next))
		var status uuid.UUID
		require.NoError(t, f.db.Get(&status, `SELECT status_id FROM tasks WHERE id=$1`, next))
		require.Equal(t, f.dstTodo, status)
		n := taskNumber(t, f, next)
		require.Greater(t, n, prev, "a moved task takes a number above every row of the target project, deleted ones included")
		prev = n
		require.NoError(t, repo.Delete(ctx, next))
	}

	// a task created in the target afterwards still gets a free number
	created := f.newTask()
	created.ProjectID, created.StatusID = f.dstProject, f.dstTodo
	require.NoError(t, repo.Create(ctx, created, nil))
	require.Greater(t, taskNumber(t, f, created.ID), prev)
}
