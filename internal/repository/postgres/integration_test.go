//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

func testDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://evc:evc@localhost:5437/evc_mesh_test?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

// ---------------------------------------------------------------------------
// WorkspaceRepository
// ---------------------------------------------------------------------------

func TestWorkspaceRepo_CreateAndGetByID(t *testing.T) {
	db := testDB(t)
	repo := NewWorkspaceRepo(db)
	ctx := context.Background()

	ws := &domain.Workspace{
		ID:        uuid.New(),
		Name:      "Test Workspace",
		Slug:      "test-ws-" + uuid.New().String()[:8],
		OwnerID:   uuid.New(),
		Settings:  json.RawMessage(`{"theme":"dark"}`),
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		UpdatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}

	err := repo.Create(ctx, ws)
	require.NoError(t, err)

	got, err := repo.GetByID(ctx, ws.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, ws.ID, got.ID)
	assert.Equal(t, ws.Name, got.Name)
	assert.Equal(t, ws.Slug, got.Slug)
	assert.Equal(t, ws.OwnerID, got.OwnerID)

	// Cleanup
	_, _ = db.ExecContext(ctx, "DELETE FROM workspaces WHERE id = $1", ws.ID)
}

func TestWorkspaceRepo_GetBySlug(t *testing.T) {
	db := testDB(t)
	repo := NewWorkspaceRepo(db)
	ctx := context.Background()

	slug := "slug-" + uuid.New().String()[:8]
	ws := &domain.Workspace{
		ID:        uuid.New(),
		Name:      "Slug Test",
		Slug:      slug,
		OwnerID:   uuid.New(),
		Settings:  json.RawMessage(`{}`),
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		UpdatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	require.NoError(t, repo.Create(ctx, ws))

	got, err := repo.GetBySlug(ctx, slug)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, ws.ID, got.ID)

	// Not found
	got, err = repo.GetBySlug(ctx, "nonexistent-slug-abc")
	require.NoError(t, err)
	assert.Nil(t, got)

	_, _ = db.ExecContext(ctx, "DELETE FROM workspaces WHERE id = $1", ws.ID)
}

func TestWorkspaceRepo_ListByOwner(t *testing.T) {
	db := testDB(t)
	repo := NewWorkspaceRepo(db)
	ctx := context.Background()

	ownerID := uuid.New()
	for i := 0; i < 3; i++ {
		ws := &domain.Workspace{
			ID:        uuid.New(),
			Name:      "Owner Test",
			Slug:      "ot-" + uuid.New().String()[:8],
			OwnerID:   ownerID,
			Settings:  json.RawMessage(`{}`),
			CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
			UpdatedAt: time.Now().UTC().Truncate(time.Microsecond),
		}
		require.NoError(t, repo.Create(ctx, ws))
		t.Cleanup(func() {
			_, _ = db.ExecContext(ctx, "DELETE FROM workspaces WHERE id = $1", ws.ID)
		})
	}

	list, err := repo.ListByOwner(ctx, ownerID)
	require.NoError(t, err)
	assert.Len(t, list, 3)
}

// ---------------------------------------------------------------------------
// TaskRepository
// ---------------------------------------------------------------------------

// createTestProject sets up a workspace, project, and default status for task tests.
func createTestProject(t *testing.T, db *sqlx.DB) (workspace *domain.Workspace, project *domain.Project, status *domain.TaskStatus) {
	t.Helper()
	ctx := context.Background()

	ws := &domain.Workspace{
		ID:        uuid.New(),
		Name:      "Task Test WS",
		Slug:      "tws-" + uuid.New().String()[:8],
		OwnerID:   uuid.New(),
		Settings:  json.RawMessage(`{}`),
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		UpdatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug, owner_id, settings, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		ws.ID, ws.Name, ws.Slug, ws.OwnerID, ws.Settings, ws.CreatedAt, ws.UpdatedAt,
	)
	require.NoError(t, err)

	proj := &domain.Project{
		ID:                  uuid.New(),
		WorkspaceID:         ws.ID,
		Name:                "Task Test Project",
		Slug:                "tp-" + uuid.New().String()[:8],
		DefaultAssigneeType: domain.DefaultAssigneeNone,
		Settings:            json.RawMessage(`{}`),
		CreatedAt:           time.Now().UTC().Truncate(time.Microsecond),
		UpdatedAt:           time.Now().UTC().Truncate(time.Microsecond),
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO projects (id, workspace_id, name, slug, default_assignee_type, settings, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		proj.ID, proj.WorkspaceID, proj.Name, proj.Slug, proj.DefaultAssigneeType, proj.Settings, proj.CreatedAt, proj.UpdatedAt,
	)
	require.NoError(t, err)

	ts := &domain.TaskStatus{
		ID:             uuid.New(),
		ProjectID:      proj.ID,
		Name:           "Open",
		Slug:           "open",
		Color:          "#00FF00",
		Position:       0,
		Category:       domain.StatusCategoryTodo,
		IsDefault:      true,
		AutoTransition: json.RawMessage(`{}`),
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO task_statuses (id, project_id, name, slug, color, position, category, is_default, auto_transition) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		ts.ID, ts.ProjectID, ts.Name, ts.Slug, ts.Color, ts.Position, ts.Category, ts.IsDefault, ts.AutoTransition,
	)
	require.NoError(t, err)

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", proj.ID)
		_, _ = db.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", proj.ID)
		_, _ = db.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", proj.ID)
		_, _ = db.ExecContext(ctx, "DELETE FROM workspaces WHERE id = $1", ws.ID)
	})

	return ws, proj, ts
}

