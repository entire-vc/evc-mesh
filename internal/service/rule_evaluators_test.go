package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// makeRequireCommentRule builds a transition_gate.require_comment rule with the given config.
func makeRequireCommentRule(minLen int, targetCategories ...string) domain.Rule {
	cfg, _ := json.Marshal(map[string]interface{}{
		"min_comment_length":       minLen,
		"target_status_categories": targetCategories,
	})
	return domain.Rule{
		ID:          uuid.New(),
		Name:        "Agent must comment before done",
		RuleType:    "transition_gate.require_comment",
		Enforcement: domain.RuleEnforcementBlock,
		Config:      cfg,
	}
}

// makeDoneStatus returns a TaskStatus whose category is "done".
func makeDoneStatus() *domain.TaskStatus {
	return &domain.TaskStatus{
		ID:       uuid.New(),
		Name:     "Done",
		Category: domain.StatusCategoryDone,
	}
}

// requireCommentInput builds a minimal EvaluateInput for a move_task to done.
func requireCommentInput(taskID, actorID uuid.UUID, status *domain.TaskStatus) EvaluateInput {
	return EvaluateInput{
		Action:       "move_task",
		TaskID:       &taskID,
		TargetStatus: status,
		ActorID:      actorID,
		ActorType:    domain.ActorTypeAgent,
		WorkspaceID:  uuid.New(),
	}
}

// addComment inserts a comment into the mock repo with explicit timestamps.
func addComment(repo *MockCommentRepository, taskID, authorID uuid.UUID, body string, createdAt time.Time) {
	c := &domain.Comment{
		ID:        uuid.New(),
		TaskID:    taskID,
		AuthorID:  authorID,
		Body:      body,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}
	_ = repo.Create(context.Background(), c)
}

// TestEvalRequireComment_ActorHasRecentComment verifies that when the mover (actor)
// has a qualifying recent comment the rule passes — even if the assignee has no comment.
func TestEvalRequireComment_ActorHasRecentComment(t *testing.T) {
	taskID := uuid.New()
	actorID := uuid.New()
	assigneeID := uuid.New() // different from actor

	commentRepo := NewMockCommentRepository()
	addComment(commentRepo, taskID, actorID, "Verified SHIP: all ACs green, service probe OK.", time.Now().Add(-10*time.Minute))

	rule := makeRequireCommentRule(20, "done")
	status := makeDoneStatus()
	input := requireCommentInput(taskID, actorID, status)
	// Make assigneeID different to confirm it's the actor's comment that counts.
	_ = assigneeID

	deps := evaluatorDeps{commentRepo: commentRepo}
	violation, err := evalRequireComment(context.Background(), rule, input, deps)
	require.NoError(t, err)
	assert.Nil(t, violation, "actor's recent ≥20-char comment should satisfy the rule")
}

// TestEvalRequireComment_OnlyAssigneeHasComment verifies that when only the assignee
// (not the actor/mover) has a recent comment, the rule is violated.
func TestEvalRequireComment_OnlyAssigneeHasComment(t *testing.T) {
	taskID := uuid.New()
	actorID := uuid.New()
	assigneeID := uuid.New()

	commentRepo := NewMockCommentRepository()
	// Assignee commented, but actor (coordinator) did not.
	addComment(commentRepo, taskID, assigneeID, "Verified SHIP: all ACs green, service probe OK.", time.Now().Add(-5*time.Minute))

	rule := makeRequireCommentRule(20, "done")
	status := makeDoneStatus()
	input := requireCommentInput(taskID, actorID, status)

	deps := evaluatorDeps{commentRepo: commentRepo}
	violation, err := evalRequireComment(context.Background(), rule, input, deps)
	require.NoError(t, err)
	assert.NotNil(t, violation, "only the assignee commented; actor has no comment → violation expected")
}

// TestEvalRequireComment_ActorCommentTooShort verifies that a comment below minLength is rejected.
func TestEvalRequireComment_ActorCommentTooShort(t *testing.T) {
	taskID := uuid.New()
	actorID := uuid.New()

	commentRepo := NewMockCommentRepository()
	addComment(commentRepo, taskID, actorID, "ok", time.Now().Add(-5*time.Minute)) // 2 chars < 20

	rule := makeRequireCommentRule(20, "done")
	status := makeDoneStatus()
	input := requireCommentInput(taskID, actorID, status)

	deps := evaluatorDeps{commentRepo: commentRepo}
	violation, err := evalRequireComment(context.Background(), rule, input, deps)
	require.NoError(t, err)
	assert.NotNil(t, violation, "actor comment is too short → violation expected")
}

