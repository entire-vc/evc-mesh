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

// Cross-workspace assignee: a member of workspace A names a principal from
// workspace B and the system hands B's principal a task in A.
//
// Every test here is paired with a positive control on the same path, because a
// guard that refuses everything satisfies the negative half by itself and would
// break assignment for the whole fleet while looking green.

// tenancyEnv is a two-workspace world: one project in HOME, one agent in each.
type tenancyEnv struct {
	svc      *taskService
	tasks    *MockTaskRepository
	statuses *MockTaskStatusRepository
	agents   *MockAgentRepository
	projects *MockProjectRepository
	members  *MockProjectMemberRepository
	users    *MockUserRepository
	ws       *MockWorkspaceMembershipReader

	homeWS, foreignWS uuid.UUID
	projectID         uuid.UUID
	nativeAgent       uuid.UUID
	foreignAgent      uuid.UUID
	nativeUser        uuid.UUID
	foreignUser       uuid.UUID
}

func setupTenancyEnv(t *testing.T, wfResp *domain.WorkflowRulesResponse) *tenancyEnv {
	t.Helper()
	env := &tenancyEnv{
		homeWS:       uuid.New(),
		foreignWS:    uuid.New(),
		projectID:    uuid.New(),
		nativeAgent:  uuid.New(),
		foreignAgent: uuid.New(),
		nativeUser:   uuid.New(),
		foreignUser:  uuid.New(),
	}

	svc, taskRepo, statusRepo, agentRepo, projRepo := setupTaskServiceWithWorkflow(wfResp, nil)
	pmRepo := NewMockProjectMemberRepository()
	userRepo := NewMockUserRepository()
	// Strict reader: only the home workspace's members are members. The permissive
	// reader used by the enrolment tests would make every user path here vacuous.
	wsReader := NewMembershipTableReader()

	svc.projectMemberRepo = pmRepo
	svc.userRepo = userRepo
	svc.wsMembership = wsReader

	projRepo.items[env.projectID] = &domain.Project{ID: env.projectID, WorkspaceID: env.homeWS}
	agentRepo.items[env.nativeAgent] = &domain.Agent{ID: env.nativeAgent, WorkspaceID: env.homeWS, Slug: "native"}
	agentRepo.items[env.foreignAgent] = &domain.Agent{ID: env.foreignAgent, WorkspaceID: env.foreignWS, Slug: "foreign"}
	userRepo.AddUser(env.homeWS, &domain.User{ID: env.nativeUser, Username: "native-human"})
	userRepo.AddUser(env.foreignWS, &domain.User{ID: env.foreignUser, Username: "foreign-human"})
	wsReader.Allow(env.homeWS, env.nativeUser)
	wsReader.Allow(env.foreignWS, env.foreignUser)

	env.svc, env.tasks, env.statuses = svc, taskRepo, statusRepo
	env.agents, env.projects, env.members, env.users, env.ws = agentRepo, projRepo, pmRepo, userRepo, wsReader
	return env
}

// seedTask puts a task in the home project, in a todo status.
func (e *tenancyEnv) seedTask(t *testing.T) (taskID, statusID uuid.UUID) {
	t.Helper()
	taskID, statusID = uuid.New(), uuid.New()
	e.statuses.items[statusID] = &domain.TaskStatus{
		ID: statusID, ProjectID: e.projectID, Category: domain.StatusCategoryTodo, Name: "todo",
	}
	e.tasks.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: e.projectID, StatusID: statusID, Title: "home task",
	}
	return taskID, statusID
}

// assertRefusedForeign checks the error is the tenancy refusal and that no
// enrolment row was manufactured as a side effect. The second half matters more
// than the first: the project_members row is what makes a foreign principal look
// native to every check that runs after this one.
func (e *tenancyEnv) assertRefusedForeign(t *testing.T, err error, what string) {
	t.Helper()
	require.Error(t, err, "%s: a principal from another workspace must be refused", what)
	var foreign *AssigneeNotInWorkspaceError
	require.ErrorAs(t, err, &foreign, "%s: refusal must be the typed tenancy error", what)
	assert.Empty(t, e.members.members,
		"%s: a refused assignment must leave no project_members row behind", what)
}

