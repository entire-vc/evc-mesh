//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
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

// createTestUser inserts a user row (workspace_members.user_id has an FK to
// users) and registers its cleanup.
func createTestUser(t *testing.T, db *sqlx.DB, prefix string) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	id := uuid.New()
	suffix := uuid.New().String()[:8]
	_, err := db.ExecContext(ctx,
		`INSERT INTO users (id, email, password_hash, display_name, username, is_active, created_at, updated_at)
		 VALUES ($1, $2, 'testhash', $3, $4, true, $5, $5)`,
		id, prefix+"-"+suffix+"@test.example", prefix+" user", prefix+suffix, time.Now().UTC(),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM users WHERE id = $1", id)
	})
	return id
}

// createTestWorkspace inserts a workspace owned by ownerID and registers cleanup.
func createTestWorkspace(t *testing.T, db *sqlx.DB, name string, ownerID uuid.UUID, createdAt time.Time) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	id := uuid.New()
	_, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug, owner_id, settings, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, '{}', $5, $5)`,
		id, name, "lfu-"+uuid.New().String()[:8], ownerID, createdAt,
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM workspace_members WHERE workspace_id = $1", id)
		_, _ = db.ExecContext(ctx, "DELETE FROM workspaces WHERE id = $1", id)
	})
	return id
}

func addWorkspaceMember(t *testing.T, db *sqlx.DB, workspaceID, userID uuid.UUID, role string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at, updated_at)
		 VALUES ($1, $2, $3, $4::workspace_role, $5, $5)`,
		uuid.New(), workspaceID, userID, role, time.Now().UTC(),
	)
	require.NoError(t, err)
}

// containsWorkspace reports whether the list contains the given workspace ID.
func containsWorkspace(list []domain.Workspace, id uuid.UUID) bool {
	for i := range list {
		if list[i].ID == id {
			return true
		}
	}
	return false
}

// countWorkspace returns how many times the given workspace ID appears.
func countWorkspace(list []domain.Workspace, id uuid.UUID) int {
	n := 0
	for i := range list {
		if list[i].ID == id {
			n++
		}
	}
	return n
}

