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
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// No //go:build integration tag — same convention as the other *_db_test.go
// files here (e.g. agent_api_key_sha256_db_test.go): skip gracefully when no
// Postgres is reachable rather than requiring a separate CI invocation.
//
// This is the negative-control test for #c6dc694e: a tie group of comments
// sharing one created_at, with a page boundary landing inside the group.
// Pre-fix, ORDER BY c.created_at DESC has no tiebreaker and the cursor is a
// bare `created_at < $n`, so every row in the tie group not returned on the
// first page is silently dropped — same class already fixed for tasks in
// #a1012e55 (ORDER BY updated_at DESC, id ASC).

func commentCursorTestDB(t *testing.T) *sqlx.DB {
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

// cursorTestFixture is a self-contained workspace+project+status+task setup —
// no dependency on integration_test.go's //go:build integration helpers,
// same reasoning as the other *_db_test.go files here.
type cursorTestFixture struct {
	workspaceID uuid.UUID
	taskID      uuid.UUID
}

func newCursorTestFixture(t *testing.T, db *sqlx.DB) cursorTestFixture {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	ws := &domain.Workspace{
		ID: uuid.New(), Name: "cursor-tiebreak-ws", Slug: "cursor-tiebreak-ws-" + suffix, OwnerID: uuid.New(),
	}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, ws))

	proj := &domain.Project{
		ID: uuid.New(), WorkspaceID: ws.ID, Name: "cursor-tiebreak-proj",
		Slug: "cursor-tiebreak-proj-" + suffix, DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	require.NoError(t, NewProjectRepo(db).Create(ctx, proj))

	status := &domain.TaskStatus{
		ID: uuid.New(), ProjectID: proj.ID, Name: "Open", Slug: "open",
		Color: "#00FF00", Position: 0, Category: domain.StatusCategoryTodo, IsDefault: true,
	}
	require.NoError(t, NewTaskStatusRepo(db).Create(ctx, status))

	task := &domain.Task{
		ID: uuid.New(), ProjectID: proj.ID, StatusID: status.ID,
		Title: "Cursor tie-break test task", AssigneeType: domain.AssigneeTypeUnassigned,
		Priority: domain.PriorityMedium, CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
	}
	require.NoError(t, NewTaskRepo(db).Create(ctx, task))

	return cursorTestFixture{workspaceID: ws.ID, taskID: task.ID}
}

// createCursorTestUser inserts a real user row so a comment authored by it
// resolves author_name via commentViewSelect's correlated subquery instead of
// scanning NULL (the subquery finds no row for an author_id with no matching
// user, which errors the scan rather than falling through to the CASE's ELSE).
func createCursorTestUser(t *testing.T, db *sqlx.DB) uuid.UUID {
	t.Helper()
	suffix := uuid.New().String()[:8]
	u := &domain.User{
		ID: uuid.New(), Email: "cursor-tiebreak-" + suffix + "@example.com", PasswordHash: "x",
		Name: "Cursor Tiebreak User", Username: "cursor-tiebreak-" + suffix, IsActive: true,
	}
	require.NoError(t, NewUserRepo(db).Create(context.Background(), u))
	return u.ID
}

// createCursorTestComment inserts a non-internal comment with an explicit
// created_at (and, for the given authorID, so ListByAuthor can be scoped to
// exactly the fixture's rows).
func createCursorTestComment(t *testing.T, repo *CommentRepo, taskID, authorID uuid.UUID, body string, createdAt time.Time) uuid.UUID {
	t.Helper()
	c := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     taskID,
		AuthorID:   authorID,
		AuthorType: domain.ActorTypeUser,
		Body:       body,
		IsInternal: false,
		CreatedAt:  createdAt,
		UpdatedAt:  createdAt,
	}
	require.NoError(t, repo.Create(context.Background(), c))
	return c.ID
}

// walkRecentByWorkspace pages through ListRecentByWorkspace with the given
// page size, following the tuple cursor to exhaustion, and returns every
// comment ID seen.
func walkRecentByWorkspace(t *testing.T, repo *CommentRepo, wsID uuid.UUID, pageSize int) []uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var seen []uuid.UUID
	filter := repository.CommentViewFilter{Limit: pageSize}
	for i := 0; i < 100; i++ { // hard cap so a regression can't hang the suite
		rows, cursor, err := repo.ListRecentByWorkspace(ctx, wsID, filter)
		require.NoError(t, err)
		for _, r := range rows {
			seen = append(seen, r.CommentID)
		}
		if cursor == nil {
			return seen
		}
		filter.Before = &cursor.CreatedAt
		filter.BeforeID = &cursor.ID
	}
	t.Fatal("walkRecentByWorkspace: did not terminate within 100 pages")
	return nil
}

func walkByAuthor(t *testing.T, repo *CommentRepo, authorID uuid.UUID, pageSize int) []uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var seen []uuid.UUID
	filter := repository.CommentViewFilter{Limit: pageSize}
	for i := 0; i < 100; i++ {
		rows, cursor, err := repo.ListByAuthor(ctx, authorID, filter)
		require.NoError(t, err)
		for _, r := range rows {
			seen = append(seen, r.CommentID)
		}
		if cursor == nil {
			return seen
		}
		filter.Before = &cursor.CreatedAt
		filter.BeforeID = &cursor.ID
	}
	t.Fatal("walkByAuthor: did not terminate within 100 pages")
	return nil
}

