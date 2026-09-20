package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Same convention as the other *_db_test.go files: untagged, runs against the CI
// Postgres, skips locally when none is reachable (testDB requires TEST_DATABASE_URL).

func docIdxFixture(t *testing.T) (repo *DocumentChunkRepo, ws, proj uuid.UUID, mkDoc func(slug string) uuid.UUID) {
	t.Helper()
	db := userRepoTestDB(t)
	ctx := context.Background()
	ws, proj = uuid.New(), uuid.New()
	_, err := db.ExecContext(ctx, `INSERT INTO workspaces (id,name,slug,owner_id) VALUES ($1,'w',$2,$3)`, ws, "w-"+ws.String()[:8], uuid.New())
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO projects (id,workspace_id,name,slug) VALUES ($1,$2,'p',$3)`, proj, ws, "p-"+proj.String()[:8])
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, `DELETE FROM workspaces WHERE id=$1`, ws) })
	mkDoc = func(slug string) uuid.UUID {
		id := uuid.New()
		_, err := db.ExecContext(ctx, `INSERT INTO documents (id,project_id,slug,title,storage_key,created_by,created_by_type)
			VALUES ($1,$2,$3,$3,'k','`+uuid.New().String()+`','agent')`, id, proj, slug)
		require.NoError(t, err)
		return id
	}
	return NewDocumentChunkRepo(db), ws, proj, mkDoc
}

func TestDocumentChunkRepoDB_SearchReturnsDocSourceAndHidesDeleted(t *testing.T) {
	repo, ws, proj, mkDoc := docIdxFixture(t)
	ctx := context.Background()
	live, dead := mkDoc("audit"), mkDoc("gone")
	for _, id := range []uuid.UUID{live, dead} {
		require.NoError(t, repo.ReplaceChunks(ctx, id, proj, 1, []domain.DocumentChunk{
			{ChunkIdx: 0, Heading: "3.5 Predicate", Content: "fleet audit gate predicate reads its own output"},
		}))
	}
	got, err := repo.FullTextSearch(ctx, ws, &proj, "fleet audit gate predicate", 10)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, domain.SourceDoc, got[0].SourceType)
	assert.Equal(t, "3.5 Predicate", got[0].DocHeading)

	_, err = repo.db.ExecContext(ctx, `UPDATE documents SET deleted_at = now() WHERE id=$1`, dead)
	require.NoError(t, err)
	got, err = repo.FullTextSearch(ctx, ws, &proj, "fleet audit gate predicate", 10)
	require.NoError(t, err)
	require.Len(t, got, 1, "a soft-deleted document must not be returned even before its chunks are purged")
	assert.Equal(t, live, *got[0].SourceDocID)

	n, err := repo.PurgeDeleted(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, int64(1))

	other := uuid.New()
	got, err = repo.FullTextSearch(ctx, other, nil, "fleet audit gate predicate", 10)
	require.NoError(t, err)
	assert.Empty(t, got, "another workspace must see nothing")
}

func TestDocumentChunkRepoDB_BackfillSelectionIsIdempotentAndVersionGuarded(t *testing.T) {
	repo, ws, proj, mkDoc := docIdxFixture(t)
	ctx := context.Background()
	a, b := mkDoc("a"), mkDoc("b")

	st, err := repo.Status(ctx, ws, &proj)
	require.NoError(t, err)
	assert.Equal(t, domain.DocIndexStatus{LiveDocs: 2, IndexedDocs: 0}, st)

	stale, err := repo.ListStale(ctx, ws, &proj, 10)
	require.NoError(t, err)
	assert.Len(t, stale, 2)

	// documents.version defaults to 1; index both at that version.
	for _, id := range []uuid.UUID{a, b} {
		require.NoError(t, repo.ReplaceChunks(ctx, id, proj, 1, []domain.DocumentChunk{{ChunkIdx: 0, Content: "x"}}))
	}
	stale, err = repo.ListStale(ctx, ws, &proj, 10)
	require.NoError(t, err)
	assert.Empty(t, stale, "re-running the backfill selection after a full pass must find nothing")
	st, _ = repo.Status(ctx, ws, &proj)
	assert.Equal(t, 2, st.IndexedDocs)

	// Re-index at the same version: no duplicate rows.
	require.NoError(t, repo.ReplaceChunks(ctx, a, proj, 1, []domain.DocumentChunk{{ChunkIdx: 0, Content: "x"}}))
	var cnt int
	require.NoError(t, repo.db.GetContext(ctx, &cnt, `SELECT count(*) FROM document_chunks WHERE document_id=$1`, a))
	assert.Equal(t, 1, cnt)

	// An older async write must not clobber a newer one (document itself is at v5).
	_, err = repo.db.ExecContext(ctx, `UPDATE documents SET version = 5 WHERE id=$1`, a)
	require.NoError(t, err)
	require.NoError(t, repo.ReplaceChunks(ctx, a, proj, 5, []domain.DocumentChunk{{ChunkIdx: 0, Content: "new"}}))
	require.NoError(t, repo.ReplaceChunks(ctx, a, proj, 3, []domain.DocumentChunk{{ChunkIdx: 0, Content: "old"}}))
	var content string
	require.NoError(t, repo.db.GetContext(ctx, &content, `SELECT content FROM document_chunks WHERE document_id=$1`, a))
	assert.Equal(t, "new", content)

	// Deleting the document's chunks makes it stale again.
	require.NoError(t, repo.DeleteByDocument(ctx, b))
	stale, _ = repo.ListStale(ctx, ws, &proj, 10)
	require.Len(t, stale, 1)
	assert.Equal(t, b, stale[0].ID)
}