func TestTaskRepo_CreateAndGetByID(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	repo := NewTaskRepo(db)
	ctx := context.Background()

	task := &domain.Task{
		ID:            uuid.New(),
		ProjectID:     proj.ID,
		StatusID:      status.ID,
		Title:         "Integration test task",
		Description:   "Testing the task repo",
		AssigneeType:  domain.AssigneeTypeUnassigned,
		Priority:      domain.PriorityMedium,
		Position:      1.0,
		CustomFields:  json.RawMessage(`{"key":"val"}`),
		Labels:        pq.StringArray{"test", "integration"},
		CreatedBy:     uuid.New(),
		CreatedByType: domain.ActorTypeUser,
		CreatedAt:     time.Now().UTC().Truncate(time.Microsecond),
		UpdatedAt:     time.Now().UTC().Truncate(time.Microsecond),
	}

	err := repo.Create(ctx, task)
	require.NoError(t, err)

	got, err := repo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, task.ID, got.ID)
	assert.Equal(t, task.Title, got.Title)
	assert.Equal(t, task.Priority, got.Priority)
	assert.Equal(t, pq.StringArray{"test", "integration"}, got.Labels)
}

func TestTaskRepo_ListWithFilters(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	repo := NewTaskRepo(db)
	ctx := context.Background()

	// Create several tasks
	for i := 0; i < 5; i++ {
		task := &domain.Task{
			ID:            uuid.New(),
			ProjectID:     proj.ID,
			StatusID:      status.ID,
			Title:         "Task " + uuid.New().String()[:4],
			AssigneeType:  domain.AssigneeTypeUnassigned,
			Priority:      domain.PriorityMedium,
			Position:      float64(i),
			CustomFields:  json.RawMessage(`{}`),
			Labels:        pq.StringArray{},
			CreatedBy:     uuid.New(),
			CreatedByType: domain.ActorTypeUser,
			CreatedAt:     time.Now().UTC().Truncate(time.Microsecond),
			UpdatedAt:     time.Now().UTC().Truncate(time.Microsecond),
		}
		require.NoError(t, repo.Create(ctx, task))
	}

	// List all
	pg := pagination.Params{Page: 1, PageSize: 10}
	page, err := repo.List(ctx, proj.ID, repository.TaskFilter{}, pg)
	require.NoError(t, err)
	assert.Equal(t, 5, page.TotalCount)
	assert.Len(t, page.Items, 5)

	// Filter by status
	page, err = repo.List(ctx, proj.ID, repository.TaskFilter{
		StatusIDs: []uuid.UUID{status.ID},
	}, pg)
	require.NoError(t, err)
	assert.Equal(t, 5, page.TotalCount)
}

func TestTaskRepo_ListSubtasks(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	repo := NewTaskRepo(db)
	ctx := context.Background()

	parentTask := &domain.Task{
		ID:            uuid.New(),
		ProjectID:     proj.ID,
		StatusID:      status.ID,
		Title:         "Parent Task",
		AssigneeType:  domain.AssigneeTypeUnassigned,
		Priority:      domain.PriorityHigh,
		Position:      0,
		CustomFields:  json.RawMessage(`{}`),
		Labels:        pq.StringArray{},
		CreatedBy:     uuid.New(),
		CreatedByType: domain.ActorTypeUser,
		CreatedAt:     time.Now().UTC().Truncate(time.Microsecond),
		UpdatedAt:     time.Now().UTC().Truncate(time.Microsecond),
	}
	require.NoError(t, repo.Create(ctx, parentTask))

	// Create subtasks
	for i := 0; i < 3; i++ {
		child := &domain.Task{
			ID:            uuid.New(),
			ProjectID:     proj.ID,
			StatusID:      status.ID,
			Title:         "Subtask",
			AssigneeType:  domain.AssigneeTypeUnassigned,
			Priority:      domain.PriorityLow,
			ParentTaskID:  &parentTask.ID,
			Position:      float64(i),
			CustomFields:  json.RawMessage(`{}`),
			Labels:        pq.StringArray{},
			CreatedBy:     uuid.New(),
			CreatedByType: domain.ActorTypeUser,
			CreatedAt:     time.Now().UTC().Truncate(time.Microsecond),
			UpdatedAt:     time.Now().UTC().Truncate(time.Microsecond),
		}
		require.NoError(t, repo.Create(ctx, child))
	}

	subtasks, err := repo.ListSubtasks(ctx, parentTask.ID)
	require.NoError(t, err)
	assert.Len(t, subtasks, 3)
}