// TestWorkspaceRepo_ListForUser covers workspace discovery by membership, which
// is what GET /api/v1/workspaces relies on. Ownership alone used to be the
// predicate, which made a workspace invisible to everyone but its owner.
func TestWorkspaceRepo_ListForUser(t *testing.T) {
	db := testDB(t)
	repo := NewWorkspaceRepo(db)
	ctx := context.Background()

	ownerID := createTestUser(t, db, "lfu-owner")
	memberID := createTestUser(t, db, "lfu-member")
	outsiderID := createTestUser(t, db, "lfu-outsider")

	now := time.Now().UTC().Truncate(time.Microsecond)

	// Normal workspace: owner + member both have workspace_members rows.
	wsID := createTestWorkspace(t, db, "ListForUser WS", ownerID, now)
	addWorkspaceMember(t, db, wsID, ownerID, "owner")
	addWorkspaceMember(t, db, wsID, memberID, "member")

	// Legacy workspace: owner has NO workspace_members row (the auto-insert in
	// workspaceService.Create / auth.Service.Register is best-effort).
	legacyID := createTestWorkspace(t, db, "Legacy WS", ownerID, now.Add(time.Second))

	// Soft-deleted workspace the member belongs to: must never be listed.
	deletedID := createTestWorkspace(t, db, "Deleted WS", ownerID, now.Add(2*time.Second))
	addWorkspaceMember(t, db, deletedID, memberID, "member")
	_, err := db.ExecContext(ctx, "UPDATE workspaces SET deleted_at = NOW() WHERE id = $1", deletedID)
	require.NoError(t, err)

	t.Run("owner sees their own workspace", func(t *testing.T) {
		list, err := repo.ListForUser(ctx, ownerID)
		require.NoError(t, err)
		assert.True(t, containsWorkspace(list, wsID), "owner must see the workspace they own")
	})

	t.Run("member who is not the owner sees the workspace", func(t *testing.T) {
		list, err := repo.ListForUser(ctx, memberID)
		require.NoError(t, err)
		assert.True(t, containsWorkspace(list, wsID), "member must see the workspace")
	})

	t.Run("non-member does not see the workspace", func(t *testing.T) {
		list, err := repo.ListForUser(ctx, outsiderID)
		require.NoError(t, err)
		assert.False(t, containsWorkspace(list, wsID))
		assert.False(t, containsWorkspace(list, legacyID))
		assert.Empty(t, list, "a user with no workspaces gets an empty list")
	})

	t.Run("owner with a missing workspace_members row still sees the workspace", func(t *testing.T) {
		var memberCount int
		require.NoError(t, db.GetContext(ctx, &memberCount,
			"SELECT COUNT(*) FROM workspace_members WHERE workspace_id = $1", legacyID))
		require.Equal(t, 0, memberCount, "precondition: legacy workspace has no member rows")

		list, err := repo.ListForUser(ctx, ownerID)
		require.NoError(t, err)
		assert.True(t, containsWorkspace(list, legacyID), "legacy owner must still see their workspace")
	})

	t.Run("owner who is also a member is not duplicated", func(t *testing.T) {
		list, err := repo.ListForUser(ctx, ownerID)
		require.NoError(t, err)
		assert.Equal(t, 1, countWorkspace(list, wsID), "workspace must appear exactly once")
	})

	t.Run("soft-deleted workspaces are excluded", func(t *testing.T) {
		list, err := repo.ListForUser(ctx, memberID)
		require.NoError(t, err)
		assert.False(t, containsWorkspace(list, deletedID))
	})

	t.Run("results are ordered by created_at ascending", func(t *testing.T) {
		list, err := repo.ListForUser(ctx, ownerID)
		require.NoError(t, err)
		require.Len(t, list, 2)
		assert.Equal(t, wsID, list[0].ID)
		assert.Equal(t, legacyID, list[1].ID)
	})

	t.Run("removing the membership removes the workspace from the list", func(t *testing.T) {
		_, err := db.ExecContext(ctx,
			"DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = $2", wsID, memberID)
		require.NoError(t, err)

		list, err := repo.ListForUser(ctx, memberID)
		require.NoError(t, err)
		assert.False(t, containsWorkspace(list, wsID), "an ex-member must not see the workspace")

		// Restore for independence from subtest ordering.
		addWorkspaceMember(t, db, wsID, memberID, "member")
	})
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

// TestTaskRepo_Update_LabelsRoundTrip is the repository-level regression test for
// labels surviving Update, not just Create. Create's round trip is already covered
// by TestTaskRepo_CreateAndGetByID above; this closes the matching gap on the
// Update path — same category of bug as TestTaskRepo_Create_ReviewerRoundTrip
// below (a field present in one write path's SQL but silently missing from the
// other's). Reported live 2026-08-20 (task #19d927d9): update_task(labels=[...])
// via the fleet's MCP tool call returned labels=[] on the following get_task. At
// this repo/DB layer the write path could not be made to reproduce that symptom
// (see PR description for the live-reproduction evidence at the API layer); this
// test exists so a REAL future regression here — e.g. someone editing the columns
// list in the Update SQL and missing "labels" — fails loudly instead of silently.
func TestTaskRepo_Update_LabelsRoundTrip(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	repo := NewTaskRepo(db)
	ctx := context.Background()

	task := &domain.Task{
		ID:            uuid.New(),
		ProjectID:     proj.ID,
		StatusID:      status.ID,
		Title:         "Task for labels update round-trip",
		AssigneeType:  domain.AssigneeTypeUnassigned,
		Priority:      domain.PriorityMedium,
		Labels:        pq.StringArray{"initial"},
		CreatedBy:     uuid.New(),
		CreatedByType: domain.ActorTypeUser,
		CreatedAt:     time.Now().UTC().Truncate(time.Microsecond),
		UpdatedAt:     time.Now().UTC().Truncate(time.Microsecond),
	}
	require.NoError(t, repo.Create(ctx, task))

	got, err := repo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, pq.StringArray{"initial"}, got.Labels, "labels must survive Create before we even test Update")

	// Update to a different, larger label set — mirrors update_task(labels=[...]).
	got.Labels = pq.StringArray{"x", "y", "z"}
	got.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, repo.Update(ctx, got))

	afterUpdate, err := repo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, afterUpdate)
	assert.Equal(t, pq.StringArray{"x", "y", "z"}, afterUpdate.Labels, "labels set via Update must round-trip through GetByID, not come back empty")

	// Clearing labels back to empty must persist as an empty array, not error or no-op.
	afterUpdate.Labels = pq.StringArray{}
	afterUpdate.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, repo.Update(ctx, afterUpdate))

	afterClear, err := repo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, afterClear)
	assert.Empty(t, afterClear.Labels, "clearing labels via Update must persist as empty, not silently keep the old set")
}

