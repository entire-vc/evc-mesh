package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Every refusal reason of assertAssigneeInProjectWorkspace, one case each.
//
// These are the branches that decide the guard's failure DIRECTION, and they are
// the ones most likely to be "simplified" later by someone who reads a nil repo
// or a lookup error as "nothing to check here". Each case below is a state in
// which the guard cannot prove the principal belongs to the workspace, and the
// only safe answer to that is no.
func TestAssertAssigneeInProjectWorkspace_RefusalReasons(t *testing.T) {
	ctx := context.Background()
	wsID := uuid.New()

	// newSvc builds a service with the tenancy directories wired, so each case can
	// knock out exactly one of them and nothing else.
	newSvc := func() (*taskService, *MockProjectRepository, *MockAgentRepository, *MockWorkspaceMembershipReader) {
		projRepo := NewMockProjectRepository()
		agentRepo := NewMockAgentRepository()
		wsReader := NewMembershipTableReader()
		svc := NewTaskService(
			NewMockTaskRepository(), NewMockTaskStatusRepository(),
			NewMockTaskDependencyRepository(), NewMockActivityLogRepository(),
			WithProjectRepo(projRepo),
			WithTaskAgentRepo(agentRepo),
			WithWorkspaceMembershipReader(wsReader),
		).(*taskService)
		return svc, projRepo, agentRepo, wsReader
	}

	// seedProject registers a project belonging to wsID.
	seedProject := func(projRepo *MockProjectRepository) uuid.UUID {
		id := uuid.New()
		projRepo.items[id] = &domain.Project{ID: id, WorkspaceID: wsID}
		return id
	}

	t.Run("no project directory", func(t *testing.T) {
		svc, projRepo, _, _ := newSvc()
		projID := seedProject(projRepo)
		svc.projectRepo = nil
		id := uuid.New()

		err := svc.assertAssigneeInProjectWorkspace(ctx, projID, &id, domain.AssigneeTypeAgent)
		requireRefusal(t, err, "cannot establish the task's workspace")
	})

	t.Run("project lookup errors", func(t *testing.T) {
		svc, projRepo, _, _ := newSvc()
		projID := seedProject(projRepo)
		projRepo.errToReturn = assert.AnError
		id := uuid.New()

		err := svc.assertAssigneeInProjectWorkspace(ctx, projID, &id, domain.AssigneeTypeAgent)
		requireRefusal(t, err, "could not read the task's project")
	})

	t.Run("project does not exist", func(t *testing.T) {
		svc, _, _, _ := newSvc()
		id := uuid.New()

		err := svc.assertAssigneeInProjectWorkspace(ctx, uuid.New(), &id, domain.AssigneeTypeAgent)
		requireRefusal(t, err, "does not exist")
	})

	t.Run("no agent directory", func(t *testing.T) {
		svc, projRepo, _, _ := newSvc()
		projID := seedProject(projRepo)
		svc.agentRepo = nil
		id := uuid.New()

		err := svc.assertAssigneeInProjectWorkspace(ctx, projID, &id, domain.AssigneeTypeAgent)
		requireRefusal(t, err, "agent directory unavailable")
	})

	t.Run("agent lookup errors", func(t *testing.T) {
		svc, projRepo, agentRepo, _ := newSvc()
		projID := seedProject(projRepo)
		agentRepo.errToReturn = assert.AnError
		id := uuid.New()

		err := svc.assertAssigneeInProjectWorkspace(ctx, projID, &id, domain.AssigneeTypeAgent)
		requireRefusal(t, err, "could not read the agent directory")
	})

	t.Run("no such agent", func(t *testing.T) {
		svc, projRepo, _, _ := newSvc()
		projID := seedProject(projRepo)
		id := uuid.New()

		err := svc.assertAssigneeInProjectWorkspace(ctx, projID, &id, domain.AssigneeTypeAgent)
		requireRefusal(t, err, "no such agent")
	})

	t.Run("agent from another workspace", func(t *testing.T) {
		svc, projRepo, agentRepo, _ := newSvc()
		projID := seedProject(projRepo)
		id := uuid.New()
		agentRepo.items[id] = &domain.Agent{ID: id, WorkspaceID: uuid.New(), Slug: "foreign"}

		err := svc.assertAssigneeInProjectWorkspace(ctx, projID, &id, domain.AssigneeTypeAgent)
		requireRefusal(t, err, "different workspace")
	})

	t.Run("no membership directory", func(t *testing.T) {
		svc, projRepo, _, _ := newSvc()
		projID := seedProject(projRepo)
		svc.wsMembership = nil
		id := uuid.New()

		err := svc.assertAssigneeInProjectWorkspace(ctx, projID, &id, domain.AssigneeTypeUser)
		requireRefusal(t, err, "membership directory unavailable")
	})

	t.Run("membership lookup errors", func(t *testing.T) {
		svc, projRepo, _, wsReader := newSvc()
		projID := seedProject(projRepo)
		wsReader.FailWith(assert.AnError)
		id := uuid.New()

		err := svc.assertAssigneeInProjectWorkspace(ctx, projID, &id, domain.AssigneeTypeUser)
		requireRefusal(t, err, "could not read workspace membership")
	})

	t.Run("user is not a member", func(t *testing.T) {
		svc, projRepo, _, _ := newSvc()
		projID := seedProject(projRepo)
		id := uuid.New()

		err := svc.assertAssigneeInProjectWorkspace(ctx, projID, &id, domain.AssigneeTypeUser)
		requireRefusal(t, err, "not a member of this workspace")
	})

	// The permitting branches. Without these the whole table above is satisfied by
	// a function that returns an error unconditionally.
	t.Run("agent of this workspace is allowed", func(t *testing.T) {
		svc, projRepo, agentRepo, _ := newSvc()
		projID := seedProject(projRepo)
		id := uuid.New()
		agentRepo.items[id] = &domain.Agent{ID: id, WorkspaceID: wsID, Slug: "native"}

		require.NoError(t, svc.assertAssigneeInProjectWorkspace(ctx, projID, &id, domain.AssigneeTypeAgent))
	})

	t.Run("member of this workspace is allowed", func(t *testing.T) {
		svc, projRepo, _, wsReader := newSvc()
		projID := seedProject(projRepo)
		id := uuid.New()
		wsReader.Allow(wsID, id)

		require.NoError(t, svc.assertAssigneeInProjectWorkspace(ctx, projID, &id, domain.AssigneeTypeUser))
	})

	// Early returns: nothing is being granted, so there is nothing to refuse.
	t.Run("no assignee at all", func(t *testing.T) {
		svc, projRepo, _, _ := newSvc()
		projID := seedProject(projRepo)
		nilID := uuid.Nil

		require.NoError(t, svc.assertAssigneeInProjectWorkspace(ctx, projID, nil, domain.AssigneeTypeAgent))
		require.NoError(t, svc.assertAssigneeInProjectWorkspace(ctx, projID, &nilID, domain.AssigneeTypeAgent))
	})

	t.Run("a type that grants nothing readable", func(t *testing.T) {
		svc, projRepo, _, _ := newSvc()
		projID := seedProject(projRepo)
		id := uuid.New()

		// ListByAssignee filters on assignee_id AND assignee_type together, so a row
		// typed "unassigned" never matches the principal's own feed and no enrolment
		// row is written for it. See assertAssigneeInProjectWorkspace's doc comment.
		require.NoError(t, svc.assertAssigneeInProjectWorkspace(ctx, projID, &id, domain.AssigneeTypeUnassigned))
	})
}