// TestEvalRequireComment_ActorCommentTooOld verifies that a comment outside the 24 h window is ignored.
func TestEvalRequireComment_ActorCommentTooOld(t *testing.T) {
	taskID := uuid.New()
	actorID := uuid.New()

	commentRepo := NewMockCommentRepository()
	addComment(commentRepo, taskID, actorID, "Verified SHIP: all ACs green, service probe OK.", time.Now().Add(-25*time.Hour))

	rule := makeRequireCommentRule(20, "done")
	status := makeDoneStatus()
	input := requireCommentInput(taskID, actorID, status)

	deps := evaluatorDeps{commentRepo: commentRepo}
	violation, err := evalRequireComment(context.Background(), rule, input, deps)
	require.NoError(t, err)
	assert.NotNil(t, violation, "actor comment is >24h old → violation expected")
}

// TestEvalRequireComment_ManyOldCommentsPlusOneRecentByActor is the primary regression test:
// a task with >10 old comments (only the assignee's) plus ONE recent comment by the actor/coordinator.
// The bug was: ListByTask page:1 size:10 ORDER BY ASC returned only the oldest 10, missing the
// coordinator's fresh comment → false 422.  After the fix (HasRecentCommentBy EXISTS query),
// this must pass.
func TestEvalRequireComment_ManyOldCommentsPlusOneRecentByActor(t *testing.T) {
	taskID := uuid.New()
	actorID := uuid.New()   // coordinator / mover
	builderID := uuid.New() // original assignee who did the work

	commentRepo := NewMockCommentRepository()

	// 15 old comments from the builder (would fill and exceed a page of 10).
	for i := 0; i < 15; i++ {
		addComment(commentRepo, taskID, builderID,
			"Progress update: step completed, continuing.", time.Now().Add(-time.Duration(15-i)*time.Hour))
	}
	// One fresh comment by the coordinator (the actor doing the move).
	addComment(commentRepo, taskID, actorID, "Coordinator-close: SHIP verified, all ACs met.", time.Now().Add(-2*time.Minute))

	rule := makeRequireCommentRule(20, "done")
	status := makeDoneStatus()
	input := requireCommentInput(taskID, actorID, status)

	deps := evaluatorDeps{commentRepo: commentRepo}
	violation, err := evalRequireComment(context.Background(), rule, input, deps)
	require.NoError(t, err)
	assert.Nil(t, violation, "coordinator's fresh comment must be found even when task has >10 older comments")
}

// TestEvalRequireComment_SkipsWhenNotMoveTask ensures the evaluator is a no-op for non-move actions.
func TestEvalRequireComment_SkipsWhenNotMoveTask(t *testing.T) {
	rule := makeRequireCommentRule(20, "done")
	input := EvaluateInput{Action: "assign_task", ActorID: uuid.New()}

	violation, err := evalRequireComment(context.Background(), rule, input, evaluatorDeps{})
	require.NoError(t, err)
	assert.Nil(t, violation)
}

// TestEvalRequireComment_SkipsWhenTargetCategoryNotMatched ensures the rule is a no-op
// when moving to a category not listed in target_status_categories.
func TestEvalRequireComment_SkipsWhenTargetCategoryNotMatched(t *testing.T) {
	taskID := uuid.New()
	actorID := uuid.New()

	rule := makeRequireCommentRule(20, "done") // only "done" triggers the rule
	reviewStatus := &domain.TaskStatus{ID: uuid.New(), Name: "In Review", Category: domain.StatusCategoryReview}
	input := requireCommentInput(taskID, actorID, reviewStatus)

	violation, err := evalRequireComment(context.Background(), rule, input, evaluatorDeps{commentRepo: NewMockCommentRepository()})
	require.NoError(t, err)
	assert.Nil(t, violation, "moving to 'review' should not trigger a rule scoped to 'done'")
}
