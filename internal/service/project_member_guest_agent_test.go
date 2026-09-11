package service

// Tests for AddAgentMember recognizing a GUEST agent (invited into the
// project's workspace via agent_workspace_grants, task U1/U3) in addition to
// a HOME one (agent.WorkspaceID == project.WorkspaceID). Task #80dfb336: the
// U1-U3 invite/grant epic produced agents that can authenticate into a
// workspace but could never be added to any of its projects — every guest
// agent was refused with "agent does not belong to this workspace" because
// the check only ever looked at the agent's home workspace.

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

type guestAgentEnv struct {
	svc      ProjectMemberService
	projects *MockProjectRepository
	agents   *MockAgentRepository
	grants   *MockAgentWorkspaceGrantRepository
	members  *MockProjectMemberRepository

	homeWS, foreignWS uuid.UUID
	projectID         uuid.UUID
	guestAgentID      uuid.UUID
	strangerAgentID   uuid.UUID
}

// setupGuestAgentEnv builds a project in foreignWS, a guest agent whose HOME
// is homeWS but who holds an active grant into foreignWS, and a stranger
// agent whose home is homeWS with no grant at all.
func setupGuestAgentEnv(t *testing.T) *guestAgentEnv {
	t.Helper()
	env := &guestAgentEnv{
		homeWS:          uuid.New(),
		foreignWS:       uuid.New(),
		projectID:       uuid.New(),
		guestAgentID:    uuid.New(),
		strangerAgentID: uuid.New(),
	}

	projects := NewMockProjectRepository()
	agents := NewMockAgentRepository()
	grants := NewMockAgentWorkspaceGrantRepository()
	members := NewMockProjectMemberRepository()
	wsMembers := NewMockWorkspaceMemberRepository()

	projects.items[env.projectID] = &domain.Project{ID: env.projectID, WorkspaceID: env.foreignWS}
	agents.items[env.guestAgentID] = &domain.Agent{ID: env.guestAgentID, WorkspaceID: env.homeWS, Slug: "guest"}
	agents.items[env.strangerAgentID] = &domain.Agent{ID: env.strangerAgentID, WorkspaceID: env.homeWS, Slug: "stranger"}

	env.svc = NewProjectMemberService(members, wsMembers, projects,
		WithAgentRepo(agents),
		WithAgentWorkspaceGrantRepo(grants),
	)
	env.projects, env.agents, env.grants, env.members = projects, agents, grants, members
	return env
}

func TestAddAgentMember_GuestWithActiveGrant_Allowed(t *testing.T) {
	env := setupGuestAgentEnv(t)
	env.grants.Seed(&domain.AgentWorkspaceGrant{
		ID:          uuid.New(),
		AgentID:     env.guestAgentID,
		WorkspaceID: env.foreignWS,
		Role:        "admin",
	})

	member, err := env.svc.AddAgentMember(context.Background(), env.projectID, env.guestAgentID, domain.ProjectRoleMember)

	require.NoError(t, err, "a guest agent holding an active grant into the project's workspace must be addable")
	require.NotNil(t, member)
	assert.Equal(t, env.guestAgentID, *member.AgentID)
}

func TestAddAgentMember_NoGrantAndNoHome_StillRefused(t *testing.T) {
	env := setupGuestAgentEnv(t)
	// strangerAgentID: home is homeWS, no grant into foreignWS at all.

	_, err := env.svc.AddAgentMember(context.Background(), env.projectID, env.strangerAgentID, domain.ProjectRoleMember)

	require.Error(t, err, "an agent with neither a home match nor any grant must still be refused")
	assert.Contains(t, err.Error(), "does not belong to this workspace")
}

func TestAddAgentMember_RevokedGrant_StillRefused(t *testing.T) {
	env := setupGuestAgentEnv(t)
	revokedID := uuid.New()
	env.grants.Seed(&domain.AgentWorkspaceGrant{
		ID:          revokedID,
		AgentID:     env.guestAgentID,
		WorkspaceID: env.foreignWS,
		Role:        "admin",
	})
	env.grants.SeedRevoke(revokedID, timeNow())

	_, err := env.svc.AddAgentMember(context.Background(), env.projectID, env.guestAgentID, domain.ProjectRoleMember)

	require.Error(t, err, "a REVOKED grant must not count as workspace membership — the whole point of revoke is that it stops meaning something")
	assert.Contains(t, err.Error(), "does not belong to this workspace")
}

func TestAddAgentMember_GrantIntoADifferentWorkspace_StillRefused(t *testing.T) {
	env := setupGuestAgentEnv(t)
	otherWS := uuid.New()
	// The guest agent holds a grant, but into a THIRD workspace, not the
	// project's own foreignWS.
	env.grants.Seed(&domain.AgentWorkspaceGrant{
		ID:          uuid.New(),
		AgentID:     env.guestAgentID,
		WorkspaceID: otherWS,
		Role:        "admin",
	})

	_, err := env.svc.AddAgentMember(context.Background(), env.projectID, env.guestAgentID, domain.ProjectRoleMember)

	require.Error(t, err, "a grant into a DIFFERENT workspace must not open this project's own workspace")
	assert.Contains(t, err.Error(), "does not belong to this workspace")
}

func TestAddAgentMember_HomeAgent_StillAllowedWithGrantRepoWired(t *testing.T) {
	env := setupGuestAgentEnv(t)
	homeAgentID := uuid.New()
	env.agents.items[homeAgentID] = &domain.Agent{ID: homeAgentID, WorkspaceID: env.foreignWS, Slug: "home"}

	member, err := env.svc.AddAgentMember(context.Background(), env.projectID, homeAgentID, domain.ProjectRoleMember)

	require.NoError(t, err, "a HOME agent (workspace already matches) must still be allowed — wiring the grant repo must not regress the existing path")
	require.NotNil(t, member)
}
