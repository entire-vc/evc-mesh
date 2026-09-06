package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// ---------------------------------------------------------------------------
// qualifiesForTriage — pure function
// ---------------------------------------------------------------------------

func TestQualifiesForTriage(t *testing.T) {
	userType := domain.ActorTypeUser
	agentType := domain.ActorTypeAgent

	tests := []struct {
		name string
		task *domain.Task
		want bool
	}{
		{
			name: "nil task never qualifies",
			task: nil,
			want: false,
		},
		{
			name: "no gate ever armed does not qualify (count==3 auto-triage shape)",
			task: &domain.Task{HumanGate: false},
			want: false,
		},
		{
			name: "active hard gate, agent-authored, qualifies",
			task: &domain.Task{HumanGate: true, HumanGateClass: domain.HumanGateClassHard, GateAuthorType: &agentType},
			want: true,
		},
		{
			name: "active soft gate, human-authored, qualifies",
			task: &domain.Task{HumanGate: true, HumanGateClass: domain.HumanGateClassSoft, GateAuthorType: &userType},
			want: true,
		},
		{
			name: "active soft gate, agent-authored, does NOT qualify",
			task: &domain.Task{HumanGate: true, HumanGateClass: domain.HumanGateClassSoft, GateAuthorType: &agentType},
			want: false,
		},
		{
			name: "active gate, nil GateAuthorType, hard class still qualifies",
			task: &domain.Task{HumanGate: true, HumanGateClass: domain.HumanGateClassHard, GateAuthorType: nil},
			want: true,
		},
		{
			name: "active gate, nil GateAuthorType, soft class does NOT qualify",
			task: &domain.Task{HumanGate: true, HumanGateClass: domain.HumanGateClassSoft, GateAuthorType: nil},
			want: false,
		},
		{
			// The stale-metadata case (triage_entry.go doc comment): a task that was
			// hard-gated once, answered, and released must NOT keep re-qualifying on
			// HumanGateClass alone — HumanGate itself must still be active.
			name: "hard-classed history but gate now RELEASED does not qualify",
			task: &domain.Task{HumanGate: false, HumanGateClass: domain.HumanGateClassHard, GateAuthorType: &agentType},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, qualifiesForTriage(tt.task))
		})
	}
}

// ---------------------------------------------------------------------------
// passesTriageEntryGate — mirrors review_evidence_test.go's evidenceHarness
// ---------------------------------------------------------------------------

func newTriageGateHarness(strict bool) (*taskService, *domain.Task) {
	rules := &stubRulesSvc{cfg: &domain.MidPipelineConfig{TriageEntryStrict: strict}}
	svc := &taskService{rulesConfigSvc: rules}
	task := &domain.Task{ID: uuid.New(), ProjectID: uuid.New()}
	return svc, task
}

// The single most important test in this file, same reasoning as
// TestReviewEvidence_DefaultOff_AnyCommentStillPasses: a task with NO gate at
// all — the exact shape auto-triage-without-a-human-author produces — must
// still be allowed to move into triage when the project has not opted in.
// If turning strict mode on somewhere silently changed this, the flag would
// not be a blast-radius control.
func TestTriageEntryGate_DefaultOff_UngatedTaskStillPasses(t *testing.T) {
	svc, task := newTriageGateHarness(false)
	ok, strict := svc.passesTriageEntryGate(context.Background(), task)
	assert.True(t, ok)
	assert.False(t, strict)
}

func TestTriageEntryGate_NoRulesService_StaysLoose(t *testing.T) {
	svc := &taskService{}
	task := &domain.Task{ID: uuid.New(), ProjectID: uuid.New()}
	ok, strict := svc.passesTriageEntryGate(context.Background(), task)
	assert.True(t, ok)
	assert.False(t, strict)
}

func TestTriageEntryGate_Strict_UngatedTaskRefused(t *testing.T) {
	svc, task := newTriageGateHarness(true)
	ok, strict := svc.passesTriageEntryGate(context.Background(), task)
	assert.False(t, ok)
	assert.True(t, strict)
}

func TestTriageEntryGate_Strict_HardGateAgentAuthoredPasses(t *testing.T) {
	svc, task := newTriageGateHarness(true)
	agentType := domain.ActorTypeAgent
	task.HumanGate = true
	task.HumanGateClass = domain.HumanGateClassHard
	task.GateAuthorType = &agentType

	ok, strict := svc.passesTriageEntryGate(context.Background(), task)
	assert.True(t, ok)
	assert.True(t, strict)
}

