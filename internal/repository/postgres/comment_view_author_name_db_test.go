package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// Regression for an internal ticket: CommentView.AuthorName is a plain string, and the
// agent arm of commentViewSelect resolved the name only for live agents. A
// comment by a soft-deleted agent (or by an agent id with no row at all)
// produced author_name NULL and the whole page failed to scan, so
// GET /comments/recent and GET /me/comments answered 500 on any page holding
// such a row. Reuses the fixture of comment_cursor_tiebreak_db_test.go.

func createCommentViewTestAgent(t *testing.T, db *sqlx.DB, wsID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	suffix := uuid.New().String()[:8]
	agent := &domain.Agent{
		ID: uuid.New(), WorkspaceID: wsID, Name: name, Slug: "comment-view-" + suffix,
		AgentType: domain.AgentTypeClaudeCode, Status: domain.AgentStatusOffline,
		APIKeyHash: "$2a$12$" + suffix, APIKeyPrefix: suffix,
	}
	require.NoError(t, NewAgentRepo(db).Create(context.Background(), agent))
	return agent.ID
}

func createCommentViewAgentComment(t *testing.T, repo *CommentRepo, taskID, authorID uuid.UUID, createdAt time.Time) uuid.UUID {
	t.Helper()
	c := &domain.Comment{
		ID: uuid.New(), TaskID: taskID, AuthorID: authorID, AuthorType: domain.ActorTypeAgent,
		Body: "comment by an agent", CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	require.NoError(t, repo.Create(context.Background(), c))
	return c.ID
}

func TestCommentView_DeletedAgentAuthor_DoesNotBreakFeeds(t *testing.T) {
	db := commentCursorTestDB(t)
	fx := newCursorTestFixture(t, db)
	repo := NewCommentRepo(db)
	ctx := context.Background()

	deletedID := createCommentViewTestAgent(t, db, fx.workspaceID, "Retired Agent")
	base := time.Now().UTC().Truncate(time.Microsecond)
	deletedComment := createCommentViewAgentComment(t, repo, fx.taskID, deletedID, base)
	require.NoError(t, NewAgentRepo(db).Delete(ctx, deletedID))

	// An agent id with no agents row at all (a foreign or purged agent).
	ghostID := uuid.New()
	ghostComment := createCommentViewAgentComment(t, repo, fx.taskID, ghostID, base.Add(-time.Second))

	t.Run("recent feed", func(t *testing.T) {
		rows, _, err := repo.ListRecentByWorkspace(ctx, fx.workspaceID,
			repository.CommentViewFilter{Limit: 10, Viewer: domain.AllProjectsViewer()})
		require.NoError(t, err)
		names := map[uuid.UUID]string{}
		for _, r := range rows {
			names[r.CommentID] = r.AuthorName
		}
		require.Contains(t, names, deletedComment)
		require.Contains(t, names, ghostComment)
		assert.Equal(t, "Retired Agent", names[deletedComment], "a deleted agent keeps its name in the feed")
		assert.Equal(t, "", names[ghostComment], "an unresolvable agent reads as an empty name, not a scan error")
	})

	t.Run("author feed", func(t *testing.T) {
		for _, want := range []struct {
			authorID  uuid.UUID
			commentID uuid.UUID
			name      string
		}{
			{deletedID, deletedComment, "Retired Agent"},
			{ghostID, ghostComment, ""},
		} {
			rows, _, err := repo.ListByAuthor(ctx, want.authorID,
				repository.CommentViewFilter{Limit: 10, Viewer: domain.AllProjectsViewer()})
			require.NoError(t, err)
			require.Len(t, rows, 1)
			assert.Equal(t, want.commentID, rows[0].CommentID)
			assert.Equal(t, want.authorID, rows[0].AuthorID)
			assert.Equal(t, want.name, rows[0].AuthorName)
			assert.Equal(t, "comment by an agent", rows[0].CommentBody)
		}
	})
}
