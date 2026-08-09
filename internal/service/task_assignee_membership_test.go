package service

// Regression tests for "assignee gets 403 'not a member of this project' on any
// status transition — can work the task but cannot close it".
//
// INVARIANT UNDER TEST: becoming a task's assignee always enrols you in that
// task's project. Every write path that sets assignee_id must hold it.
//
// Why it matters (measured against production): task routes (get/comment/assign)
// are workspace-gated and succeed for a non-member, but a status transition
// resolves its status slug through the project-gated
// GET /projects/:proj_id/statuses, which 403s. An assignee who is not a project
// member can therefore do the whole job and then move the task neither forward
// nor back. Create and AssignTask already enrolled; MoveTask, Update/PATCH and
// the two reviewer-rotation paths did not — and applyReviewAssignee is what
// actually produced the observed dead end.

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// assertEnrolled fails when the agent is not a member of the project.
func assertEnrolled(t *testing.T, pm *MockProjectMemberRepository, projID, agentID uuid.UUID, msg string) {
	t.Helper()
	ok, err := pm.ExistsMember(context.Background(), projID, nil, &agentID)
	require.NoError(t, err)
	assert.True(t, ok, msg)
}

// membershipEnv bundles the repos a membership test needs to seed and assert on.
type membershipEnv struct {
	svc      *taskService
	tasks    *MockTaskRepository
	statuses *MockTaskStatusRepository
	agents   *MockAgentRepository
	projects *MockProjectRepository
	members  *MockProjectMemberRepository
}

// setupMembershipEnv wires a task service that has a project-member repo, which
// setupTaskServiceWithWorkflow deliberately omits.
func setupMembershipEnv(wfResp *domain.WorkflowRulesResponse, effectiveRules *domain.EffectiveAssignmentRules) membershipEnv {
	svc, taskRepo, statusRepo, agentRepo, projRepo := setupTaskServiceWithWorkflow(wfResp, effectiveRules)
	pmRepo := NewMockProjectMemberRepository()
	svc.projectMemberRepo = pmRepo
	// Production wires BOTH directories (main.go: WithAgentRepo + WithUserRepoTask).
	// Leaving userRepo nil here made the enrolment directory check skip on the user
	// path, so a test could observe a ghost id being enrolled as a user — an outcome
	// production cannot produce. Individual tests still override this when they need
	// to seed specific users.
	svc.userRepo = NewMockUserRepository()
	return membershipEnv{
		svc: svc, tasks: taskRepo, statuses: statusRepo,
		agents: agentRepo, projects: projRepo, members: pmRepo,
	}
}

// ---------------------------------------------------------------------------
// 1. The reported incident: a set_reviewer rotation hands the card to an agent
//    who was never enrolled. This is the path that produced the reported trap.
// ---------------------------------------------------------------------------

func TestAssigneeEnrolment_SetReviewerRotationEnrolsReviewerAndBuilder(t *testing.T) {
	workspaceID := uuid.New()
	projectID := uuid.New()
	builderID := uuid.New()
	leadID := uuid.New()

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
	env := setupMembershipEnv(wfResp, effectiveRules)
	svc, taskRepo, statusRepo, agentRepo, projRepo, pmRepo := env.svc, env.tasks, env.statuses, env.agents, env.projects, env.members

	inProgressID, reviewID, todoID, taskID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	statusRepo.items[inProgressID] = &domain.TaskStatus{ID: inProgressID, ProjectID: projectID, Category: domain.StatusCategoryInProgress, Name: "in_progress"}
	statusRepo.items[reviewID] = &domain.TaskStatus{ID: reviewID, ProjectID: projectID, Category: domain.StatusCategoryReview, Name: "review"}
	statusRepo.items[todoID] = &domain.TaskStatus{ID: todoID, ProjectID: projectID, Category: domain.StatusCategoryTodo, Name: "todo"}
	projRepo.items[projectID] = &domain.Project{ID: projectID, WorkspaceID: workspaceID}
	agentRepo.items[leadID] = &domain.Agent{ID: leadID, WorkspaceID: workspaceID, Slug: "garfield"}
	// The builder is a real agent too; enrolment now verifies the directory before
	// writing, so an unseeded id models a row the database would refuse.
	agentRepo.items[builderID] = &domain.Agent{ID: builderID, WorkspaceID: workspaceID, Slug: "builder"}
	taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: projectID, StatusID: inProgressID,
		AssigneeID: &builderID, AssigneeType: domain.AssigneeTypeAgent,
	}

	// Precondition: the reviewer is a stranger to this project. This is the state
	// the bug depended on — assert it, or the test could pass vacuously.
	exists, err := pmRepo.ExistsMember(context.Background(), projectID, nil, &leadID)
	require.NoError(t, err)
	require.False(t, exists, "precondition: reviewer must start as a non-member")

	// in_progress → review: the rule rotates the card onto the lead.
	require.NoError(t, svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &reviewID}))
	require.NotNil(t, taskRepo.items[taskID].AssigneeID)
	require.Equal(t, leadID, *taskRepo.items[taskID].AssigneeID, "precondition: rule must have rotated to the lead")
	assertEnrolled(t, pmRepo, projectID, leadID,
		"set_reviewer rotation must enrol the reviewer — otherwise they can work the card but every move_task 403s")

	// review → todo: the bounce restores the builder, who must also be enrolled.
	require.NoError(t, svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &todoID}))
	require.Equal(t, builderID, *taskRepo.items[taskID].AssigneeID, "precondition: bounce must restore the builder")
	assertEnrolled(t, pmRepo, projectID, builderID,
		"bounce out of review must enrol the restored assignee")
}