// TestTaskRepo_Create_ReviewerRoundTrip is the repository-level regression test
// for the reviewer_id/reviewer_type columns missing from Create's INSERT: a
// service-layer test with a mocked repo cannot see a real INSERT silently
// dropping columns, which is exactly how this shipped unnoticed.
func TestTaskRepo_Create_ReviewerRoundTrip(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	repo := NewTaskRepo(db)
	ctx := context.Background()

	reviewerID := uuid.New()
	reviewerType := domain.AssigneeTypeUser
	task := &domain.Task{
		ID:            uuid.New(),
		ProjectID:     proj.ID,
		StatusID:      status.ID,
		Title:         "Task with reviewer set at creation",
		AssigneeType:  domain.AssigneeTypeUnassigned,
		Priority:      domain.PriorityMedium,
		ReviewerID:    &reviewerID,
		ReviewerType:  &reviewerType,
		CreatedBy:     uuid.New(),
		CreatedByType: domain.ActorTypeUser,
		CreatedAt:     time.Now().UTC().Truncate(time.Microsecond),
		UpdatedAt:     time.Now().UTC().Truncate(time.Microsecond),
	}
	require.NoError(t, repo.Create(ctx, task))

	got, err := repo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.ReviewerID, "reviewer_id must survive Create, not just Update")
	assert.Equal(t, reviewerID, *got.ReviewerID)
	require.NotNil(t, got.ReviewerType)
	assert.Equal(t, domain.AssigneeTypeUser, *got.ReviewerType)

	// Same check for an agent-typed reviewer (AC2).
	agentTask := &domain.Task{
		ID:            uuid.New(),
		ProjectID:     proj.ID,
		StatusID:      status.ID,
		Title:         "Task with agent reviewer set at creation",
		AssigneeType:  domain.AssigneeTypeUnassigned,
		Priority:      domain.PriorityMedium,
		ReviewerID:    &reviewerID,
		ReviewerType:  func() *domain.AssigneeType { a := domain.AssigneeTypeAgent; return &a }(),
		CreatedBy:     uuid.New(),
		CreatedByType: domain.ActorTypeUser,
		CreatedAt:     time.Now().UTC().Truncate(time.Microsecond),
		UpdatedAt:     time.Now().UTC().Truncate(time.Microsecond),
	}
	require.NoError(t, repo.Create(ctx, agentTask))

	gotAgent, err := repo.GetByID(ctx, agentTask.ID)
	require.NoError(t, err)
	require.NotNil(t, gotAgent)
	require.NotNil(t, gotAgent.ReviewerID)
	assert.Equal(t, reviewerID, *gotAgent.ReviewerID)
	require.NotNil(t, gotAgent.ReviewerType)
	assert.Equal(t, domain.AssigneeTypeAgent, *gotAgent.ReviewerType)
}