// TestTaskRepo_List_DefaultSortUpdatedAtDesc verifies that tasks created later (higher
// updated_at) appear first when no explicit sort params are provided.  This is the
// write-through invariant: any task mutation bumps updated_at so the task floats to the
// top of the first page and becomes immediately visible on the board.
func TestTaskRepo_List_DefaultSortUpdatedAtDesc(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	repo := NewTaskRepo(db)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Microsecond)

	// Create tasks with staggered updated_at so the ordering is deterministic.
	taskIDs := make([]uuid.UUID, 3)
	for i := 0; i < 3; i++ {
		id := uuid.New()
		taskIDs[i] = id
		task := &domain.Task{
			ID:            id,
			ProjectID:     proj.ID,
			StatusID:      status.ID,
			Title:         "Task " + id.String()[:4],
			AssigneeType:  domain.AssigneeTypeUnassigned,
			Priority:      domain.PriorityMedium,
			Position:      float64(i),
			CustomFields:  json.RawMessage(`{}`),
			Labels:        pq.StringArray{},
			CreatedBy:     uuid.New(),
			CreatedByType: domain.ActorTypeUser,
			CreatedAt:     base.Add(time.Duration(i) * time.Second),
			UpdatedAt:     base.Add(time.Duration(i) * time.Second),
		}
		require.NoError(t, repo.Create(ctx, task))
	}

	// No sort params — should default to updated_at DESC, so taskIDs[2] comes first.
	page, err := repo.List(ctx, proj.ID, repository.TaskFilter{}, pagination.Params{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, 3, page.TotalCount)
	require.Len(t, page.Items, 3)
	assert.Equal(t, taskIDs[2], page.Items[0].ID, "newest task (highest updated_at) must be first")
	assert.Equal(t, taskIDs[0], page.Items[2].ID, "oldest task (lowest updated_at) must be last")

	// Explicit sort_by=created_at asc still works and overrides the default.
	page, err = repo.List(ctx, proj.ID, repository.TaskFilter{}, pagination.Params{
		Page: 1, PageSize: 10, SortBy: "created_at", SortDir: "asc",
	})
	require.NoError(t, err)
	require.Equal(t, 3, page.TotalCount)
	assert.Equal(t, taskIDs[0], page.Items[0].ID, "explicit created_at asc: oldest must be first")
}

// TestTaskRepo_List_IdTiebreaker verifies that when tasks share an identical updated_at
// timestamp (e.g. after a bulk migration), the secondary sort key id ASC produces a
// stable, deterministic page order.  This test would be flaky without the tiebreaker.
func TestTaskRepo_List_IdTiebreaker(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	repo := NewTaskRepo(db)
	ctx := context.Background()

	sameTime := time.Now().UTC().Truncate(time.Microsecond)

	// Use deterministic UUIDs with a known ascending order.
	id1 := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	id2 := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	id3 := uuid.MustParse("00000000-0000-0000-0000-000000000003")

	for i, id := range []uuid.UUID{id1, id2, id3} {
		task := &domain.Task{
			ID:            id,
			ProjectID:     proj.ID,
			StatusID:      status.ID,
			Title:         "Tie " + id.String()[:8],
			AssigneeType:  domain.AssigneeTypeUnassigned,
			Priority:      domain.PriorityMedium,
			Position:      float64(i),
			CustomFields:  json.RawMessage(`{}`),
			Labels:        pq.StringArray{},
			CreatedBy:     uuid.New(),
			CreatedByType: domain.ActorTypeUser,
			CreatedAt:     sameTime,
			UpdatedAt:     sameTime,
		}
		require.NoError(t, repo.Create(ctx, task))
	}

	// All tasks have the same updated_at; id ASC tiebreaker must produce id1, id2, id3.
	page, err := repo.List(ctx, proj.ID, repository.TaskFilter{}, pagination.Params{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, 3, page.TotalCount)
	require.Len(t, page.Items, 3)
	assert.Equal(t, id1, page.Items[0].ID, "id tiebreaker: smallest UUID must be first")
	assert.Equal(t, id2, page.Items[1].ID, "id tiebreaker: middle UUID must be second")
	assert.Equal(t, id3, page.Items[2].ID, "id tiebreaker: largest UUID must be last")
}