// ---------------------------------------------------------------------------
// 2. move_task carrying an explicit assignee_id — the routing hand-off shape.
// ---------------------------------------------------------------------------

func TestAssigneeEnrolment_MoveTaskWithExplicitAssignee(t *testing.T) {
	projectID, newAssignee, taskID := uuid.New(), uuid.New(), uuid.New()
	env := setupMembershipEnv(nil, nil)
	svc, taskRepo, statusRepo, agentRepo, projRepo, pmRepo := env.svc, env.tasks, env.statuses, env.agents, env.projects, env.members

	fromID, toID := uuid.New(), uuid.New()
	statusRepo.items[fromID] = &domain.TaskStatus{ID: fromID, ProjectID: projectID, Category: domain.StatusCategoryTodo, Name: "todo"}
	statusRepo.items[toID] = &domain.TaskStatus{ID: toID, ProjectID: projectID, Category: domain.StatusCategoryInProgress, Name: "in_progress"}
	projRepo.items[projectID] = &domain.Project{ID: projectID}
	agentRepo.items[newAssignee] = &domain.Agent{ID: newAssignee, Slug: "wally"}
	taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: projectID, StatusID: fromID, AssigneeType: domain.AssigneeTypeUnassigned}

	require.NoError(t, svc.MoveTask(context.Background(), taskID, MoveTaskInput{
		StatusID: &toID, AssigneeID: &newAssignee, AssigneeType: domain.AssigneeTypeAgent,
	}))

	assertEnrolled(t, pmRepo, projectID, newAssignee,
		"move_task with an explicit assignee_id must enrol that assignee")
}

// ---------------------------------------------------------------------------
// 3. update_task (PATCH) — assign_task and update_task must not disagree.
//    Acceptance criterion: "one of the two handles lies" — neither may now.
// ---------------------------------------------------------------------------

func TestAssigneeEnrolment_UpdateAndAssignTaskAgree(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name   string
		assign func(svc *taskService, taskID, projID, agentID uuid.UUID) error
	}{
		{
			name: "assign_task",
			assign: func(svc *taskService, taskID, _, agentID uuid.UUID) error {
				return svc.AssignTask(ctx, taskID, AssignTaskInput{
					AssigneeID: &agentID, AssigneeType: domain.AssigneeTypeAgent,
				})
			},
		},
		{
			name: "update_task (PATCH)",
			assign: func(svc *taskService, taskID, projID, agentID uuid.UUID) error {
				return svc.Update(ctx, &domain.Task{
					ID: taskID, ProjectID: projID,
					AssigneeID: &agentID, AssigneeType: domain.AssigneeTypeAgent,
				})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectID, agentID, taskID := uuid.New(), uuid.New(), uuid.New()
			env := setupMembershipEnv(nil, nil)
			svc, taskRepo, agentRepo, projRepo, pmRepo := env.svc, env.tasks, env.agents, env.projects, env.members

			projRepo.items[projectID] = &domain.Project{ID: projectID}
			agentRepo.items[agentID] = &domain.Agent{ID: agentID, Slug: "wally"}
			taskRepo.items[taskID] = &domain.Task{
				ID: taskID, ProjectID: projectID, AssigneeType: domain.AssigneeTypeUnassigned,
			}

			require.NoError(t, tc.assign(svc, taskID, projectID, agentID))
			assertEnrolled(t, pmRepo, projectID, agentID,
				tc.name+" must enrol the assignee — assign_task and update_task must not disagree")
		})
	}
}

