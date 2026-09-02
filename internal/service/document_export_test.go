package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// createChild is like documentFixture.create but for a document with a parent
// and an explicit sibling position — the shape every tree-walk test needs and
// the plain create() helper does not offer.
func (f *documentFixture) createChild(t *testing.T, parentID uuid.UUID, position int, title, body string) *domain.Document {
	t.Helper()
	doc, err := f.svc.Create(context.Background(), CreateDocumentInput{
		ProjectID: f.projectID,
		ParentID:  &parentID,
		Position:  position,
		Title:     title,
		Body:      body,
	})
	require.NoError(t, err)
	require.NotNil(t, doc)
	return doc
}

func TestExportTreeWalk(t *testing.T) {
	t.Run("order is depth-first by position and stable across two runs", func(t *testing.T) {
		f := setupDocumentService(t)
		ctx := context.Background()

		root := f.create(t, "Root", "root body")
		// Created in the opposite order from how they should read: "First" was
		// created first but sits at the higher position, so a walk that
		// followed insertion or created_at order (instead of Position) would
		// get this backwards.
		first := f.createChild(t, root.ID, 5, "Created-first, positioned-last", "a")
		second := f.createChild(t, root.ID, 1, "Created-second, positioned-first", "b")
		grandchild := f.createChild(t, second.ID, 0, "Only child of the earlier sibling", "c")

		want := []uuid.UUID{root.ID, second.ID, grandchild.ID, first.ID}

		for run := 1; run <= 2; run++ {
			docs, err := f.svc.WalkExportTree(ctx, root.ID, f.wsID)
			require.NoError(t, err, "run %d", run)
			got := make([]uuid.UUID, len(docs))
			for i, d := range docs {
				got[i] = d.ID
			}
			assert.Equal(t, want, got, "run %d produced a different order", run)
		}
	})

	t.Run("a caller from another workspace is refused before the walk starts", func(t *testing.T) {
		f := setupDocumentService(t)
		root := f.create(t, "Confidential", "secret")
		f.createChild(t, root.ID, 0, "Also confidential", "secret too")

		// Same repo, same document — only the caller's workspace differs, and
		// it is not the one f.projectID is registered under. This is what
		// actually exercises the workspace check: a document from a wholly
		// separate mock repo instance would prove nothing (it would 404 for
		// not existing at all, regardless of any access check).
		strangerWorkspaceID := uuid.New()

		docs, err := f.svc.WalkExportTree(context.Background(), root.ID, strangerWorkspaceID)

		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, 404, apiErr.StatusCode())
		assert.Nil(t, docs)
	})

	t.Run("a row planted with a foreign project_id cannot join by pointing its parent_id into the tree", func(t *testing.T) {
		f := setupDocumentService(t)
		ctx := context.Background()

		root := f.create(t, "Root", "root body")
		legitChild := f.createChild(t, root.ID, 0, "Legitimate child", "real")

		// Not reachable through the API: Create's requireParentInProject would
		// refuse a parent from a different project. Seeded directly, the way
		// the task's own AC requires this case to be built.
		foreignProjectID := uuid.New()
		planted := &domain.Document{
			ID:         uuid.New(),
			ProjectID:  foreignProjectID,
			ParentID:   &root.ID,
			Slug:       "planted",
			Title:      "Should not appear",
			Position:   1,
			Version:    1,
			CreatedAt:  frozenTime,
			UpdatedAt:  frozenTime,
			SourceKind: domain.DocumentSourceOwn,
		}
		f.repo.Seed(planted)

		docs, err := f.svc.WalkExportTree(ctx, root.ID, f.wsID)

		require.NoError(t, err)
		got := make([]uuid.UUID, len(docs))
		for i, d := range docs {
			got[i] = d.ID
		}
		assert.Equal(t, []uuid.UUID{root.ID, legitChild.ID}, got,
			"a document from a different project rode its parent_id into the export")
	})

	t.Run("a soft-deleted document is absent from the export at any depth", func(t *testing.T) {
		f := setupDocumentService(t)
		ctx := context.Background()

		root := f.create(t, "Root", "root body")
		kept := f.createChild(t, root.ID, 0, "Kept", "stays")
		removedParent := f.createChild(t, root.ID, 1, "Removed", "goes")
		removedChild := f.createChild(t, removedParent.ID, 0, "Removed's child", "goes too")

		require.NoError(t, f.svc.Delete(ctx, removedParent.ID, f.wsID, uuid.New(), domain.ActorTypeUser))

		docs, err := f.svc.WalkExportTree(ctx, root.ID, f.wsID)

		require.NoError(t, err)
		got := make([]uuid.UUID, len(docs))
		for i, d := range docs {
			got[i] = d.ID
		}
		assert.Equal(t, []uuid.UUID{root.ID, kept.ID}, got)
		assert.NotContains(t, got, removedParent.ID)
		assert.NotContains(t, got, removedChild.ID, "a soft-deleted document's own child survived in the export")
	})

	t.Run("a tree past the document-count ceiling is refused before any body is downloaded", func(t *testing.T) {
		f := setupDocumentService(t)
		ctx := context.Background()

		root := f.create(t, "Root", "root body")
		for i := 0; i < maxExportDocuments; i++ {
			f.createChild(t, root.ID, i, "Child", "x")
		}
		// root + maxExportDocuments children = maxExportDocuments+1, one over.

		_, err := f.svc.WalkExportTree(ctx, root.ID, f.wsID)

		var tooLarge *ExportTreeTooLargeError
		require.ErrorAs(t, err, &tooLarge)
		assert.Equal(t, "documents", tooLarge.Kind)
		assert.Equal(t, int64(maxExportDocuments+1), tooLarge.Actual)
		assert.Equal(t, int64(maxExportDocuments), tooLarge.Limit)
		assert.Equal(t, 1, f.storage.downloads,
			"want exactly the root's body downloaded (by GetByIDInWorkspace, to authorize it) and nothing past the count ceiling")
	})

	t.Run("a tree past the byte-size ceiling is refused mid-walk, not with a silent truncation", func(t *testing.T) {
		f := setupDocumentService(t)
		ctx := context.Background()

		root := f.create(t, "Root", "") // 0 bytes, keeps the arithmetic exact
		bigBody := strings.Repeat("x", maxDocumentBodyBytes)
		// 10 * 5 MiB lands EXACTLY on the 50 MiB limit (both are 52,428,800) —
		// not over it, since the check is "> limit". 11 clears it.
		const childCount = 11
		for i := 0; i < childCount; i++ {
			f.createChild(t, root.ID, i, "Big child", bigBody)
		}

		_, err := f.svc.WalkExportTree(ctx, root.ID, f.wsID)

		var tooLarge *ExportTreeTooLargeError
		require.ErrorAs(t, err, &tooLarge)
		assert.Equal(t, "bytes", tooLarge.Kind)
		assert.Equal(t, int64(childCount*maxDocumentBodyBytes), tooLarge.Actual)
		assert.Equal(t, int64(maxExportTotalBytes), tooLarge.Limit)
		assert.Greater(t, tooLarge.Actual, tooLarge.Limit)
	})

	t.Run("a missing root is a 404, not an empty export", func(t *testing.T) {
		f := setupDocumentService(t)

		docs, err := f.svc.WalkExportTree(context.Background(), uuid.New(), f.wsID)

		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, 404, apiErr.StatusCode())
		assert.Nil(t, docs)
		var tooLarge *ExportTreeTooLargeError
		assert.False(t, errors.As(err, &tooLarge), "a missing root must not be reported as a size-limit error")
	})
}