func TestCrossWorkspaceAssignee_CreateRefusesForeignAgent(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	_, statusID := env.seedTask(t)
	before := len(env.tasks.items)

	err := env.svc.Create(context.Background(), &domain.Task{
		ProjectID: env.projectID, StatusID: statusID, Title: "planted",
		AssigneeID: &env.foreignAgent, AssigneeType: domain.AssigneeTypeAgent,
	})
	env.assertRefusedForeign(t, err, "create_task")
	assert.Len(t, env.tasks.items, before,
		"a task created with a foreign assignee must not exist at all — a half-created task the "+
			"caller has to notice is unassigned is worse than a clean refusal")
}

func TestCrossWorkspaceAssignee_CreateAllowsNativeAgent(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	_, statusID := env.seedTask(t)

	require.NoError(t, env.svc.Create(context.Background(), &domain.Task{
		ProjectID: env.projectID, StatusID: statusID, Title: "legitimate",
		AssigneeID: &env.nativeAgent, AssigneeType: domain.AssigneeTypeAgent,
	}), "positive control: an agent of this workspace must still be assignable")
	assert.Len(t, env.members.members, 1, "the native assignee must still be enrolled")
}

func TestCrossWorkspaceAssignee_AssignTaskRefusesForeignAgent(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	taskID, _ := env.seedTask(t)

	err := env.svc.AssignTask(context.Background(), taskID, AssignTaskInput{
		AssigneeID: &env.foreignAgent, AssigneeType: domain.AssigneeTypeAgent,
	})
	env.assertRefusedForeign(t, err, "assign_task")
	assert.Nil(t, env.tasks.items[taskID].AssigneeID,
		"the task must still be unassigned after a refused assignment")
}

func TestCrossWorkspaceAssignee_AssignTaskAllowsNativeAgent(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	taskID, _ := env.seedTask(t)

	require.NoError(t, env.svc.AssignTask(context.Background(), taskID, AssignTaskInput{
		AssigneeID: &env.nativeAgent, AssigneeType: domain.AssigneeTypeAgent,
	}), "positive control")
	require.NotNil(t, env.tasks.items[taskID].AssigneeID)
	assert.Equal(t, env.nativeAgent, *env.tasks.items[taskID].AssigneeID)
}

func TestCrossWorkspaceAssignee_UpdateRefusesForeignAgent(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	taskID, statusID := env.seedTask(t)

	err := env.svc.Update(context.Background(), &domain.Task{
		ID: taskID, ProjectID: env.projectID, StatusID: statusID, Title: "home task",
		AssigneeID: &env.foreignAgent, AssigneeType: domain.AssigneeTypeAgent,
	})
	env.assertRefusedForeign(t, err, "PATCH /tasks/:id")
}

func TestCrossWorkspaceAssignee_CreateSubtaskRefusesForeignAgent(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	parentID, statusID := env.seedTask(t)

	_, err := env.svc.CreateSubtask(context.Background(), parentID, CreateSubtaskInput{
		Title: "child", StatusID: &statusID,
		AssigneeID: &env.foreignAgent, AssigneeType: domain.AssigneeTypeAgent,
	})
	env.assertRefusedForeign(t, err, "create_subtask")
}

func TestCrossWorkspaceAssignee_MoveTaskRefusesForeignAgentAndDoesNotMove(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	taskID, todoID := env.seedTask(t)
	inProgressID := uuid.New()
	env.statuses.items[inProgressID] = &domain.TaskStatus{
		ID: inProgressID, ProjectID: env.projectID, Category: domain.StatusCategoryInProgress, Name: "in_progress",
	}

	err := env.svc.MoveTask(context.Background(), taskID, MoveTaskInput{
		StatusID: &inProgressID, AssigneeID: &env.foreignAgent, AssigneeType: domain.AssigneeTypeAgent,
	})
	env.assertRefusedForeign(t, err, "move_task with assignee")

	// The move and the assignment are one request but two writes, and the
	// assignment write happens after the status change is already committed.
	// Checking the assignee only at the point of use would leave the task moved
	// and the caller told the request failed.
	assert.Equal(t, todoID, env.tasks.items[taskID].StatusID,
		"a refused assignment must not leave the status change applied — the request is atomic "+
			"from the caller's side or it is a lie")
}