// ---------------------------------------------------------------------------
// 3b. create_subtask — the epic-decomposition path. The assignee here is
//     usually NOT the creator: a captain decomposes an epic into subtasks owned
//     by whoever will do the work. Enrolling only the creator (which is all this
//     path used to do) leaves that owner able to comment but not transition.
// ---------------------------------------------------------------------------

func TestAssigneeEnrolment_CreateSubtaskEnrolsAssigneeNotOnlyCreator(t *testing.T) {
	ctx := context.Background()
	projectID, parentID := uuid.New(), uuid.New()
	captainID, ownerID := uuid.New(), uuid.New()

	env := setupMembershipEnv(nil, nil)
	svc, taskRepo, statusRepo, agentRepo, projRepo, pmRepo := env.svc, env.tasks, env.statuses, env.agents, env.projects, env.members

	todoID := uuid.New()
	statusRepo.items[todoID] = &domain.TaskStatus{ID: todoID, ProjectID: projectID, Category: domain.StatusCategoryTodo, Name: "todo"}
	projRepo.items[projectID] = &domain.Project{ID: projectID}
	agentRepo.items[ownerID] = &domain.Agent{ID: ownerID, Slug: "owner"}
	// The creator is a real agent in production; seed it so enrolment's directory
	// check sees what it would see live.
	agentRepo.items[captainID] = &domain.Agent{ID: captainID, Slug: "captain"}
	taskRepo.items[parentID] = &domain.Task{ID: parentID, ProjectID: projectID, StatusID: todoID, Title: "epic"}

	// The captain creates the subtask; a different agent is meant to own it.
	ctx = actorctx.WithActor(ctx, captainID, domain.ActorTypeAgent)
	_, err := svc.CreateSubtask(ctx, parentID, CreateSubtaskInput{
		Title:        "unit of work",
		StatusID:     &todoID,
		AssigneeID:   &ownerID,
		AssigneeType: domain.AssigneeTypeAgent,
	})
	require.NoError(t, err)

	assertEnrolled(t, pmRepo, projectID, captainID,
		"precondition: the creator was already enrolled before this fix")
	assertEnrolled(t, pmRepo, projectID, ownerID,
		"create_subtask must enrol the ASSIGNEE too, not just the creator — this is the epic-decomposition path")
}

// ---------------------------------------------------------------------------
// 3c. Create with an explicit assignee. This path already enrolled before the
//     fix, but nothing pinned it, so a refactor could silently drop it.
// ---------------------------------------------------------------------------

func TestAssigneeEnrolment_CreateWithAssignee(t *testing.T) {
	projectID, agentID := uuid.New(), uuid.New()
	env := setupMembershipEnv(nil, nil)
	svc, agentRepo, projRepo, pmRepo := env.svc, env.agents, env.projects, env.members

	projRepo.items[projectID] = &domain.Project{ID: projectID}
	agentRepo.items[agentID] = &domain.Agent{ID: agentID, Slug: "owner"}

	require.NoError(t, svc.Create(context.Background(), &domain.Task{
		ProjectID: projectID, Title: "t",
		AssigneeID: &agentID, AssigneeType: domain.AssigneeTypeAgent,
	}))

	assertEnrolled(t, pmRepo, projectID, agentID,
		"create_task with an assignee must enrol that assignee")
}

// ---------------------------------------------------------------------------
// 4. Human assignees take the same path (assignee_type=user).
// ---------------------------------------------------------------------------

