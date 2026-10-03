package service

// Ratchet for audit finding #819e7b29 (task #2e98f50c): every task creation
// path must leave an activity_log row (entity_id = task.id, action =
// 'task.created') atomically with the task insert — fix fc3136dd made
// TaskRepo.Create take the activity entry as a parameter for exactly this
// contract. The audit found 314/3709 tasks (16.08–14.09, all created_by
// system) with no event; the fix closed the gap, and this file is the check
// that catches its return.
//
// No //go:build integration tag, deliberately — same reasoning as
// recurring_empty_actor_type_db_test.go: CI's untagged `go test ./...` in the
// `test` job runs this against a migrated DATABASE_URL on every pipeline; a
// plain local run skips if no DB is reachable.
//
// Each subtest drives one creation path with the context shape that path's
// production caller produces (handler DualAuth → actorctx user/agent, the
// cmd/api recurring ticker → bare context.Background()). The final subtest is
// the reconciliation the prod read-only check runs (tasks without a
// task.created event), and the red-control subtest proves that reconciliation
// has teeth by deleting a real event and demanding the entity_id back.
//
// The reconciliation scopes by explicit task ids, never a created_at window:
// taskSvc.Create stamps CreatedAt from the package-level timeNow hook
// (task_service.go), which task_checkout_test.go's setupCheckoutTaskService
// re-points at a frozen 2025 clock without restoring it — every task this
// fixture creates mid-suite is dated 2025-06-15, so a time-windowed query
// would go vacuously green (found that in a full-package run: the red control
// passed DELETE then saw an empty missing list). The prod read-only check
// keeps the 7-day window because it cannot know the ids; the NOT EXISTS core
// is identical.

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// ratchetFixture extends newRecurringActorTypeFixture with the extra repos the
// remaining creation paths need. Same workspace/project/status rows, same real
// TaskService wiring as cmd/api/main.go.
type ratchetFixture struct {
	*recurringActorTypeFixture
	templateRepo *postgres.TaskTemplateRepo
	agentID      uuid.UUID
	// taskIDs collects every task the subtests create, in creation order —
	// the exact scope the reconciliation runs over.
	taskIDs []uuid.UUID
}

// trackTask registers a created task for the reconciliation subtests.
func (f *ratchetFixture) trackTask(t *testing.T, taskID uuid.UUID) {
	t.Helper()
	f.taskIDs = append(f.taskIDs, taskID)
}

func newRatchetFixture(t *testing.T) *ratchetFixture {
	t.Helper()
	f := &ratchetFixture{recurringActorTypeFixture: newRecurringActorTypeFixture(t)}
	f.templateRepo = postgres.NewTaskTemplateRepo(f.db)
	t.Cleanup(func() {
		_, _ = f.db.ExecContext(context.Background(),
			"DELETE FROM task_templates WHERE project_id = $1", f.projectID)
		_, _ = f.db.ExecContext(context.Background(),
			"DELETE FROM activity_log WHERE workspace_id = $1", f.workspaceID)
	})

	// A real agents row: the agent-actor path auto-enrolls the creating agent
	// into project_members, whose agent_id has an FK to agents — a made-up
	// uuid would fail the task insert for a reason unrelated to what this
	// ratchet tests.
	agent := &domain.Agent{
		ID:          uuid.New(),
		WorkspaceID: f.workspaceID,
		Name:        "ratchet-agent",
		Slug:        "ratchet-agent-" + uuid.NewString()[:8],
		AgentType:   domain.AgentTypeClaudeCode,
		Status:      domain.AgentStatusOffline,
	}
	require.NoError(t, postgres.NewAgentRepo(f.db).Create(context.Background(), agent))
	t.Cleanup(func() {
		_, _ = f.db.ExecContext(context.Background(), "DELETE FROM agents WHERE id = $1", agent.ID)
	})
	f.agentID = agent.ID

	return f
}

