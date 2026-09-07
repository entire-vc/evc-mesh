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

// newArmingTestServiceWithComments is newArmingTestService plus a wired
// MockCommentRepository, for tests that need to observe the WARNING comment
// ArmHumanGate posts when it auto-fills recommended_default on a marker-sourced arm
// (task 1.4b, #4d61d877). The plain helper above leaves commentRepo nil, which is
// exactly the "not wired" state postMarkerDefaultAppliedComment's own nil guard exists
// for — every pre-existing test using it stays a no-op on this new code path.
func newArmingTestServiceWithComments(t *testing.T) (TaskService, *MockTaskRepository, *MockCommentRepository, uuid.UUID) {
	t.Helper()
	taskRepo := NewMockTaskRepository()
	commentRepo := NewMockCommentRepository()
	svc := newTestTaskService(taskRepo, NewMockTaskStatusRepository(),
		NewMockTaskDependencyRepository(), NewMockActivityLogRepository(),
		WithCommentRepoTask(commentRepo))

	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{ID: taskID, Title: "gate arming fixture"}
	return svc, taskRepo, commentRepo, taskID
}

// firstCommentOn returns the first comment posted to taskID, for tests that only ever
// expect exactly one.
func firstCommentOn(repo *MockCommentRepository, taskID uuid.UUID) *domain.Comment {
	for _, c := range repo.items {
		if c.TaskID == taskID {
			return c
		}
	}
	return nil
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
// and #f421ad57, i.e. strictly worse than the bug being fixed.
//
// Task 1.4b (#4d61d877) changed WHAT "allows through" means, but not the asymmetry
// itself: a marker with no stated default is still armed (never refused), but no longer
// leaves recommended_default NULL forever — it is auto-filled with
// domain.DefaultMarkerRecommendedDefault so the gate gets a computed gate_deadline and
// can be resolved by a clock. This test used to assert the field stayed nil; that
// assertion is now the very defect this card exists to fix (94 of 97 live gates on
// 2026-09-07 had exactly that shape).
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
	require.NotNil(t, got.RecommendedDefault,
		"no default was stated, so the system default must be auto-filled — a permanently NULL field is the bug")
	assert.Equal(t, domain.DefaultMarkerRecommendedDefault, *got.RecommendedDefault)

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

// TestArmHumanGate_MarkerWithNoDefault_PostsWarningComment is the positive control for
// task 1.4b (#4d61d877): a marker-sourced arm that names no recommended_default posts a
// task comment naming the auto-applied system default AND the computed deadline — the
// pre-existing log-only WARNING reached nobody in practice.
func TestArmHumanGate_MarkerWithNoDefault_PostsWarningComment(t *testing.T) {
	svc, taskRepo, commentRepo, taskID := newArmingTestServiceWithComments(t)

	require.NoError(t, svc.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID:     taskID,
		Author:     uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Reason:     "какой шлюз выбираем?",
		Class:      domain.HumanGateClassSoft,
		Source:     domain.ArmHumanGateSourceMarker,
	}))

	got := taskRepo.items[taskID]
	require.NotNil(t, got.RecommendedDefault)
	assert.Equal(t, domain.DefaultMarkerRecommendedDefault, *got.RecommendedDefault)
	require.NotNil(t, got.GateDeadline, "soft + a real default (even auto-filled) must get a computed deadline")

	found := firstCommentOn(commentRepo, taskID)
	require.NotNil(t, found, "the WARNING must reach the task as a comment, not only the server log")
	assert.Equal(t, domain.ActorTypeSystem, found.AuthorType)
	assert.Contains(t, found.Body, "системный дефолт")
	assert.Contains(t, found.Body, domain.DefaultMarkerRecommendedDefault)
	assert.Contains(t, found.Body, got.GateDeadline.Format(time.RFC3339))
	assert.Contains(t, found.Body, "применится автоматически",
		"soft class must say the default applies automatically at the deadline")
}

// TestArmHumanGate_MarkerWithNoDefault_HardClassNotesEscalationOnly covers the other
// class message: a hard gate gets the same auto-filled default and computed deadline,
// but the comment must say the deadline is for escalation/visibility only — no code path
// auto-releases a hard gate on a clock (FindExpiredDefaultGates's own
// human_gate_class != 'hard' predicate, task_repo.go).
func TestArmHumanGate_MarkerWithNoDefault_HardClassNotesEscalationOnly(t *testing.T) {
	svc, taskRepo, commentRepo, taskID := newArmingTestServiceWithComments(t)

	require.NoError(t, svc.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID:     taskID,
		Author:     uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Reason:     "перевести партнёру $500?",
		Class:      domain.HumanGateClassHard,
		Source:     domain.ArmHumanGateSourceMarker,
	}))

	got := taskRepo.items[taskID]
	require.NotNil(t, got.GateDeadline,
		"hard now gets a deadline too once it has a real default — for escalation, not auto-release")

	found := firstCommentOn(commentRepo, taskID)
	require.NotNil(t, found)
	assert.Contains(t, found.Body, "эскалации")
	assert.NotContains(t, found.Body, "применится автоматически",
		"a hard gate's deadline must never be described as self-applying — nothing releases it on a clock")
}

// TestArmHumanGate_APISourceWithDefault_DoesNotPostWarningComment is the negative
// control: the auto-fill and its comment are specific to a marker-sourced arm with no
// stated default. An ordinary API arm that already names one must not get a spurious
// notice.
func TestArmHumanGate_APISourceWithDefault_DoesNotPostWarningComment(t *testing.T) {
	svc, _, commentRepo, taskID := newArmingTestServiceWithComments(t)

	require.NoError(t, svc.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID: taskID, Author: uuid.New(), AuthorType: domain.ActorTypeAgent,
		Reason: "r", RecommendedDefault: "d",
		Source:    domain.ArmHumanGateSourceAPI,
		Predicate: allowingPredicate(),
	}))

	assert.Nil(t, firstCommentOn(commentRepo, taskID),
		"an API arm that already states a default must not get the auto-fill notice")
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