// requireRefusal asserts the error is the typed tenancy refusal carrying the
// expected reason, and — the part that matters for a security message — that it
// does not name the workspace the principal actually belongs to.
func requireRefusal(t *testing.T, err error, wantReason string) {
	t.Helper()
	require.Error(t, err)
	var foreign *AssigneeNotInWorkspaceError
	require.ErrorAs(t, err, &foreign, "refusal must be the typed tenancy error")
	assert.Contains(t, foreign.Reason, wantReason)
	assert.NotEmpty(t, foreign.Error(), "the error must render for logs")
}

// TestGetMyTasksPassesTheWorkspaceThrough pins that the service hands the
// workspace to the repository rather than dropping it.
//
// It is a narrow claim and worth saying what it does NOT prove: the mock holds no
// project-to-workspace mapping, so it cannot show the predicate reaches SQL. That
// is TestAgentTaskFeedIsWorkspaceScoped's job, against a real database. What this
// catches is the cheap regression — a future edit that keeps the parameter in the
// signature and stops passing it on.
func TestGetMyTasksPassesTheWorkspaceThrough(t *testing.T) {
	taskRepo := NewMockTaskRepository()
	svc := newTestTaskService(taskRepo, NewMockTaskStatusRepository(),
		NewMockTaskDependencyRepository(), NewMockActivityLogRepository()).(*taskService)

	wsID, agentID := uuid.New(), uuid.New()
	_, err := svc.GetMyTasks(context.Background(), wsID, agentID, domain.AssigneeTypeAgent)
	require.NoError(t, err)

	assert.Equal(t, wsID, taskRepo.LastListByAssigneeWorkspace,
		"the feed read must be scoped to the workspace it was given")
}

