package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

type onEnterAssignEnv struct {
	svc          *taskService
	tasks        *MockTaskRepository
	commentRepo  *MockCommentRepository
	activityRepo *MockActivityLogRepository

	projectID, ownerID, targetID uuid.UUID
	inboxID, qualifiedID         uuid.UUID
}

// setupOnEnterAssignEnv wires a project whose workflow carries the given on_enter
// map (nil = a project that never opted in) and a card sitting in "inbox".
func setupOnEnterAssignEnv(t *testing.T, onEnter map[string]domain.OnEnterAction) *onEnterAssignEnv {
	t.Helper()
	env := &onEnterAssignEnv{
		projectID: uuid.New(), ownerID: uuid.New(), targetID: uuid.New(),
		inboxID: uuid.New(), qualifiedID: uuid.New(),
	}
	wf := &domain.WorkflowRulesResponse{WorkflowRulesConfig: domain.WorkflowRulesConfig{OnEnter: onEnter}}
	svc, taskRepo, statusRepo, agentRepo, projRepo := setupTaskServiceWithWorkflow(wf, &domain.EffectiveAssignmentRules{})
	env.commentRepo = NewMockCommentRepository()
	svc.commentRepo = env.commentRepo
	env.activityRepo = svc.activityRepo.(*MockActivityLogRepository)

	statusRepo.items[env.inboxID] = &domain.TaskStatus{ID: env.inboxID, ProjectID: env.projectID, Category: domain.StatusCategoryTodo, Name: "inbox"}
	statusRepo.items[env.qualifiedID] = &domain.TaskStatus{ID: env.qualifiedID, ProjectID: env.projectID, Category: domain.StatusCategoryTodo, Name: "qualified"}
	projRepo.items[env.projectID] = &domain.Project{ID: env.projectID, WorkspaceID: testDefaultWorkspaceID}
	agentRepo.items[env.targetID] = &domain.Agent{ID: env.targetID, WorkspaceID: testDefaultWorkspaceID, Slug: "sales"}
	agentRepo.items[env.ownerID] = &domain.Agent{ID: env.ownerID, WorkspaceID: testDefaultWorkspaceID, Slug: "owner"}
	env.svc, env.tasks = svc, taskRepo
	return env
}

func (e *onEnterAssignEnv) seedTask(assignee uuid.UUID) uuid.UUID {
	id := uuid.New()
	e.tasks.items[id] = &domain.Task{
		ID: id, ProjectID: e.projectID, StatusID: e.inboxID,
		AssigneeID: &assignee, AssigneeType: domain.AssigneeTypeAgent,
	}
	return id
}

func (e *onEnterAssignEnv) move(t *testing.T, id uuid.UUID) {
	t.Helper()
	require.NoError(t, e.svc.MoveTask(context.Background(), id, MoveTaskInput{StatusID: &e.qualifiedID}))
}

func TestOnEnterAssign_NoRuleLeavesAssignee(t *testing.T) {
	env := setupOnEnterAssignEnv(t, nil)
	id := env.seedTask(env.ownerID)
	env.move(t, id)
	assert.Equal(t, env.ownerID, *env.tasks.items[id].AssigneeID, "a project without the rule must behave as before")
	assert.Empty(t, env.commentRepo.items)
}

func TestOnEnterAssign_RuleAssignsAndPostsAuditComment(t *testing.T) {
	env := setupOnEnterAssignEnv(t, map[string]domain.OnEnterAction{"qualified": {AssignTo: "agent:sales"}})
	id := env.seedTask(env.ownerID)
	env.move(t, id)
	assert.Equal(t, env.targetID, *env.tasks.items[id].AssigneeID)
	assert.Equal(t, domain.AssigneeTypeAgent, env.tasks.items[id].AssigneeType)
	require.Len(t, env.commentRepo.items, 1)
	for _, c := range env.commentRepo.items {
		assert.Contains(t, c.Body, "Auto: assignee changed")
		assert.Contains(t, c.Body, "on_enter assign_to agent:sales")
	}
}

func TestOnEnterAssign_RuleOnOtherStatusDoesNotFire(t *testing.T) {
	env := setupOnEnterAssignEnv(t, map[string]domain.OnEnterAction{"some_other": {AssignTo: "agent:sales"}})
	id := env.seedTask(env.ownerID)
	env.move(t, id)
	assert.Equal(t, env.ownerID, *env.tasks.items[id].AssigneeID)
}

func TestOnEnterAssign_SameAssigneeIsNoOp(t *testing.T) {
	env := setupOnEnterAssignEnv(t, map[string]domain.OnEnterAction{"qualified": {AssignTo: "agent:sales"}})
	id := env.seedTask(env.targetID)
	env.move(t, id)
	assert.Equal(t, env.targetID, *env.tasks.items[id].AssigneeID)
	assert.Empty(t, env.commentRepo.items, "no change, no audit comment")
}

func TestOnEnterAssign_UnknownTargetKeepsTransitionAndWarns(t *testing.T) {
	for _, target := range []string{"agent:nobody", "user:ghost", "sales", "team:x", "agent:"} {
		t.Run(target, func(t *testing.T) {
			env := setupOnEnterAssignEnv(t, map[string]domain.OnEnterAction{"qualified": {AssignTo: target}})
			id := env.seedTask(env.ownerID)
			env.move(t, id)
			task := env.tasks.items[id]
			assert.Equal(t, env.qualifiedID, task.StatusID, "the transition must go through")
			assert.Equal(t, env.ownerID, *task.AssigneeID, "assignee must stay")
			var warned bool
			for _, e := range env.activityRepo.items {
				if e.EntityID != id || e.Action != "task.on_enter_assign_skipped" {
					continue
				}
				warned = true
				var ch map[string]any
				require.NoError(t, json.Unmarshal(e.Changes, &ch))
				assert.Equal(t, target, ch["assign_to"])
				assert.NotEmpty(t, ch["warning"])
			}
			assert.True(t, warned, "a warning must be written to the audit log")
		})
	}
}