func TestAssigneeEnrolment_UserAssigneeEnrolledOnMove(t *testing.T) {
	projectID, userID, taskID := uuid.New(), uuid.New(), uuid.New()
	env := setupMembershipEnv(nil, nil)
	svc, taskRepo, statusRepo, projRepo, pmRepo := env.svc, env.tasks, env.statuses, env.projects, env.members

	userRepo := NewMockUserRepository()
	wsID := uuid.New()
	userRepo.AddUser(wsID, &domain.User{ID: userID, Username: "pavel"})
	svc.userRepo = userRepo

	fromID, toID := uuid.New(), uuid.New()
	statusRepo.items[fromID] = &domain.TaskStatus{ID: fromID, ProjectID: projectID, Category: domain.StatusCategoryTodo, Name: "todo"}
	statusRepo.items[toID] = &domain.TaskStatus{ID: toID, ProjectID: projectID, Category: domain.StatusCategoryInProgress, Name: "in_progress"}
	projRepo.items[projectID] = &domain.Project{ID: projectID, WorkspaceID: wsID}
	taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: projectID, StatusID: fromID, AssigneeType: domain.AssigneeTypeUnassigned}

	require.NoError(t, svc.MoveTask(context.Background(), taskID, MoveTaskInput{
		StatusID: &toID, AssigneeID: &userID, AssigneeType: domain.AssigneeTypeUser,
	}))

	ok, err := pmRepo.ExistsMember(context.Background(), projectID, &userID, nil)
	require.NoError(t, err)
	assert.True(t, ok, "a human assignee must be enrolled too — the same 403 traps users")
}

// ---------------------------------------------------------------------------
// 5. Guard: enrolment must not fire when there is no assignee, and must not
//    duplicate an existing membership row.
// ---------------------------------------------------------------------------

func TestAssigneeEnrolment_NoAssigneeAndNoDuplicate(t *testing.T) {
	ctx := context.Background()
	projectID, agentID := uuid.New(), uuid.New()
	env := setupMembershipEnv(nil, nil)
	svc, pmRepo := env.svc, env.members

	// Seed the agent. Enrolment now refuses an id that is in no directory, because the
	// real project_members.agent_id foreign key would reject that insert anyway — an
	// unseeded id models a state production cannot reach.
	env.agents.items[agentID] = &domain.Agent{ID: agentID, Slug: "worker"}

	// No assignee → no row.
	svc.ensureAssigneeProjectMember(ctx, projectID, nil, domain.AssigneeTypeAgent)
	assert.Empty(t, pmRepo.members, "a nil assignee must not create a membership row")

	// Unassigned type → no row, even with an id present.
	svc.ensureAssigneeProjectMember(ctx, projectID, &agentID, domain.AssigneeTypeUnassigned)
	assert.Empty(t, pmRepo.members, "assignee_type=unassigned must not create a membership row")

	// Enrol once, then twice — still one row.
	svc.ensureAssigneeProjectMember(ctx, projectID, &agentID, domain.AssigneeTypeAgent)
	svc.ensureAssigneeProjectMember(ctx, projectID, &agentID, domain.AssigneeTypeAgent)
	assert.Len(t, pmRepo.members, 1, "re-enrolling an existing member must not duplicate the row")
}

// ---------------------------------------------------------------------------
// 6. CreateSubtask must resolve the assignee's REAL type before enrolling.
// ---------------------------------------------------------------------------