// TestTaskRepo_Create_NoReviewerIsNull verifies the common no-reviewer path
// still inserts cleanly with both columns NULL — a naive fix (e.g. defaulting
// reviewer_type to the empty string or to "unassigned") would either break
// every task creation with an enum-mismatch error or desync the two columns.
func TestTaskRepo_Create_NoReviewerIsNull(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	repo := NewTaskRepo(db)
	ctx := context.Background()

	task := &domain.Task{
		ID:            uuid.New(),
		ProjectID:     proj.ID,
		StatusID:      status.ID,
		Title:         "Task with no reviewer",
		AssigneeType:  domain.AssigneeTypeUnassigned,
		Priority:      domain.PriorityMedium,
		CreatedBy:     uuid.New(),
		CreatedByType: domain.ActorTypeUser,
		CreatedAt:     time.Now().UTC().Truncate(time.Microsecond),
		UpdatedAt:     time.Now().UTC().Truncate(time.Microsecond),
	}
	require.NoError(t, repo.Create(ctx, task))

	got, err := repo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Nil(t, got.ReviewerID)
	assert.Nil(t, got.ReviewerType)
}

func TestTaskRepo_Update_PreReviewAssigneeRoundTrip(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	repo := NewTaskRepo(db)
	ctx := context.Background()

	builderID := uuid.New()
	task := &domain.Task{
		ID:            uuid.New(),
		ProjectID:     proj.ID,
		StatusID:      status.ID,
		Title:         "Pre-review assignee stash",
		AssigneeType:  domain.AssigneeTypeUnassigned,
		Priority:      domain.PriorityMedium,
		CreatedBy:     uuid.New(),
		CreatedByType: domain.ActorTypeUser,
		CreatedAt:     time.Now().UTC().Truncate(time.Microsecond),
		UpdatedAt:     time.Now().UTC().Truncate(time.Microsecond),
	}
	require.NoError(t, repo.Create(ctx, task))

	// Stash a pre-review assignee, mirroring applyReviewAssignee.
	builderType := domain.AssigneeTypeAgent
	task.PreReviewAssigneeID = &builderID
	task.PreReviewAssigneeType = &builderType
	task.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, repo.Update(ctx, task))

	got, err := repo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.PreReviewAssigneeID)
	assert.Equal(t, builderID, *got.PreReviewAssigneeID)
	require.NotNil(t, got.PreReviewAssigneeType)
	assert.Equal(t, domain.AssigneeTypeAgent, *got.PreReviewAssigneeType)

	// Clearing the stash (restorePreReviewAssignee's cleanup) must persist as NULL, not stick.
	task.PreReviewAssigneeID = nil
	task.PreReviewAssigneeType = nil
	task.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, repo.Update(ctx, task))

	got, err = repo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Nil(t, got.PreReviewAssigneeID)
	assert.Nil(t, got.PreReviewAssigneeType)
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

// ---------------------------------------------------------------------------
// NULL avatar_url regression
// ---------------------------------------------------------------------------

