package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ---------------------------------------------------------------------------
// resolveAssigneeType
// ---------------------------------------------------------------------------

func TestResolveAssigneeType_NilID(t *testing.T) {
	svc := &taskService{}
	got := svc.resolveAssigneeType(context.Background(), nil, domain.AssigneeTypeAgent)
	assert.Equal(t, domain.AssigneeTypeUnassigned, got)
}

func TestResolveAssigneeType_ZeroID(t *testing.T) {
	svc := &taskService{}
	zeroID := uuid.Nil
	got := svc.resolveAssigneeType(context.Background(), &zeroID, domain.AssigneeTypeAgent)
	assert.Equal(t, domain.AssigneeTypeUnassigned, got)
}

func TestResolveAssigneeType_AgentUUID(t *testing.T) {
	agentRepo := NewMockAgentRepository()
	userRepo := NewMockUserRepository()
	agentID := uuid.New()
	_ = agentRepo.Create(context.Background(), &domain.Agent{ID: agentID, Name: "riker"})

	svc := &taskService{agentRepo: agentRepo, userRepo: userRepo}
	got := svc.resolveAssigneeType(context.Background(), &agentID, domain.AssigneeTypeUser)
	assert.Equal(t, domain.AssigneeTypeAgent, got, "agent UUID must resolve to 'agent' regardless of fallback")
}

func TestResolveAssigneeType_UserUUID(t *testing.T) {
	agentRepo := NewMockAgentRepository()
	userRepo := NewMockUserRepository()
	wsID := uuid.New()
	userID := uuid.New()
	userRepo.AddUser(wsID, &domain.User{ID: userID, Username: "pavel"})

	svc := &taskService{agentRepo: agentRepo, userRepo: userRepo}
	got := svc.resolveAssigneeType(context.Background(), &userID, domain.AssigneeTypeAgent)
	assert.Equal(t, domain.AssigneeTypeUser, got, "user UUID must resolve to 'user' regardless of fallback")
}

func TestResolveAssigneeType_UnknownUUID_UsesFallback(t *testing.T) {
	agentRepo := NewMockAgentRepository()
	userRepo := NewMockUserRepository()
	unknownID := uuid.New()

	svc := &taskService{agentRepo: agentRepo, userRepo: userRepo}
	got := svc.resolveAssigneeType(context.Background(), &unknownID, domain.AssigneeTypeAgent)
	assert.Equal(t, domain.AssigneeTypeAgent, got, "unknown UUID should fall back to caller's value")
}

// ---------------------------------------------------------------------------
// AssignTask: human assignee saves assignee_type='user'
// ---------------------------------------------------------------------------

func TestAssignTask_HumanUUID_SavesTypeUser(t *testing.T) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()
	agentRepo := NewMockAgentRepository()
	userRepo := NewMockUserRepository()
	pmRepo := NewMockProjectMemberRepository()

	svc := NewTaskService(taskRepo, statusRepo, depRepo, activityRepo,
		WithTaskAgentRepo(agentRepo),
		WithUserRepoTask(userRepo),
		WithProjectMemberRepoTask(pmRepo),
	).(*taskService)

	// Register a human user (simulates Pavel).
	wsID := uuid.New()
	userID := uuid.New()
	userRepo.AddUser(wsID, &domain.User{ID: userID, Username: "pavel"})

	// Seed a task in the repo.
	projID := uuid.New()
	taskID := uuid.New()
	require.NoError(t, taskRepo.Create(context.Background(), &domain.Task{
		ID:           taskID,
		ProjectID:    projID,
		AssigneeType: domain.AssigneeTypeUnassigned,
	}))

	// Assign with wrong type (simulates Riker MCP defaulting to "agent").
	err := svc.AssignTask(context.Background(), taskID, AssignTaskInput{
		AssigneeID:   &userID,
		AssigneeType: domain.AssigneeTypeAgent,
	})
	require.NoError(t, err)

	stored, err := taskRepo.GetByID(context.Background(), taskID)
	require.NoError(t, err)
	assert.Equal(t, domain.AssigneeTypeUser, stored.AssigneeType, "assignee_type must be normalized to 'user'")
	assert.Equal(t, &userID, stored.AssigneeID)
}

func TestAssignTask_AgentUUID_SavesTypeAgent(t *testing.T) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()
	agentRepo := NewMockAgentRepository()
	userRepo := NewMockUserRepository()
	pmRepo := NewMockProjectMemberRepository()

	svc := NewTaskService(taskRepo, statusRepo, depRepo, activityRepo,
		WithTaskAgentRepo(agentRepo),
		WithUserRepoTask(userRepo),
		WithProjectMemberRepoTask(pmRepo),
	).(*taskService)

	agentID := uuid.New()
	require.NoError(t, agentRepo.Create(context.Background(), &domain.Agent{ID: agentID, Name: "linus"}))

	projID := uuid.New()
	taskID := uuid.New()
	require.NoError(t, taskRepo.Create(context.Background(), &domain.Task{
		ID:           taskID,
		ProjectID:    projID,
		AssigneeType: domain.AssigneeTypeUnassigned,
	}))

	// Assign with correct type (should stay 'agent').
	err := svc.AssignTask(context.Background(), taskID, AssignTaskInput{
		AssigneeID:   &agentID,
		AssigneeType: domain.AssigneeTypeAgent,
	})
	require.NoError(t, err)

	stored, err := taskRepo.GetByID(context.Background(), taskID)
	require.NoError(t, err)
	assert.Equal(t, domain.AssigneeTypeAgent, stored.AssigneeType)
}