// TestAssigneeEnrolment_SubtaskToUserWithMistypedAssigneeType covers the one
// enrolment site whose assignee type is caller-supplied and therefore untrustworthy.
//
// The HTTP handler defaults an omitted assignee_type to "agent", so a subtask
// assigned to a human arrived here typed as an agent. ensureAssigneeProjectMember
// dispatches on that type, so enrolment went to ensureAgentProjectMember with a user
// UUID; in production the project_members.agent_id foreign key rejects that insert and
// the error is logged and swallowed — the assignee ends up holding a task they cannot
// transition, with no signal anywhere.
//
// The test deliberately passes the WRONG type rather than omitting it: omitting it
// would let a future handler-side default satisfy the test without the service ever
// resolving anything, which is how a test passes for the wrong reason.
func TestAssigneeEnrolment_SubtaskToUserWithMistypedAssigneeType(t *testing.T) {
	ctx := context.Background()
	projectID, parentID := uuid.New(), uuid.New()
	captainID, humanID := uuid.New(), uuid.New()
	wsID := uuid.New()

	env := setupMembershipEnv(nil, nil)
	svc, taskRepo, statusRepo, projRepo, pmRepo := env.svc, env.tasks, env.statuses, env.projects, env.members

	userRepo := NewMockUserRepository()
	userRepo.AddUser(wsID, &domain.User{ID: humanID, Username: "pavel"})
	svc.userRepo = userRepo

	todoID := uuid.New()
	statusRepo.items[todoID] = &domain.TaskStatus{ID: todoID, ProjectID: projectID, Category: domain.StatusCategoryTodo, Name: "todo"}
	projRepo.items[projectID] = &domain.Project{ID: projectID, WorkspaceID: wsID}
	taskRepo.items[parentID] = &domain.Task{ID: parentID, ProjectID: projectID, StatusID: todoID, Title: "epic"}

	ctx = actorctx.WithActor(ctx, captainID, domain.ActorTypeAgent)
	child, err := svc.CreateSubtask(ctx, parentID, CreateSubtaskInput{
		Title:      "human review step",
		StatusID:   &todoID,
		AssigneeID: &humanID,
		// The lie the handler tells when assignee_type is omitted:
		AssigneeType: domain.AssigneeTypeAgent,
	})
	require.NoError(t, err)

	// Enrolled against the USER column, not the agent one.
	ok, err := pmRepo.ExistsMember(ctx, projectID, &humanID, nil)
	require.NoError(t, err)
	assert.True(t, ok,
		"a human subtask assignee must be enrolled as a user — dispatching on the "+
			"handler's guess sends the insert at project_members.agent_id, where the "+
			"foreign key rejects it silently")

	// And must NOT have been attempted as an agent.
	asAgent, err := pmRepo.ExistsMember(ctx, projectID, nil, &humanID)
	require.NoError(t, err)
	assert.False(t, asAgent, "must not be enrolled against the agent column")

	// The stored type is corrected too, so the row does not persist the guess.
	assert.Equal(t, domain.AssigneeTypeUser, child.AssigneeType,
		"the subtask must be stored with the resolved type, not the caller's")
}

// TestAssigneeEnrolment_SubtaskAgentTypeStillResolves is the positive control: the
// ordinary agent case must keep working, so the fix cannot be satisfied by a service
// that simply always resolves to user.
func TestAssigneeEnrolment_SubtaskAgentTypeStillResolves(t *testing.T) {
	ctx := context.Background()
	projectID, parentID := uuid.New(), uuid.New()
	captainID, ownerID := uuid.New(), uuid.New()

	env := setupMembershipEnv(nil, nil)
	svc, taskRepo, statusRepo, agentRepo, projRepo, pmRepo := env.svc, env.tasks, env.statuses, env.agents, env.projects, env.members

	todoID := uuid.New()
	statusRepo.items[todoID] = &domain.TaskStatus{ID: todoID, ProjectID: projectID, Category: domain.StatusCategoryTodo, Name: "todo"}
	projRepo.items[projectID] = &domain.Project{ID: projectID}
	agentRepo.items[ownerID] = &domain.Agent{ID: ownerID, Slug: "owner"}
	taskRepo.items[parentID] = &domain.Task{ID: parentID, ProjectID: projectID, StatusID: todoID, Title: "epic"}

	ctx = actorctx.WithActor(ctx, captainID, domain.ActorTypeAgent)
	child, err := svc.CreateSubtask(ctx, parentID, CreateSubtaskInput{
		Title: "agent unit of work", StatusID: &todoID,
		AssigneeID: &ownerID, AssigneeType: domain.AssigneeTypeAgent,
	})
	require.NoError(t, err)

	assertEnrolled(t, pmRepo, projectID, ownerID, "an agent assignee must still enrol as an agent")
	assert.Equal(t, domain.AssigneeTypeAgent, child.AssigneeType)
}

// ---------------------------------------------------------------------------
// 7. An assignee that exists in NEITHER directory must not be enrolled by guess.
// ---------------------------------------------------------------------------

