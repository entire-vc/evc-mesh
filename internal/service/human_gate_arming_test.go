package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Task #4545660b — one implementation of "this card is waiting on a human".
//
// The defect these tests pin down is not "the gate does not work". `human_gate` has
// been correct all along. The defect is that the REST of the answer — who asked, what
// they asked, what happens if nobody answers — lived only in comment TEXT, so 21
// separate places across the fleet re-derived it by grepping, each with its own marker
// dictionary. That is what made class-C3 phantom gates possible at all: a driver printed
// the marker as instructional boilerplate and the next driver read its own output back
// as a raised blocker (#84ab54fd).
//
// So the assertions below are deliberately about the TASK ROW, never about a message or
// a call count: after arming, everything a client needs must be readable from the task,
// because that is the only property that lets `is_human_gated` collapse to one field.

// allowingPredicate is a four-answer set that legitimately needs a human: the action is
// NOT reversible, so the "just do it" refusal does not apply (task #5d3dc714). Used by
// the pre-existing #4545660b tests, which predate the predicate requirement and are
// about a different property — they must keep testing THAT property, not accidentally
// become predicate tests.
func allowingPredicate() *domain.GateArmPredicate {
	return &domain.GateArmPredicate{
		CredentialExists:   true,
		CredentialReason:   "gateway token is in keys.env",
		Reversible:         false,
		ReversibleReason:   "an outbound payment cannot be un-sent",
		BlockedByOtherTask: false,
		BlockedReason:      "no other card owns this",
		CustomerVisibleNow: false,
		CustomerReason:     "gateway is inactive, nobody can be charged",
	}
}

func newArmingTestService(t *testing.T) (TaskService, *MockTaskRepository, uuid.UUID) {
	t.Helper()
	taskRepo := NewMockTaskRepository()
	svc := newTestTaskService(taskRepo, NewMockTaskStatusRepository(),
		NewMockTaskDependencyRepository(), NewMockActivityLogRepository())

	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{ID: taskID, Title: "gate arming fixture"}
	return svc, taskRepo, taskID
}

// TestArmHumanGate_WritesWholeAskOntoTask is the positive control for AC "API-тест на
// арм": one call must leave every field a reader needs on the task itself.
func TestArmHumanGate_WritesWholeAskOntoTask(t *testing.T) {
	svc, repo, taskID := newArmingTestService(t)
	author := uuid.New()
	deadline := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

	err := svc.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID:             taskID,
		Author:             author,
		AuthorType:         domain.ActorTypeAgent,
		Reason:             "Точка gateway is inactive — merge now or wait?",
		RecommendedDefault: "merge; the gateway is inactive so no client can be charged",
		Deadline:           &deadline,
		Class:              domain.HumanGateClassSoft,
		Source:             domain.ArmHumanGateSourceAPI,
		Predicate:          allowingPredicate(),
	})
	require.NoError(t, err)

	got := repo.items[taskID]
	assert.True(t, got.HumanGate, "the flag every client reads must be armed")
	require.NotNil(t, got.GateAuthor)
	assert.Equal(t, author, *got.GateAuthor, "gate_author must name WHO is waiting, not just THAT someone is")
	require.NotNil(t, got.GateAuthorType)
	assert.Equal(t, domain.ActorTypeAgent, *got.GateAuthorType)
	require.NotNil(t, got.GateReason)
	assert.Contains(t, *got.GateReason, "Точка gateway")
	require.NotNil(t, got.RecommendedDefault)
	assert.Contains(t, *got.RecommendedDefault, "merge")
	require.NotNil(t, got.GateDeadline)
	assert.Equal(t, deadline, *got.GateDeadline)
	assert.Equal(t, domain.HumanGateClassSoft, got.HumanGateClass)
}

// TestArmHumanGate_RejectsIncompleteAsk is the RED half. Each case is a shape that used
// to be armable and produced a gate nobody could resolve except by finding a human.
func TestArmHumanGate_RejectsIncompleteAsk(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*domain.ArmHumanGateInput)
		wantField string
	}{
		{
			name:      "no author — the gate could never be withdrawn by its owner",
			mutate:    func(in *domain.ArmHumanGateInput) { in.Author = uuid.Nil },
			wantField: "gate_author",
		},
		{
			name:      "no recommended_default on an API arm — the gate can never time out",
			mutate:    func(in *domain.ArmHumanGateInput) { in.RecommendedDefault = "" },
			wantField: "recommended_default",
		},
		{
			name:      "author type not an actor type",
			mutate:    func(in *domain.ArmHumanGateInput) { in.AuthorType = domain.ActorType("robot") },
			wantField: "gate_author_type",
		},
		{
			name:      "no task",
			mutate:    func(in *domain.ArmHumanGateInput) { in.TaskID = uuid.Nil },
			wantField: "task_id",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, taskID := newArmingTestService(t)
			in := domain.ArmHumanGateInput{
				TaskID:             taskID,
				Author:             uuid.New(),
				AuthorType:         domain.ActorTypeAgent,
				Reason:             "why",
				RecommendedDefault: "what I will do otherwise",
				Source:             domain.ArmHumanGateSourceAPI,
				Predicate:          allowingPredicate(),
			}
			tc.mutate(&in)

			err := svc.ArmHumanGate(context.Background(), in)
			require.Error(t, err, "an incomplete ask must be refused, not silently armed")

			var vErr *domain.ArmHumanGateValidationError
			require.ErrorAs(t, err, &vErr)
			assert.Equal(t, tc.wantField, vErr.Field,
				"the refusal must NAME the field — an unnamed 'Validation failed' gets retried verbatim")

			// The wall actually stands: a refused arm leaves the task untouched. Without
			// this half, a validator that returned an error AND armed anyway would pass.
			if in.TaskID != uuid.Nil {
				assert.False(t, repo.items[taskID].HumanGate,
					"a refused arm must not have armed the gate anyway")
			}
		})
	}
}

