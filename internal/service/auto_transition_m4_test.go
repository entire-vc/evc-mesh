package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

type transitionRules struct {
	repository.AutoTransitionRuleRepository
	rules      []domain.AutoTransitionRule
	err        error
	beforeList func()
}

func (r *transitionRules) List(context.Context, uuid.UUID) ([]domain.AutoTransitionRule, error) {
	if r.beforeList != nil {
		r.beforeList()
	}
	return r.rules, r.err
}

func TestAutoTransitionM4_DependencyParkGates(t *testing.T) {
	future := time.Now().Add(time.Hour)
	for _, tc := range []struct {
		name      string
		configure func(*domain.Task)
		wake      bool
	}{
		{"explicit", func(t *domain.Task) { t.CustomFields = json.RawMessage(`{"park_reason":"dependency"}`) }, true},
		{"legacy", func(t *domain.Task) { t.Labels = []string{"park:dependency"} }, true},
		{"unknown", func(t *domain.Task) {}, false},
		{"invalid_metadata", func(t *domain.Task) { t.CustomFields = json.RawMessage(`{"park_reason":3}`) }, false},
		{"unknown_marker", func(t *domain.Task) { t.Labels = []string{"park:dependency", "park:other"} }, false},
		{"external_field", func(t *domain.Task) { t.CustomFields = json.RawMessage(`{"park_reason":"wait-external"}`) }, false},
		{"monitor", func(t *domain.Task) { t.Labels = []string{"park:dependency", "kind:monitor"} }, false},
		{"human", func(t *domain.Task) { t.Labels = []string{"park:dependency"}; t.HumanGate = true }, false},
		{"date", func(t *domain.Task) { t.Labels = []string{"park:dependency"}; t.StartAfter = &future }, false},
		{"external", func(t *domain.Task) { t.Labels = []string{"park:dependency", "park:wait-external"} }, false},
		{"manual", func(t *domain.Task) { t.Labels = []string{"park:dependency", "wake:manual"} }, false},
		{"conflict", func(t *domain.Task) {
			t.Labels = []string{"park:dependency"}
			t.CustomFields = json.RawMessage(`{"park_reason":"external"}`)
		}, false},
		{"supervised", func(t *domain.Task) {
			t.Labels = []string{"park:dependency"}
			t.DelegationLevel = domain.DelegationLevelSupervised
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, tasks, statuses, deps := buildAutoTransitionFixture()
			project := uuid.New()
			backlog := seedStatus(statuses, project, domain.StatusCategoryBacklog, "Backlog")
			todo := seedStatus(statuses, project, domain.StatusCategoryTodo, "Todo")
			done := seedStatus(statuses, project, domain.StatusCategoryDone, "Done")
			blocked := seedTask(tasks, project, backlog.ID, nil, "parked")
			blocker := seedTask(tasks, project, done.ID, nil, "resolved")
			tc.configure(blocked)
			d := &domain.TaskDependency{ID: uuid.New(), TaskID: blocked.ID, DependsOnTaskID: blocker.ID, DependencyType: domain.DependencyTypeBlocks}
			deps.items[d.ID] = d
			require.NoError(t, svc.CheckDependencyResolution(context.Background(), blocker.ID))
			want := backlog.ID
			if tc.wake {
				want = todo.ID
			}
			require.Equal(t, want, tasks.items[blocked.ID].StatusID)
		})
	}
}

func TestAutoTransitionM4_DisabledRule(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "disabled"}[disabled], func(t *testing.T) {
			svc, tasks, statuses, deps := buildAutoTransitionFixture()
			project := uuid.New()
			backlog := seedStatus(statuses, project, domain.StatusCategoryBacklog, "Backlog")
			todo := seedStatus(statuses, project, domain.StatusCategoryTodo, "Todo")
			done := seedStatus(statuses, project, domain.StatusCategoryDone, "Done")
			blocked := seedTask(tasks, project, backlog.ID, nil, "parked")
			blocked.Labels = []string{"park:dependency"}
			blocker := seedTask(tasks, project, done.ID, nil, "resolved")
			d := &domain.TaskDependency{ID: uuid.New(), TaskID: blocked.ID, DependsOnTaskID: blocker.ID, DependencyType: domain.DependencyTypeBlocks}
			deps.items[d.ID] = d
			rules := &transitionRules{}
			if disabled {
				rules.rules = []domain.AutoTransitionRule{{Trigger: domain.TriggerBlockingDepResolved, TargetStatusID: todo.ID, IsEnabled: false}}
			}
			svc.(*autoTransitionService).ruleRepo = rules
			require.NoError(t, svc.CheckDependencyResolution(context.Background(), blocker.ID))
			want := todo.ID
			if disabled {
				want = backlog.ID
			}
			require.Equal(t, want, tasks.items[blocked.ID].StatusID)
		})
	}
}

