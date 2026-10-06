package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

type transitionReadBarrier struct {
	repository.TaskRepository
	taskID    uuid.UUID
	reads     int
	ready     chan struct{}
	committed chan error
}

func (r *transitionReadBarrier) GetByID(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	if id == r.taskID {
		r.reads++
		if r.reads == 2 && r.ready != nil {
			close(r.ready)
			if err := <-r.committed; err != nil {
				return nil, err
			}
		}
	}
	return r.TaskRepository.GetByID(ctx, id)
}

// Untagged: the standard CI test job supplies migrated PostgreSQL. These are
// real commits by a second connection between the candidate and its fresh GET,
// including a new dependency edge which does not update tasks.updated_at.
func TestAutoTransitionM4_PostgresBarrier(t *testing.T) {
	for _, change := range []string{"positive_duplicate", "reblock", "status", "human_gate"} {
		t.Run(change, func(t *testing.T) {
			f := newRecurringActorTypeFixture(t)
			ctx := actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)
			tasks := postgres.NewTaskRepo(f.db)
			statuses := postgres.NewTaskStatusRepo(f.db)
			deps := postgres.NewTaskDependencyRepo(f.db)
			activity := postgres.NewActivityLogRepo(f.db)
			svc := NewTaskService(tasks, statuses, deps, activity, WithProjectRepo(postgres.NewProjectRepo(f.db)))
			createStatus := func(category domain.StatusCategory) *domain.TaskStatus {
				st := &domain.TaskStatus{ID: uuid.New(), ProjectID: f.projectID, Name: string(category), Slug: string(category), Category: category, Color: "#ffffff", Position: 1}
				require.NoError(t, statuses.Create(ctx, st))
				return st
			}
			backlog := createStatus(domain.StatusCategoryBacklog)
			done := createStatus(domain.StatusCategoryDone)
			createTask := func(status uuid.UUID, labels []string) *domain.Task {
				task := &domain.Task{ID: uuid.New(), ProjectID: f.projectID, StatusID: status, Title: "transition fixture", Labels: labels, AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium, CreatedByType: domain.ActorTypeSystem}
				require.NoError(t, svc.Create(ctx, task))
				return task
			}
			blocked := createTask(backlog.ID, []string{"park:dependency"})
			blocker := createTask(done.ID, nil)
			unresolved := createTask(f.statusID, nil)
			require.NoError(t, deps.Create(ctx, &domain.TaskDependency{ID: uuid.New(), TaskID: blocked.ID, DependsOnTaskID: blocker.ID, DependencyType: domain.DependencyTypeBlocks, CreatedAt: time.Now()}))
			barrier := &transitionReadBarrier{TaskRepository: tasks, taskID: blocked.ID}
			if change != "positive_duplicate" {
				barrier.ready = make(chan struct{})
				barrier.committed = make(chan error, 1)
				go func() {
					<-barrier.ready
					var err error
					switch change {
					case "reblock":
						err = deps.Create(ctx, &domain.TaskDependency{ID: uuid.New(), TaskID: blocked.ID, DependsOnTaskID: unresolved.ID, DependencyType: domain.DependencyTypeBlocks, CreatedAt: time.Now()})
					case "status":
						_, err = f.db.ExecContext(ctx, "UPDATE tasks SET status_id=$1,updated_at=now() WHERE id=$2", f.statusID, blocked.ID)
					case "human_gate":
						_, err = f.db.ExecContext(ctx, "UPDATE tasks SET human_gate=true,updated_at=now() WHERE id=$1", blocked.ID)
					}
					barrier.committed <- err
				}()
			}
			auto := NewAutoTransitionService(barrier, statuses, deps, svc, postgres.NewAutoTransitionRuleRepo(f.db), nil)
			require.NoError(t, auto.CheckDependencyResolution(ctx, blocker.ID))
			var count int
			require.NoError(t, f.db.GetContext(ctx, &count, "SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action='task.moved'", blocked.ID))
			fresh, err := tasks.GetByID(ctx, blocked.ID)
			require.NoError(t, err)
			if change == "positive_duplicate" {
				require.Equal(t, f.statusID, fresh.StatusID)
				require.Equal(t, 1, count)
				require.NoError(t, auto.CheckDependencyResolution(ctx, blocker.ID))
				require.NoError(t, f.db.GetContext(ctx, &count, `SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action='task.moved' AND changes->>'source'='auto_transition' AND changes->>'reason'='blocking_dep_resolved' AND changes->>'trigger_task_id'=$2`, blocked.ID, blocker.ID.String()))
				require.Equal(t, 1, count)
			} else {
				require.Equal(t, 0, count, "concurrent reblock/status/gate must not cause an automatic write")
				if change != "status" {
					require.Equal(t, backlog.ID, fresh.StatusID)
				}
			}
			t.Cleanup(func() {
				_, _ = f.db.ExecContext(context.Background(), "DELETE FROM activity_log WHERE workspace_id=$1", f.workspaceID)
			})
		})
	}
}
