package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// reviewAssigneeAuditEnv wires a task service identical to
// TestTaskService_MoveTask_ReviewAssignee's SetReviewer=lead fixture, plus a
// MockCommentRepository — the review-reassign tests above never wire one, so
// applyReviewAssignee/restorePreReviewAssignee ran with s.commentRepo == nil and
// the "post a comment" half of this fix would silently no-op under them.
type reviewAssigneeAuditEnv struct {
	svc          *taskService
	tasks        *MockTaskRepository
	commentRepo  *MockCommentRepository
	activityRepo *MockActivityLogRepository

	projectID, builderID, leadID       uuid.UUID
	inProgressStatusID, reviewStatusID uuid.UUID
}

func setupReviewAssigneeAuditEnv(t *testing.T) *reviewAssigneeAuditEnv {
	t.Helper()
	workspaceID := testDefaultWorkspaceID
	env := &reviewAssigneeAuditEnv{
		projectID:          uuid.New(),
		builderID:          uuid.New(),
		leadID:             uuid.New(),
		inProgressStatusID: uuid.New(),
		reviewStatusID:     uuid.New(),
	}

	wfResp := &domain.WorkflowRulesResponse{
		WorkflowRulesConfig: domain.WorkflowRulesConfig{
			Transitions: map[string]domain.TransitionRule{
				"in_progress": {
					Allowed:      []string{"review"},
					OnTransition: &domain.TransitionAction{SetReviewer: "lead"},
				},
			},
		},
	}
	effectiveRules := &domain.EffectiveAssignmentRules{
		DefaultAssignee: &domain.EffectiveAssignmentRule{Value: "garfield", Source: "project"},
	}
	svc, taskRepo, statusRepo, agentRepo, projRepo := setupTaskServiceWithWorkflow(wfResp, effectiveRules)

	env.commentRepo = NewMockCommentRepository()
	svc.commentRepo = env.commentRepo
	// setupTaskServiceWithWorkflow does not hand back the activity mock it wires;
	// it is the same package, so pull it back out of the constructed service
	// rather than re-wiring a second one that MoveTask would never see.
	env.activityRepo = svc.activityRepo.(*MockActivityLogRepository)

	statusRepo.items[env.inProgressStatusID] = &domain.TaskStatus{
		ID: env.inProgressStatusID, ProjectID: env.projectID, Category: domain.StatusCategoryInProgress, Name: "in_progress",
	}
	statusRepo.items[env.reviewStatusID] = &domain.TaskStatus{
		ID: env.reviewStatusID, ProjectID: env.projectID, Category: domain.StatusCategoryReview, Name: "review",
	}
	projRepo.items[env.projectID] = &domain.Project{ID: env.projectID, WorkspaceID: workspaceID}
	agentRepo.items[env.leadID] = &domain.Agent{ID: env.leadID, WorkspaceID: workspaceID, Slug: "garfield"}
	agentRepo.items[env.builderID] = &domain.Agent{ID: env.builderID, WorkspaceID: workspaceID, Slug: "builder"}

	env.svc, env.tasks = svc, taskRepo
	return env
}

// seedTask puts a task in the in_progress status, assigned to the builder, with
// enough evidence (an artifact) to clear the review-evidence gate — which is
// only active in this fixture because, unlike setupTaskServiceWithWorkflow's
// bare default, a commentRepo is wired here.
func (e *reviewAssigneeAuditEnv) seedTask() uuid.UUID {
	taskID := uuid.New()
	e.tasks.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: e.projectID, StatusID: e.inProgressStatusID,
		AssigneeID: &e.builderID, AssigneeType: domain.AssigneeTypeAgent,
		ArtifactCount: 1,
	}
	return taskID
}

// activityAssigneeOld unmarshals one activity entry's Changes and returns the
// assignee_id.old field, or nil if the entry carries no such key. Returns an
// "isPresent" flag so a genuinely-nil-but-recorded old (unassigned before) can be
// told apart from "never checked, so it wasn't found".
func activityAssigneeOld(t *testing.T, entry *domain.ActivityLog) (old any, isPresent bool) {
	t.Helper()
	var changes map[string]any
	require.NoError(t, json.Unmarshal(entry.Changes, &changes))
	assigneeChange, ok := changes["assignee_id"].(map[string]any)
	if !ok {
		return nil, false
	}
	old, isPresent = assigneeChange["old"]
	return old, isPresent
}