func TestAutoTransitionM4_ParentHumanGate(t *testing.T) {
	svc, tasks, statuses, _ := buildAutoTransitionFixture()
	project := uuid.New()
	inProgress := seedStatus(statuses, project, domain.StatusCategoryInProgress, "In Progress")
	seedStatus(statuses, project, domain.StatusCategoryReview, "Review")
	done := seedStatus(statuses, project, domain.StatusCategoryDone, "Done")
	parent := seedTask(tasks, project, inProgress.ID, nil, "gated parent")
	parent.HumanGate = true
	seedTask(tasks, project, done.ID, &parent.ID, "finished child")
	require.NoError(t, svc.CheckSubtaskCompletion(context.Background(), parent.ID))
	require.Equal(t, inProgress.ID, tasks.items[parent.ID].StatusID)
}

func TestAutoTransitionM4_FreshReadRejectsChangedCandidate(t *testing.T) {
	for _, change := range []string{"status", "human_gate", "reblock", "disabled"} {
		t.Run(change, func(t *testing.T) {
			svc, tasks, statuses, deps := buildAutoTransitionFixture()
			project := uuid.New()
			backlog := seedStatus(statuses, project, domain.StatusCategoryBacklog, "Backlog")
			todo := seedStatus(statuses, project, domain.StatusCategoryTodo, "Todo")
			done := seedStatus(statuses, project, domain.StatusCategoryDone, "Done")
			blocked := seedTask(tasks, project, backlog.ID, nil, "parked")
			blocked.Labels = []string{"park:dependency"}
			blocker := seedTask(tasks, project, done.ID, nil, "resolved")
			d := &domain.TaskDependency{ID: uuid.New(), TaskID: blocked.ID, DependsOnTaskID: blocker.ID, DependencyType: domain.DependencyTypeBlocks}
			deps.items[d.ID] = d
			rules := &transitionRules{}
			calls := 0
			rules.beforeList = func() {
				calls++
				if calls != 1 {
					return
				}
				switch change {
				case "status":
					blocked.StatusID = todo.ID
				case "human_gate":
					blocked.HumanGate = true
				case "reblock":
					unresolved := seedTask(tasks, project, todo.ID, nil, "new blocker")
					edge := &domain.TaskDependency{ID: uuid.New(), TaskID: blocked.ID, DependsOnTaskID: unresolved.ID, DependencyType: domain.DependencyTypeBlocks}
					deps.items[edge.ID] = edge
				case "disabled":
					rules.beforeList = func() {
						rules.rules = []domain.AutoTransitionRule{{Trigger: domain.TriggerBlockingDepResolved, TargetStatusID: todo.ID, IsEnabled: false}}
					}
				}
			}
			svc.(*autoTransitionService).ruleRepo = rules
			wrapped := &beforeAutomaticMove{TaskService: svc.(*autoTransitionService).taskSvc}
			svc.(*autoTransitionService).taskSvc = wrapped
			require.NoError(t, svc.CheckDependencyResolution(context.Background(), blocker.ID))
			require.Equal(t, 0, wrapped.calls, "no automatic write may follow a changed candidate")
		})
	}
}

type beforeAutomaticMove struct {
	TaskService
	before func(MoveTaskInput)
	calls  int
}

func (s *beforeAutomaticMove) MoveTask(ctx context.Context, id uuid.UUID, input MoveTaskInput) error {
	s.calls++
	if s.before != nil {
		s.before(input)
	}
	return s.TaskService.MoveTask(ctx, id, input)
}

