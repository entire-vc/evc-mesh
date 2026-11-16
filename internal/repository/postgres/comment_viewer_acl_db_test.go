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

// The two comment feeds (GET /me/comments → ListByAuthor, GET
// /workspaces/:ws_id/comments/recent → ListRecentByWorkspace) used to filter
// by author/workspace only, so a workspace member read comment bodies, task
// titles and project names of projects they are not a member of, including
// their own old comments on a project they were removed from. Each feed now
// serves only rows of projects the viewer may open.

type commentACLFixture struct {
	coverageFixture
	repo     *CommentRepo
	projectB uuid.UUID
	taskB    uuid.UUID
}

func newCommentACLFixture(t *testing.T) commentACLFixture {
	t.Helper()
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	ctx := context.Background()
	now := time.Now()

	projB := &domain.Project{
		ID: uuid.New(), WorkspaceID: fx.workspaceID, Name: "closed project " + fx.suffix,
		Slug: "closed-proj-" + fx.suffix, DefaultAssigneeType: domain.DefaultAssigneeNone,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, NewProjectRepo(db).Create(ctx, projB))
	stB := seedStatus(t, db, projB.ID, "open-b", domain.StatusCategoryTodo)
	taskB := &domain.Task{
		ID: uuid.New(), ProjectID: projB.ID, StatusID: stB, Title: "closed task",
		AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium,
		CreatedBy: fx.userID, CreatedByType: domain.ActorTypeUser,
	}
	require.NoError(t, NewTaskRepo(db).Create(ctx, taskB, nil))

	return commentACLFixture{coverageFixture: fx, repo: NewCommentRepo(db), projectB: projB.ID, taskB: taskB.ID}
}

func (fx commentACLFixture) comment(t *testing.T, taskID, author uuid.UUID, kind domain.ActorType, at time.Time) uuid.UUID {
	t.Helper()
	c := &domain.Comment{
		ID: uuid.New(), TaskID: taskID, AuthorID: author, AuthorType: kind,
		Body: "acl body", CreatedAt: at, UpdatedAt: at,
	}
	require.NoError(t, fx.repo.Create(context.Background(), c))
	return c.ID
}

