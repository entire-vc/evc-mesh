package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Task fee35355, the auto-derivation entry point: AddMemberWithCreate derives
// a username from the new member's email address without ever asking a human
// — so if that derived candidate happens to equal an existing agent's slug in
// the target workspace, the account would be created with that exact
// colliding handle, silently, with nobody ever having chosen it. This is the
// "заведение пользователя" path least likely to be caught by hand-testing,
// since it never surfaces the username to anyone to notice.

// TestAddMemberWithCreate_SkipsAgentSlugCollisionInDerivedUsername is the RED
// case: an agent slug "mario" already exists in the target workspace, and the
// new member's email derives to exactly "mario" — the created account must
// NOT end up with that username; it should fall through to the same
// numeric-suffix path already used for a taken username.
func TestAddMemberWithCreate_SkipsAgentSlugCollisionInDerivedUsername(t *testing.T) {
	userRepo := NewMockUserRepository()
	memberRepo := &minimalWorkspaceMemberRepo{}
	agentRepo := NewMockAgentRepository()
	svc := NewWorkspaceMemberService(memberRepo, userRepo, NewMockProjectMemberRepository(), nil, agentRepo)

	wsID := uuid.New()
	require.NoError(t, agentRepo.Create(context.Background(), &domain.Agent{
		ID: uuid.New(), WorkspaceID: wsID, Name: "Mario", Slug: "mario",
	}))

	member, err := svc.AddMemberWithCreate(context.Background(), wsID,
		"mario@example.com", "New Person", domain.RoleMember, "StrongP4ss", uuid.Nil)
	require.NoError(t, err)
	assert.NotEqual(t, "mario", member.User.Username,
		"the derived username must not silently collide with an existing agent's slug in this workspace")
}

// TestAddMemberWithCreate_FreeEmailPrefixStillWorks is the negative control:
// an ordinary email with no collision at all must still derive the plain
// prefix, unaffected by the added agent-slug check.
func TestAddMemberWithCreate_FreeEmailPrefixStillWorks(t *testing.T) {
	userRepo := NewMockUserRepository()
	memberRepo := &minimalWorkspaceMemberRepo{}
	agentRepo := NewMockAgentRepository()
	svc := NewWorkspaceMemberService(memberRepo, userRepo, NewMockProjectMemberRepository(), nil, agentRepo)

	member, err := svc.AddMemberWithCreate(context.Background(), uuid.New(),
		"totally-free-prefix@example.com", "New Person", domain.RoleMember, "StrongP4ss", uuid.Nil)
	require.NoError(t, err)
	assert.Equal(t, "totally-free-prefix", member.User.Username)
}

// TestAddMemberWithCreate_AgentSlugInDifferentWorkspaceStillDerivesPlain proves
// the skip is workspace-scoped: an agent slug matching the derived prefix in
// an UNRELATED workspace must not perturb derivation for THIS workspace.
func TestAddMemberWithCreate_AgentSlugInDifferentWorkspaceStillDerivesPlain(t *testing.T) {
	userRepo := NewMockUserRepository()
	memberRepo := &minimalWorkspaceMemberRepo{}
	agentRepo := NewMockAgentRepository()
	svc := NewWorkspaceMemberService(memberRepo, userRepo, NewMockProjectMemberRepository(), nil, agentRepo)

	otherWorkspaceID := uuid.New()
	require.NoError(t, agentRepo.Create(context.Background(), &domain.Agent{
		ID: uuid.New(), WorkspaceID: otherWorkspaceID, Name: "Ralph", Slug: "ralph",
	}))

	member, err := svc.AddMemberWithCreate(context.Background(), uuid.New(),
		"ralph@example.com", "New Person", domain.RoleMember, "StrongP4ss", uuid.Nil)
	require.NoError(t, err)
	assert.Equal(t, "ralph", member.User.Username,
		"an agent slug collision in a different workspace must not affect this one")
}
