package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func TestTaskDependencyService_Create_WarnsOnMonitorBlocker(t *testing.T) {
	ctx := context.Background()
	scheduleID := uuid.New()
	tests := []struct {
		name     string
		blocker  domain.Task
		wantWarn bool
	}{
		{"kind:monitor", domain.Task{Labels: []string{"kind:monitor"}}, true},
		{"kind:umbrella", domain.Task{Labels: []string{"kind:umbrella"}}, true},
		{"recurring schedule", domain.Task{RecurringScheduleID: &scheduleID}, true},
		{"ordinary card", domain.Task{Labels: []string{"mesh"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, taskRepo := setupTaskDependencyService()
			dependent, blocker := uuid.New(), uuid.New()
			addTask(taskRepo, dependent)
			b := tt.blocker
			b.ID, b.Title = blocker, "blocker"
			taskRepo.items[blocker] = &b

			dep := &domain.TaskDependency{TaskID: dependent, DependsOnTaskID: blocker, DependencyType: domain.DependencyTypeBlocks}
			require.NoError(t, svc.Create(ctx, dep))
			assert.Equal(t, tt.wantWarn, dep.Warning != "")
			assert.Equal(t, domain.DependencyConditionTaskDone, dep.Condition)
		})
	}
}

func TestTaskDependencyService_Create_NoWarningWithCondition(t *testing.T) {
	svc, _, taskRepo := setupTaskDependencyService()
	dependent, monitor := uuid.New(), uuid.New()
	addTask(taskRepo, dependent)
	taskRepo.items[monitor] = &domain.Task{ID: monitor, Labels: []string{"kind:monitor"}}

	dep := &domain.TaskDependency{
		TaskID: dependent, DependsOnTaskID: monitor, DependencyType: domain.DependencyTypeBlocks,
		Condition: domain.DependencyConditionAccessReady, EvidenceURL: strPtr("https://mesh.example/e"),
	}
	require.NoError(t, svc.Create(context.Background(), dep))
	assert.Empty(t, dep.Warning, "an edge that names its own condition is not the deadlock shape")
	require.NotNil(t, dep.ConditionMetAt)
	assert.True(t, dep.ConditionSatisfied)
}

func TestTaskDependencyService_Create_ConditionValidation(t *testing.T) {
	svc, _, taskRepo := setupTaskDependencyService()
	a, b := uuid.New(), uuid.New()
	addTask(taskRepo, a)
	addTask(taskRepo, b)

	err := svc.Create(context.Background(), &domain.TaskDependency{TaskID: a, DependsOnTaskID: b, DependencyType: domain.DependencyTypeBlocks, Condition: "nonsense"})
	require.Error(t, err)
	assert.Equal(t, http.StatusBadRequest, err.(*apierror.Error).Code)

	err = svc.Create(context.Background(), &domain.TaskDependency{TaskID: a, DependsOnTaskID: b, DependencyType: domain.DependencyTypeRelatesTo, Condition: domain.DependencyConditionDeployed})
	require.Error(t, err, "conditions are for blocks edges only")
}

func TestTaskDependencyService_UpdateCondition(t *testing.T) {
	ctx := context.Background()
	svc, depRepo, taskRepo := setupTaskDependencyService()
	a, b := uuid.New(), uuid.New()
	addTask(taskRepo, a)
	addTask(taskRepo, b)
	dep := &domain.TaskDependency{TaskID: a, DependsOnTaskID: b, DependencyType: domain.DependencyTypeBlocks}
	require.NoError(t, svc.Create(ctx, dep))

	cond := domain.DependencyConditionAccessReady
	_, err := svc.UpdateCondition(ctx, dep.ID, UpdateDependencyConditionInput{Condition: &cond})
	require.NoError(t, err)
	assert.False(t, depRepo.items[dep.ID].EvidenceSatisfied(), "condition without evidence does not release")

	got, err := svc.UpdateCondition(ctx, dep.ID, UpdateDependencyConditionInput{EvidenceURL: strPtr("https://mesh.example/e")})
	require.NoError(t, err)
	assert.True(t, got.ConditionSatisfied)
	assert.NotNil(t, got.ConditionMetAt)

	got, err = svc.UpdateCondition(ctx, dep.ID, UpdateDependencyConditionInput{EvidenceURL: strPtr("")})
	require.NoError(t, err)
	assert.False(t, got.ConditionSatisfied, "clearing the evidence re-arms the edge")
	assert.Nil(t, got.ConditionMetAt)

	task := domain.DependencyConditionTaskDone
	_, err = svc.UpdateCondition(ctx, dep.ID, UpdateDependencyConditionInput{Condition: &task, EvidenceURL: strPtr("https://mesh.example/e")})
	require.Error(t, err, "evidence on a task_done edge is meaningless")

	_, err = svc.UpdateCondition(ctx, uuid.New(), UpdateDependencyConditionInput{Condition: &cond})
	require.Error(t, err)
}

type resolverSpy struct{ resolved []uuid.UUID }

func (r *resolverSpy) CheckDependencyResolution(_ context.Context, id uuid.UUID) error {
	r.resolved = append(r.resolved, id)
	return nil
}

func TestTaskDependencyService_UpdateCondition_NotifiesResolver(t *testing.T) {
	ctx := context.Background()
	svc, _, taskRepo := setupTaskDependencyService()
	spy := &resolverSpy{}
	WithDependencyResolver(svc, spy)
	a, b := uuid.New(), uuid.New()
	addTask(taskRepo, a)
	addTask(taskRepo, b)
	dep := &domain.TaskDependency{TaskID: a, DependsOnTaskID: b, DependencyType: domain.DependencyTypeBlocks}
	require.NoError(t, svc.Create(ctx, dep))

	cond := domain.DependencyConditionAccessReady
	_, err := svc.UpdateCondition(ctx, dep.ID, UpdateDependencyConditionInput{Condition: &cond})
	require.NoError(t, err)
	assert.Empty(t, spy.resolved, "no evidence yet: nothing was released")
	_, err = svc.UpdateCondition(ctx, dep.ID, UpdateDependencyConditionInput{EvidenceURL: strPtr("https://mesh.example/e")})
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{b}, spy.resolved, "the dependents of the blocker are re-evaluated")
}