// createTaskDirect mirrors the minimal task shape every taskSvc.Create caller
// hands over: pinned status (never resolved through defaults), unassigned, no
// foreign keys beyond the fixture's project.
func (f *ratchetFixture) createTaskDirect(t *testing.T, ctx context.Context, title string, actorID uuid.UUID, actorType domain.ActorType) *domain.Task {
	task := &domain.Task{
		ID: uuid.New(), ProjectID: f.projectID, StatusID: f.statusID,
		Title: title, AssigneeType: domain.AssigneeTypeUnassigned,
		Priority: domain.PriorityMedium, CreatedBy: actorID, CreatedByType: actorType,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, f.taskSvc.Create(ctx, task))
	return task
}

// requireTaskCreatedEvent asserts the task.created row exists for the task and
// — when wantActorType is non-empty — that the event is attributed to that
// actor. The per-path actor assertion is what pins the fd8a3e43 class: an
// event written with a broken/empty actor would still "exist".
func (f *ratchetFixture) requireTaskCreatedEvent(t *testing.T, taskID uuid.UUID, wantActorType, what string) {
	t.Helper()
	var actorType string
	err := f.db.GetContext(context.Background(), &actorType,
		`SELECT actor_type FROM activity_log WHERE entity_id = $1 AND action = 'task.created'`, taskID)
	require.NoError(t, err,
		"%s: task %s has no task.created activity_log row — the #819e7b29 defect class", what, taskID)
	if wantActorType != "" {
		assert.Equal(t, wantActorType, actorType,
			"%s: task %s event actor_type must be %q, got %q", what, taskID, wantActorType, actorType)
	}
}

// missingTaskCreatedEvents is the reconciliation: every tracked task must
// have a task.created event. Returns the offending entity_ids — the same NOT
// EXISTS core as the prod read-only check, scoped by the ids this run created
// (see the file header for why not a created_at window).
func (f *ratchetFixture) missingTaskCreatedEvents(t *testing.T, ids []uuid.UUID) []string {
	t.Helper()
	// pq.Array is lib/pq's explicit array encoding (this repo's driver, see
	// go.mod) — verified empirically that both it and the raw slice bind fine
	// to ANY($1); the wrapper makes the array intent explicit instead of
	// relying on the driver's reflection path.
	rows, err := f.db.QueryContext(context.Background(),
		`SELECT t.id::text
		   FROM tasks t
		  WHERE t.id = ANY($1)
		    AND NOT EXISTS (SELECT 1 FROM activity_log a
		                     WHERE a.entity_id = t.id AND a.action = 'task.created')
		  ORDER BY t.created_at`, pq.Array(ids))
	require.NoError(t, err)
	defer rows.Close()
	var missing []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		missing = append(missing, id)
	}
	require.NoError(t, rows.Err())
	return missing
}