// TestSetReviewerCannotResolveAnAgentFromAnotherWorkspace pins the protection
// that actually holds this path shut — which is NOT the guard added by this
// change.
//
// An earlier version of this test moved a card to review under a set_reviewer
// rule naming a foreign agent and asserted the card did not rotate. It passed
// against the UNFIXED code, which makes it worthless as evidence for the fix: the
// rotation never had a chance to happen, because resolveSetReviewer looks the
// slug up with agentRepo.GetBySlug(proj.WorkspaceID, slug) and the real query is
// `WHERE workspace_id = $1 AND slug = $2`. The reviewer-rotation path was already
// tenant-safe by construction.
//
// So the honest split is: this test pins the workspace-scoped slug lookup, so a
// refactor to a global one fails here; and the tenancy guard on
// applyReviewAssignee stays as defence in depth for exactly that regression,
// tested directly below rather than through a path that cannot reach it.
func TestSetReviewerCannotResolveAnAgentFromAnotherWorkspace(t *testing.T) {
	env := setupTenancyEnv(t, nil)

	// The only agent carrying this slug lives in the other workspace.
	sharedSlug := "reviewer"
	env.agents.items[env.foreignAgent] = &domain.Agent{
		ID: env.foreignAgent, WorkspaceID: env.foreignWS, Slug: sharedSlug,
	}

	id, _, err := env.svc.resolveSetReviewer(context.Background(), env.projectID, sharedSlug)

	require.Error(t, err,
		"a set_reviewer slug that exists only in another workspace must not resolve — if this "+
			"lookup ever becomes global, the rotation starts handing cards across tenants")
	assert.Nil(t, id)
}

// TestApplyReviewAssigneeRefusesAForeignReviewer tests the defence-in-depth guard
// directly, by handing applyReviewAssignee's own check the state a regression in
// resolveSetReviewer would produce: a principal from another workspace.
//
// Reaching it through MoveTask is impossible today (see above), so testing it
// through MoveTask would only re-prove that the slug lookup is scoped.
func TestApplyReviewAssigneeRefusesAForeignReviewer(t *testing.T) {
	env := setupTenancyEnv(t, nil)

	err := env.svc.assertAssigneeInProjectWorkspace(
		context.Background(), env.projectID, &env.foreignAgent, domain.AssigneeTypeAgent)

	require.Error(t, err, "the rotation's own tenancy check must refuse a foreign principal")
	var foreign *AssigneeNotInWorkspaceError
	require.ErrorAs(t, err, &foreign)
}

// TestRestorePreReviewAssigneeRechecksTheStash covers the one reviewer-path
// refusal that IS reachable in production: the stashed pre-review assignee was
// valid when it was stashed, but the card can sit in review while that agent is
// deleted. Restoring it unchecked would put the task back on a principal the
// directory no longer knows.
func TestRestorePreReviewAssigneeRechecksTheStash(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	ghost := uuid.New() // in no directory — models an agent deleted while in review

	err := env.svc.assertAssigneeInProjectWorkspace(
		context.Background(), env.projectID, &ghost, domain.AssigneeTypeAgent)

	require.Error(t, err, "a stash naming a principal the directory no longer has must not be restored")
	assert.Empty(t, env.members.members)
}

func TestCrossWorkspaceAssignee_RefusesForeignUser(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	taskID, _ := env.seedTask(t)

	err := env.svc.AssignTask(context.Background(), taskID, AssignTaskInput{
		AssigneeID: &env.foreignUser, AssigneeType: domain.AssigneeTypeUser,
	})
	env.assertRefusedForeign(t, err, "assign_task to a foreign human")
}

func TestCrossWorkspaceAssignee_AllowsNativeUser(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	taskID, _ := env.seedTask(t)

	require.NoError(t, env.svc.AssignTask(context.Background(), taskID, AssignTaskInput{
		AssigneeID: &env.nativeUser, AssigneeType: domain.AssigneeTypeUser,
	}), "positive control: a member of this workspace must still be assignable")
}