func TestTriageEntryGate_Strict_SoftGateAgentAuthoredRefused(t *testing.T) {
	svc, task := newTriageGateHarness(true)
	agentType := domain.ActorTypeAgent
	task.HumanGate = true
	task.HumanGateClass = domain.HumanGateClassSoft
	task.GateAuthorType = &agentType

	ok, _ := svc.passesTriageEntryGate(context.Background(), task)
	assert.False(t, ok)
}

func TestTriageEntryGate_Strict_SoftGateHumanAuthoredPasses(t *testing.T) {
	svc, task := newTriageGateHarness(true)
	userType := domain.ActorTypeUser
	task.HumanGate = true
	task.HumanGateClass = domain.HumanGateClassSoft
	task.GateAuthorType = &userType

	ok, _ := svc.passesTriageEntryGate(context.Background(), task)
	assert.True(t, ok)
}

// ---------------------------------------------------------------------------
// MoveTask integration — mirrors TestTaskService_MoveTask_ReviewGate_* in
// task_service_test.go
// ---------------------------------------------------------------------------

func setupTaskServiceForTriageGate(strict bool) (*taskService, *MockTaskRepository, *MockTaskStatusRepository) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()
	rules := &stubRulesSvc{cfg: &domain.MidPipelineConfig{TriageEntryStrict: strict}}

	svc := newTestTaskService(taskRepo, statusRepo, depRepo, activityRepo,
		WithRulesConfigService(rules),
	).(*taskService)
	return svc, taskRepo, statusRepo
}

func TestTaskService_MoveTask_TriageGate_DefaultOff_UngatedTaskStillMoves(t *testing.T) {
	projectID := uuid.New()
	taskID := uuid.New()
	triageStatusID := uuid.New()

	svc, taskRepo, statusRepo := setupTaskServiceForTriageGate(false)
	taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: projectID, StatusID: uuid.New(), Title: "Ungated"}
	statusRepo.items[triageStatusID] = &domain.TaskStatus{ID: triageStatusID, ProjectID: projectID, Category: domain.StatusCategoryTriage}

	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &triageStatusID})
	require.NoError(t, err)
}

func TestTaskService_MoveTask_TriageGate_Strict_BlockedWhenUngated(t *testing.T) {
	projectID := uuid.New()
	taskID := uuid.New()
	triageStatusID := uuid.New()

	svc, taskRepo, statusRepo := setupTaskServiceForTriageGate(true)
	taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: projectID, StatusID: uuid.New(), Title: "Ungated"}
	statusRepo.items[triageStatusID] = &domain.TaskStatus{ID: triageStatusID, ProjectID: projectID, Category: domain.StatusCategoryTriage}

	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &triageStatusID})
	require.Error(t, err)
	var triageErr *TriageEntryError
	require.ErrorAs(t, err, &triageErr)
}

func TestTaskService_MoveTask_TriageGate_Strict_PassesWithHardGate(t *testing.T) {
	projectID := uuid.New()
	taskID := uuid.New()
	triageStatusID := uuid.New()
	agentType := domain.ActorTypeAgent

	svc, taskRepo, statusRepo := setupTaskServiceForTriageGate(true)
	taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: projectID, StatusID: uuid.New(), Title: "Hard-gated",
		HumanGate: true, HumanGateClass: domain.HumanGateClassHard, GateAuthorType: &agentType,
	}
	statusRepo.items[triageStatusID] = &domain.TaskStatus{ID: triageStatusID, ProjectID: projectID, Category: domain.StatusCategoryTriage}

	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &triageStatusID})
	require.NoError(t, err)
}

func TestTaskService_MoveTask_TriageGate_Strict_SystemActorNotExempt(t *testing.T) {
	// Unlike the review/done gates, no actor type is exempt here — the whole
	// point is that even a system-authored call cannot move an ungated task
	// into triage. This is the case that actually stands in for the
	// dispatcher's count==3 auto-triage path.
	projectID := uuid.New()
	taskID := uuid.New()
	triageStatusID := uuid.New()

	svc, taskRepo, statusRepo := setupTaskServiceForTriageGate(true)
	taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: projectID, StatusID: uuid.New(), Title: "Ungated"}
	statusRepo.items[triageStatusID] = &domain.TaskStatus{ID: triageStatusID, ProjectID: projectID, Category: domain.StatusCategoryTriage}

	ctx := actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)
	err := svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &triageStatusID})
	require.Error(t, err)
	var triageErr *TriageEntryError
	require.ErrorAs(t, err, &triageErr)
}