// TestAssigneeEnrolment_UnknownUUIDIsNotEnrolledByGuess covers the last way a wrong
// column could be written.
//
// resolveAssigneeType falls back to the caller's value when the id is in neither the
// agent nor the user directory — it cannot do better, and preserving caller intent on
// the stored row is right. But enrolment then dispatched on that guess: a deleted or
// bogus id typed "agent" was inserted against project_members.agent_id, the foreign
// key rejected it, and the error was logged and swallowed. No row, no signal, and
// nothing to distinguish that from "already a member".
//
// Enrolment now checks the directory it is about to write to.
func TestAssigneeEnrolment_UnknownUUIDIsNotEnrolledByGuess(t *testing.T) {
	ctx := context.Background()
	projectID, ghostID := uuid.New(), uuid.New()
	env := setupMembershipEnv(nil, nil)
	svc, pmRepo := env.svc, env.members

	// ghostID is deliberately in no directory — a deleted agent, or a typo.
	svc.ensureAssigneeProjectMember(ctx, projectID, &ghostID, domain.AssigneeTypeAgent)
	assert.Empty(t, pmRepo.members,
		"an id that is in no directory must not be enrolled as an agent on the strength of a guessed type")

	// And the same id guessed the other way must not land in the user column either.
	svc.ensureAssigneeProjectMember(ctx, projectID, &ghostID, domain.AssigneeTypeUser)
	assert.Empty(t, pmRepo.members,
		"nor as a user — the guess is wrong in both directions")
}

// TestAssigneeEnrolment_KnownAgentStillEnrols is the positive control. Without it the
// test above is satisfied by an enrolment path that refuses everything, which would
// re-open the dead end for every legitimate assignee.
func TestAssigneeEnrolment_KnownAgentStillEnrols(t *testing.T) {
	ctx := context.Background()
	projectID, agentID := uuid.New(), uuid.New()
	env := setupMembershipEnv(nil, nil)
	svc, pmRepo := env.svc, env.members
	env.agents.items[agentID] = &domain.Agent{ID: agentID, Slug: "real"}

	svc.ensureAssigneeProjectMember(ctx, projectID, &agentID, domain.AssigneeTypeAgent)
	assert.Len(t, pmRepo.members, 1, "a known agent must still be enrolled")
}

// TestAssigneeEnrolment_UnreadableDirectoryDoesNotBlockEnrolment pins the failure
// DIRECTION, which is the subtle half of this change.
//
// "I could not check" is not "I checked and found nothing". Refusing on a lookup
// error would convert a transient directory blip into a missing enrolment — exactly
// the dead end this mechanism exists to prevent — so an error falls through and lets
// the database's foreign key remain the backstop. Only a positive absence refuses.
func TestAssigneeEnrolment_UnreadableDirectoryDoesNotBlockEnrolment(t *testing.T) {
	ctx := context.Background()
	projectID, agentID := uuid.New(), uuid.New()
	env := setupMembershipEnv(nil, nil)
	svc, pmRepo := env.svc, env.members

	env.agents.errToReturn = assert.AnError // the directory is unreadable, not empty

	svc.ensureAssigneeProjectMember(ctx, projectID, &agentID, domain.AssigneeTypeAgent)
	assert.Len(t, pmRepo.members, 1,
		"a directory we could not read must not block enrolment — that would turn a "+
			"transient failure into the permission dead end this guard protects against")
}

// ---------------------------------------------------------------------------
// 7. Reviewer — the eighth write path that puts a principal on a task.
//
//	The Reviewer field arrived on a branch that predated the enrolment work, so
//	it set reviewer_id without ever enrolling. It is the worst-placed instance of
//	the bug rather than just another one: being made reviewer *notifies you*, so
//	the reviewer follows a notification to a task whose every transition 403s.
//
// ---------------------------------------------------------------------------

// reviewerUpdate returns the task as an Update caller would submit it: a fresh
// struct, not the pointer the repo holds. That separation matters — it is what
// uuidPtrChanged exists to handle, and reusing the stored pointer would let a
// change go undetected.
func reviewerUpdate(stored *domain.Task, reviewerID uuid.UUID, reviewerType *domain.AssigneeType) *domain.Task {
	updated := *stored
	id := reviewerID
	updated.ReviewerID = &id
	updated.ReviewerType = reviewerType
	return &updated
}