func TestOnEnterAssign_ParsesFromWorkflowJSON(t *testing.T) {
	var cfg domain.WorkflowRulesConfig
	require.NoError(t, json.Unmarshal([]byte(`{"on_enter":{"qualified":{"assign_to":"agent:sales"}}}`), &cfg))
	assert.Equal(t, "agent:sales", cfg.OnEnter["qualified"].AssignTo)
	var empty domain.WorkflowRulesConfig
	require.NoError(t, json.Unmarshal([]byte(`{}`), &empty))
	assert.Nil(t, empty.OnEnter)
}

func TestOnEnterAssign_UserTargets(t *testing.T) {
	userID := uuid.New()
	for _, tc := range []struct{ name, target string }{
		{"by uuid", "user:" + userID.String()},
		{"by email", "user:pavel@example.com"},
		{"by username", "user:pavel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupOnEnterAssignEnv(t, map[string]domain.OnEnterAction{"qualified": {AssignTo: tc.target}})
			users := NewMockUserRepository()
			users.AddUser(testDefaultWorkspaceID, &domain.User{ID: userID, Username: "pavel", Email: "pavel@example.com"})
			WithUserRepoTask(users)(env.svc)
			id := env.seedTask(env.ownerID)
			env.move(t, id)
			task := env.tasks.items[id]
			require.NotNil(t, task.AssigneeID)
			assert.Equal(t, userID, *task.AssigneeID)
			assert.Equal(t, domain.AssigneeTypeUser, task.AssigneeType)
		})
	}

	t.Run("unknown user keeps the assignee", func(t *testing.T) {
		env := setupOnEnterAssignEnv(t, map[string]domain.OnEnterAction{"qualified": {AssignTo: "user:ghost"}})
		WithUserRepoTask(NewMockUserRepository())(env.svc)
		id := env.seedTask(env.ownerID)
		env.move(t, id)
		assert.Equal(t, env.ownerID, *env.tasks.items[id].AssigneeID)
	})
}

// A target from another workspace is refused by the tenancy guard: the move goes
// through, the assignee stays, the warning lands in the activity log.
func TestOnEnterAssign_ForeignWorkspaceTargetIsRefused(t *testing.T) {
	env := setupOnEnterAssignEnv(t, map[string]domain.OnEnterAction{"qualified": {AssignTo: "agent:outsider"}})
	outsider := uuid.New()
	agents := env.svc.agentRepo.(*MockAgentRepository)
	agents.items[outsider] = &domain.Agent{ID: outsider, WorkspaceID: uuid.New(), Slug: "outsider"}
	id := env.seedTask(env.ownerID)
	env.move(t, id)
	task := env.tasks.items[id]
	assert.Equal(t, env.qualifiedID, task.StatusID)
	assert.Equal(t, env.ownerID, *task.AssigneeID)
	var warned bool
	for _, e := range env.activityRepo.items {
		if e.EntityID == id && e.Action == "task.on_enter_assign_skipped" {
			warned = true
		}
	}
	assert.True(t, warned)
}

// A rule on a review-category status keeps the pre-review stash, so a bounce
// out of review restores the previous assignee.
func TestOnEnterAssign_ReviewStatusKeepsPreReviewStash(t *testing.T) {
	env := setupOnEnterAssignEnv(t, map[string]domain.OnEnterAction{"in_review": {AssignTo: "agent:sales"}})
	reviewID := uuid.New()
	env.svc.statusRepo.(*MockTaskStatusRepository).items[reviewID] = &domain.TaskStatus{
		ID: reviewID, ProjectID: env.projectID, Category: domain.StatusCategoryReview, Name: "in_review",
	}
	id := env.seedTask(env.ownerID)
	env.tasks.items[id].ArtifactCount = 1
	require.NoError(t, env.svc.MoveTask(context.Background(), id, MoveTaskInput{StatusID: &reviewID}))
	task := env.tasks.items[id]
	assert.Equal(t, env.targetID, *task.AssigneeID)
	require.NotNil(t, task.PreReviewAssigneeID)
	assert.Equal(t, env.ownerID, *task.PreReviewAssigneeID)
}

// An assignee passed explicitly on the move wins over the status rule.
func TestOnEnterAssign_ExplicitAssigneeWinsOverRule(t *testing.T) {
	env := setupOnEnterAssignEnv(t, map[string]domain.OnEnterAction{"qualified": {AssignTo: "agent:sales"}})
	explicitID := uuid.New()
	env.svc.agentRepo.(*MockAgentRepository).items[explicitID] = &domain.Agent{ID: explicitID, WorkspaceID: testDefaultWorkspaceID, Slug: "explicit"}
	id := env.seedTask(env.ownerID)

	require.NoError(t, env.svc.MoveTask(context.Background(), id, MoveTaskInput{
		StatusID: &env.qualifiedID, AssigneeID: &explicitID, AssigneeType: domain.AssigneeTypeAgent,
	}))

	assert.Equal(t, explicitID, *env.tasks.items[id].AssigneeID, "the explicit assignee must not be replaced by the rule")
	assert.NotEqual(t, env.targetID, *env.tasks.items[id].AssigneeID)
}