// TestCrossWorkspaceAssignee_UnreadableMembershipRefuses pins the failure
// direction for the human path, matching the agent path.
func TestCrossWorkspaceAssignee_UnreadableMembershipRefuses(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	taskID, _ := env.seedTask(t)
	env.ws.FailWith(assert.AnError)

	err := env.svc.AssignTask(context.Background(), taskID, AssignTaskInput{
		AssigneeID: &env.nativeUser, AssigneeType: domain.AssigneeTypeUser,
	})
	env.assertRefusedForeign(t, err,
		"membership directory unreadable — 'I could not check' must not read as 'I checked'")
}

// TestCrossWorkspaceAssignee_AgentKeyActorIsNotAnExemption covers the specific
// trap named in the report: rbac() short-circuits to a static capability map for
// an agent key and never looks at the target object's workspace, so "the route
// carries rbac(...)" has never been a tenancy check. This guard sits in the
// service layer precisely so the actor's credential type cannot change the answer.
func TestCrossWorkspaceAssignee_AgentKeyActorIsNotAnExemption(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	taskID, _ := env.seedTask(t)

	ctx := actorctx.WithActor(context.Background(), env.nativeAgent, domain.ActorTypeAgent)
	err := env.svc.AssignTask(ctx, taskID, AssignTaskInput{
		AssigneeID: &env.foreignAgent, AssigneeType: domain.AssigneeTypeAgent,
	})
	env.assertRefusedForeign(t, err, "assign_task under an agent key")
}

// TestCrossWorkspaceAssignee_UpdateRefusesForeignReviewer covers the NINTH path,
// which did not exist when this defect was reported — `reviewer_id` landed on
// main while the fix was being written.
//
// It grants strictly less than assignee_id: nothing feeds on it and no callback
// fires for it. What it does leak is a name. taskComputedCols resolves
// reviewer_name with an unscoped `SELECT name FROM agents WHERE id = ...`, so
// pointing reviewer_id at another tenant's agent echoes that agent's display name
// back — a disclosure, and an oracle for which ids exist elsewhere on the
// instance.
//
// The interesting part is not the severity but the arrival: a new principal
// pointer appeared within days, exactly as the report predicted a ninth path
// would. That is what TestEveryAssigneeWritePathIsWorkspaceChecked is for.
func TestCrossWorkspaceAssignee_UpdateRefusesForeignReviewer(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	taskID, statusID := env.seedTask(t)
	reviewerType := domain.AssigneeTypeAgent

	err := env.svc.Update(context.Background(), &domain.Task{
		ID: taskID, ProjectID: env.projectID, StatusID: statusID, Title: "home task",
		ReviewerID: &env.foreignAgent, ReviewerType: &reviewerType,
	})

	require.Error(t, err, "a reviewer from another workspace must be refused")
	var foreign *AssigneeNotInWorkspaceError
	require.ErrorAs(t, err, &foreign)
}

// TestCrossWorkspaceAssignee_CreateRefusesForeignReviewer covers the reviewer's
// SECOND write path, which landed on main (#545, "allow setting reviewer at task
// creation") while this branch sat in conflict — the ninth path acquired a tenth
// entrance before the fix for it had merged.
//
// This is the rule from the guard's own file arriving on schedule twice over: a
// field that names a principal by id grows new write sites faster than a review
// cycle closes, so the guard has to sit in the funnel rather than at any one
// call site. Update's reviewer error was propagated by this branch; Create's had
// to be, too, and a discarded error there would have reopened exactly the gap
// the ninth-path commit closed — silently, since Create's happy path is green
// either way.
func TestCrossWorkspaceAssignee_CreateRefusesForeignReviewer(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	reviewerType := domain.AssigneeTypeAgent

	err := env.svc.Create(context.Background(), &domain.Task{
		ProjectID: env.projectID, Title: "new task",
		ReviewerID: &env.foreignAgent, ReviewerType: &reviewerType,
	})

	require.Error(t, err, "a reviewer from another workspace must be refused at creation")
	var foreign *AssigneeNotInWorkspaceError
	require.ErrorAs(t, err, &foreign)
	assert.Empty(t, env.tasks.items,
		"a refused reviewer must leave no task behind — the check runs before taskRepo.Create, "+
			"so the caller gets a refusal rather than a created task they must notice is unreviewed")
}

