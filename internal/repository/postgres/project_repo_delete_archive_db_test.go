package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// Task #ddd219f4: a project Pavel archived and then deleted stayed in his
// sidebar, because DELETE only archived and the list never filtered the
// archive. These run ProjectRepo against a live Postgres — what a mock can't
// show is that the rows really are gone from the reads that matter.
//
// Untagged — same convention as the other *_db_test.go files in this
// package. Skips when no DB is reachable.

type projectDeleteFixture struct {
	db        *sqlx.DB
	repo      *ProjectRepo
	workspace uuid.UUID
	project   *domain.Project
}

func newProjectDeleteFixture(t *testing.T) projectDeleteFixture {
	t.Helper()
	db := slugRenameTestDB(t)
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	ws := &domain.Workspace{ID: uuid.New(), Name: "pd-ws", Slug: "pd-ws-" + suffix, OwnerID: slugRenameOwner(t, db)}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, ws))

	repo := NewProjectRepo(db)
	proj := &domain.Project{
		ID: uuid.New(), WorkspaceID: ws.ID, Name: "pd-proj",
		Slug: "pd-proj-" + suffix, DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	require.NoError(t, repo.Create(ctx, proj))

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM tasks WHERE project_id IN (SELECT id FROM projects WHERE workspace_id = $1)`, ws.ID)
		_, _ = db.Exec(`DELETE FROM task_statuses WHERE project_id IN (SELECT id FROM projects WHERE workspace_id = $1)`, ws.ID)
		_, _ = db.Exec(`DELETE FROM documents WHERE project_id IN (SELECT id FROM projects WHERE workspace_id = $1)`, ws.ID)
		_, _ = db.Exec(`DELETE FROM projects WHERE workspace_id = $1`, ws.ID)
		_, _ = db.Exec(`DELETE FROM workspaces WHERE id = $1`, ws.ID)
	})
	return projectDeleteFixture{db: db, repo: repo, workspace: ws.ID, project: proj}
}

func (f projectDeleteFixture) listIDs(t *testing.T, archived *bool) []uuid.UUID {
	t.Helper()
	page, err := f.repo.List(context.Background(), f.workspace, repository.ProjectFilter{IsArchived: archived}, pagination.Params{})
	require.NoError(t, err)
	ids := make([]uuid.UUID, 0, len(page.Items))
	for _, p := range page.Items {
		ids = append(ids, p.ID)
	}
	return ids
}

func TestProjectRepo_Delete_RemovesFromListAndCascades(t *testing.T) {
	f := newProjectDeleteFixture(t)
	ctx := context.Background()

	statusID := uuid.New()
	_, err := f.db.Exec(`INSERT INTO task_statuses (id, project_id, name, slug, category, position)
		VALUES ($1, $2, 'Todo', 'todo', 'todo', 1)`, statusID, f.project.ID)
	require.NoError(t, err)
	taskID := uuid.New()
	_, err = f.db.Exec(`INSERT INTO tasks (id, project_id, status_id, title, task_number, created_by, position)
		VALUES ($1, $2, $3, 'orphan-to-be', 1, $4, 1)`, taskID, f.project.ID, statusID, uuid.New())
	require.NoError(t, err)
	docID := uuid.New()
	_, err = f.db.Exec(`INSERT INTO documents (id, project_id, title, slug, storage_key, created_by, created_by_type)
		VALUES ($1, $2, 'doc', $3, $4, $5, 'agent')`,
		docID, f.project.ID, "doc-"+docID.String()[:8], "documents/"+docID.String()+".md", uuid.New())
	require.NoError(t, err)

	require.Contains(t, f.listIDs(t, nil), f.project.ID, "precondition: the live project is listed")

	require.NoError(t, f.repo.Delete(ctx, f.project.ID))

	assert.NotContains(t, f.listIDs(t, nil), f.project.ID, "a deleted project must not be listed")
	got, err := f.repo.GetByID(ctx, f.project.ID)
	require.NoError(t, err)
	assert.Nil(t, got, "a deleted project must not be readable by id")

	var taskDeleted, docDeleted bool
	require.NoError(t, f.db.Get(&taskDeleted, `SELECT deleted_at IS NOT NULL FROM tasks WHERE id = $1`, taskID))
	require.NoError(t, f.db.Get(&docDeleted, `SELECT deleted_at IS NOT NULL FROM documents WHERE id = $1`, docID))
	assert.True(t, taskDeleted, "the project's tasks must be soft-deleted with it")
	assert.True(t, docDeleted, "the project's documents must be soft-deleted with it")

	// The slug is freed: a new project can take it in the same workspace.
	again := &domain.Project{
		ID: uuid.New(), WorkspaceID: f.workspace, Name: "pd-proj again",
		Slug: f.project.Slug, DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	require.NoError(t, f.repo.Create(ctx, again), "a deleted project's slug must be reusable")

	// A second delete of the same row is a 404, not a silent success.
	assert.Error(t, f.repo.Delete(ctx, f.project.ID))
}

func TestProjectRepo_Delete_LongSlugRenameSatisfiesCheck(t *testing.T) {
	f := newProjectDeleteFixture(t)
	ctx := context.Background()

	long := &domain.Project{
		ID: uuid.New(), WorkspaceID: f.workspace, Name: "long",
		DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	long.Slug = "l" + uuid.New().String()[:8]
	for len(long.Slug) < 100 {
		long.Slug += "x"
	}
	require.NoError(t, f.repo.Create(ctx, long))
	require.NoError(t, f.repo.Delete(ctx, long.ID), "renaming a 100-char slug must still pass chk_projects_slug_format")
}

func TestProjectRepo_ArchivedFilter(t *testing.T) {
	f := newProjectDeleteFixture(t)
	ctx := context.Background()

	f.project.IsArchived = true
	require.NoError(t, f.repo.Update(ctx, f.project))

	no, yes := false, true
	assert.NotContains(t, f.listIDs(t, &no), f.project.ID, "an archived project must not be in the active list")
	assert.Contains(t, f.listIDs(t, &yes), f.project.ID, "an archived project must be in the archived list")
}
