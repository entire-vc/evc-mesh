package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// A completed reviewer subtask does not finish its parent's installation or
// live acceptance. Exercise the same persisted rule and service path as Lab.
func TestAutoTransitionPostMergeAC_ParentCompletion(t *testing.T) {
	for _, tc := range []struct {
		name        string
		labels      []string
		description string
		fromReview  bool
		dependency  bool
		lateLabel   bool
		wantHold    bool
	}{
		{name: "ordinary_parent"},
		{name: "explicit_label", labels: []string{LabelPostMergeAC}, wantHold: true},
		{name: "description", description: "Run live acceptance after deploy", wantHold: true},
		{name: "label_added_before_fresh_read", lateLabel: true, wantHold: true},
		{name: "reviewed_umbrella", labels: []string{"kind:umbrella", LabelPostMergeAC}, fromReview: true},
		{name: "dependency_release_with_marker", labels: []string{"park:dependency", LabelPostMergeAC}, dependency: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecurringActorTypeFixture(t)
			ctx := actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)
			tasks := postgres.NewTaskRepo(f.db)
			statuses := postgres.NewTaskStatusRepo(f.db)
			deps := postgres.NewTaskDependencyRepo(f.db)
			rules := postgres.NewAutoTransitionRuleRepo(f.db)
			svc := NewTaskService(tasks, statuses, deps, postgres.NewActivityLogRepo(f.db), WithProjectRepo(postgres.NewProjectRepo(f.db)))
			createStatus := func(category domain.StatusCategory) *domain.TaskStatus {
				st := &domain.TaskStatus{ID: uuid.New(), ProjectID: f.projectID, Name: string(category), Slug: string(category), Category: category, Color: "#ffffff", Position: 1}
				require.NoError(t, statuses.Create(ctx, st))
				return st
			}
			inProgress := createStatus(domain.StatusCategoryInProgress)
			backlog := createStatus(domain.StatusCategoryBacklog)
			review := createStatus(domain.StatusCategoryReview)
			done := createStatus(domain.StatusCategoryDone)
			rule := &domain.AutoTransitionRule{ID: uuid.New(), ProjectID: f.projectID, Trigger: domain.TriggerAllSubtasksDone, TargetStatusID: review.ID, IsEnabled: true}
			require.NoError(t, rules.Create(ctx, rule))
			initial := inProgress.ID
			if tc.fromReview {
				initial = review.ID
			} else if tc.dependency {
				initial = backlog.ID
			}
			parent := &domain.Task{ID: uuid.New(), ProjectID: f.projectID, StatusID: initial, Title: "release delivery", Description: tc.description, Labels: tc.labels, AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium, CreatedByType: domain.ActorTypeSystem}
			require.NoError(t, svc.Create(ctx, parent))
			child := &domain.Task{ID: uuid.New(), ProjectID: f.projectID, StatusID: done.ID, ParentTaskID: &parent.ID, Title: "completed component review", AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium, CreatedByType: domain.ActorTypeSystem}
			if tc.dependency {
				child.ParentTaskID = nil
			}
			require.NoError(t, svc.Create(ctx, child))
			if tc.dependency {
				require.NoError(t, deps.Create(ctx, &domain.TaskDependency{ID: uuid.New(), TaskID: parent.ID, DependsOnTaskID: child.ID, DependencyType: domain.DependencyTypeBlocks}))
			}
			barrier := &transitionReadBarrier{TaskRepository: tasks, taskID: parent.ID}
			if tc.lateLabel {
				barrier.ready = make(chan struct{})
				barrier.committed = make(chan error, 1)
				go func() {
					<-barrier.ready
					_, err := f.db.ExecContext(ctx, "UPDATE tasks SET labels=$1,updated_at=now() WHERE id=$2", pq.Array([]string{LabelPostMergeAC}), parent.ID)
					barrier.committed <- err
				}()
			}
			auto := NewAutoTransitionService(barrier, statuses, deps, svc, rules, nil)
			require.NoError(t, auto.EvaluateOnTaskMove(ctx, child.ID, domain.StatusCategoryDone))
			fresh, err := tasks.GetByID(ctx, parent.ID)
			require.NoError(t, err)
			wantStatus, wantMoves := review.ID, 1
			switch {
			case tc.wantHold:
				wantStatus, wantMoves = initial, 0
			case tc.fromReview:
				wantStatus = done.ID
			case tc.dependency:
				wantStatus = f.statusID
			}
			require.Equal(t, wantStatus, fresh.StatusID)
			var moves int
			require.NoError(t, f.db.GetContext(ctx, &moves, "SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action='task.moved'", parent.ID))
			require.Equal(t, wantMoves, moves)
			if tc.wantHold {
				// The marker suppresses the automatic handoff, not an explicit one.
				require.NoError(t, svc.MoveTask(ctx, parent.ID, MoveTaskInput{StatusID: &review.ID}))
				fresh, err = tasks.GetByID(ctx, parent.ID)
				require.NoError(t, err)
				require.Equal(t, review.ID, fresh.StatusID)
			}
			t.Cleanup(func() {
				_, _ = f.db.ExecContext(context.Background(), "DELETE FROM activity_log WHERE workspace_id=$1", f.workspaceID)
			})
		})
	}
}