// 24.10: 15 one-off CRM cards waited on a permanent inbox monitor whose input
// already worked. Evidence-satisfied edges must leave none of them held while
// an ordinary edge on the same monitor still does.
func TestHasUnresolvedBlockers_EvidenceReleasesMonitorEdges(t *testing.T) {
	monitor := uuid.New()
	cats := map[uuid.UUID]domain.StatusCategory{monitor: domain.StatusCategoryInProgress}
	evidence := "https://mesh.example/e"
	met := timeNow()

	for i := 0; i < 15; i++ {
		edge := domain.TaskDependency{
			TaskID: uuid.New(), DependsOnTaskID: monitor, DependencyType: domain.DependencyTypeBlocks,
			Condition: domain.DependencyConditionAccessReady, EvidenceURL: &evidence, ConditionMetAt: &met,
		}
		assert.False(t, hasUnresolvedBlockers([]domain.TaskDependency{edge}, cats))
	}
	legacy := domain.TaskDependency{TaskID: uuid.New(), DependsOnTaskID: monitor, DependencyType: domain.DependencyTypeBlocks}
	assert.True(t, hasUnresolvedBlockers([]domain.TaskDependency{legacy}, cats), "empty condition behaves as task_done")
	legacy.Condition = domain.DependencyConditionTaskDone
	assert.True(t, hasUnresolvedBlockers([]domain.TaskDependency{legacy}, cats))
	noEvidence := legacy
	noEvidence.Condition, noEvidence.ConditionMetAt = domain.DependencyConditionAccessReady, &met
	assert.True(t, hasUnresolvedBlockers([]domain.TaskDependency{noEvidence}, cats), "met without evidence_url holds")
}

func TestTaskDependencyService_Create_RejectPolicyReturns409(t *testing.T) {
	svc, depRepo, taskRepo := setupTaskDependencyService()
	projects := NewMockProjectRepository().WithDefaultWorkspace(uuid.New())
	svc.projectRepo = projects
	dependent, monitor := uuid.New(), uuid.New()
	addTask(taskRepo, dependent)
	taskRepo.items[monitor] = &domain.Task{ID: monitor, Labels: []string{"kind:monitor"}}

	// Default (policy off): warning only.
	ok := &domain.TaskDependency{TaskID: dependent, DependsOnTaskID: monitor, DependencyType: domain.DependencyTypeBlocks}
	require.NoError(t, svc.Create(context.Background(), ok))
	assert.NotEmpty(t, ok.Warning)
	delete(depRepo.items, ok.ID)

	projects.items[uuid.Nil] = &domain.Project{ID: uuid.Nil, WorkspaceID: projects.defaultWorkspace, Settings: []byte(`{"reject_blocks_on_monitor":true}`)}
	err := svc.Create(context.Background(), &domain.TaskDependency{TaskID: dependent, DependsOnTaskID: monitor, DependencyType: domain.DependencyTypeBlocks})
	require.Error(t, err)
	assert.Equal(t, http.StatusConflict, err.(*apierror.Error).Code)
	assert.Empty(t, depRepo.items, "a refused edge is not stored")

	// An edge that names its own condition is allowed even under the policy.
	good := &domain.TaskDependency{TaskID: dependent, DependsOnTaskID: monitor, DependencyType: domain.DependencyTypeBlocks,
		Condition: domain.DependencyConditionAccessReady}
	require.NoError(t, svc.Create(context.Background(), good))
}

