package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// No //go:build integration tag, deliberately — same reasoning as
// memory_service_collapse_getbykey_db_test.go: CI's untagged `go test ./...`
// runs these against a migrated DATABASE_URL and skip()s rather than fails
// when no database is reachable.
//
// Regression test for #b73171fa: on the update branch (explicit status onto
// an existing (task_id, provider, link_type, external_id)), Upsert's SQL
// never touches id/created_at — real Postgres, not a fake, is what proves the
// RETURNING (xmax = 0) trick actually distinguishes insert from update and
// that the scanned-back id/created_at genuinely match what a fresh GetByID
// returns, not merely what the fakes were told to preserve.

func vcsLinkUpsertTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres at %s, skipping: %v", dsn, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Skipf("Postgres at %s not accepting connections, skipping: %v", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// vcsLinkTestTask creates the minimal workspace/project/status/task chain
// vcs_links.task_id's FK requires.
func vcsLinkTestTask(t *testing.T, db *sqlx.DB) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	ws := &domain.Workspace{ID: uuid.New(), Name: "vcs-link-upsert-ws", Slug: "vcs-link-upsert-" + uuid.New().String()[:8], OwnerID: uuid.New()}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, ws))

	proj := &domain.Project{
		ID: uuid.New(), WorkspaceID: ws.ID, Name: "vcs-link-upsert-proj",
		Slug: "vcs-link-upsert-p-" + uuid.New().String()[:8], DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	require.NoError(t, NewProjectRepo(db).Create(ctx, proj))

	status := &domain.TaskStatus{
		ID: uuid.New(), ProjectID: proj.ID, Name: "Open", Slug: "open",
		Color: "#00FF00", Position: 0, Category: domain.StatusCategoryTodo, IsDefault: true,
	}
	require.NoError(t, NewTaskStatusRepo(db).Create(ctx, status))

	task := &domain.Task{
		ID: uuid.New(), ProjectID: proj.ID, StatusID: status.ID,
		Title: "vcs-link upsert regression task", AssigneeType: domain.AssigneeTypeUnassigned,
		Priority: domain.PriorityMedium, CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
	}
	require.NoError(t, NewTaskRepo(db).Create(ctx, task))

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM tasks WHERE id = $1", task.ID)
		_, _ = db.ExecContext(ctx, "DELETE FROM task_statuses WHERE id = $1", status.ID)
		_, _ = db.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", proj.ID)
		_, _ = db.ExecContext(ctx, "DELETE FROM workspaces WHERE id = $1", ws.ID)
	})

	return task.ID
}

func TestVCSLinkRepo_Upsert_InsertReportsCreatedTrue(t *testing.T) {
	db := vcsLinkUpsertTestDB(t)
	ctx := context.Background()
	repo := NewVCSLinkRepo(db)
	taskID := vcsLinkTestTask(t, db)

	link := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitHub,
		LinkType: domain.VCSLinkTypePR, ExternalID: "40",
		URL:    "https://github.com/entire-vc/evc-mesh-mcp/pull/40",
		Status: domain.VCSLinkStatusOpen, CreatedAt: time.Now(),
	}

	created, err := repo.Upsert(ctx, link)
	require.NoError(t, err)
	assert.True(t, created, "a genuinely new (task,provider,link_type,external_id) must report created=true")
}

// A DB-level failure (here: task_id's FK to tasks(id), violated by a task
// that was never created) must surface as an error with created=false, not a
// false-positive success.
func TestVCSLinkRepo_Upsert_DBErrorReturnsFalseAndError(t *testing.T) {
	db := vcsLinkUpsertTestDB(t)
	ctx := context.Background()
	repo := NewVCSLinkRepo(db)

	link := &domain.VCSLink{
		ID: uuid.New(), TaskID: uuid.New(), Provider: domain.VCSProviderGitHub,
		LinkType: domain.VCSLinkTypePR, ExternalID: "40",
		URL:    "https://github.com/entire-vc/evc-mesh-mcp/pull/40",
		Status: domain.VCSLinkStatusOpen, CreatedAt: time.Now(),
	}

	created, err := repo.Upsert(ctx, link)
	require.Error(t, err, "task_id must satisfy the FK to tasks(id)")
	assert.False(t, created)
}

// The core regression: the update branch must return the row that a
// subsequent GetByID actually finds — not the id/created_at the caller
// happened to generate before discovering the link already existed.
func TestVCSLinkRepo_Upsert_UpdateBranchReturnsActualPersistedRow(t *testing.T) {
	db := vcsLinkUpsertTestDB(t)
	ctx := context.Background()
	repo := NewVCSLinkRepo(db)
	taskID := vcsLinkTestTask(t, db)

	first := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitHub,
		LinkType: domain.VCSLinkTypePR, ExternalID: "40",
		URL:    "https://github.com/entire-vc/evc-mesh-mcp/pull/40",
		Status: domain.VCSLinkStatusOpen, CreatedAt: time.Now().Add(-time.Hour),
	}
	created, err := repo.Upsert(ctx, first)
	require.NoError(t, err)
	require.True(t, created)
	originalID := first.ID
	originalCreatedAt := first.CreatedAt

	// Same (task_id, provider, link_type, external_id), a fresh id/created_at
	// the caller has no way to know will be discarded — exactly what
	// vcsLinkService.Create constructs on every call.
	second := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitHub,
		LinkType: domain.VCSLinkTypePR, ExternalID: "40",
		URL:    "https://github.com/entire-vc/evc-mesh-mcp/pull/40",
		Status: domain.VCSLinkStatusMerged, CreatedAt: time.Now(),
	}
	require.NotEqual(t, originalID, second.ID, "test setup sanity: the caller-generated id must differ from the original")

	created, err = repo.Upsert(ctx, second)
	require.NoError(t, err)

	// RED before the fix: created was true and second.ID/CreatedAt were left
	// as the fresh, never-persisted values generated above.
	assert.False(t, created, "updating an existing row must report created=false")
	assert.Equal(t, originalID, second.ID, "Upsert must mutate the caller's link back to the row's REAL id")
	assert.Equal(t, originalCreatedAt.UTC().Truncate(time.Microsecond), second.CreatedAt.UTC().Truncate(time.Microsecond),
		"Upsert must mutate the caller's link back to the row's REAL created_at")

	// Equality against a fresh, independent GET — not just against what
	// Upsert echoed back — is the actual acceptance bar (#b73171fa AC1).
	stored, err := repo.GetByID(ctx, second.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, originalID, stored.ID)
	assert.Equal(t, domain.VCSLinkStatusMerged, stored.Status, "the update's new fields must still land")

	all, err := repo.ListByTask(ctx, taskID)
	require.NoError(t, err)
	require.Len(t, all, 1, "no second row may exist under the same natural key")
}
