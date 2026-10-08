package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// Introduce a real committed edit at the read/write barrier, then use the
// production PostgreSQL conditional writer to reject the losing transition.
type moveInterleavingRepo struct {
	*postgres.TaskRepo
	beforeWrite func(context.Context, *domain.Task) error
}

func (r *moveInterleavingRepo) UpdateTransition(ctx context.Context, task *domain.Task, input domain.TaskTransition) error {
	if r.beforeWrite != nil {
		if err := r.beforeWrite(ctx, task); err != nil {
			return err
		}
	}
	return r.TaskRepo.UpdateTransition(ctx, task, input)
}

func TestMoveTask_PostgresAdvisoryAfterCommit(t *testing.T) {
	for _, loseCAS := range []bool{false, true} {
		t.Run(fmt.Sprintf("lose_cas=%t", loseCAS), func(t *testing.T) {
			f, repo, _, id, _ := newCheckoutM1Fixture(t)
			ctx := actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)
			statusRepo := postgres.NewTaskStatusRepo(f.db)
			from, err := statusRepo.GetByID(ctx, f.statusID)
			require.NoError(t, err)
			target := &domain.TaskStatus{ID: uuid.New(), ProjectID: f.projectID, Name: "Next", Slug: "next", Category: domain.StatusCategoryTodo, Color: "#ffffff", Position: 1}
			require.NoError(t, statusRepo.Create(ctx, target))
			rules := NewRulesService(nil, postgres.NewProjectRuleRepo(f.db), nil, nil, nil, nil, nil)
			require.NoError(t, rules.SetProjectWorkflowRules(ctx, f.projectID, domain.WorkflowRulesConfig{
				EnforcementMode: domain.RuleConfigEnforcementAdvisory, EnforceSystemActors: true,
				Transitions: map[string]domain.TransitionRule{from.Slug: {Allowed: []string{"review"}}},
			}))
			writer := &moveInterleavingRepo{TaskRepo: repo}
			if loseCAS {
				writer.beforeWrite = func(ctx context.Context, task *domain.Task) error {
					edit, editErr := repo.GetByID(ctx, task.ID)
					if editErr != nil {
						return editErr
					}
					edit.Title = "concurrent title edit"
					return repo.Update(ctx, edit)
				}
			}
			svc := NewTaskService(writer, statusRepo, nil, postgres.NewActivityLogRepo(f.db),
				WithProjectRepo(postgres.NewProjectRepo(f.db)), WithRulesConfigService(rules),
				WithEventBusService(NewEventBusService(postgres.NewEventBusMessageRepo(f.db), nil)))
			err = svc.MoveTask(ctx, id, MoveTaskInput{StatusID: &target.ID})
			fresh, readErr := repo.GetByID(ctx, id)
			require.NoError(t, readErr)
			var activity, events int
			require.NoError(t, f.db.Get(&activity, `SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action IN ('task.transition_violation','task.moved')`, id))
			require.NoError(t, f.db.Get(&events, `SELECT count(*) FROM event_bus_messages WHERE task_id=$1 AND subject IN ('task.transition_violation','task.moved')`, id))
			if loseCAS {
				var conflict *domain.TaskConflict
				require.ErrorAs(t, err, &conflict)
				require.Equal(t, f.statusID, fresh.StatusID)
				require.Equal(t, "concurrent title edit", fresh.Title)
				require.Zero(t, activity, "failed move must leave no advisory or moved activity")
				require.Zero(t, events, "failed move must publish no advisory or moved event")
			} else {
				require.NoError(t, err)
				require.Equal(t, target.ID, fresh.StatusID)
				require.Equal(t, 2, activity, "positive control: successful advisory move logs both actions")
				require.Equal(t, 2, events)
			}
		})
	}
}

