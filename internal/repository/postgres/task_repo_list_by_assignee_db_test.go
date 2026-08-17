package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// No //go:build integration tag, deliberately — same reasoning as the other
// *_db_test.go files here: CI's untagged `go test ./...` runs these against a
// migrated DATABASE_URL and skip()s when no database is reachable.
//
// These pin that ListByAssignee's narrowing happens IN SQL. A fake cannot show
// that: the previous implementation returned every row and the handler filtered
// afterwards, and against a mock both spellings look identical. What separates
// them is whether the LIMIT and the category predicate reach Postgres, so the
// assertions below are about the rows the database hands back.

func listByAssigneeTestDB(t *testing.T) *sqlx.DB {
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

// assigneeFeedFixture builds one workspace with one project, a todo status and a
// done status, and n tasks in each assigned to a fresh agent id.
type assigneeFeedFixture struct {
	workspaceID uuid.UUID
	projectID   uuid.UUID
	assigneeID  uuid.UUID
	todoStatus  uuid.UUID
	doneStatus  uuid.UUID
}

func newAssigneeFeedFixture(t *testing.T, db *sqlx.DB, perCategory int) assigneeFeedFixture {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	ws := &domain.Workspace{
		ID: uuid.New(), Name: "feed-ws", Slug: "feed-ws-" + suffix, OwnerID: uuid.New(),
	}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, ws))

	proj := &domain.Project{
		ID: uuid.New(), WorkspaceID: ws.ID, Name: "feed-proj",
		Slug: "feed-proj-" + suffix, DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	require.NoError(t, NewProjectRepo(db).Create(ctx, proj))

	statusRepo := NewTaskStatusRepo(db)
	todo := &domain.TaskStatus{
		ID: uuid.New(), ProjectID: proj.ID, Name: "Todo", Slug: "todo",
		Color: "#00FF00", Position: 0, Category: domain.StatusCategoryTodo, IsDefault: true,
	}
	require.NoError(t, statusRepo.Create(ctx, todo))
	done := &domain.TaskStatus{
		ID: uuid.New(), ProjectID: proj.ID, Name: "Done", Slug: "done",
		Color: "#0000FF", Position: 1, Category: domain.StatusCategoryDone,
	}
	require.NoError(t, statusRepo.Create(ctx, done))

	fx := assigneeFeedFixture{
		workspaceID: ws.ID, projectID: proj.ID, assigneeID: uuid.New(),
		todoStatus: todo.ID, doneStatus: done.ID,
	}

	taskRepo := NewTaskRepo(db)
	for i := 0; i < perCategory; i++ {
		for _, statusID := range []uuid.UUID{todo.ID, done.ID} {
			assignee := fx.assigneeID
			require.NoError(t, taskRepo.Create(ctx, &domain.Task{
				ID: uuid.New(), ProjectID: proj.ID, StatusID: statusID,
				Title:        fmt.Sprintf("feed task %d", i),
				AssigneeID:   &assignee,
				AssigneeType: domain.AssigneeTypeAgent,
				Priority:     domain.PriorityNone,
				CreatedBy:    uuid.New(), CreatedByType: domain.ActorTypeUser,
			}))
		}
	}
	return fx
}

func TestListByAssignee_NoFilterReturnsEverythingAndTotalMatches(t *testing.T) {
	db := listByAssigneeTestDB(t)
	fx := newAssigneeFeedFixture(t, db, 5)

	tasks, total, err := NewTaskRepo(db).ListByAssignee(
		context.Background(), fx.workspaceID, fx.assigneeID, domain.AssigneeTypeAgent,
		repository.AssigneeTaskFilter{})
	require.NoError(t, err)

	assert.Len(t, tasks, 10, "an unfiltered feed must still return the whole feed")
	assert.Equal(t, 10, total,
		"with no limit there is no truncation, so total must equal what was returned")
}

// The category predicate has to be in the WHERE clause, not applied afterwards:
// filtering in Go still reads and marshals every row, which is what made a
// request answering with 40 rows cost the same as one answering with 836.
func TestListByAssignee_StatusCategoryFiltersInSQL(t *testing.T) {
	db := listByAssigneeTestDB(t)
	fx := newAssigneeFeedFixture(t, db, 5)
	repo := NewTaskRepo(db)

	done := domain.StatusCategoryDone
	tasks, total, err := repo.ListByAssignee(
		context.Background(), fx.workspaceID, fx.assigneeID, domain.AssigneeTypeAgent,
		repository.AssigneeTaskFilter{StatusCategory: &done})
	require.NoError(t, err)

	require.Len(t, tasks, 5)
	assert.Equal(t, 5, total)
	for _, task := range tasks {
		assert.Equal(t, fx.doneStatus, task.StatusID,
			"a category filter must not return tasks from another category")
	}
}