func TestReviewerEnrolment_UserReviewerEnrolledOnUpdate(t *testing.T) {
	ctx := context.Background()
	projectID, reviewerID, taskID, wsID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	env := setupMembershipEnv(nil, nil)
	svc, taskRepo, projRepo, pmRepo := env.svc, env.tasks, env.projects, env.members

	userRepo := NewMockUserRepository()
	userRepo.AddUser(wsID, &domain.User{ID: reviewerID, Username: "pavel"})
	svc.userRepo = userRepo

	projRepo.items[projectID] = &domain.Project{ID: projectID, WorkspaceID: wsID}
	taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: projectID, Title: "t", AssigneeType: domain.AssigneeTypeUnassigned,
	}

	// Precondition: the reviewer is a stranger to this project, or the test passes vacuously.
	exists, err := pmRepo.ExistsMember(ctx, projectID, &reviewerID, nil)
	require.NoError(t, err)
	require.False(t, exists, "precondition: reviewer must start as a non-member")

	userType := domain.AssigneeTypeUser
	require.NoError(t, svc.Update(ctx, reviewerUpdate(taskRepo.items[taskID], reviewerID, &userType)))

	ok, err := pmRepo.ExistsMember(ctx, projectID, &reviewerID, nil)
	require.NoError(t, err)
	assert.True(t, ok,
		"being made reviewer must enrol you — the notification sends you to a task "+
			"whose status transitions would otherwise all 403")
}

// reviewer_type is caller-supplied on updateTaskRequest and is not to be trusted,
// exactly as CreateSubtask's was in #501. Enrolment dispatches on the type, so an
// agent handed over as "user" would be written against project_members.user_id —
// rejected by the foreign key, logged, and swallowed, leaving no membership row.
func TestReviewerEnrolment_MistypedAgentReviewerIsResolvedNotTrusted(t *testing.T) {
	ctx := context.Background()
	projectID, reviewerID, taskID, wsID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	env := setupMembershipEnv(nil, nil)
	svc, taskRepo, projRepo, agentRepo, pmRepo := env.svc, env.tasks, env.projects, env.agents, env.members

	projRepo.items[projectID] = &domain.Project{ID: projectID, WorkspaceID: wsID}
	agentRepo.items[reviewerID] = &domain.Agent{ID: reviewerID, WorkspaceID: wsID, Slug: "garfield"}
	taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: projectID, Title: "t", AssigneeType: domain.AssigneeTypeUnassigned,
	}

	// The caller insists this agent is a user.
	wrongType := domain.AssigneeTypeUser
	require.NoError(t, svc.Update(ctx, reviewerUpdate(taskRepo.items[taskID], reviewerID, &wrongType)))

	assertEnrolled(t, pmRepo, projectID, reviewerID,
		"an agent reviewer mistyped as a user must be enrolled against the agent column")

	stored := taskRepo.items[taskID]
	require.NotNil(t, stored.ReviewerType)
	assert.Equal(t, domain.AssigneeTypeAgent, *stored.ReviewerType,
		"the resolved type must be persisted, not the caller's guess")
}

// A nil reviewer_type is a legitimate shape: the field is optional on the request.
// Resolution must still find the directory entry rather than skipping enrolment.
func TestReviewerEnrolment_NilReviewerTypeStillResolves(t *testing.T) {
	ctx := context.Background()
	projectID, reviewerID, taskID, wsID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	env := setupMembershipEnv(nil, nil)
	svc, taskRepo, projRepo, agentRepo, pmRepo := env.svc, env.tasks, env.projects, env.agents, env.members

	projRepo.items[projectID] = &domain.Project{ID: projectID, WorkspaceID: wsID}
	agentRepo.items[reviewerID] = &domain.Agent{ID: reviewerID, WorkspaceID: wsID, Slug: "garfield"}
	taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: projectID, Title: "t", AssigneeType: domain.AssigneeTypeUnassigned,
	}

	require.NoError(t, svc.Update(ctx, reviewerUpdate(taskRepo.items[taskID], reviewerID, nil)))

	assertEnrolled(t, pmRepo, projectID, reviewerID,
		"an omitted reviewer_type must be resolved from the directory, not treated as no reviewer")
}

// Guard: clearing the reviewer must not enrol anything, and an unchanged reviewer
// must not re-enrol on every PATCH.
func TestReviewerEnrolment_ClearAndUnchangedDoNotEnrol(t *testing.T) {
	ctx := context.Background()
	projectID, reviewerID, taskID, wsID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	env := setupMembershipEnv(nil, nil)
	svc, taskRepo, projRepo, agentRepo, pmRepo := env.svc, env.tasks, env.projects, env.agents, env.members

	projRepo.items[projectID] = &domain.Project{ID: projectID, WorkspaceID: wsID}
	agentRepo.items[reviewerID] = &domain.Agent{ID: reviewerID, WorkspaceID: wsID, Slug: "garfield"}
	agentType := domain.AssigneeTypeAgent
	taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: projectID, Title: "t", AssigneeType: domain.AssigneeTypeUnassigned,
		ReviewerID: &reviewerID, ReviewerType: &agentType,
	}

	// Same reviewer, submitted through a separate struct: uuidPtrChanged must read
	// this as unchanged even though the pointers differ.
	require.NoError(t, svc.Update(ctx, reviewerUpdate(taskRepo.items[taskID], reviewerID, &agentType)))
	assert.Empty(t, pmRepo.members,
		"an unchanged reviewer must not re-enrol — a raw pointer compare would fire on every PATCH")

	// Clearing the reviewer enrols nobody.
	cleared := *taskRepo.items[taskID]
	cleared.ReviewerID = nil
	cleared.ReviewerType = nil
	require.NoError(t, svc.Update(ctx, &cleared))
	assert.Empty(t, pmRepo.members, "clearing the reviewer must not enrol anyone")
}