func TestAutoTransitionM4_AuditAndCAS(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "duplicate_event", true: "stale_at_move"}[stale], func(t *testing.T) {
			tasks := NewMockTaskRepository()
			statuses := NewMockTaskStatusRepository()
			deps := NewMockTaskDependencyRepository()
			activity := NewMockActivityLogRepository()
			taskSvc := newTestTaskService(tasks, statuses, deps, activity)
			wrapped := &beforeAutomaticMove{TaskService: taskSvc}
			svc := NewAutoTransitionService(tasks, statuses, deps, wrapped, nil, nil)
			project := uuid.New()
			backlog := seedStatus(statuses, project, domain.StatusCategoryBacklog, "Backlog")
			todo := seedStatus(statuses, project, domain.StatusCategoryTodo, "Todo")
			done := seedStatus(statuses, project, domain.StatusCategoryDone, "Done")
			blocked := seedTask(tasks, project, backlog.ID, nil, "parked")
			blocked.Labels = []string{"park:dependency"}
			blocked.UpdatedAt = time.Now()
			blocker := seedTask(tasks, project, done.ID, nil, "resolved")
			d := &domain.TaskDependency{ID: uuid.New(), TaskID: blocked.ID, DependsOnTaskID: blocker.ID, DependencyType: domain.DependencyTypeBlocks}
			deps.items[d.ID] = d
			wrapped.before = func(input MoveTaskInput) {
				require.NotNil(t, input.ExpectedStatusID)
				require.Equal(t, backlog.ID, *input.ExpectedStatusID)
				require.NotNil(t, input.ExpectedUpdatedAt)
				require.Equal(t, blocked.UpdatedAt, *input.ExpectedUpdatedAt)
				require.Equal(t, "auto_transition", input.Source)
				require.Equal(t, string(domain.TriggerBlockingDepResolved), input.Reason)
				require.Equal(t, blocker.ID, *input.TriggerTaskID)
				if stale {
					blocked.UpdatedAt = blocked.UpdatedAt.Add(time.Second)
				}
			}
			require.NoError(t, svc.CheckDependencyResolution(context.Background(), blocker.ID))
			if stale {
				require.Equal(t, backlog.ID, tasks.items[blocked.ID].StatusID)
				require.Empty(t, activity.items)
				return
			}
			require.NoError(t, svc.CheckDependencyResolution(context.Background(), blocker.ID))
			require.Equal(t, todo.ID, tasks.items[blocked.ID].StatusID)
			require.Equal(t, 1, wrapped.calls)
			moved := 0
			for _, entry := range activity.items {
				if entry.Action != "task.moved" {
					continue
				}
				moved++
				var changes map[string]interface{}
				require.NoError(t, json.Unmarshal(entry.Changes, &changes))
				require.Equal(t, "auto_transition", changes["source"])
				require.Equal(t, string(domain.TriggerBlockingDepResolved), changes["reason"])
				require.Equal(t, blocker.ID.String(), changes["trigger_task_id"])
			}
			require.Equal(t, 1, moved)
		})
	}
}

func TestAutoTransitionM4_ParentRulesAndLabels(t *testing.T) {
	for _, tc := range []struct {
		name     string
		labels   []string
		disabled bool
		gate     bool
		wake     bool
	}{
		{"unlabelled", nil, false, false, false},
		{"bare_epic", []string{"epic"}, false, false, false},
		{"umbrella", []string{"kind:umbrella"}, false, false, true},
		{"epic", []string{"kind:epic"}, false, false, true},
		{"disabled", []string{"kind:umbrella"}, true, false, false},
		{"gated", []string{"kind:umbrella"}, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, tasks, statuses, _ := buildAutoTransitionFixture()
			project := uuid.New()
			review := seedStatus(statuses, project, domain.StatusCategoryReview, "Review")
			done := seedStatus(statuses, project, domain.StatusCategoryDone, "Done")
			parent := seedTask(tasks, project, review.ID, nil, "parent")
			parent.Labels = tc.labels
			parent.HumanGate = tc.gate
			seedTask(tasks, project, done.ID, &parent.ID, "finished child")
			rules := &transitionRules{}
			if tc.disabled {
				rules.rules = []domain.AutoTransitionRule{{Trigger: domain.TriggerAllSubtasksDone, IsEnabled: false, TargetStatusID: done.ID}}
			}
			svc.(*autoTransitionService).ruleRepo = rules
			require.NoError(t, svc.CheckSubtaskCompletion(context.Background(), parent.ID))
			want := review.ID
			if tc.wake {
				want = done.ID
			}
			require.Equal(t, want, tasks.items[parent.ID].StatusID)
		})
	}
}