func TestCommentFeeds_ServeOnlyProjectsTheViewerMayOpen(t *testing.T) {
	fx := newCommentACLFixture(t)
	db := selectStarCoverageTestDB(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond)

	// B is a member of project A (fx.projectID) only; its old comment on B's
	// closed project stays in the table.
	memberB := seedMemberUser(t, db, fx.workspaceID, fx.projectID)
	bOnB := fx.comment(t, fx.taskB, memberB, domain.ActorTypeUser, base)
	bOnA := fx.comment(t, fx.taskID, memberB, domain.ActorTypeUser, base.Add(time.Second))

	// Owner and admin: no project memberships at all.
	ownerOnA := fx.comment(t, fx.taskID, fx.userID, domain.ActorTypeUser, base.Add(2*time.Second))
	ownerOnB := fx.comment(t, fx.taskB, fx.userID, domain.ActorTypeUser, base.Add(3*time.Second))
	admin := seedMemberUser(t, db, fx.workspaceID)
	_, err := db.ExecContext(ctx, `UPDATE workspace_members SET role = 'admin' WHERE workspace_id = $1 AND user_id = $2`, fx.workspaceID, admin)
	require.NoError(t, err)
	adminOnA := fx.comment(t, fx.taskID, admin, domain.ActorTypeUser, base.Add(4*time.Second))
	adminOnB := fx.comment(t, fx.taskB, admin, domain.ActorTypeUser, base.Add(5*time.Second))

	// The agent holds an admin grant but is a member of project A only.
	agentID := fx.agentID
	insertGrant(t, db, agentID, fx.workspaceID, "admin", "acl"+fx.suffix, "$2a$12$acl-hash", nil)
	require.NoError(t, NewProjectMemberRepo(db).Create(ctx, &domain.ProjectMember{
		ID: uuid.New(), ProjectID: fx.projectID, AgentID: &agentID, Role: domain.ProjectRoleMember,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))
	agentOnA := fx.comment(t, fx.taskID, agentID, domain.ActorTypeAgent, base.Add(6*time.Second))
	agentOnB := fx.comment(t, fx.taskB, agentID, domain.ActorTypeAgent, base.Add(7*time.Second))

	onA := []uuid.UUID{bOnA, ownerOnA, adminOnA, agentOnA}
	all := append([]uuid.UUID{bOnB, ownerOnB, adminOnB, agentOnB}, onA...)

	recent := func(t *testing.T, v domain.ProjectViewer) []uuid.UUID {
		t.Helper()
		rows, _, err := fx.repo.ListRecentByWorkspace(ctx, fx.workspaceID, repository.CommentViewFilter{Limit: 200, Viewer: v})
		require.NoError(t, err)
		return commentViewIDs(rows)
	}
	mine := func(t *testing.T, author uuid.UUID, v domain.ProjectViewer) []uuid.UUID {
		t.Helper()
		rows, _, err := fx.repo.ListByAuthor(ctx, author, repository.CommentViewFilter{Limit: 200, Viewer: v})
		require.NoError(t, err)
		return commentViewIDs(rows)
	}

	t.Run("a member outside a project gets neither feed's rows from it", func(t *testing.T) {
		v := domain.ProjectViewer{UserID: &memberB}
		assert.ElementsMatch(t, onA, recent(t, v))
		assert.Equal(t, []uuid.UUID{bOnA}, mine(t, memberB, v), "B's old comment on the closed project must not come back")
	})

	t.Run("owner and admin see both projects", func(t *testing.T) {
		assert.ElementsMatch(t, all, recent(t, domain.AllProjectsViewer()))
		owner := fx.userID
		assert.ElementsMatch(t, []uuid.UUID{ownerOnA, ownerOnB}, mine(t, owner, domain.ProjectViewer{UserID: &owner}))
		assert.ElementsMatch(t, []uuid.UUID{adminOnA, adminOnB}, mine(t, admin, domain.ProjectViewer{UserID: &admin}))
	})

	t.Run("an agent with an admin grant sees only its memberships", func(t *testing.T) {
		v := domain.ProjectViewer{AgentID: &agentID}
		assert.ElementsMatch(t, onA, recent(t, v))
		assert.Equal(t, []uuid.UUID{agentOnA}, mine(t, agentID, v))
	})

	t.Run("the zero viewer gets nothing", func(t *testing.T) {
		assert.Empty(t, recent(t, domain.ProjectViewer{}))
		assert.Empty(t, mine(t, memberB, domain.ProjectViewer{}))
	})

	t.Run("filtering happens before LIMIT and the cursor still walks", func(t *testing.T) {
		v := domain.ProjectViewer{UserID: &memberB}
		filter := repository.CommentViewFilter{Limit: 1, Viewer: v}
		var seen []uuid.UUID
		for i := 0; i < 10; i++ {
			rows, cursor, err := fx.repo.ListRecentByWorkspace(ctx, fx.workspaceID, filter)
			require.NoError(t, err)
			require.LessOrEqual(t, len(rows), 1)
			seen = append(seen, commentViewIDs(rows)...)
			if cursor == nil {
				break
			}
			filter.Before, filter.BeforeID = &cursor.CreatedAt, &cursor.ID
		}
		assert.Equal(t, []uuid.UUID{agentOnA, adminOnA, ownerOnA, bOnA}, seen)
	})
}

// GET /me/mentions and its unseen badge had the same hole: the inbox kept
// listing mentions (task title, comment body) on a project the addressee had
// been removed from.
func TestCommentMentions_ServeOnlyProjectsTheViewerMayOpen(t *testing.T) {
	fx := newCommentACLFixture(t)
	db := selectStarCoverageTestDB(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond)
	mentions := NewCommentMentionRepo(db)

	memberB := seedMemberUser(t, db, fx.workspaceID, fx.projectID)
	owner := fx.userID
	onA := fx.comment(t, fx.taskID, owner, domain.ActorTypeUser, base)
	onB := fx.comment(t, fx.taskB, owner, domain.ActorTypeUser, base.Add(time.Second))
	mention := func(commentID, who uuid.UUID, kind string, at time.Time) {
		require.NoError(t, mentions.InsertBatch(ctx, []domain.CommentMention{{
			CommentID: commentID, MentionedID: who, MentionedKind: kind, MentionedSlug: "acl", ExtractedAt: at,
		}}))
	}
	mention(onA, memberB, "user", base)
	mention(onB, memberB, "user", base.Add(time.Second))
	mention(onA, owner, "user", base)
	mention(onB, owner, "user", base.Add(time.Second))

	agentID := fx.agentID
	insertGrant(t, db, agentID, fx.workspaceID, "admin", "aclm"+fx.suffix, "$2a$12$acl-hash", nil)
	require.NoError(t, NewProjectMemberRepo(db).Create(ctx, &domain.ProjectMember{
		ID: uuid.New(), ProjectID: fx.projectID, AgentID: &agentID, Role: domain.ProjectRoleMember,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))
	mention(onA, agentID, "agent", base)
	mention(onB, agentID, "agent", base.Add(time.Second))

	ws := fx.workspaceID
	list := func(t *testing.T, who uuid.UUID, kind string, v domain.ProjectViewer) []uuid.UUID {
		t.Helper()
		rows, err := mentions.List(ctx, who, kind, repository.MentionFilter{WorkspaceID: &ws, Limit: 50, Viewer: v})
		require.NoError(t, err)
		ids := make([]uuid.UUID, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.CommentID)
		}
		return ids
	}
	count := func(t *testing.T, who uuid.UUID, kind string, v domain.ProjectViewer) int64 {
		t.Helper()
		n, err := mentions.CountUnseen(ctx, who, kind, ws, v)
		require.NoError(t, err)
		return n
	}

	t.Run("a member outside a project neither lists nor counts its mentions", func(t *testing.T) {
		v := domain.ProjectViewer{UserID: &memberB}
		assert.Equal(t, []uuid.UUID{onA}, list(t, memberB, "user", v))
		assert.EqualValues(t, 1, count(t, memberB, "user", v))
	})
	t.Run("the workspace owner keeps both", func(t *testing.T) {
		v := domain.ProjectViewer{UserID: &owner}
		assert.ElementsMatch(t, []uuid.UUID{onA, onB}, list(t, owner, "user", v))
		assert.EqualValues(t, 2, count(t, owner, "user", v))
	})
	t.Run("an agent with an admin grant keeps only its memberships", func(t *testing.T) {
		v := domain.ProjectViewer{AgentID: &agentID}
		assert.Equal(t, []uuid.UUID{onA}, list(t, agentID, "agent", v))
		assert.EqualValues(t, 1, count(t, agentID, "agent", v))
	})
	t.Run("the zero viewer gets nothing", func(t *testing.T) {
		assert.Empty(t, list(t, memberB, "user", domain.ProjectViewer{}))
		assert.Zero(t, count(t, memberB, "user", domain.ProjectViewer{}))
	})
}