func TestTaskDependencyService_UpdateCondition_Branches(t *testing.T) {
	ctx := context.Background()
	svc, depRepo, taskRepo := setupTaskDependencyService()
	a, b := uuid.New(), uuid.New()
	addTask(taskRepo, a)
	addTask(taskRepo, b)
	cond := domain.DependencyConditionAccessReady

	// relates_to edges carry no condition.
	rel := &domain.TaskDependency{TaskID: a, DependsOnTaskID: b, DependencyType: domain.DependencyTypeRelatesTo}
	require.NoError(t, svc.Create(ctx, rel))
	_, err := svc.UpdateCondition(ctx, rel.ID, UpdateDependencyConditionInput{Condition: &cond})
	require.Error(t, err)

	// Invalid condition.
	bad := domain.DependencyCondition("nonsense")
	_, err = svc.UpdateCondition(ctx, rel.ID, UpdateDependencyConditionInput{Condition: &bad})
	require.Error(t, err)

	// Re-sending the same evidence keeps the original met-time.
	dep := &domain.TaskDependency{TaskID: b, DependsOnTaskID: a, DependencyType: domain.DependencyTypeBlocks}
	require.NoError(t, svc.Create(ctx, dep))
	first, err := svc.UpdateCondition(ctx, dep.ID, UpdateDependencyConditionInput{Condition: &cond, EvidenceURL: strPtr("https://mesh.example/e")})
	require.NoError(t, err)
	timeNow = func() time.Time { return frozenTime.Add(time.Hour) }
	again, err := svc.UpdateCondition(ctx, dep.ID, UpdateDependencyConditionInput{EvidenceURL: strPtr("https://mesh.example/e")})
	require.NoError(t, err)
	assert.Equal(t, first.ConditionMetAt, again.ConditionMetAt)
	changed, err := svc.UpdateCondition(ctx, dep.ID, UpdateDependencyConditionInput{EvidenceURL: strPtr("https://mesh.example/e2")})
	require.NoError(t, err)
	assert.NotEqual(t, first.ConditionMetAt, changed.ConditionMetAt, "new evidence is a new observation")

	// Back to task_done drops the evidence; a store failure is returned.
	done := domain.DependencyConditionTaskDone
	_, err = svc.UpdateCondition(ctx, dep.ID, UpdateDependencyConditionInput{Condition: &done, EvidenceURL: strPtr("")})
	require.NoError(t, err)
	assert.Nil(t, depRepo.items[dep.ID].EvidenceURL)
	depRepo.errToReturn = assert.AnError
	_, err = svc.UpdateCondition(ctx, dep.ID, UpdateDependencyConditionInput{Condition: &cond})
	require.Error(t, err)
}

func TestTaskDependencyService_Create_ConditionNormalisation(t *testing.T) {
	ctx := context.Background()
	svc, _, taskRepo := setupTaskDependencyService()
	a, b := uuid.New(), uuid.New()
	addTask(taskRepo, a)
	addTask(taskRepo, b)

	// Blank evidence is dropped, so the edge is not satisfied.
	dep := &domain.TaskDependency{TaskID: a, DependsOnTaskID: b, DependencyType: domain.DependencyTypeBlocks,
		Condition: domain.DependencyConditionDeployed, EvidenceURL: strPtr("  ")}
	require.NoError(t, svc.Create(ctx, dep))
	assert.Nil(t, dep.EvidenceURL)
	assert.Nil(t, dep.ConditionMetAt)
	assert.False(t, dep.ConditionSatisfied)

	// Evidence on a task_done edge is meaningless and is dropped.
	c := uuid.New()
	addTask(taskRepo, c)
	dep2 := &domain.TaskDependency{TaskID: c, DependsOnTaskID: b, DependencyType: domain.DependencyTypeBlocks, EvidenceURL: strPtr("https://mesh.example/e")}
	require.NoError(t, svc.Create(ctx, dep2))
	assert.Nil(t, dep2.EvidenceURL)
}

func TestTaskDependencyService_Create_SatisfiedEdgeNotifiesResolver(t *testing.T) {
	ctx := context.Background()
	svc, _, taskRepo := setupTaskDependencyService()
	spy := &resolverSpy{}
	WithDependencyResolver(svc, spy)
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	addTask(taskRepo, a)
	addTask(taskRepo, b)
	addTask(taskRepo, c)

	plain := &domain.TaskDependency{TaskID: a, DependsOnTaskID: b, DependencyType: domain.DependencyTypeBlocks}
	require.NoError(t, svc.Create(ctx, plain))
	assert.Empty(t, spy.resolved, "an unsatisfied edge releases nothing")

	met := &domain.TaskDependency{
		TaskID: a, DependsOnTaskID: c, DependencyType: domain.DependencyTypeBlocks,
		Condition: domain.DependencyConditionAccessReady, EvidenceURL: strPtr("https://mesh.example/e"),
	}
	require.NoError(t, svc.Create(ctx, met))
	assert.Equal(t, []uuid.UUID{c}, spy.resolved, "an edge born satisfied re-evaluates the blocker's dependents")
}
