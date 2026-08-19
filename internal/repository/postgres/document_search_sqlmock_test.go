package postgres

import (
	"context"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The parts of document search that need no database to prove: that the query
// joins through to projects (the tenancy check), that it hides soft-deleted
// rows, that it ranks, and that a snippet without a marker is reported as not
// being the match.

func searchRows(snippets ...string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"id", "project_id", "title", "slug", "snippet", "rank"})
	for i, s := range snippets {
		rows.AddRow(uuid.New(), uuid.New(), "Doc", "doc", s, 0.5-float64(i)*0.1)
	}
	return rows
}

func TestDocumentRepo_SearchInProject_ScopesToTheWorkspace(t *testing.T) {
	repo, mock := newDocumentRepoMock(t)
	projectID, wsID := uuid.New(), uuid.New()

	// The JOIN and both predicates are the assertion. A project id is a
	// caller-supplied value; without the join to projects, knowing one would be
	// enough to read another tenant's documents through this endpoint.
	mock.ExpectQuery(`FROM documents d\s+JOIN projects p ON d\.project_id = p\.id.*WHERE d\.project_id = \$1\s+AND p\.workspace_id = \$2\s+AND d\.deleted_at IS NULL\s+AND d\.search_vector @@ tsq`).
		WithArgs(projectID, wsID, "rollback", 20, searchHeadlineWindow, searchMarkStart, searchMarkEnd).
		WillReturnRows(searchRows("a " + searchMarkStart + "rollback" + searchMarkEnd + " b"))

	hits, err := repo.SearchInProject(context.Background(), projectID, wsID, "rollback", 20)

	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentRepo_SearchInProject_MarksWhetherTheSnippetIsTheMatch(t *testing.T) {
	repo, mock := newDocumentRepoMock(t)

	mock.ExpectQuery("FROM documents d").
		WillReturnRows(searchRows(
			"context "+searchMarkStart+"rollback"+searchMarkEnd+" more",
			"the opening of a document whose match lies past the window",
		))

	hits, err := repo.SearchInProject(context.Background(), uuid.New(), uuid.New(), "rollback", 20)

	require.NoError(t, err)
	require.Len(t, hits, 2)
	// Without this flag the caller cannot tell a matched fragment from a
	// document's first sentence, and would highlight both — presenting the
	// opening of a document as the reason it matched.
	assert.True(t, hits[0].SnippetIsMatch)
	assert.False(t, hits[1].SnippetIsMatch)
}

func TestDocumentRepo_SearchInProject_OrdersByRank(t *testing.T) {
	repo, mock := newDocumentRepoMock(t)

	mock.ExpectQuery(`ORDER BY rank DESC, d\.updated_at DESC, d\.id ASC`).
		WillReturnRows(searchRows("a", "b"))

	_, err := repo.SearchInProject(context.Background(), uuid.New(), uuid.New(), "q", 20)

	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentRepo_SetSearchText_DoesNotTouchUpdatedAt(t *testing.T) {
	repo, mock := newDocumentRepoMock(t)
	id := uuid.New()

	// Named columns, not a wildcard: indexing is not an edit, and moving
	// updated_at would reorder every list that sorts by it — a document would
	// jump to the top of "recently changed" because a background write reindexed
	// it.
	mock.ExpectExec(`UPDATE documents SET search_text = \$2 WHERE id = \$1 AND deleted_at IS NULL`).
		WithArgs(id, "body text").
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.SetSearchText(context.Background(), id, "body text"))
	assert.NoError(t, mock.ExpectationsWereMet())
}

// The markers are duplicated in web/src/lib/docs/document-search.ts, which has no
// way to import them. So each side pins its own value: if either drifts, that
// side's test fails, instead of the product silently never highlighting anything.
func TestDocumentSearch_MarkersArePinned(t *testing.T) {
	assert.Equal(t, "", searchMarkStart)
	assert.Equal(t, "", searchMarkEnd)
}