func idSet(ids []uuid.UUID) map[uuid.UUID]bool {
	m := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// TestCommentRepo_ListRecentByWorkspace_TieGroupCursor_AllRowsReachable is the
// negative control: against the pre-fix single-timestamp cursor, a page
// boundary inside the tie group makes the walk lose every unread member of
// that group. With the (created_at, id) tuple cursor + tiebreaker ORDER BY,
// the full walk must recover every row regardless of where the page boundary
// falls inside the group.
func TestCommentRepo_ListRecentByWorkspace_TieGroupCursor_AllRowsReachable(t *testing.T) {
	db := commentCursorTestDB(t)
	fx := newCursorTestFixture(t, db)
	commentRepo := NewCommentRepo(db)

	author := createCursorTestUser(t, db)
	base := time.Now().UTC().Truncate(time.Microsecond)
	older := createCursorTestComment(t, commentRepo, fx.taskID, author, "older", base.Add(-time.Minute))

	const tieGroupSize = 7
	tieGroup := make([]uuid.UUID, tieGroupSize)
	for i := 0; i < tieGroupSize; i++ {
		tieGroup[i] = createCursorTestComment(t, commentRepo, fx.taskID, author, "tied", base)
	}

	newer := createCursorTestComment(t, commentRepo, fx.taskID, author, "newer", base.Add(time.Minute))

	want := append([]uuid.UUID{newer}, tieGroup...)
	want = append(want, older)

	for _, pageSize := range []int{1, 2, 3, 4} {
		t.Run("", func(t *testing.T) {
			got := walkRecentByWorkspace(t, commentRepo, fx.workspaceID, pageSize)
			assert.Equal(t, idSet(want), idSet(got),
				"page_size=%d: full walk must recover every row, including the tie group of %d", pageSize, tieGroupSize)
			assert.Len(t, got, len(want), "page_size=%d: no duplicates either", pageSize)
		})
	}
}

// TestCommentRepo_ListByAuthor_TieGroupCursor_AllRowsReachable is the same
// negative control against the second cursor query in this file (/me/comments).
func TestCommentRepo_ListByAuthor_TieGroupCursor_AllRowsReachable(t *testing.T) {
	db := commentCursorTestDB(t)
	fx := newCursorTestFixture(t, db)
	commentRepo := NewCommentRepo(db)
	authorID := createCursorTestUser(t, db)

	base := time.Now().UTC().Truncate(time.Microsecond)
	older := createCursorTestComment(t, commentRepo, fx.taskID, authorID, "older", base.Add(-time.Minute))

	const tieGroupSize = 5
	tieGroup := make([]uuid.UUID, tieGroupSize)
	for i := 0; i < tieGroupSize; i++ {
		tieGroup[i] = createCursorTestComment(t, commentRepo, fx.taskID, authorID, "tied", base)
	}

	newer := createCursorTestComment(t, commentRepo, fx.taskID, authorID, "newer", base.Add(time.Minute))

	want := append([]uuid.UUID{newer}, tieGroup...)
	want = append(want, older)

	got := walkByAuthor(t, commentRepo, authorID, 2)
	assert.Equal(t, idSet(want), idSet(got),
		"full walk with page_size=2 must recover every row, including the tie group of %d", tieGroupSize)
	assert.Len(t, got, len(want), "no duplicates")
}

// TestCommentRepo_ListRecentByWorkspace_OldSingleTimestampCursor_StillWorks
// pins backward compat: a client that only ever sends `before` (no
// `before_id`) — e.g. an already-open tab from before this change shipped —
// must not error. It legitimately still loses tie-group members on a boundary
// hit (that's the bug this fix targets), so this test only proves the old
// query shape keeps working, not that it recovers everything.
func TestCommentRepo_ListRecentByWorkspace_OldSingleTimestampCursor_StillWorks(t *testing.T) {
	db := commentCursorTestDB(t)
	fx := newCursorTestFixture(t, db)
	commentRepo := NewCommentRepo(db)
	ctx := context.Background()

	author := createCursorTestUser(t, db)
	base := time.Now().UTC().Truncate(time.Microsecond)
	createCursorTestComment(t, commentRepo, fx.taskID, author, "a", base)
	createCursorTestComment(t, commentRepo, fx.taskID, author, "b", base.Add(time.Second))

	rows, cursor, err := commentRepo.ListRecentByWorkspace(ctx, fx.workspaceID, repository.CommentViewFilter{Limit: 1})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NotNil(t, cursor)

	// Old-style client: echoes back only the timestamp half of the cursor.
	rows2, _, err := commentRepo.ListRecentByWorkspace(ctx, fx.workspaceID, repository.CommentViewFilter{
		Limit:  10,
		Before: &cursor.CreatedAt,
	})
	require.NoError(t, err)
	assert.Len(t, rows2, 1, "old-style cursor still returns the older comment")
}