// TestCrossWorkspaceAssignee_CreateAllowsNativeReviewer is its positive control:
// without it the test above is satisfied by a Create that refuses every reviewer.
func TestCrossWorkspaceAssignee_CreateAllowsNativeReviewer(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	reviewerType := domain.AssigneeTypeAgent

	require.NoError(t, env.svc.Create(context.Background(), &domain.Task{
		ProjectID: env.projectID, Title: "new task",
		ReviewerID: &env.nativeAgent, ReviewerType: &reviewerType,
	}), "an agent of this workspace must still be settable as reviewer at creation")

	assert.Len(t, env.tasks.items, 1, "the task must actually have been created")
}

// TestCrossWorkspaceAssignee_UpdateAllowsNativeReviewer is its positive control.
func TestCrossWorkspaceAssignee_UpdateAllowsNativeReviewer(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	taskID, statusID := env.seedTask(t)
	reviewerType := domain.AssigneeTypeAgent

	require.NoError(t, env.svc.Update(context.Background(), &domain.Task{
		ID: taskID, ProjectID: env.projectID, StatusID: statusID, Title: "home task",
		ReviewerID: &env.nativeAgent, ReviewerType: &reviewerType,
	}), "an agent of this workspace must still be settable as reviewer")

	assert.Len(t, env.members.members, 1,
		"a native reviewer must still be enrolled in the project — being made reviewer sends "+
			"a notification, and without enrolment they follow it to a task whose status "+
			"transitions all 403")
}

// ---------------------------------------------------------------------------
// ValidateAssigneeForProject — the funnel non-task write paths (template,
// recurring schedule) call into. Everything above exercises it indirectly
// through Create/Update/MoveTask/etc; these call it directly, against the real
// taskService (not a mock/stub), the way task_template_service.go and
// recurring_service.go actually do.
// ---------------------------------------------------------------------------

func TestValidateAssigneeForProject_RefusesForeignAgent(t *testing.T) {
	env := setupTenancyEnv(t, nil)

	resolved, err := env.svc.ValidateAssigneeForProject(
		context.Background(), env.projectID, &env.foreignAgent, domain.AssigneeTypeAgent)

	var foreign *AssigneeNotInWorkspaceError
	require.ErrorAs(t, err, &foreign, "a foreign agent must be refused")
	assert.Equal(t, domain.AssigneeTypeAgent, resolved,
		"the resolved type is still returned alongside the error — callers may want it for logging")
}

// TestValidateAssigneeForProject_AllowsNativeAgent is the positive control:
// without it the refusal test above is satisfied by a funnel that refuses
// everything, which would break every template/schedule assignment fleet-wide.
func TestValidateAssigneeForProject_AllowsNativeAgent(t *testing.T) {
	env := setupTenancyEnv(t, nil)

	resolved, err := env.svc.ValidateAssigneeForProject(
		context.Background(), env.projectID, &env.nativeAgent, domain.AssigneeTypeAgent)

	require.NoError(t, err, "an agent of this workspace must be accepted")
	assert.Equal(t, domain.AssigneeTypeAgent, resolved)
}

func TestValidateAssigneeForProject_ResolvesTypeFromDirectoryNotCaller(t *testing.T) {
	env := setupTenancyEnv(t, nil)

	// Caller claims "user", but nativeAgent is only registered in the agent
	// directory — resolveAssigneeType must correct it, the same as every other
	// task write path does, not trust the caller-supplied type.
	resolved, err := env.svc.ValidateAssigneeForProject(
		context.Background(), env.projectID, &env.nativeAgent, domain.AssigneeTypeUser)

	require.NoError(t, err)
	assert.Equal(t, domain.AssigneeTypeAgent, resolved,
		"resolved type must come from the directory lookup, not the caller-supplied type")
}

func TestValidateAssigneeForProject_NilAssignee_ReturnsUnassignedWithoutLookup(t *testing.T) {
	env := setupTenancyEnv(t, nil)

	resolved, err := env.svc.ValidateAssigneeForProject(
		context.Background(), env.projectID, nil, domain.AssigneeTypeAgent)

	require.NoError(t, err)
	assert.Equal(t, domain.AssigneeTypeUnassigned, resolved,
		"no assignee means nothing to validate — must not error and must not claim a real type")
}
