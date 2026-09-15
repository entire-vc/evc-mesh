//go:build integration

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
)

// Regression test for #819e7b29: 314 of 3709 system-created tasks over a 30-day
// window lost their task.created activity_log row to a fire-and-forget,
// post-commit, non-retried write under concurrent recurring-tick load, with no
// error surfaced anywhere. TaskRepo.Create now writes the task row and its
// creation activity_log row in ONE transaction — this file proves that is
// actually true, not just "usually both happen".

type createAtomicityFixture struct {
	db          *sqlx.DB
	workspaceID uuid.UUID
	projectID   uuid.UUID
	statusID    uuid.UUID
}

func newCreateAtomicityFixture(t *testing.T) *createAtomicityFixture {
	t.Helper()
	db := testDB(t)
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	ws := &domain.Workspace{
		ID: uuid.New(), Name: "create-atomicity-ws", Slug: "create-atomicity-ws-" + suffix, OwnerID: uuid.New(),
	}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, ws))

	proj := &domain.Project{
		ID: uuid.New(), WorkspaceID: ws.ID, Name: "create-atomicity-proj",
		Slug: "create-atomicity-proj-" + suffix, DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	require.NoError(t, NewProjectRepo(db).Create(ctx, proj))

	status := &domain.TaskStatus{
		ID: uuid.New(), ProjectID: proj.ID, Name: "Open", Slug: "open",
		Color: "#00FF00", Position: 0, Category: domain.StatusCategoryTodo, IsDefault: true,
	}
	require.NoError(t, NewTaskStatusRepo(db).Create(ctx, status))

	return &createAtomicityFixture{db: db, workspaceID: ws.ID, projectID: proj.ID, statusID: status.ID}
}

func (f *createAtomicityFixture) newTask() *domain.Task {
	return &domain.Task{
		ID: uuid.New(), ProjectID: f.projectID, StatusID: f.statusID,
		Title: "atomicity test task", AssigneeType: domain.AssigneeTypeUnassigned,
		Priority: domain.PriorityMedium, CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
}

func countActivityLogRows(t *testing.T, db *sqlx.DB, entityID uuid.UUID, action string) int {
	t.Helper()
	var n int
	err := db.GetContext(context.Background(), &n,
		`SELECT COUNT(*) FROM activity_log WHERE entity_id = $1 AND action = $2`, entityID, action)
	require.NoError(t, err)
	return n
}

// TestTaskRepo_Create_WritesTaskAndActivityTogether is the positive control: a
// valid activity entry, in the normal case, produces exactly one task row and
// exactly one matching activity_log row.
func TestTaskRepo_Create_WritesTaskAndActivityTogether(t *testing.T) {
	f := newCreateAtomicityFixture(t)
	ctx := context.Background()
	task := f.newTask()

	activity := &domain.ActivityLog{
		ID: uuid.New(), WorkspaceID: f.workspaceID, EntityType: "task", EntityID: task.ID,
		Action: "task.created", ActorID: uuid.New(), ActorType: domain.ActorTypeAgent,
		Changes: nil, CreatedAt: time.Now().UTC(),
	}

	require.NoError(t, NewTaskRepo(f.db).Create(ctx, task, activity))

	got, err := NewTaskRepo(f.db).GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got, "task must exist after a successful Create")

	assert.Equal(t, 1, countActivityLogRows(t, f.db, task.ID, "task.created"),
		"exactly one task.created activity_log row must exist alongside the task")
}

// TestTaskRepo_Create_NilActivitySkipsLogging documents the escape hatch used
// by fixtures/tests that don't care about the audit trail: passing activity =
// nil creates the task with no activity_log row and no error. Real service
// call sites always pass a real entry (see task_service.go); nil exists for
// call sites — mostly test fixtures — that have no workspace context to build
// one from.
func TestTaskRepo_Create_NilActivitySkipsLogging(t *testing.T) {
	f := newCreateAtomicityFixture(t)
	ctx := context.Background()
	task := f.newTask()

	require.NoError(t, NewTaskRepo(f.db).Create(ctx, task, nil))

	got, err := NewTaskRepo(f.db).GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, 0, countActivityLogRows(t, f.db, task.ID, "task.created"))
}

// TestTaskRepo_Create_ActivityFailureRollsBackTheTaskToo is the negative
// control that actually proves atomicity rather than assuming it: an activity
// entry with a workspace_id that violates activity_log's FK to workspaces(id)
// makes the activity INSERT fail INSIDE the same transaction as the task
// INSERT. If Create only "usually" wrote both (the #819e7b29 bug's shape —
// task committed, activity fire-and-forget), the task would still exist here.
// With a real single transaction, Create must return an error AND the task
// must NOT exist — same contract as "a task created with a foreign assignee
// must not exist at all" elsewhere in this package.
func TestTaskRepo_Create_ActivityFailureRollsBackTheTaskToo(t *testing.T) {
	f := newCreateAtomicityFixture(t)
	ctx := context.Background()
	task := f.newTask()

	bogusWorkspace := uuid.New() // deliberately never inserted into workspaces
	activity := &domain.ActivityLog{
		ID: uuid.New(), WorkspaceID: bogusWorkspace, EntityType: "task", EntityID: task.ID,
		Action: "task.created", ActorID: uuid.New(), ActorType: domain.ActorTypeAgent,
		Changes: nil, CreatedAt: time.Now().UTC(),
	}

	err := NewTaskRepo(f.db).Create(ctx, task, activity)
	require.Error(t, err, "an activity_log FK violation must fail the whole Create")

	got, gerr := NewTaskRepo(f.db).GetByID(ctx, task.ID)
	require.NoError(t, gerr)
	assert.Nil(t, got,
		"a task whose creation event could not be recorded must not exist at all — "+
			"same contract as the assignee-tenancy guard, and the whole point of #819e7b29's fix")
	assert.Equal(t, 0, countActivityLogRows(t, f.db, task.ID, "task.created"))
}
