package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Task #318de303, AC3: "throwaway/probe-карточкам hard-гейт запрещён (soft-гейт
// разрешён)". A disposable task (labeled throwaway/probe/kind:sweep, or titled as a
// HARNESS FIXTURE) is never coming back to a human for an answer — a hard gate on one
// just accumulates in Pavel's queue forever. Soft self-releases on the
// default-on-timeout sweep, so it does not have that failure mode.

// newDisposableArmingTestService seeds a task carrying the given labels/title, mirroring
// newArmingTestService (human_gate_arming_test.go) but with a customizable task shape —
// that helper hardcodes an undisposable fixture task and is not reused here on purpose.
func newDisposableArmingTestService(t *testing.T, labels []string, title string) (TaskService, *MockTaskRepository, uuid.UUID) {
	t.Helper()
	taskRepo := NewMockTaskRepository()
	svc := newTestTaskService(taskRepo, NewMockTaskStatusRepository(),
		NewMockTaskDependencyRepository(), NewMockActivityLogRepository())

	taskID := uuid.New()
	if title == "" {
		title = "ordinary task"
	}
	taskRepo.items[taskID] = &domain.Task{ID: taskID, Title: title, Labels: labels}
	return svc, taskRepo, taskID
}

// TestArmHumanGate_DisposableAPIHardRefused covers every marker named in AC3 —
// throwaway/probe/kind:sweep labels and the HARNESS FIXTURE title convention — on the
// ArmHumanGateSourceAPI path (the literal set_human_gate/POST call, where a 422 is a
// meaningful, client-visible outcome).
func TestArmHumanGate_DisposableAPIHardRefused(t *testing.T) {
	cases := []struct {
		name   string
		labels []string
		title  string
	}{
		{name: "throwaway label", labels: []string{"throwaway"}},
		{name: "probe label", labels: []string{"probe"}},
		{name: "kind:sweep label", labels: []string{"kind:sweep"}},
		{name: "label case-insensitive", labels: []string{"Throwaway"}},
		{name: "HARNESS FIXTURE in title", title: "TestFoo — HARNESS FIXTURE, delete after run"},
		{name: "title marker case-insensitive", title: "harness fixture for #4545660b"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, taskID := newDisposableArmingTestService(t, tc.labels, tc.title)

			err := svc.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
				TaskID:             taskID,
				Author:             uuid.New(),
				AuthorType:         domain.ActorTypeAgent,
				Reason:             "why",
				RecommendedDefault: "what I will do otherwise",
				Class:              domain.HumanGateClassHard,
				Source:             domain.ArmHumanGateSourceAPI,
				Predicate:          allowingPredicate(),
			})
			require.Error(t, err, "a hard gate on a disposable task must be refused")

			var vErr *domain.ArmHumanGateValidationError
			require.ErrorAs(t, err, &vErr)
			assert.Equal(t, "class", vErr.Field)

			assert.False(t, repo.items[taskID].HumanGate,
				"a refused arm must not have armed the gate anyway")
		})
	}
}

// TestArmHumanGate_DisposableAPISoftAllowed is the other half of AC3: soft is not
// refused. A guard that also blocked soft would make a disposable task ungatable at
// all, which is not what was asked for.
func TestArmHumanGate_DisposableAPISoftAllowed(t *testing.T) {
	svc, repo, taskID := newDisposableArmingTestService(t, []string{"probe"}, "")

	err := svc.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID:             taskID,
		Author:             uuid.New(),
		AuthorType:         domain.ActorTypeAgent,
		Reason:             "why",
		RecommendedDefault: "what I will do otherwise",
		Class:              domain.HumanGateClassSoft,
		Source:             domain.ArmHumanGateSourceAPI,
		Predicate:          allowingPredicate(),
	})
	require.NoError(t, err, "a soft gate on a disposable task must still be allowed")
	assert.True(t, repo.items[taskID].HumanGate)
	assert.Equal(t, domain.HumanGateClassSoft, repo.items[taskID].HumanGateClass)
}

// TestArmHumanGate_DisposableMarkerDowngradedNotDropped is the asymmetric half, mirroring
// TestArmHumanGate_MarkerSourceAllowsMissingDefault right above it in
// human_gate_arming_test.go: a live "❓ Blocking @pavel" comment is a real human ask
// arriving through a channel this service does not control. Refusing it outright would
// silently drop the ask for its author — the exact #58a6f4ff/#f421ad57 failure class —
// so a disposable task downgrades the marker to soft instead of refusing it.
func TestArmHumanGate_DisposableMarkerDowngradedNotDropped(t *testing.T) {
	svc, repo, taskID := newDisposableArmingTestService(t, []string{"throwaway"}, "")
	author := uuid.New()

	err := svc.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID:     taskID,
		Author:     author,
		AuthorType: domain.ActorTypeAgent,
		Reason:     "какой шлюз выбираем?",
		Class:      domain.HumanGateClassHard,
		Source:     domain.ArmHumanGateSourceMarker,
	})
	require.NoError(t, err, "a marker-sourced ask must always deliver, never be dropped")

	got := repo.items[taskID]
	assert.True(t, got.HumanGate, "the ask must still arm the gate")
	require.NotNil(t, got.GateAuthor)
	assert.Equal(t, author, *got.GateAuthor)
	assert.Equal(t, domain.HumanGateClassSoft, got.HumanGateClass,
		"hard must be downgraded to soft on a disposable task, not honored as hard")
}

// TestArmHumanGate_NonDisposableTaskHardStillAllowed is the negative control: an
// ordinary task with no disposable label and no fixture-title marker is unaffected by
// this guard. Without this, a guard that refused EVERY hard arm would pass the tests
// above for the wrong reason.
func TestArmHumanGate_NonDisposableTaskHardStillAllowed(t *testing.T) {
	svc, repo, taskID := newDisposableArmingTestService(t, []string{"mesh-dev", "urgent"}, "Fix the real bug")

	err := svc.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID:             taskID,
		Author:             uuid.New(),
		AuthorType:         domain.ActorTypeAgent,
		Reason:             "why",
		RecommendedDefault: "what I will do otherwise",
		Class:              domain.HumanGateClassHard,
		Source:             domain.ArmHumanGateSourceAPI,
		Predicate:          allowingPredicate(),
	})
	require.NoError(t, err, "an ordinary task must not be caught by the disposable guard")
	assert.True(t, repo.items[taskID].HumanGate)
	assert.Equal(t, domain.HumanGateClassHard, repo.items[taskID].HumanGateClass)
}