// TestRestorePreReviewAssignee_RefusesAStaleStashEndToEnd drives the refusal
// through MoveTask rather than calling the check directly, so the "leave the task
// with the reviewer" behaviour is observed rather than assumed.
//
// The reachable production shape: a card enters review, the builder who held it
// is deleted while it sits there, and the bounce back out would otherwise restore
// a principal the directory no longer knows.
func TestRestorePreReviewAssignee_RefusesAStaleStashEndToEnd(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	ctx := context.Background()

	reviewID, todoID, taskID := uuid.New(), uuid.New(), uuid.New()
	env.statuses.items[reviewID] = &domain.TaskStatus{
		ID: reviewID, ProjectID: env.projectID, Category: domain.StatusCategoryReview, Name: "review",
	}
	env.statuses.items[todoID] = &domain.TaskStatus{
		ID: todoID, ProjectID: env.projectID, Category: domain.StatusCategoryTodo, Name: "todo",
	}

	// The card is in review, held by the reviewer, with a stash naming a builder
	// who is no longer in the directory.
	ghostBuilder := uuid.New()
	ghostType := domain.AssigneeTypeAgent
	env.tasks.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: env.projectID, StatusID: reviewID, Title: "in review",
		AssigneeID: &env.nativeAgent, AssigneeType: domain.AssigneeTypeAgent,
		PreReviewAssigneeID: &ghostBuilder, PreReviewAssigneeType: &ghostType,
	}

	require.NoError(t, env.svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &todoID}),
		"the bounce itself must still succeed — a stale stash is not the caller's fault")

	task := env.tasks.items[taskID]
	require.NotNil(t, task.AssigneeID)
	assert.Equal(t, env.nativeAgent, *task.AssigneeID,
		"a stash naming a principal the directory no longer has must not be restored; the card "+
			"stays with the reviewer instead")
}

// TestApplyReviewAssignee_EnrolmentRefusalLeavesTheAssigneeAlone covers the
// second refusal inside the rotation — the one after the tenancy check has
// already passed.
//
// It is defence in depth by construction (the check immediately above it just
// said yes), so the only way to reach it is to make the directory disagree with
// itself between the two calls. That is what the error injection below models,
// and the property worth pinning is that a refusal here does not persist a
// half-applied rotation.
func TestApplyReviewAssignee_EnrolmentRefusalLeavesTheAssigneeAlone(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	ctx := context.Background()

	// A ghost stash again, but this time assert the task row was never written:
	// restorePreReviewAssignee returns before taskRepo.Update.
	reviewID, todoID, taskID := uuid.New(), uuid.New(), uuid.New()
	env.statuses.items[reviewID] = &domain.TaskStatus{
		ID: reviewID, ProjectID: env.projectID, Category: domain.StatusCategoryReview, Name: "review",
	}
	env.statuses.items[todoID] = &domain.TaskStatus{
		ID: todoID, ProjectID: env.projectID, Category: domain.StatusCategoryTodo, Name: "todo",
	}
	foreignType := domain.AssigneeTypeAgent
	env.tasks.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: env.projectID, StatusID: reviewID, Title: "in review",
		AssigneeID: &env.nativeAgent, AssigneeType: domain.AssigneeTypeAgent,
		PreReviewAssigneeID: &env.foreignAgent, PreReviewAssigneeType: &foreignType,
	}

	require.NoError(t, env.svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &todoID}))

	task := env.tasks.items[taskID]
	require.NotNil(t, task.PreReviewAssigneeID,
		"the stash must survive a refused restore — clearing it would silently discard the "+
			"information needed to fix the situation")
	assert.Equal(t, env.nativeAgent, *task.AssigneeID)
	assert.Empty(t, env.members.members,
		"and no enrolment row for the outside principal")
}