func TestTaskCreatedActivityRatchet_EveryCreationPathLeavesEvent(t *testing.T) {
	f := newRatchetFixture(t)

	var parentForSubtask *domain.Task

	t.Run("service: user-actor context (REST handler shape)", func(t *testing.T) {
		// The context shape the task handler produces after JWT auth. The
		// HTTP entry points themselves are exercised by the companion
		// internal/handler/task_created_activity_ratchet_http_test.go.
		ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeUser)
		task := f.createTaskDirect(t, ctx, "ratchet: human path", uuid.New(), domain.ActorTypeUser)
		f.trackTask(t, task.ID)
		f.requireTaskCreatedEvent(t, task.ID, "user", "user-actor service path")
		parentForSubtask = task
	})

	t.Run("service: agent-actor context (X-Agent-Key handler shape)", func(t *testing.T) {
		// The context shape DualAuth produces for an agent key; the agent row
		// is real because this path auto-enrolls the creator into the project.
		ctx := actorctx.WithActor(context.Background(), f.agentID, domain.ActorTypeAgent)
		task := f.createTaskDirect(t, ctx, "ratchet: agent path", f.agentID, domain.ActorTypeAgent)
		f.trackTask(t, task.ID)
		f.requireTaskCreatedEvent(t, task.ID, "agent", "agent-actor service path")
	})

	t.Run("recurring scheduler (bare ticker context)", func(t *testing.T) {
		// Exactly what cmd/api/main.go's ticker passes: no actorctx anywhere
		// on the chain (the fd8a3e43 call shape).
		rs := &recurringService{taskSvc: f.taskSvc}
		task, err := rs.createInstance(context.Background(), f.newSchedule(t), time.Now())
		require.NoError(t, err)
		f.trackTask(t, task.ID)
		f.requireTaskCreatedEvent(t, task.ID, "system", "recurring scheduler path")
	})

	t.Run("system bare-context caller", func(t *testing.T) {
		// The audit's 314 tasks were all created_by system on contexts like
		// this — a background caller with no actor at all.
		task := f.createTaskDirect(t, context.Background(), "ratchet: system path", uuid.Nil, domain.ActorTypeSystem)
		f.trackTask(t, task.ID)
		f.requireTaskCreatedEvent(t, task.ID, "system", "system bare-context path")
	})

	t.Run("template instantiation", func(t *testing.T) {
		tmpl := &domain.TaskTemplate{
			ID: uuid.New(), ProjectID: f.projectID, Name: "ratchet template",
			TitleTemplate: "ratchet: template path", Priority: domain.PriorityMedium,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		require.NoError(t, f.templateRepo.Create(context.Background(), tmpl))
		tmplSvc := NewTaskTemplateService(f.templateRepo, f.taskSvc)
		ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeUser)
		task, err := tmplSvc.CreateTaskFromTemplate(ctx, tmpl.ID, uuid.New(), domain.ActorTypeUser, nil)
		require.NoError(t, err)
		f.trackTask(t, task.ID)
		f.requireTaskCreatedEvent(t, task.ID, "user", "template instantiation path")
	})

	t.Run("subtask (second taskRepo.Create call site)", func(t *testing.T) {
		require.NotNil(t, parentForSubtask, "human-path task must exist to parent the subtask")
		ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeUser)
		child, err := f.taskSvc.CreateSubtask(ctx, parentForSubtask.ID, CreateSubtaskInput{
			Title: "ratchet: subtask path", Priority: domain.PriorityMedium, StatusID: &f.statusID,
		})
		require.NoError(t, err)
		f.trackTask(t, child.ID)
		f.requireTaskCreatedEvent(t, child.ID, "", "subtask path")
	})

	t.Run("red control: a deleted event is caught by entity_id", func(t *testing.T) {
		// AC1 shape: a task whose event is gone (any path that silently stops
		// writing the journal looks exactly like this from the outside). The
		// reconciliation must return its entity_id — if this ever fails, the
		// ratchet has gone blind (SQL typo, wrong scope) and guards nothing.
		ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeUser)
		victim := f.createTaskDirect(t, ctx, "ratchet: red control victim", uuid.New(), domain.ActorTypeUser)
		f.trackTask(t, victim.ID)
		_, err := f.db.ExecContext(context.Background(),
			`DELETE FROM activity_log WHERE entity_id = $1 AND action = 'task.created'`, victim.ID)
		require.NoError(t, err)

		missing := f.missingTaskCreatedEvents(t, f.taskIDs)
		require.Contains(t, missing, victim.ID.String(),
			"the reconciliation must name the task whose event was deleted")
		require.Len(t, missing, 1,
			"exactly one tracked task may be missing its event (the victim); got %v", missing)

		// Restore the invariant so the aggregate below reconciles to zero:
		// remove the orphaned task itself.
		_, err = f.db.ExecContext(context.Background(), `DELETE FROM tasks WHERE id = $1`, victim.ID)
		require.NoError(t, err)
	})

	t.Run("reconcile: no tracked task lacks its event", func(t *testing.T) {
		missing := f.missingTaskCreatedEvents(t, f.taskIDs)
		require.Empty(t, missing,
			"tasks created without a task.created activity_log row (the #819e7b29 defect returning): %v", missing)
	})
}