// findActivityByReason returns the single "task.assigned" entry on taskID whose
// Changes.reason matches, or fails the test — each subtest here triggers exactly
// one assignee-changing transition, so exactly one entry must match.
func findActivityByReason(t *testing.T, repo *MockActivityLogRepository, taskID uuid.UUID, reason string) *domain.ActivityLog {
	t.Helper()
	var match *domain.ActivityLog
	for _, e := range repo.items {
		if e.EntityID != taskID || e.Action != "task.assigned" {
			continue
		}
		var changes map[string]any
		if err := json.Unmarshal(e.Changes, &changes); err != nil {
			continue
		}
		if r, _ := changes["reason"].(string); r == reason {
			require.Nil(t, match, "more than one task.assigned activity entry carries reason %q", reason)
			match = e
		}
	}
	require.NotNil(t, match, "no task.assigned activity entry found with reason %q", reason)
	return match
}

// TestApplyReviewAssignee_RecordsRealOldAndPostsComment pins DoD #1/#2 from task
// f06ebeb7 for the set_reviewer review-transition bounce: before the fix, the
// activity entry always recorded "old": nil regardless of who held the card, and
// no comment was posted at all — an agent reading the thread had no way to tell
// the card had moved out from under them.
func TestApplyReviewAssignee_RecordsRealOldAndPostsComment(t *testing.T) {
	env := setupReviewAssigneeAuditEnv(t)
	taskID := env.seedTask()

	require.NoError(t, env.svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &env.reviewStatusID}))

	entry := findActivityByReason(t, env.activityRepo, taskID, "set_reviewer on review transition")
	old, isPresent := activityAssigneeOld(t, entry)
	require.True(t, isPresent, "assignee_id.old must be present in the activity entry")
	assert.Equal(t, env.builderID.String(), old,
		"assignee_id.old must name the builder who actually held the card, not nil")

	require.Len(t, env.commentRepo.items, 1, "the reviewer bounce must post exactly one comment")
	for _, c := range env.commentRepo.items {
		assert.Equal(t, domain.ActorTypeSystem, c.AuthorType)
		assert.Contains(t, c.Body, env.builderID.String(), "comment must name who the card came from")
		assert.Contains(t, c.Body, env.leadID.String(), "comment must name who the card went to")
	}
}

// TestRestorePreReviewAssignee_RecordsRealOldAndPostsComment pins the same two
// guarantees for the symmetric restore-on-bounce-out-of-review path.
func TestRestorePreReviewAssignee_RecordsRealOldAndPostsComment(t *testing.T) {
	env := setupReviewAssigneeAuditEnv(t)
	taskID := env.seedTask()

	backToTodoID := uuid.New()
	env.svc.statusRepo.(*MockTaskStatusRepository).items[backToTodoID] = &domain.TaskStatus{
		ID: backToTodoID, ProjectID: env.projectID, Category: domain.StatusCategoryTodo, Name: "todo",
	}

	require.NoError(t, env.svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &env.reviewStatusID}))
	require.NoError(t, env.svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &backToTodoID}))

	entry := findActivityByReason(t, env.activityRepo, taskID, "restored pre-review assignee on bounce out of review")
	old, isPresent := activityAssigneeOld(t, entry)
	require.True(t, isPresent, "assignee_id.old must be present in the activity entry")
	assert.Equal(t, env.leadID.String(), old,
		"assignee_id.old must name the reviewer the card is being taken FROM, not nil")

	require.Len(t, env.commentRepo.items, 2, "one comment for the bounce-in, one for the restore")
	var restoreComment *domain.Comment
	for _, c := range env.commentRepo.items {
		if strings.Contains(c.Body, "restored pre-review assignee") {
			require.Nil(t, restoreComment, "more than one comment carries the restore reason")
			restoreComment = c
		}
	}
	require.NotNil(t, restoreComment, "no comment found carrying the restore reason")
	assert.Contains(t, restoreComment.Body, env.leadID.String(), "restore comment must name who the card came from (the reviewer)")
	assert.Contains(t, restoreComment.Body, env.builderID.String(), "restore comment must name who the card went back to")
}

// TestApplyReviewAssignee_CommentPostFailureDoesNotBlockAssignment pins DoD #2's
// second half: a comment-post failure must not unwind the assignee change that
// already happened. Without this, a transient comment-repo error would silently
// strand the card on whichever principal the write reached before failing.
func TestApplyReviewAssignee_CommentPostFailureDoesNotBlockAssignment(t *testing.T) {
	env := setupReviewAssigneeAuditEnv(t)
	env.commentRepo.errToReturn = assert.AnError
	taskID := env.seedTask()

	require.NoError(t, env.svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &env.reviewStatusID}),
		"MoveTask itself must not fail just because the audit comment could not be posted")

	task := env.tasks.items[taskID]
	require.NotNil(t, task.AssigneeID)
	assert.Equal(t, env.leadID, *task.AssigneeID, "the reviewer rotation must still take effect")
	assert.Empty(t, env.commentRepo.items, "the failed comment write must not have landed")
}