func TestAutoTransitionM4_RuleConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*transitionRules, uuid.UUID)
		wake      bool
	}{
		{"enabled", func(r *transitionRules, target uuid.UUID) {
			r.rules = []domain.AutoTransitionRule{{Trigger: domain.TriggerBlockingDepResolved, TargetStatusID: target, IsEnabled: true}}
		}, true},
		{"list_error", func(r *transitionRules, _ uuid.UUID) { r.err = fmt.Errorf("configuration unavailable") }, false},
		{"duplicate", func(r *transitionRules, target uuid.UUID) {
			r.rules = []domain.AutoTransitionRule{{Trigger: domain.TriggerBlockingDepResolved, TargetStatusID: target, IsEnabled: true}, {Trigger: domain.TriggerBlockingDepResolved, IsEnabled: false}}
		}, false},
		{"missing_target", func(r *transitionRules, _ uuid.UUID) {
			r.rules = []domain.AutoTransitionRule{{Trigger: domain.TriggerBlockingDepResolved, IsEnabled: true}}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, tasks, statuses, deps := buildAutoTransitionFixture()
			project := uuid.New()
			backlog := seedStatus(statuses, project, domain.StatusCategoryBacklog, "Backlog")
			todo := seedStatus(statuses, project, domain.StatusCategoryTodo, "Todo")
			done := seedStatus(statuses, project, domain.StatusCategoryDone, "Done")
			blocked := seedTask(tasks, project, backlog.ID, nil, "parked")
			blocked.Labels = []string{"park:dependency"}
			blocker := seedTask(tasks, project, done.ID, nil, "resolved")
			d := &domain.TaskDependency{ID: uuid.New(), TaskID: blocked.ID, DependsOnTaskID: blocker.ID, DependencyType: domain.DependencyTypeBlocks}
			deps.items[d.ID] = d
			rules := &transitionRules{}
			tc.configure(rules, todo.ID)
			svc.(*autoTransitionService).ruleRepo = rules
			err := svc.(*autoTransitionService).tryUnblockTask(context.Background(), blocked.ID, blocker.ID)
			if tc.wake {
				require.NoError(t, err)
				require.Equal(t, todo.ID, tasks.items[blocked.ID].StatusID)
			} else {
				require.Error(t, err)
				require.Equal(t, backlog.ID, tasks.items[blocked.ID].StatusID)
			}
		})
	}
}

func TestAutoTransitionM4_ParentFreshChildrenAndTrigger(t *testing.T) {
	for _, addChild := range []bool{false, true} {
		t.Run(map[bool]string{false: "child_trigger_audit", true: "new_child_before_move"}[addChild], func(t *testing.T) {
			tasks := NewMockTaskRepository()
			statuses := NewMockTaskStatusRepository()
			deps := NewMockTaskDependencyRepository()
			activity := NewMockActivityLogRepository()
			taskSvc := newTestTaskService(tasks, statuses, deps, activity)
			svc := NewAutoTransitionService(tasks, statuses, deps, taskSvc, nil, nil)
			project := uuid.New()
			progress := seedStatus(statuses, project, domain.StatusCategoryInProgress, "In Progress")
			review := seedStatus(statuses, project, domain.StatusCategoryReview, "Review")
			done := seedStatus(statuses, project, domain.StatusCategoryDone, "Done")
			parent := seedTask(tasks, project, progress.ID, nil, "parent")
			child := seedTask(tasks, project, done.ID, &parent.ID, "finished child")
			rules := &transitionRules{}
			calls := 0
			rules.beforeList = func() {
				calls++
				if addChild && calls == 1 {
					seedTask(tasks, project, progress.ID, &parent.ID, "new child")
				}
			}
			svc.(*autoTransitionService).ruleRepo = rules
			require.NoError(t, svc.EvaluateOnTaskMove(context.Background(), child.ID, domain.StatusCategoryDone))
			if addChild {
				require.Equal(t, progress.ID, tasks.items[parent.ID].StatusID)
				require.Empty(t, activity.items)
				return
			}
			require.Equal(t, review.ID, tasks.items[parent.ID].StatusID)
			moved := 0
			for _, entry := range activity.items {
				if entry.Action != "task.moved" {
					continue
				}
				moved++
				var changes map[string]interface{}
				require.NoError(t, json.Unmarshal(entry.Changes, &changes))
				require.Equal(t, child.ID.String(), changes["trigger_task_id"])
				require.Equal(t, "all_subtasks_done", changes["reason"])
				require.Equal(t, "auto_transition", changes["source"])
			}
			require.Equal(t, 1, moved)
		})
	}
}
