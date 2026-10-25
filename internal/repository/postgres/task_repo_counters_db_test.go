package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// No //go:build integration tag — same convention as comment_system_code_db_test.go.
//
// Board card counters: open_subtask_count, dependency_count and
// open_dependency_count come from the enriched SELECT, for both GetByID
// (no alias) and List (aliased) variants.
func TestTaskRepo_DependencyAndOpenSubtaskCounters(t *testing.T) {
	db := commentCursorTestDB(t)
	fx := newCursorTestFixture(t, db)
	repo := NewTaskRepo(db)
	ctx := context.Background()

	var projectID, openStatus uuid.UUID
	require.NoError(t, db.QueryRowx(`SELECT project_id, status_id FROM tasks WHERE id = $1`, fx.taskID).Scan(&projectID, &openStatus))

	mkStatus := func(slug string, cat domain.StatusCategory, pos int) uuid.UUID {
		st := &domain.TaskStatus{ID: uuid.New(), ProjectID: projectID, Name: slug, Slug: slug, Color: "#111111", Position: pos, Category: cat}
		require.NoError(t, NewTaskStatusRepo(db).Create(ctx, st))
		return st.ID
	}
	doneStatus := mkStatus("done", domain.StatusCategoryDone, 1)
	cancelledStatus := mkStatus("cancelled", domain.StatusCategoryCancelled, 2)

	mkTask := func(title string, status uuid.UUID, parent *uuid.UUID) uuid.UUID {
		task := &domain.Task{
			ID: uuid.New(), ProjectID: projectID, StatusID: status, ParentTaskID: parent, Title: title,
			AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium,
			CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
		}
		require.NoError(t, repo.Create(ctx, task, nil))
		return task.ID
	}
	// Parent: 1 open, 1 done, 1 cancelled subtask -> 3 total, 1 open.
	parent := fx.taskID
	mkTask("open child", openStatus, &parent)
	mkTask("done child", doneStatus, &parent)
	mkTask("cancelled child", cancelledStatus, &parent)

	addEdge := func(task, dependsOn uuid.UUID, typ domain.DependencyType) {
		_, err := db.ExecContext(ctx, `INSERT INTO task_dependencies (id, task_id, depends_on_task_id, dependency_type) VALUES ($1,$2,$3,$4)`,
			uuid.New(), task, dependsOn, typ)
		require.NoError(t, err)
	}
	// parent waits on: open blocker, done blocker, cancelled blocker (3 total, 1 open);
	// relates_to and is_child_of must not count; the reverse edge (parent blocks other) must not count.
	other := mkTask("other", openStatus, nil)
	addEdge(parent, mkTask("open blocker", openStatus, nil), domain.DependencyTypeBlocks)
	addEdge(parent, mkTask("done blocker", doneStatus, nil), domain.DependencyTypeBlocks)
	addEdge(parent, mkTask("cancelled blocker", cancelledStatus, nil), domain.DependencyTypeBlocks)
	// A soft-deleted blocker is gone from the live graph: it counts in neither total nor open.
	deleted := mkTask("deleted blocker", openStatus, nil)
	addEdge(parent, deleted, domain.DependencyTypeBlocks)
	_, err := db.ExecContext(ctx, `UPDATE tasks SET deleted_at = now() WHERE id = $1`, deleted)
	require.NoError(t, err)
	addEdge(parent, mkTask("related", openStatus, nil), domain.DependencyTypeRelatesTo)
	addEdge(parent, mkTask("child-of", openStatus, nil), domain.DependencyTypeIsChildOf)
	addEdge(other, parent, domain.DependencyTypeBlocks)

	assertCounters := func(got *domain.Task) {
		t.Helper()
		assert.Equal(t, 3, got.SubtaskCount)
		assert.Equal(t, 1, got.OpenSubtaskCount, "cancelled/done subtasks are not open")
		assert.Equal(t, 3, got.DependencyCount, "only blocks edges where the card is dependent")
		assert.Equal(t, 1, got.OpenDependencyCount)
	}

	got, err := repo.GetByID(ctx, parent)
	require.NoError(t, err)
	assertCounters(got)

	// A card with nothing keeps all three at zero.
	none, err := repo.GetByID(ctx, other)
	require.NoError(t, err)
	assert.Equal(t, 0, none.OpenSubtaskCount)
	assert.Equal(t, 1, none.DependencyCount)
	assert.Equal(t, 1, none.OpenDependencyCount, "parent is open")

	page, err := repo.List(ctx, projectID, repository.TaskFilter{}, pagination.Params{Page: 1, PageSize: 50})
	require.NoError(t, err)
	tasks := page.Items
	var found bool
	for i := range tasks {
		if tasks[i].ID == parent {
			assertCounters(&tasks[i])
			found = true
		}
	}
	assert.True(t, found, "parent present in List")
}