func TestListByAssignee_UnusedCategoryReturnsNothing(t *testing.T) {
	db := listByAssigneeTestDB(t)
	fx := newAssigneeFeedFixture(t, db, 3)

	review := domain.StatusCategoryReview
	tasks, total, err := NewTaskRepo(db).ListByAssignee(
		context.Background(), fx.workspaceID, fx.assigneeID, domain.AssigneeTypeAgent,
		repository.AssigneeTaskFilter{StatusCategory: &review})
	require.NoError(t, err)

	assert.Empty(t, tasks)
	assert.Zero(t, total)
}

func TestListByAssignee_LimitTruncatesAndTotalStaysHonest(t *testing.T) {
	db := listByAssigneeTestDB(t)
	fx := newAssigneeFeedFixture(t, db, 5)

	tasks, total, err := NewTaskRepo(db).ListByAssignee(
		context.Background(), fx.workspaceID, fx.assigneeID, domain.AssigneeTypeAgent,
		repository.AssigneeTaskFilter{Limit: 3})
	require.NoError(t, err)

	assert.Len(t, tasks, 3, "the LIMIT must reach Postgres")
	assert.Equal(t, 10, total,
		"total is counted BEFORE the limit — a caller cannot tell a truncated feed from a "+
			"complete one otherwise, and an agent reading 3 of 10 tasks as 'all my work' is "+
			"the failure this number exists to prevent")
}

func TestListByAssignee_LimitAndCategoryCompose(t *testing.T) {
	db := listByAssigneeTestDB(t)
	fx := newAssigneeFeedFixture(t, db, 5)

	todo := domain.StatusCategoryTodo
	tasks, total, err := NewTaskRepo(db).ListByAssignee(
		context.Background(), fx.workspaceID, fx.assigneeID, domain.AssigneeTypeAgent,
		repository.AssigneeTaskFilter{StatusCategory: &todo, Limit: 2})
	require.NoError(t, err)

	require.Len(t, tasks, 2)
	assert.Equal(t, 5, total, "the total must count the category matches, not the whole feed")
	for _, task := range tasks {
		assert.Equal(t, fx.todoStatus, task.StatusID)
	}
}

func TestListByAssignee_ProjectFilterNarrows(t *testing.T) {
	db := listByAssigneeTestDB(t)
	fx := newAssigneeFeedFixture(t, db, 3)

	other := uuid.New()
	tasks, total, err := NewTaskRepo(db).ListByAssignee(
		context.Background(), fx.workspaceID, fx.assigneeID, domain.AssigneeTypeAgent,
		repository.AssigneeTaskFilter{ProjectID: &other})
	require.NoError(t, err)
	assert.Empty(t, tasks, "a project the agent has no tasks in must yield nothing")
	assert.Zero(t, total)

	tasks, _, err = NewTaskRepo(db).ListByAssignee(
		context.Background(), fx.workspaceID, fx.assigneeID, domain.AssigneeTypeAgent,
		repository.AssigneeTaskFilter{ProjectID: &fx.projectID})
	require.NoError(t, err)
	assert.Len(t, tasks, 6)
}

// The workspace predicate is the cross-tenant invariant, and adding filters must
// not have loosened it. A foreign workspace must return nothing even when every
// other term matches.
func TestListByAssignee_WorkspacePredicateSurvivesFiltering(t *testing.T) {
	db := listByAssigneeTestDB(t)
	fx := newAssigneeFeedFixture(t, db, 3)

	for _, filter := range []repository.AssigneeTaskFilter{
		{},
		{Limit: 10},
		{ProjectID: &fx.projectID},
	} {
		tasks, total, err := NewTaskRepo(db).ListByAssignee(
			context.Background(), uuid.New(), fx.assigneeID, domain.AssigneeTypeAgent, filter)
		require.NoError(t, err)
		assert.Empty(t, tasks, "another workspace's feed must never be readable")
		assert.Zero(t, total)
	}
}