func TestMoveTask_PostgresExplicitAssignment(t *testing.T) {
	for _, changedStatus := range []bool{false, true} {
		for _, rejectWrite := range []bool{false, true} {
			t.Run(fmt.Sprintf("status_change=%t/reject_write=%t", changedStatus, rejectWrite), func(t *testing.T) {
				f, repo, _, id, oldAssignee := newCheckoutM1Fixture(t)
				ctx := actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)
				nextAssignee := uuid.New()
				_, err := f.db.Exec(`INSERT INTO agents(id,workspace_id,name,slug,api_key_hash,api_key_prefix) VALUES($1,$2,'move fixture',$3,'fixture-hash','test')`, nextAssignee, f.workspaceID, nextAssignee.String())
				require.NoError(t, err)
				_, err = f.db.Exec(`UPDATE tasks SET assignee_id=$2,assignee_type='agent' WHERE id=$1`, id, oldAssignee)
				require.NoError(t, err)
				before, err := repo.GetByID(ctx, id)
				require.NoError(t, err)
				target := f.statusID
				if changedStatus {
					st := &domain.TaskStatus{ID: uuid.New(), ProjectID: f.projectID, Name: "Next", Slug: "next", Category: domain.StatusCategoryTodo, Color: "#ffffff", Position: 1}
					require.NoError(t, postgres.NewTaskStatusRepo(f.db).Create(ctx, st))
					target = st.ID
				}

				// mock: external boundary — capture push delivery without sending callbacks.
				push := NewMockAgentNotifyService()
				svc := NewTaskService(repo, postgres.NewTaskStatusRepo(f.db), nil, postgres.NewActivityLogRepo(f.db),
					WithProjectRepo(postgres.NewProjectRepo(f.db)), WithTaskAgentRepo(postgres.NewAgentRepo(f.db)),
					WithProjectMemberRepoTask(postgres.NewProjectMemberRepo(f.db)), WithAgentNotifyService(push),
					WithEventBusService(NewEventBusService(postgres.NewEventBusMessageRepo(f.db), nil)))

				if rejectWrite {
					// A real CHECK refusal on this fixture's isolated task table. BeginTx
					// explicitly chooses READ WRITE, so session default_transaction_read_only
					// no longer injects a write failure once the transition is transactional.
					f.db.SetMaxOpenConns(1)
					f.db.SetMaxIdleConns(1)
					schema := pq.QuoteIdentifier("refuse_" + uuid.NewString())
					// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifiers are pq.QuoteIdentifier of generated UUIDs; table selectors are fixed literals, values are bound or canonical UUIDs; no external input
					_, err = f.db.Exec(`CREATE SCHEMA ` + schema)
					require.NoError(t, err)
					t.Cleanup(func() {
						// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifiers are pq.QuoteIdentifier of generated UUIDs; table selectors are fixed literals, values are bound or canonical UUIDs; no external input
						_, e := f.db.Exec(`SET search_path=public; DROP SCHEMA ` + schema + ` CASCADE`)
						require.NoError(t, e)
					})
					// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifiers are pq.QuoteIdentifier of generated UUIDs; table selectors are fixed literals, values are bound or canonical UUIDs; no external input
					_, err = f.db.Exec(`CREATE TABLE ` + schema + `.tasks(LIKE public.tasks INCLUDING ALL)`)
					require.NoError(t, err)
					_, err = f.db.Exec(`INSERT INTO `+schema+`.tasks SELECT * FROM public.tasks WHERE id=$1`, id)
					require.NoError(t, err)
					// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifiers are pq.QuoteIdentifier of generated UUIDs; table selectors are fixed literals, values are bound or canonical UUIDs; no external input
					_, err = f.db.Exec(`ALTER TABLE ` + schema + `.tasks ADD CONSTRAINT refuse_update CHECK (id <> '` + id.String() + `'::uuid) NOT VALID; SET search_path=` + schema + `,public`)
					require.NoError(t, err)
				}

				input := MoveTaskInput{StatusID: &target, AssigneeID: &nextAssignee, AssigneeType: domain.AssigneeTypeAgent, Source: "api"}
				err = svc.MoveTask(ctx, id, input)
				if rejectWrite {
					require.ErrorContains(t, err, "refuse_update", "database refusal must not report success")
				} else {
					require.NoError(t, err)
				}
				fresh, err := repo.GetByID(ctx, id)
				require.NoError(t, err)
				var assigned, moved, events int
				require.NoError(t, f.db.Get(&assigned, `SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action='task.assigned'`, id))
				require.NoError(t, f.db.Get(&moved, `SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action='task.moved'`, id))
				require.NoError(t, f.db.Get(&events, `SELECT count(*) FROM event_bus_messages WHERE task_id=$1 AND subject IN ('task.assigned','task.moved')`, id))
				if rejectWrite {
					require.Equal(t, before.AssigneeID, fresh.AssigneeID)
					require.Equal(t, before.StatusID, fresh.StatusID, "status and explicit assignment must commit together")
					require.Equal(t, before.UpdatedAt, fresh.UpdatedAt)
					require.Zero(t, assigned)
					require.Zero(t, moved)
					require.Zero(t, events)
					require.Empty(t, push.Calls(), "failed write must not notify")
					return
				}
				require.Equal(t, &nextAssignee, fresh.AssigneeID, "same-status move must apply explicit assignee")
				require.Equal(t, domain.AssigneeTypeAgent, fresh.AssigneeType)
				require.Equal(t, target, fresh.StatusID)
				require.True(t, fresh.UpdatedAt.After(before.UpdatedAt))
				require.Equal(t, 1, assigned)
				expectedMoved := 0
				if changedStatus {
					expectedMoved = 1
				}
				require.Equal(t, expectedMoved, moved, "assignment alone is not a status move")
				require.Equal(t, 1+expectedMoved, events)
				require.Len(t, push.Calls(), 1+expectedMoved)
				require.Equal(t, "task.assigned", push.Calls()[0].EventType)
				require.Equal(t, &nextAssignee, push.Calls()[0].Task["assignee_id"])

				// Retrying an identical request is a no-op, including its notifications.
				require.NoError(t, svc.MoveTask(ctx, id, input))
				retried, err := repo.GetByID(ctx, id)
				require.NoError(t, err)
				require.Equal(t, fresh.UpdatedAt, retried.UpdatedAt)
				require.NoError(t, f.db.Get(&events, `SELECT count(*) FROM event_bus_messages WHERE task_id=$1 AND subject IN ('task.assigned','task.moved')`, id))
				require.Equal(t, 1+expectedMoved, events)
				require.Len(t, push.Calls(), 1+expectedMoved)
			})
		}
	}
}