// ---------------------------------------------------------------------------
// 8. Reviewer set at task creation (task #6bf97281) — the ninth write path.
//    Same contract as Update's reviewer handling: enrol on set, no-op when
//    absent.
// ---------------------------------------------------------------------------

func TestReviewerEnrolment_UserReviewerEnrolledOnCreate(t *testing.T) {
	ctx := context.Background()
	projectID, reviewerID, wsID := uuid.New(), uuid.New(), uuid.New()
	env := setupMembershipEnv(nil, nil)
	svc, projRepo, pmRepo := env.svc, env.projects, env.members

	userRepo := NewMockUserRepository()
	userRepo.AddUser(wsID, &domain.User{ID: reviewerID, Username: "pavel"})
	svc.userRepo = userRepo

	projRepo.items[projectID] = &domain.Project{ID: projectID, WorkspaceID: wsID}

	userType := domain.AssigneeTypeUser
	task := &domain.Task{
		ProjectID: projectID, Title: "t",
		ReviewerID: &reviewerID, ReviewerType: &userType,
	}
	require.NoError(t, svc.Create(ctx, task))

	ok, err := pmRepo.ExistsMember(ctx, projectID, &reviewerID, nil)
	require.NoError(t, err)
	assert.True(t, ok,
		"a reviewer set at task creation must be enrolled — same trap as the update path: "+
			"being made reviewer notifies you, and an un-enrolled reviewer's status transitions all 403")
}

// reviewer_type is caller-supplied on createTaskRequest too — resolve it against
// the directory rather than trust it, exactly as the Update path does.
func TestReviewerEnrolment_MistypedAgentReviewerIsResolvedNotTrustedOnCreate(t *testing.T) {
	ctx := context.Background()
	projectID, reviewerID, wsID := uuid.New(), uuid.New(), uuid.New()
	env := setupMembershipEnv(nil, nil)
	svc, projRepo, agentRepo, pmRepo := env.svc, env.projects, env.agents, env.members

	projRepo.items[projectID] = &domain.Project{ID: projectID, WorkspaceID: wsID}
	agentRepo.items[reviewerID] = &domain.Agent{ID: reviewerID, WorkspaceID: wsID, Slug: "garfield"}

	// The caller insists this agent is a user.
	wrongType := domain.AssigneeTypeUser
	task := &domain.Task{
		ProjectID: projectID, Title: "t",
		ReviewerID: &reviewerID, ReviewerType: &wrongType,
	}
	require.NoError(t, svc.Create(ctx, task))

	assertEnrolled(t, pmRepo, projectID, reviewerID,
		"an agent reviewer mistyped as a user at creation must be enrolled against the agent column")
	require.NotNil(t, task.ReviewerType)
	assert.Equal(t, domain.AssigneeTypeAgent, *task.ReviewerType,
		"the resolved type must be persisted, not the caller's guess")
}

// Guard: creating a task with no reviewer must not enrol anyone.
func TestReviewerEnrolment_NoReviewerAtCreateEnrolsNothing(t *testing.T) {
	ctx := context.Background()
	projectID := uuid.New()
	env := setupMembershipEnv(nil, nil)
	svc, projRepo, pmRepo := env.svc, env.projects, env.members

	projRepo.items[projectID] = &domain.Project{ID: projectID}

	require.NoError(t, svc.Create(ctx, &domain.Task{ProjectID: projectID, Title: "no reviewer"}))
	assert.Empty(t, pmRepo.members, "creating a task with no reviewer must not enrol anyone")
}
