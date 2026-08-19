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

// No //go:build integration tag — same convention as the other *_db_test.go
// files here: skip gracefully when no Postgres is reachable.
//
// These cases run against a real database on purpose. GetByPathInWorkspace is a
// recursive CTE, and the things most likely to be wrong about it — whether the
// workspace join actually filters, whether the recursion steps in the right
// order, whether soft-deleted rows drop out at every level, whether the depth it
// reports is the depth it walked — are properties of the SQL. An in-memory fake
// re-implements the walk in Go and would agree with a broken query.

func documentPathTestDB(t *testing.T) *sqlx.DB {
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

// docPathFixture is one workspace holding one project, with a helper to grow a
// document tree in it.
type docPathFixture struct {
	db        *sqlx.DB
	repo      *DocumentRepo
	wsID      uuid.UUID
	projectID uuid.UUID
}

func newDocPathFixture(t *testing.T) *docPathFixture {
	t.Helper()
	db := documentPathTestDB(t)

	wsID, projectID, owner := uuid.New(), uuid.New(), uuid.New()
	suffix := wsID.String()[:8]

	_, err := db.Exec(
		`INSERT INTO workspaces (id, name, slug, owner_id) VALUES ($1, 'Doc Path Test', $2, $3)`,
		wsID, "docpath-ws-"+suffix, owner,
	)
	require.NoError(t, err)
	// The workspace cascade takes the project and every document with it.
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id = $1`, wsID) })

	_, err = db.Exec(
		`INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1, $2, 'Doc Path Test', $3)`,
		projectID, wsID, "docpath-proj-"+suffix,
	)
	require.NoError(t, err)

	return &docPathFixture{db: db, repo: NewDocumentRepo(db), wsID: wsID, projectID: projectID}
}

// doc inserts a document and returns its id.
func (f *docPathFixture) doc(t *testing.T, slug string, parent *uuid.UUID) uuid.UUID {
	t.Helper()
	return f.docIn(t, f.projectID, slug, parent)
}

func (f *docPathFixture) docIn(t *testing.T, projectID uuid.UUID, slug string, parent *uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	now := time.Now().UTC()
	err := f.repo.Create(context.Background(), &domain.Document{
		ID:            id,
		ProjectID:     projectID,
		ParentID:      parent,
		Slug:          slug,
		Title:         slug,
		StorageKey:    "documents/" + projectID.String() + "/" + id.String() + ".md",
		CreatedBy:     uuid.New(),
		CreatedByType: domain.ActorTypeUser,
		CreatedAt:     now,
		UpdatedAt:     now,
	})
	require.NoError(t, err)
	return id
}

func TestDocumentRepo_GetByPathInWorkspace(t *testing.T) {
	f := newDocPathFixture(t)
	ctx := context.Background()

	arch := f.doc(t, "architecture", nil)
	adr := f.doc(t, "adr", &arch)
	adr004 := f.doc(t, "adr-004", &adr)

	doc, matched, err := f.repo.GetByPathInWorkspace(ctx, f.projectID, f.wsID,
		[]string{"architecture", "adr", "adr-004"})
	require.NoError(t, err)
	require.NotNil(t, doc)
	assert.Equal(t, adr004, doc.ID)
	assert.Equal(t, 3, matched)
	assert.Equal(t, "adr-004", doc.Slug)

	// A prefix resolves to the ancestor it names, not to the deepest leaf under
	// it: the walk stops where the caller stopped asking.
	doc, matched, err = f.repo.GetByPathInWorkspace(ctx, f.projectID, f.wsID,
		[]string{"architecture", "adr"})
	require.NoError(t, err)
	require.NotNil(t, doc)
	assert.Equal(t, adr, doc.ID)
	assert.Equal(t, 2, matched)
}

// The partial match is the whole reason this method returns a depth. A caller
// that only learns "not found" cannot tell a typo in the last segment from the
// wrong project.
func TestDocumentRepo_GetByPathInWorkspace_ReportsWhereThePathBroke(t *testing.T) {
	f := newDocPathFixture(t)
	ctx := context.Background()

	arch := f.doc(t, "architecture", nil)
	adr := f.doc(t, "adr", &arch)

	doc, matched, err := f.repo.GetByPathInWorkspace(ctx, f.projectID, f.wsID,
		[]string{"architecture", "adr", "adr-999"})
	require.NoError(t, err)
	require.NotNil(t, doc, "the walk reached `adr` and says so")
	assert.Equal(t, adr, doc.ID)
	assert.Equal(t, 2, matched)

	// Nothing at the root: no row, no error. Asking for a path that does not
	// exist is a normal request, not a failure.
	doc, matched, err = f.repo.GetByPathInWorkspace(ctx, f.projectID, f.wsID,
		[]string{"nope", "adr"})
	require.NoError(t, err)
	assert.Nil(t, doc)
	assert.Zero(t, matched)
}

// A path is a chain, not a set of slugs that happen to exist in the project.
func TestDocumentRepo_GetByPathInWorkspace_OrderMatters(t *testing.T) {
	f := newDocPathFixture(t)
	ctx := context.Background()

	arch := f.doc(t, "architecture", nil)
	f.doc(t, "adr", &arch)

	doc, matched, err := f.repo.GetByPathInWorkspace(ctx, f.projectID, f.wsID,
		[]string{"adr", "architecture"})
	require.NoError(t, err)
	assert.Nil(t, doc, "`adr` is not a root document, so the walk cannot start there")
	assert.Zero(t, matched)
}

// The SQL analogue of duplicate-heading disambiguation: the same slug under two
// different parents is two different documents, and the path says which.
func TestDocumentRepo_GetByPathInWorkspace_SameSlugUnderDifferentParents(t *testing.T) {
	f := newDocPathFixture(t)
	ctx := context.Background()

	install := f.doc(t, "installation", nil)
	configure := f.doc(t, "configuration", nil)
	installLinux := f.doc(t, "linux", &install)
	configureLinux := f.doc(t, "linux", &configure)
	require.NotEqual(t, installLinux, configureLinux)

	doc, matched, err := f.repo.GetByPathInWorkspace(ctx, f.projectID, f.wsID,
		[]string{"installation", "linux"})
	require.NoError(t, err)
	require.NotNil(t, doc)
	assert.Equal(t, installLinux, doc.ID)
	assert.Equal(t, 2, matched)

	doc, matched, err = f.repo.GetByPathInWorkspace(ctx, f.projectID, f.wsID,
		[]string{"configuration", "linux"})
	require.NoError(t, err)
	require.NotNil(t, doc)
	assert.Equal(t, configureLinux, doc.ID)
	assert.Equal(t, 2, matched)
}

// Non-ASCII slugs are stored and matched byte-for-byte. The slug column is not
// slugified by this layer, so a Cyrillic slug has to survive the array
// round-trip through pq.Array and the `= ($3::text[])[n]` comparison.
func TestDocumentRepo_GetByPathInWorkspace_NonASCIISlugs(t *testing.T) {
	f := newDocPathFixture(t)
	ctx := context.Background()

	guide := f.doc(t, "руководство", nil)
	section := f.doc(t, "установка", &guide)

	doc, matched, err := f.repo.GetByPathInWorkspace(ctx, f.projectID, f.wsID,
		[]string{"руководство", "установка"})
	require.NoError(t, err)
	require.NotNil(t, doc)
	assert.Equal(t, section.String(), doc.ID.String())
	assert.Equal(t, 2, matched)
}

// A soft-deleted document is gone from the path at every level, so a live child
// under a deleted parent stays unreachable rather than becoming a side door back
// into a deleted subtree.
func TestDocumentRepo_GetByPathInWorkspace_SkipsSoftDeleted(t *testing.T) {
	f := newDocPathFixture(t)
	ctx := context.Background()

	arch := f.doc(t, "architecture", nil)
	adr := f.doc(t, "adr", &arch)
	f.doc(t, "adr-004", &adr)

	// Delete the middle of the chain only — the leaf row stays live.
	_, err := f.db.Exec(
		`UPDATE documents SET deleted_at = NOW() WHERE id = $1`, adr)
	require.NoError(t, err)

	doc, matched, err := f.repo.GetByPathInWorkspace(ctx, f.projectID, f.wsID,
		[]string{"architecture", "adr", "adr-004"})
	require.NoError(t, err)
	require.NotNil(t, doc)
	assert.Equal(t, arch, doc.ID, "the walk stops at the last live document")
	assert.Equal(t, 1, matched)
}

// Another tenant's project does not resolve, whatever the caller names. The
// workspace is joined in the anchor of the recursion, so a wrong workspace kills
// the walk before the first segment.
func TestDocumentRepo_GetByPathInWorkspace_RefusesAnotherWorkspace(t *testing.T) {
	f := newDocPathFixture(t)
	ctx := context.Background()

	arch := f.doc(t, "architecture", nil)
	f.doc(t, "adr", &arch)

	doc, matched, err := f.repo.GetByPathInWorkspace(ctx, f.projectID, uuid.New(),
		[]string{"architecture", "adr"})
	require.NoError(t, err)
	assert.Nil(t, doc, "a document resolved for a workspace that does not own it")
	assert.Zero(t, matched)
}

// Two projects in the same workspace may hold the same slugs; the walk must stay
// inside the project it was given, at every recursion step and not only the
// first.
func TestDocumentRepo_GetByPathInWorkspace_StaysInsideTheProject(t *testing.T) {
	f := newDocPathFixture(t)
	ctx := context.Background()

	otherProject := uuid.New()
	_, err := f.db.Exec(
		`INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1, $2, 'Other', $3)`,
		otherProject, f.wsID, "docpath-other-"+otherProject.String()[:8],
	)
	require.NoError(t, err)

	arch := f.doc(t, "architecture", nil)
	otherArch := f.docIn(t, otherProject, "architecture", nil)
	f.docIn(t, otherProject, "adr", &otherArch)

	doc, matched, err := f.repo.GetByPathInWorkspace(ctx, f.projectID, f.wsID,
		[]string{"architecture", "adr"})
	require.NoError(t, err)
	require.NotNil(t, doc)
	assert.Equal(t, arch, doc.ID, "the walk crossed into another project's subtree")
	assert.Equal(t, 1, matched, "`adr` exists, but not in this project")
}

// Order has to be enforced at every step, not only at the root. The previous
// test starts the walk at a slug that is not a root document, so it never
// exercises the recursive step at all — a step matching "any of these segments"
// would still pass it. Here the first segment is right and the rest are
// transposed, which is the case that separates "walks the chain" from "collects
// slugs that appear somewhere below".
func TestDocumentRepo_GetByPathInWorkspace_OrderMattersBelowTheRoot(t *testing.T) {
	f := newDocPathFixture(t)
	ctx := context.Background()

	arch := f.doc(t, "architecture", nil)
	adr := f.doc(t, "adr", &arch)
	f.doc(t, "adr-004", &adr)

	doc, matched, err := f.repo.GetByPathInWorkspace(ctx, f.projectID, f.wsID,
		[]string{"architecture", "adr-004", "adr"})
	require.NoError(t, err)
	require.NotNil(t, doc)
	assert.Equal(t, arch, doc.ID, "the walk matched segments out of order")
	assert.Equal(t, 1, matched,
		"`adr-004` is not a child of `architecture`, so the path stops at depth 1")
}

// The project filter on the RECURSIVE step is defence-in-depth, and this is the
// row that shows it is load-bearing rather than redundant with the parent_id
// join: a document whose parent lives in another project. Nothing in the schema
// forbids it — only requireParentInProject in the service does — so the query
// must not rely on the tree being well-formed.
func TestDocumentRepo_GetByPathInWorkspace_RefusesACrossProjectParent(t *testing.T) {
	f := newDocPathFixture(t)
	ctx := context.Background()

	otherProject := uuid.New()
	_, err := f.db.Exec(
		`INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1, $2, 'Other', $3)`,
		otherProject, f.wsID, "docpath-xp-"+otherProject.String()[:8],
	)
	require.NoError(t, err)

	// A root in the other project, with a child that CLAIMS to belong to ours.
	otherArch := f.docIn(t, otherProject, "architecture", nil)
	f.docIn(t, f.projectID, "adr", &otherArch)

	doc, matched, err := f.repo.GetByPathInWorkspace(ctx, otherProject, f.wsID,
		[]string{"architecture", "adr"})
	require.NoError(t, err)
	require.NotNil(t, doc)
	assert.Equal(t, otherArch, doc.ID,
		"the walk descended into a document belonging to a different project")
	assert.Equal(t, 1, matched)
}

func TestDocumentRepo_GetByPathInWorkspace_EmptyPath(t *testing.T) {
	f := newDocPathFixture(t)

	doc, matched, err := f.repo.GetByPathInWorkspace(context.Background(), f.projectID, f.wsID, nil)
	require.NoError(t, err)
	assert.Nil(t, doc)
	assert.Zero(t, matched)
}