// TestArmHumanGate_MarkerSourceAllowsMissingDefault pins the ONE deliberate asymmetry.
// Refusing a marker with no stated default would be silent to its author — they post the
// question, believe it was handed over, and the card keeps being fed. That is #58a6f4ff
// and #f421ad57, i.e. strictly worse than the bug being fixed. Tightening this is 1.4.
func TestArmHumanGate_MarkerSourceAllowsMissingDefault(t *testing.T) {
	svc, repo, taskID := newArmingTestService(t)
	author := uuid.New()

	err := svc.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID:     taskID,
		Author:     author,
		AuthorType: domain.ActorTypeAgent,
		Reason:     "какой шлюз выбираем?",
		Source:     domain.ArmHumanGateSourceMarker,
	})
	require.NoError(t, err, "a live marker must always deliver, even without a stated default")

	got := repo.items[taskID]
	assert.True(t, got.HumanGate)
	require.NotNil(t, got.GateAuthor)
	assert.Equal(t, author, *got.GateAuthor)
	assert.Nil(t, got.RecommendedDefault, "no default was stated, so none is recorded")

	// NEGATIVE CONTROL for the asymmetry: the SAME input from the API source is refused.
	// Without this the test above would pass equally on a service that validates nothing.
	svc2, repo2, taskID2 := newArmingTestService(t)
	err = svc2.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID:     taskID2,
		Author:     author,
		AuthorType: domain.ActorTypeAgent,
		Reason:     "какой шлюз выбираем?",
		Source:     domain.ArmHumanGateSourceAPI,
		Predicate:  allowingPredicate(),
	})
	require.Error(t, err, "the same incomplete ask from the API path must be refused")
	assert.False(t, repo2.items[taskID2].HumanGate)
}

// TestClearHumanGate_DropsTheAskWithIt: the ask metadata describes a LIVE question.
// Leaving a recommended_default on a released task is residue every reader would have to
// learn to ignore — and a reader who did not would apply a default to a settled question.
func TestClearHumanGate_DropsTheAskWithIt(t *testing.T) {
	svc, repo, taskID := newArmingTestService(t)
	deadline := time.Now().Add(72 * time.Hour)

	require.NoError(t, svc.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID: taskID, Author: uuid.New(), AuthorType: domain.ActorTypeAgent,
		Reason: "r", RecommendedDefault: "d", Deadline: &deadline,
		Class: domain.HumanGateClassSoft, Source: domain.ArmHumanGateSourceAPI,
		Predicate: allowingPredicate(),
	}))
	require.True(t, repo.items[taskID].HumanGate, "precondition: gate is armed")

	require.NoError(t, svc.ClearHumanGate(context.Background(), taskID))

	got := repo.items[taskID]
	assert.False(t, got.HumanGate)
	assert.Equal(t, domain.HumanGateClassHard, got.HumanGateClass,
		"class resets to hard — a soft classification must not outlive the ask it was set for")
	assert.Nil(t, got.GateAuthor, "the ask is over; its author is no longer waiting on anything")
	assert.Nil(t, got.GateReason)
	assert.Nil(t, got.RecommendedDefault,
		"a default left on a settled question is a default something will eventually apply")
	assert.Nil(t, got.GateDeadline)
}

// TestArmHumanGate_NormalizesFailClosed: an omitted class must land on hard, never on the
// empty string. Empty would violate the column CHECK, and "softened by omission" is the
// exact direction a gate must never drift.
func TestArmHumanGate_NormalizesFailClosed(t *testing.T) {
	svc, repo, taskID := newArmingTestService(t)

	require.NoError(t, svc.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID: taskID, Author: uuid.New(), AuthorType: domain.ActorTypeUser,
		Reason: "r", RecommendedDefault: "d",
		Predicate: allowingPredicate(),
		// Class and Source deliberately omitted.
	}))
	assert.Equal(t, domain.HumanGateClassHard, repo.items[taskID].HumanGateClass)
}
