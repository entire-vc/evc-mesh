package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Document paths that survive a move (#ed10e6aa), exercised through the real
// service on a real Postgres: the alias write lives in the same transaction as
// the move and the fallback lookups are recursive CTEs, neither of which an
// in-memory repository can prove. Object storage is the only fake — it is the
// external boundary, and these tests never look at a body.
//
// Skips, not fails, when no Postgres is reachable — same convention as the
// neighbouring *_db_test.go files.

type pathAliasEnv struct {
	svc       *documentService
	db        *sqlx.DB
	projectID uuid.UUID
	wsID      uuid.UUID
	actor     uuid.UUID
}

func newPathAliasEnv(t *testing.T) *pathAliasEnv {
	t.Helper()
	db := versionTestDB(t)
	ws := versionTestWorkspace(t, db)
	ctx := context.Background()
	proj := &domain.Project{
		ID: uuid.New(), WorkspaceID: ws.ID, Name: "alias-proj",
		Slug: "alias-proj-" + uuid.New().String()[:8], DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	projectRepo := postgres.NewProjectRepo(db)
	require.NoError(t, projectRepo.Create(ctx, proj))
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM document_path_aliases WHERE project_id = $1`, proj.ID)
		_, _ = db.Exec(`DELETE FROM documents WHERE project_id = $1`, proj.ID)
		_, _ = db.Exec(`DELETE FROM projects WHERE id = $1`, proj.ID)
		_, _ = db.Exec(`DELETE FROM workspaces WHERE id = $1`, ws.ID)
	})

	// mock: external boundary — object storage (S3).
	store := NewMockStorageClient()
	svc := NewDocumentService(postgres.NewDocumentRepo(db), store, projectRepo,
		postgres.NewDocumentCommentRepo(db)).(*documentService)
	return &pathAliasEnv{svc: svc, db: db, projectID: proj.ID, wsID: ws.ID, actor: uuid.New()}
}

func (e *pathAliasEnv) create(t *testing.T, parent *uuid.UUID, slug string) *domain.Document {
	t.Helper()
	doc, err := e.svc.Create(context.Background(), CreateDocumentInput{
		ProjectID: e.projectID, ParentID: parent, Title: slug, Slug: slug, Body: "# " + slug + "\n",
		CreatedBy: e.actor, CreatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)
	return doc
}

func (e *pathAliasEnv) move(t *testing.T, doc *domain.Document, parent *uuid.UUID) {
	t.Helper()
	in := UpdateDocumentInput{ParentID: parent, ClearParent: parent == nil,
		UpdatedBy: e.actor, UpdatedByType: domain.ActorTypeUser}
	_, err := e.svc.Update(context.Background(), doc.ID, e.wsID, in)
	require.NoError(t, err)
}

func (e *pathAliasEnv) aliasCount(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, e.db.Get(&n, `SELECT count(*) FROM document_path_aliases WHERE project_id = $1`, e.projectID))
	return n
}

func TestDocumentPathAlias_MovedDocumentStillResolvesByOldPathSlugAndID(t *testing.T) {
	e := newPathAliasEnv(t)
	ctx := context.Background()
	drafts := e.create(t, nil, "drafts")
	specs := e.create(t, nil, "specs")
	doc := e.create(t, &drafts.ID, "threat-model")

	e.move(t, doc, &specs.ID)

	byOldPath, err := e.svc.GetByPath(ctx, e.projectID, "drafts/threat-model")
	require.NoError(t, err, "the path the document was moved away from must still resolve")
	assert.Equal(t, doc.ID, byOldPath.ID)
	assert.Equal(t, "specs/threat-model", byOldPath.MovedTo, "moved_to names where it lives now")

	bySlug, err := e.svc.GetByPath(ctx, e.projectID, "threat-model")
	require.NoError(t, err, "a bare slug names the single live document with it, at any depth")
	assert.Equal(t, doc.ID, bySlug.ID)
	assert.Equal(t, "specs/threat-model", bySlug.MovedTo)

	byID, err := e.svc.GetByIDInWorkspace(ctx, doc.ID, e.wsID)
	require.NoError(t, err)
	assert.Equal(t, doc.ID, byID.ID)

	exact, err := e.svc.GetByPath(ctx, e.projectID, "specs/threat-model")
	require.NoError(t, err)
	assert.Equal(t, doc.ID, exact.ID)
	assert.Empty(t, exact.MovedTo, "an exact live hit is not a redirect")
}

func TestDocumentPathAlias_MovingAFolderKeepsItsChildrensOldPaths(t *testing.T) {
	e := newPathAliasEnv(t)
	ctx := context.Background()
	reports := e.create(t, nil, "reports")
	q1 := e.create(t, &reports.ID, "q1")
	details := e.create(t, &q1.ID, "details")
	archive := e.create(t, nil, "archive")

	e.move(t, reports, &archive.ID)

	for path, want := range map[string]struct {
		id      uuid.UUID
		movedTo string
	}{
		"reports":            {reports.ID, "archive/reports"},
		"reports/q1":         {q1.ID, "archive/reports/q1"},
		"reports/q1/details": {details.ID, "archive/reports/q1/details"},
	} {
		got, err := e.svc.GetByPath(ctx, e.projectID, path)
		require.NoError(t, err, path)
		assert.Equal(t, want.id, got.ID, path)
		assert.Equal(t, want.movedTo, got.MovedTo, path)
	}
}

func TestDocumentPathAlias_AmbiguousBareSlugListsEveryCandidate(t *testing.T) {
	e := newPathAliasEnv(t)
	a := e.create(t, nil, "alpha")
	b := e.create(t, nil, "beta")
	e.create(t, &a.ID, "notes")
	e.create(t, &b.ID, "notes")

	_, err := e.svc.GetByPath(context.Background(), e.projectID, "notes")

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 409, apiErr.StatusCode(), "two candidates is not a guess to make")
	assert.Contains(t, apiErr.Details, "alpha/notes")
	assert.Contains(t, apiErr.Details, "beta/notes")
}

func TestDocumentPathAlias_DeletedDocumentsAliasDoesNotResolve(t *testing.T) {
	e := newPathAliasEnv(t)
	ctx := context.Background()
	from := e.create(t, nil, "from")
	to := e.create(t, nil, "to")
	doc := e.create(t, &from.ID, "gone")
	e.move(t, doc, &to.ID)
	require.Equal(t, 1, e.aliasCount(t), "the move recorded the old path")

	require.NoError(t, e.svc.Delete(ctx, doc.ID, e.wsID, e.actor, domain.ActorTypeUser))

	for _, path := range []string{"from/gone", "gone"} {
		_, err := e.svc.GetByPath(ctx, e.projectID, path)
		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr, path)
		assert.Equal(t, 404, apiErr.StatusCode(), path)
	}
}

func TestDocumentPathAlias_LiveDocumentAtTheOldPathWinsOverTheAlias(t *testing.T) {
	e := newPathAliasEnv(t)
	from := e.create(t, nil, "from")
	to := e.create(t, nil, "to")
	moved := e.create(t, &from.ID, "page")
	e.move(t, moved, &to.ID)

	newcomer := e.create(t, &from.ID, "page")

	got, err := e.svc.GetByPath(context.Background(), e.projectID, "from/page")
	require.NoError(t, err)
	assert.Equal(t, newcomer.ID, got.ID, "a live document at a path always wins over an alias")
	assert.Empty(t, got.MovedTo)
}

func TestDocumentPathAlias_UpdateThatDoesNotMoveRecordsNothing(t *testing.T) {
	e := newPathAliasEnv(t)
	folder := e.create(t, nil, "folder")
	doc := e.create(t, &folder.ID, "page")

	pos, title := 7, "Renamed"
	_, err := e.svc.Update(context.Background(), doc.ID, e.wsID, UpdateDocumentInput{
		ParentID: &folder.ID, Position: &pos, Title: &title,
		UpdatedBy: e.actor, UpdatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)

	assert.Zero(t, e.aliasCount(t), "same parent, new title and position: the path did not move")
}

func TestDocumentPathAlias_CreateRefusesSlashInSlug(t *testing.T) {
	e := newPathAliasEnv(t)

	_, err := e.svc.Create(context.Background(), CreateDocumentInput{
		ProjectID: e.projectID, Title: "X", Slug: "reports/x", Body: "x",
		CreatedBy: e.actor, CreatedByType: domain.ActorTypeUser,
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Contains(t, apiErr.Validation["slug"], `slug must not contain "/"`)
	assert.Contains(t, apiErr.Validation["slug"], "parent_id")
	assert.Contains(t, apiErr.Validation["slug"], `"reports/x"`)

	var n int
	require.NoError(t, e.db.Get(&n, `SELECT count(*) FROM documents WHERE project_id = $1`, e.projectID))
	assert.Zero(t, n, "nothing is created")
}

func (e *pathAliasEnv) rename(doc *domain.Document, slug string) error {
	_, err := e.svc.Update(context.Background(), doc.ID, e.wsID, UpdateDocumentInput{
		Slug: &slug, UpdatedBy: e.actor, UpdatedByType: domain.ActorTypeUser,
	})
	return err
}

func TestDocumentPathAlias_RenamedSlugStillResolvesByOldPathAndID(t *testing.T) {
	e := newPathAliasEnv(t)
	ctx := context.Background()
	specs := e.create(t, nil, "specs")
	doc := e.create(t, &specs.ID, "threat-model")

	require.NoError(t, e.rename(doc, "threat-model-v2"))

	byOldPath, err := e.svc.GetByPath(ctx, e.projectID, "specs/threat-model")
	require.NoError(t, err, "the path before the rename must still resolve")
	assert.Equal(t, doc.ID, byOldPath.ID)
	assert.Equal(t, "specs/threat-model-v2", byOldPath.MovedTo)

	byID, err := e.svc.GetByIDInWorkspace(ctx, doc.ID, e.wsID)
	require.NoError(t, err)
	assert.Equal(t, doc.ID, byID.ID)
	assert.Equal(t, "threat-model-v2", byID.Slug)

	current, err := e.svc.GetByPath(ctx, e.projectID, "specs/threat-model-v2")
	require.NoError(t, err)
	assert.Equal(t, doc.ID, current.ID)
	assert.Empty(t, current.MovedTo, "the new path is a live hit, not a redirect")
}

func TestDocumentPathAlias_RenamingAFolderKeepsDescendantsOldPaths(t *testing.T) {
	e := newPathAliasEnv(t)
	ctx := context.Background()
	notes := e.create(t, nil, "notes")
	q1 := e.create(t, &notes.ID, "q1")
	leaf := e.create(t, &q1.ID, "leaf")

	require.NoError(t, e.rename(notes, "journal"))

	for path, want := range map[string]struct {
		id      uuid.UUID
		movedTo string
	}{
		"notes":         {notes.ID, "journal"},
		"notes/q1":      {q1.ID, "journal/q1"},
		"notes/q1/leaf": {leaf.ID, "journal/q1/leaf"},
	} {
		got, err := e.svc.GetByPath(ctx, e.projectID, path)
		require.NoError(t, err, path)
		assert.Equal(t, want.id, got.ID, path)
		assert.Equal(t, want.movedTo, got.MovedTo, path)
	}
}

func TestDocumentPathAlias_RefusedRenameChangesNothing(t *testing.T) {
	e := newPathAliasEnv(t)
	folder := e.create(t, nil, "folder")
	a := e.create(t, &folder.ID, "alpha")
	e.create(t, &folder.ID, "beta")
	before := e.aliasCount(t)

	require.Error(t, e.rename(a, "x/y"), "a slash is refused")
	err := e.rename(a, "beta")
	require.Error(t, err, "a sibling with that slug exists")
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, 409, ae.Code)

	got, gerr := e.svc.GetByIDInWorkspace(context.Background(), a.ID, e.wsID)
	require.NoError(t, gerr)
	assert.Equal(t, "alpha", got.Slug)
	assert.Equal(t, before, e.aliasCount(t), "no alias rows written by a refused rename")
}
