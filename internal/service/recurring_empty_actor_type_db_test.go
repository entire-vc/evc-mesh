package service

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// Regression test for fd8a3e43 (parent incident #9b508f76): commit fc3136dd made
// TaskRepo.Create write the task.created activity_log row in the SAME transaction
// as the task insert, reading actor_type from actorctx.FromContext(ctx) — correct
// for #819e7b29, but the recurring scheduler's ticker (cmd/api/main.go) calls
// recurringService.RunDue on a bare context.Background(), which was never wrapped
// with actorctx.WithActor. actorctx.FromContext then returns actorType == "",
// activity_log.actor_type is a NOT NULL enum with no DEFAULT, so the INSERT fails
// with Postgres error 22P02 ("invalid input value for enum actor_type") and the
// whole transaction — including the task row itself — rolls back. Every recurring
// schedule in every project failed on its next tick, not just the ones observed
// first.
//
// No //go:build integration tag, deliberately — same reasoning as
// pushMembershipTestDB / userRepoTestDB: CI's untagged `go test ./...` against a
// migrated DATABASE_URL runs this; a plain local run skips if no DB is reachable.

func recurringActorTypeTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5437/mesh?sslmode=disable"
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

// recurringActorTypeFixture is one workspace/project/status, real Postgres rows,
// wired to a real TaskService (real TaskRepo + real ProjectRepo) exactly the way
// cmd/api/main.go wires the production recurring ticker.
type recurringActorTypeFixture struct {
	db            *sqlx.DB
	workspaceID   uuid.UUID
	projectID     uuid.UUID
	statusID      uuid.UUID
	taskSvc       TaskService
	recurringRepo *postgres.RecurringRepo
}

func newRecurringActorTypeFixture(t *testing.T) *recurringActorTypeFixture {
	t.Helper()
	db := recurringActorTypeTestDB(t)
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	ws := &domain.Workspace{
		ID: uuid.New(), Name: "recurring-actor-type-ws", Slug: "recurring-actor-type-ws-" + suffix, OwnerID: uuid.New(),
	}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(ctx, ws))
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DELETE FROM workspaces WHERE id = $1", ws.ID) })

	proj := &domain.Project{
		ID: uuid.New(), WorkspaceID: ws.ID, Name: "recurring-actor-type-proj",
		Slug: "recurring-actor-type-proj-" + suffix, DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	require.NoError(t, postgres.NewProjectRepo(db).Create(ctx, proj))
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", proj.ID) })

	status := &domain.TaskStatus{
		ID: uuid.New(), ProjectID: proj.ID, Name: "Open", Slug: "open",
		Color: "#00FF00", Position: 0, Category: domain.StatusCategoryTodo, IsDefault: true,
	}
	require.NoError(t, postgres.NewTaskStatusRepo(db).Create(ctx, status))
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", proj.ID) })
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", proj.ID) })

	taskSvc := NewTaskService(
		postgres.NewTaskRepo(db), postgres.NewTaskStatusRepo(db), nil, nil,
		WithProjectRepo(postgres.NewProjectRepo(db)),
	)

	return &recurringActorTypeFixture{
		db: db, workspaceID: ws.ID, projectID: proj.ID, statusID: status.ID,
		taskSvc: taskSvc, recurringRepo: postgres.NewRecurringRepo(db),
	}
}

// newSchedule creates and PERSISTS a minimal, valid schedule for createInstance
// (tasks.recurring_schedule_id has an FK to recurring_schedules(id), so the
// schedule has to be a real row, not just an in-memory struct). StatusID is set
// explicitly so createInstance never needs GetDefaultStatus (this fixture's
// TaskService's statusRepo has no default-status row seeded — the point here is
// isolating the actor_type bug, not exercising status resolution). Unassigned,
// like a system-owned recurring task.
func (f *recurringActorTypeFixture) newSchedule(t *testing.T) *domain.RecurringSchedule {
	t.Helper()
	sched := &domain.RecurringSchedule{
		ID:            uuid.New(),
		WorkspaceID:   f.workspaceID,
		ProjectID:     f.projectID,
		TitleTemplate: "Recurring instance {{.Number}}",
		Frequency:     domain.RecurringFrequencyCustom,
		CronExpr:      "0 9 * * *",
		Timezone:      "UTC",
		StatusID:      &f.statusID,
		AssigneeType:  domain.AssigneeTypeUnassigned,
		Priority:      domain.PriorityMedium,
		CreatedBy:     uuid.New(),
		CreatedByType: domain.ActorTypeSystem,
		StartsAt:      time.Now().UTC(),
		InstanceCount: 0, // skip getPreviousInstanceSummary
	}
	require.NoError(t, f.recurringRepo.Create(context.Background(), sched))
	t.Cleanup(func() {
		_, _ = f.db.ExecContext(context.Background(), "DELETE FROM recurring_schedules WHERE id = $1", sched.ID)
	})
	return sched
}