// ---------------------------------------------------------------------------
// AssignTask: user assignee is enrolled in project members; agent enroll unchanged
// ---------------------------------------------------------------------------

func TestAssignTask_UserAssignee_EnrolledInProjectMembers(t *testing.T) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()
	agentRepo := NewMockAgentRepository()
	userRepo := NewMockUserRepository()
	pmRepo := NewMockProjectMemberRepository()

	svc := NewTaskService(taskRepo, statusRepo, depRepo, activityRepo,
		WithTaskAgentRepo(agentRepo),
		WithUserRepoTask(userRepo),
		WithProjectMemberRepoTask(pmRepo),
	).(*taskService)

	wsID := uuid.New()
	userID := uuid.New()
	userRepo.AddUser(wsID, &domain.User{ID: userID, Username: "pavel"})

	projID := uuid.New()
	taskID := uuid.New()
	require.NoError(t, taskRepo.Create(context.Background(), &domain.Task{
		ID:           taskID,
		ProjectID:    projID,
		AssigneeType: domain.AssigneeTypeUnassigned,
	}))

	require.NoError(t, svc.AssignTask(context.Background(), taskID, AssignTaskInput{
		AssigneeID:   &userID,
		AssigneeType: domain.AssigneeTypeAgent,
	}))

	// User should now be enrolled as a project member.
	isMember, err := pmRepo.ExistsMember(context.Background(), projID, &userID, nil)
	require.NoError(t, err)
	assert.True(t, isMember, "workspace owner/user assignee should be enrolled in project_members")
}

func TestAssignTask_AgentAssignee_NoUserMemberEnroll(t *testing.T) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()
	agentRepo := NewMockAgentRepository()
	userRepo := NewMockUserRepository()
	pmRepo := NewMockProjectMemberRepository()

	svc := NewTaskService(taskRepo, statusRepo, depRepo, activityRepo,
		WithTaskAgentRepo(agentRepo),
		WithUserRepoTask(userRepo),
		WithProjectMemberRepoTask(pmRepo),
	).(*taskService)

	agentID := uuid.New()
	require.NoError(t, agentRepo.Create(context.Background(), &domain.Agent{ID: agentID, Name: "linus"}))

	projID := uuid.New()
	taskID := uuid.New()
	require.NoError(t, taskRepo.Create(context.Background(), &domain.Task{
		ID:           taskID,
		ProjectID:    projID,
		AssigneeType: domain.AssigneeTypeUnassigned,
	}))

	require.NoError(t, svc.AssignTask(context.Background(), taskID, AssignTaskInput{
		AssigneeID:   &agentID,
		AssigneeType: domain.AssigneeTypeAgent,
	}))

	// Agent should be in project_members (agent slot), not user slot.
	isAgentMember, err := pmRepo.ExistsMember(context.Background(), projID, nil, &agentID)
	require.NoError(t, err)
	assert.True(t, isAgentMember, "agent should be enrolled")

	// User slot should be empty.
	isUserMember, err := pmRepo.ExistsMember(context.Background(), projID, &agentID, nil)
	require.NoError(t, err)
	assert.False(t, isUserMember, "agent UUID should not appear as user member")
}

// ---------------------------------------------------------------------------
// Create: human assignee saves assignee_type='user'
// ---------------------------------------------------------------------------

func TestCreate_HumanAssigneeID_NormalizesToUser(t *testing.T) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()
	agentRepo := NewMockAgentRepository()
	userRepo := NewMockUserRepository()

	svc := NewTaskService(taskRepo, statusRepo, depRepo, activityRepo,
		WithTaskAgentRepo(agentRepo),
		WithUserRepoTask(userRepo),
	).(*taskService)

	wsID := uuid.New()
	userID := uuid.New()
	userRepo.AddUser(wsID, &domain.User{ID: userID, Username: "pavel"})

	task := &domain.Task{
		ProjectID:    uuid.New(),
		StatusID:     uuid.New(),
		Title:        "Fix login bug",
		AssigneeID:   &userID,
		AssigneeType: domain.AssigneeTypeAgent,
	}
	require.NoError(t, svc.Create(context.Background(), task))

	stored, err := taskRepo.GetByID(context.Background(), task.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.AssigneeTypeUser, stored.AssigneeType)
}