// TestNullAvatarURL inserts a user row with avatar_url = NULL directly (bypassing
// the Create path which always writes a value) and asserts that GetByID, GetByEmail,
// SearchAddableUsers, and WorkspaceMemberRepo.List all return "" instead of failing with
// "converting NULL to string is unsupported".
func TestNullAvatarURL(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	wsID := uuid.New()
	_, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug, owner_id, settings, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		wsID, "Avatar NULL Test WS", "navtws-"+uuid.New().String()[:6], uuid.New(),
		json.RawMessage(`{}`), time.Now().UTC(), time.Now().UTC(),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM workspace_members WHERE workspace_id = $1", wsID)
		_, _ = db.ExecContext(ctx, "DELETE FROM workspaces WHERE id = $1", wsID)
	})

	userID := uuid.New()
	email := "nullavatar-" + uuid.New().String()[:8] + "@test.example"
	username := "navu" + uuid.New().String()[:8]
	_, err = db.ExecContext(ctx,
		`INSERT INTO users (id, email, password_hash, display_name, username, avatar_url, is_active, created_at, updated_at)
		 VALUES ($1, $2, 'testhash', 'Null Avatar User', $3, NULL, true, $4, $5)`,
		userID, email, username, time.Now().UTC(), time.Now().UTC(),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM users WHERE id = $1", userID)
	})

	_, err = db.ExecContext(ctx,
		`INSERT INTO workspace_members (id, workspace_id, user_id, role, invited_by, created_at, updated_at)
		 VALUES ($1,$2,$3,'member',NULL,$4,$5)`,
		uuid.New(), wsID, userID, time.Now().UTC(), time.Now().UTC(),
	)
	require.NoError(t, err)

	userRepo := NewUserRepo(db)
	memberRepo := NewWorkspaceMemberRepo(db)

	t.Run("GetByEmail", func(t *testing.T) {
		u, err := userRepo.GetByEmail(ctx, email)
		require.NoError(t, err)
		require.NotNil(t, u)
		assert.Equal(t, "", u.AvatarURL)
	})

	t.Run("GetByID", func(t *testing.T) {
		u, err := userRepo.GetByID(ctx, userID)
		require.NoError(t, err)
		require.NotNil(t, u)
		assert.Equal(t, "", u.AvatarURL)
	})

	t.Run("SearchAddableUsers", func(t *testing.T) {
		// Exact address, which resolves regardless of who is asking.
		users, err := userRepo.SearchAddableUsers(ctx, uuid.Nil, email, 10)
		require.NoError(t, err)
		found := false
		for _, u := range users {
			if u.ID == userID {
				found = true
				assert.Equal(t, "", u.AvatarURL)
			}
		}
		assert.True(t, found, "expected to find null-avatar user in search results")
	})

	t.Run("WorkspaceMember_List", func(t *testing.T) {
		members, err := memberRepo.List(ctx, wsID)
		require.NoError(t, err)
		require.Len(t, members, 1)
		assert.Equal(t, "", members[0].User.AvatarURL)
	})
}

// TestTaskRepo_ConcurrentCreate_NoTaskNumberConflict verifies that concurrent
// task creation in the same project never produces a duplicate task_number.
// This is the regression test for the recurring-scheduler dup-key race where
// the pg_advisory_xact_lock CTE was unreferenced and could be dropped by the
// PostgreSQL optimizer, letting two goroutines read the same MAX(task_number).
func TestTaskRepo_ConcurrentCreate_NoTaskNumberConflict(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	repo := NewTaskRepo(db)
	ctx := context.Background()

	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup

	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			task := &domain.Task{
				ID:            uuid.New(),
				ProjectID:     proj.ID,
				StatusID:      status.ID,
				Title:         fmt.Sprintf("concurrent task %d", i),
				AssigneeType:  domain.AssigneeTypeUnassigned,
				Priority:      domain.PriorityMedium,
				CreatedBy:     uuid.New(),
				CreatedByType: domain.ActorTypeSystem,
				CreatedAt:     time.Now().UTC(),
				UpdatedAt:     time.Now().UTC(),
			}
			if err := repo.Create(ctx, task); err != nil {
				errs <- fmt.Errorf("worker %d: %w", i, err)
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent Create failed: %v", err)
	}

	// Verify all tasks have distinct task_numbers.
	var nums []int
	err := db.SelectContext(ctx, &nums,
		`SELECT task_number FROM tasks WHERE project_id = $1 ORDER BY task_number`, proj.ID)
	require.NoError(t, err)
	require.Len(t, nums, workers)
	for i, n := range nums {
		assert.Equal(t, i+1, n, "task_numbers must be consecutive and unique")
	}
}
