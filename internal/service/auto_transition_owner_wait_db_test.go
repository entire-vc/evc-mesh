package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// A parent whose owner waits on its subtask through wait_for on=card (a blocks
// edge owned by the typed wait) is not finished when that subtask closes: the
// released wait must wake the owner instead of all_subtasks_done handing the
// unfinished parent to review (seen live).
//
// Any other wait of the owner still in force holds the parent as well, whatever
// its target or kind (a live parent waited on=card for a card that was not its
// subtask, and was handed to review with that wait cancelled).
func TestAutoTransitionAllSubtasksDone_OwnerWaitOnSubtask(t *testing.T) {
	for _, tc := range []struct {
		name string
		// edge: none, owned (typed wait), manual (blocks edge without owner).
		edge string
		// waitResolved: the server already resolved the wait; the edge stays
		// until the runner removes it.
		waitResolved bool
		// edgeOnOutsider: the owned edge points at a card that is not a subtask.
		edgeOnOutsider bool
		// mrWait: an extra wait on=mr without an edge, declared by the parent's
		// owner ("owner") or by an agent the card was taken from ("former").
		mrWait   string
		wantHold bool
	}{
		{name: "owner_wait_on_subtask", edge: "owned", wantHold: true},
		{name: "owner_wait_resolved_edge_kept", edge: "owned", waitResolved: true, wantHold: true},
		{name: "no_edge_moves_to_review", edge: "none"},
		{name: "manual_edge_without_owner_moves", edge: "manual"},
		{name: "owner_wait_on_non_subtask_holds", edge: "owned", edgeOnOutsider: true, wantHold: true},
		{name: "owner_mr_wait_holds", edge: "none", mrWait: "owner", wantHold: true},
		{name: "resolved_wait_on_non_subtask_moves", edge: "owned", edgeOnOutsider: true, waitResolved: true},
		{name: "former_owner_wait_moves", edge: "none", mrWait: "former"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecurringActorTypeFixture(t)
			ctx := actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)
			tasks := postgres.NewTaskRepo(f.db)
			statuses := postgres.NewTaskStatusRepo(f.db)
			deps := postgres.NewTaskDependencyRepo(f.db)
			rules := postgres.NewAutoTransitionRuleRepo(f.db)
			svc := NewTaskService(tasks, statuses, deps, postgres.NewActivityLogRepo(f.db), WithProjectRepo(postgres.NewProjectRepo(f.db)))
			auto := NewAutoTransitionService(tasks, statuses, deps, svc, rules, nil)
			svc.(TaskServiceAutoTransitionConfigurable).SetAutoTransitionService(auto)
			t.Cleanup(func() {
				_, _ = f.db.ExecContext(context.Background(), "DELETE FROM activity_log WHERE workspace_id=$1", f.workspaceID)
				_, _ = f.db.ExecContext(context.Background(), "DELETE FROM agents WHERE workspace_id=$1", f.workspaceID)
			})

			createStatus := func(category domain.StatusCategory) *domain.TaskStatus {
				st := &domain.TaskStatus{ID: uuid.New(), ProjectID: f.projectID, Name: string(category), Slug: string(category), Category: category, Color: "#ffffff", Position: 1}
				require.NoError(t, statuses.Create(ctx, st))
				return st
			}
			inProgress := createStatus(domain.StatusCategoryInProgress)
			review := createStatus(domain.StatusCategoryReview)
			done := createStatus(domain.StatusCategoryDone)
			require.NoError(t, rules.Create(ctx, &domain.AutoTransitionRule{ID: uuid.New(), ProjectID: f.projectID, Trigger: domain.TriggerAllSubtasksDone, TargetStatusID: review.ID, IsEnabled: true}))

			owner := uuid.New()
			_, err := f.db.Exec(`INSERT INTO agents(id,workspace_id,name,slug,api_key_hash,api_key_prefix) VALUES($1,$2,'parent owner',$3,'fixture-hash','test')`, owner, f.workspaceID, owner.String())
			require.NoError(t, err)

			parent := &domain.Task{ID: uuid.New(), ProjectID: f.projectID, StatusID: inProgress.ID, Title: "partner acceptance", AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityHigh, CreatedByType: domain.ActorTypeSystem}
			require.NoError(t, svc.Create(ctx, parent))
			// The fixture has no agent directory: the owner is assigned in SQL.
			_, err = f.db.Exec(`UPDATE tasks SET assignee_id=$1, assignee_type='agent' WHERE id=$2`, owner, parent.ID)
			require.NoError(t, err)
			child := &domain.Task{ID: uuid.New(), ProjectID: f.projectID, StatusID: inProgress.ID, ParentTaskID: &parent.ID, Title: "ops unblock", AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityHigh, CreatedByType: domain.ActorTypeSystem}
			require.NoError(t, svc.Create(ctx, child))
			blocker := child.ID
			if tc.edgeOnOutsider {
				outsider := &domain.Task{ID: uuid.New(), ProjectID: f.projectID, StatusID: inProgress.ID, Title: "unrelated card", AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityHigh, CreatedByType: domain.ActorTypeSystem}
				require.NoError(t, svc.Create(ctx, outsider))
				blocker = outsider.ID
			}

			switch tc.edge {
			case "owned":
				waitID, depID := uuid.New(), uuid.New()
				_, err = f.db.Exec(`INSERT INTO task_waits(id,task_id,agent_id,kind,target_task_id,target_condition,server_observed) VALUES($1,$2,$3,'card',$4,'task_done',true)`, waitID, parent.ID, owner, blocker)
				require.NoError(t, err)
				_, err = f.db.Exec(`INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type,condition,owner_wait_id,wait_agent_id) VALUES($1,$2,$3,'blocks','task_done',$4,$5)`, depID, parent.ID, blocker, waitID, owner)
				require.NoError(t, err)
				if tc.waitResolved {
					_, err = f.db.Exec(`UPDATE task_waits SET resolved_at=now(), resolution='met' WHERE id=$1`, waitID)
					require.NoError(t, err)
				}
			case "manual":
				require.NoError(t, deps.Create(ctx, &domain.TaskDependency{ID: uuid.New(), TaskID: parent.ID, DependsOnTaskID: blocker, DependencyType: domain.DependencyTypeBlocks}))
			}
			if tc.mrWait != "" {
				waiter := owner
				if tc.mrWait == "former" {
					waiter = uuid.New()
					_, err = f.db.Exec(`INSERT INTO agents(id,workspace_id,name,slug,api_key_hash,api_key_prefix) VALUES($1,$2,'former owner',$3,'fixture-hash','test')`, waiter, f.workspaceID, waiter.String())
					require.NoError(t, err)
				}
				_, err = f.db.Exec(`INSERT INTO task_waits(id,task_id,agent_id,kind,provider,project_path,mr_iid,until_state) VALUES($1,$2,$3,'mr','gitlab','entire-vc/evc-mesh',1,'merged')`, uuid.New(), parent.ID, waiter)
				require.NoError(t, err)
			}
			var activeBefore int
			require.NoError(t, f.db.Get(&activeBefore, `SELECT count(*) FROM task_waits WHERE task_id=$1 AND resolved_at IS NULL`, parent.ID))
			// The fixture waits are in force exactly when the case says so.
			wantActive := 0
			if tc.edge == "owned" && !tc.waitResolved {
				wantActive++
			}
			if tc.mrWait != "" {
				wantActive++
			}
			require.Equal(t, wantActive, activeBefore, "fixture active waits")
			if tc.edge != "none" {
				// The fixture edge is what the hold reads: owned exactly when expected.
				edges, listErr := deps.ListByTask(ctx, parent.ID)
				require.NoError(t, listErr)
				require.Len(t, edges, 1)
				require.Equal(t, tc.edge == "owned", edges[0].OwnerWaitID != nil)
			}

			// The ordinary path: the subtask is moved to done through the service.
			require.NoError(t, svc.MoveTask(ctx, child.ID, MoveTaskInput{StatusID: &done.ID}))

			fresh, err := tasks.GetByID(ctx, parent.ID)
			require.NoError(t, err)
			var moves int
			require.NoError(t, f.db.Get(&moves, `SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action='task.moved' AND changes->>'reason'='all_subtasks_done'`, parent.ID))
			if tc.wantHold {
				require.Equal(t, inProgress.ID, fresh.StatusID, "parent waiting on its subtask must stay in progress")
				require.NotNil(t, fresh.AssigneeID)
				require.Equal(t, owner, *fresh.AssigneeID, "parent keeps its assignee")
				require.Zero(t, moves)
				var dropped int
				require.NoError(t, f.db.Get(&dropped, `SELECT count(*) FROM task_waits WHERE task_id=$1 AND resolution IN ('cancelled','invalid','superseded')`, parent.ID))
				require.Zero(t, dropped, "the owner's wait is not cancelled")
				if tc.edgeOnOutsider || tc.mrWait != "" {
					// A wait on something other than the closed subtask stays in force.
					var activeAfter int
					require.NoError(t, f.db.Get(&activeAfter, `SELECT count(*) FROM task_waits WHERE task_id=$1 AND resolved_at IS NULL`, parent.ID))
					require.Equal(t, activeBefore, activeAfter, "the owner's wait stays active")
				}
				return
			}
			require.Equal(t, review.ID, fresh.StatusID, "parent without an active owner wait still goes to review")
			require.Equal(t, 1, moves)
		})
	}
}
