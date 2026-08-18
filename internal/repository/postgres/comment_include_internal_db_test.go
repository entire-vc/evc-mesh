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

// No //go:build integration tag — same convention as comment_cursor_tiebreak_db_test.go.
//
// Covers #a7ae4c76 pt.2: ListRecentByWorkspace hardcoded `c.is_internal =
// false` with no way to see internal comments, which makes the workspace
// feed structurally blind to any orphaned-mention sweep over internal
// comments. Fix: CommentViewFilter.IncludeInternal (default false, so
// existing behavior is unchanged) plus CommentView.IsInternal on the DTO so
// a caller that does ask for internal comments can tell them apart.

func createCursorTestCommentWithVisibility(t *testing.T, repo *CommentRepo, taskID, authorID uuid.UUID, body string, createdAt time.Time, isInternal bool) uuid.UUID {
	t.Helper()
	c := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     taskID,
		AuthorID:   authorID,
		AuthorType: domain.ActorTypeUser,
		Body:       body,
		IsInternal: isInternal,
		CreatedAt:  createdAt,
		UpdatedAt:  createdAt,
	}
	require.NoError(t, repo.Create(context.Background(), c))
	return c.ID
}

func TestCommentRepo_ListRecentByWorkspace_IncludeInternal(t *testing.T) {
	db := commentCursorTestDB(t)
	fx := newCursorTestFixture(t, db)
	commentRepo := NewCommentRepo(db)
	author := createCursorTestUser(t, db)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Microsecond)
	external1 := createCursorTestCommentWithVisibility(t, commentRepo, fx.taskID, author, "external 1", base, false)
	internal1 := createCursorTestCommentWithVisibility(t, commentRepo, fx.taskID, author, "internal 1", base.Add(time.Second), true)
	external2 := createCursorTestCommentWithVisibility(t, commentRepo, fx.taskID, author, "external 2", base.Add(2*time.Second), false)
	internal2 := createCursorTestCommentWithVisibility(t, commentRepo, fx.taskID, author, "internal 2", base.Add(3*time.Second), true)

	t.Run("default (no filter set) excludes internal — unchanged behavior", func(t *testing.T) {
		rows, _, err := commentRepo.ListRecentByWorkspace(ctx, fx.workspaceID, repository.CommentViewFilter{Limit: 200})
		require.NoError(t, err)
		got := commentViewIDs(rows)
		assert.ElementsMatch(t, []uuid.UUID{external1, external2}, got)
		for _, r := range rows {
			assert.False(t, r.IsInternal)
		}
	})

	t.Run("IncludeInternal=false is equivalent to unset", func(t *testing.T) {
		rows, _, err := commentRepo.ListRecentByWorkspace(ctx, fx.workspaceID, repository.CommentViewFilter{Limit: 200, IncludeInternal: false})
		require.NoError(t, err)
		assert.ElementsMatch(t, []uuid.UUID{external1, external2}, commentViewIDs(rows))
	})

	t.Run("IncludeInternal=true adds exactly the internal rows, each flagged", func(t *testing.T) {
		rows, _, err := commentRepo.ListRecentByWorkspace(ctx, fx.workspaceID, repository.CommentViewFilter{Limit: 200, IncludeInternal: true})
		require.NoError(t, err)
		assert.ElementsMatch(t, []uuid.UUID{external1, external2, internal1, internal2}, commentViewIDs(rows))

		byID := make(map[uuid.UUID]domain.CommentView, len(rows))
		for _, r := range rows {
			byID[r.CommentID] = r
		}
		assert.False(t, byID[external1].IsInternal)
		assert.False(t, byID[external2].IsInternal)
		assert.True(t, byID[internal1].IsInternal)
		assert.True(t, byID[internal2].IsInternal)
	})
}

// TestCommentRepo_ListByAuthor_IgnoresIncludeInternal pins the task's explicit
// scope decision: IncludeInternal is not honored by ListByAuthor — a caller's
// own comments feed is unrelated to the internal-mentions corpus this exists
// for. Setting it must not change ListByAuthor's output.
func TestCommentRepo_ListByAuthor_IgnoresIncludeInternal(t *testing.T) {
	db := commentCursorTestDB(t)
	fx := newCursorTestFixture(t, db)
	commentRepo := NewCommentRepo(db)
	author := createCursorTestUser(t, db)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Microsecond)
	external := createCursorTestCommentWithVisibility(t, commentRepo, fx.taskID, author, "external", base, false)
	createCursorTestCommentWithVisibility(t, commentRepo, fx.taskID, author, "internal", base.Add(time.Second), true)

	rows, _, err := commentRepo.ListByAuthor(ctx, author, repository.CommentViewFilter{Limit: 200, IncludeInternal: true})
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{external}, commentViewIDs(rows))
}

func commentViewIDs(rows []domain.CommentView) []uuid.UUID {
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.CommentID
	}
	return ids
}