func TestMoveTask_PostgresAtomicParkAlarm(t *testing.T) {
	f, repo, _, id, _ := newCheckoutM1Fixture(t)
	ctx := actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)
	backlog := &domain.TaskStatus{ID: uuid.New(), ProjectID: f.projectID, Name: "Parked", Slug: "parked", Category: domain.StatusCategoryBacklog, Color: "#ffffff", Position: 2}
	require.NoError(t, postgres.NewTaskStatusRepo(f.db).Create(ctx, backlog))
	_, err := f.db.Exec(`UPDATE tasks SET labels=ARRAY['kind:monitor'] WHERE id=$1`, id)
	require.NoError(t, err)
	before, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	svc := NewTaskService(repo, postgres.NewTaskStatusRepo(f.db), nil, postgres.NewActivityLogRepo(f.db))
	due := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	input := MoveTaskInput{StatusID: &backlog.ID, AlarmDue: &due, AlarmLabels: []string{"kind:monitor", "monitor"}}
	require.NoError(t, svc.MoveTask(ctx, id, input), "gate must evaluate the alarm committed with the move")
	after, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	require.Equal(t, backlog.ID, after.StatusID)
	require.Equal(t, &due, after.DueDate)
	require.Contains(t, after.Labels, "monitor")
	require.Equal(t, before.Version+1, after.Version, "alarm and status share one write")
}
