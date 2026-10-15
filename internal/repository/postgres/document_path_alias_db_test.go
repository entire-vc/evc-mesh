package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// The alias write of a move against a live Postgres (#ed10e6aa). The service
// tests cover what the resolver returns; these pin the transactional half: a
// move that is refused must leave no alias behind, and one that lands records
// the paths the subtree had BEFORE it moved.
//
// Skips, not fails, when no Postgres is reachable — same convention as the
// neighbouring *_db_test.go files.

func aliasTestDoc(t *testing.T, repo *DocumentRepo, f sourceFixture, parent *uuid.UUID, slug string) *domain.Document {
	t.Helper()
	id, _, storageKey, createdBy := f.newDocRow()
	now := time.Now().UTC()
	doc := &domain.Document{
		ID: id, ProjectID: f.projectID, ParentID: parent, Slug: slug, Title: slug,
		StorageKey: storageKey, Version: 1, CreatedBy: createdBy, CreatedByType: domain.ActorTypeUser,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, repo.Create(context.Background(), doc))
	return doc
}

func TestDocumentRepo_MoveRecordsOldSubtreePaths(t *testing.T) {
	db := externalSourceTestDB(t)
	repo, f := NewDocumentRepo(db), newSourceFixture(t, db)
	ctx := context.Background()

	folder := aliasTestDoc(t, repo, f, nil, "folder")
	child := aliasTestDoc(t, repo, f, &folder.ID, "child")
	dest := aliasTestDoc(t, repo, f, nil, "dest")

	folder.ParentID = &dest.ID
	_, err := repo.Update(ctx, folder, nil, false)
	require.NoError(t, err)

	got, err := repo.GetByPathAlias(ctx, f.projectID, "folder/child")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, child.ID, got.ID)

	path, err := repo.PathOf(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, "dest/folder/child", path)

	hits, err := repo.FindLiveBySlug(ctx, f.projectID, "child", 10)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Equal(t, repository.DocumentPathHit{ID: child.ID, Path: "dest/folder/child"}, hits[0])
}

func TestDocumentRepo_RefusedMoveLeavesNoAlias(t *testing.T) {
	db := externalSourceTestDB(t)
	repo, f := NewDocumentRepo(db), newSourceFixture(t, db)
	ctx := context.Background()

	doc := aliasTestDoc(t, repo, f, nil, "page")
	dest := aliasTestDoc(t, repo, f, nil, "dest")

	stale := doc.Version - 1
	doc.ParentID = &dest.ID
	_, err := repo.Update(ctx, doc, &stale, false)
	require.ErrorIs(t, err, repository.ErrDocumentVersionMismatch)

	var n int
	require.NoError(t, db.Get(&n, `SELECT count(*) FROM document_path_aliases WHERE project_id = $1`, f.projectID))
	assert.Zero(t, n, "the alias insert rolls back with the refused UPDATE")
}

// A slug rename moves the path of the document and of its whole subtree, so the
// old paths must stay resolvable (card 1fe1793e / e5a0ee89).
func TestDocumentRepo_RenameRecordsOldSubtreePaths(t *testing.T) {
	db := externalSourceTestDB(t)
	repo, f := NewDocumentRepo(db), newSourceFixture(t, db)
	ctx := context.Background()

	folder := aliasTestDoc(t, repo, f, nil, "adrscommit-guard")
	child := aliasTestDoc(t, repo, f, &folder.ID, "child")

	folder.Slug = "adr-commit-guard"
	_, err := repo.Update(ctx, folder, nil, false)
	require.NoError(t, err)

	for old, want := range map[string]uuid.UUID{
		"adrscommit-guard":       folder.ID,
		"adrscommit-guard/child": child.ID,
	} {
		got, gerr := repo.GetByPathAlias(ctx, f.projectID, old)
		require.NoError(t, gerr)
		require.NotNil(t, got, old)
		assert.Equal(t, want, got.ID, old)
	}
	path, err := repo.PathOf(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, "adr-commit-guard/child", path)
}

func TestDocumentRepo_RenameCollisionIsRefusedAndLeavesNoAlias(t *testing.T) {
	db := externalSourceTestDB(t)
	repo, f := NewDocumentRepo(db), newSourceFixture(t, db)
	ctx := context.Background()

	aliasTestDoc(t, repo, f, nil, "taken")
	doc := aliasTestDoc(t, repo, f, nil, "page")

	doc.Slug = "taken"
	_, err := repo.Update(ctx, doc, nil, false)
	require.Error(t, err)

	var n int
	require.NoError(t, db.Get(&n, `SELECT count(*) FROM document_path_aliases WHERE project_id = $1`, f.projectID))
	assert.Zero(t, n, "the alias insert rolls back with the refused rename")
}