func countActivityLogRowsForActorType(t *testing.T, db *sqlx.DB, entityID uuid.UUID) int {
	t.Helper()
	var n int
	err := db.GetContext(context.Background(), &n,
		`SELECT COUNT(*) FROM activity_log WHERE entity_id = $1 AND action = 'task.created'`, entityID)
	require.NoError(t, err)
	return n
}

// TestRecurringService_CreateInstance_BackgroundCtx_DoesNotCrash is the RED/GREEN
// control for this fix. Before the fix: recurringService.createInstance on a bare
// context.Background() (exactly what the 60s ticker in cmd/api/main.go passes)
// fails with Postgres SQLSTATE 22P02 ("invalid input value for enum actor_type")
// and creates no task at all — the fleet-wide outage this task fixes. After the
// fix: the same call succeeds and the task exists with a real activity_log row.
func TestRecurringService_CreateInstance_BackgroundCtx_DoesNotCrash(t *testing.T) {
	f := newRecurringActorTypeFixture(t)
	rs := &recurringService{taskSvc: f.taskSvc}

	// The exact context shape the production ticker uses: no actorctx.WithActor
	// anywhere on the call chain (cmd/api/main.go:1855, recurringService.RunDue,
	// runOneSchedule, createInstance all pass this ctx through unmodified).
	ctx := context.Background()

	task, err := rs.createInstance(ctx, f.newSchedule(t), time.Now())
	require.NoError(t, err, "createInstance on a bare context.Background() must not fail — "+
		"this is exactly the recurring ticker's call shape and it must never crash the whole "+
		"transaction with SQLSTATE 22P02 (invalid empty actor_type)")
	require.NotNil(t, task)

	got, gerr := postgres.NewTaskRepo(f.db).GetByID(ctx, task.ID)
	require.NoError(t, gerr)
	require.NotNil(t, got, "the task must actually exist — the pre-fix bug rolled back the whole "+
		"transaction, so the task row never committed even though createInstance's caller saw an error")

	assert.Equal(t, 1, countActivityLogRowsForActorType(t, f.db, task.ID),
		"a task.created activity_log row must exist alongside the task (same atomicity contract as #819e7b29)")

	var actorType string
	require.NoError(t, f.db.GetContext(ctx, &actorType,
		`SELECT actor_type FROM activity_log WHERE entity_id = $1 AND action = 'task.created'`, task.ID))
	assert.Equal(t, "system", actorType,
		"a task created from an unauthenticated background context must be attributed to the system actor, not left empty")
}

// TestTaskRepo_Create_EmptyActorTypeIsRejectedByPostgres pins down the exact
// failure mode this task's diagnosis depends on: an empty ActorType is not
// silently coerced or defaulted by the database — it is a real enum violation.
// This is what proves the fix has to happen in Go (the service layer), not that
// there's some laxer DB behavior to lean on.
func TestTaskRepo_Create_EmptyActorTypeIsRejectedByPostgres(t *testing.T) {
	f := newRecurringActorTypeFixture(t)
	ctx := context.Background()

	task := &domain.Task{
		ID: uuid.New(), ProjectID: f.projectID, StatusID: f.statusID,
		Title: "empty actor_type probe", AssigneeType: domain.AssigneeTypeUnassigned,
		Priority: domain.PriorityMedium, CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeSystem,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	changesJSON, _ := json.Marshal(map[string]interface{}{"title": map[string]interface{}{"old": nil, "new": task.Title}})
	activity := &domain.ActivityLog{
		ID: uuid.New(), WorkspaceID: uuid.Nil, EntityType: "task", EntityID: task.ID,
		Action: "task.created", ActorID: uuid.Nil, ActorType: "", // the exact shape actorctx.FromContext(context.Background()) returns
		Changes: changesJSON, CreatedAt: time.Now().UTC(),
	}

	err := postgres.NewTaskRepo(f.db).Create(ctx, task, activity)
	require.Error(t, err, "Postgres must reject an empty actor_type enum value")
	assert.Contains(t, err.Error(), "22P02", "must fail with the exact SQLSTATE the incident reported")

	got, gerr := postgres.NewTaskRepo(f.db).GetByID(ctx, task.ID)
	require.NoError(t, gerr)
	assert.Nil(t, got, "the whole transaction — including the task row — must roll back, per the "+
		"#819e7b29 atomicity contract this incident is a side effect of")
}
