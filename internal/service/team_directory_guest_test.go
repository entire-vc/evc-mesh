package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// TestGetTeamDirectory_GuestAgentVisibility covers AC3 of #71627c5a:
// GET /workspaces/:ws_id/team must show a GUEST agent (home workspace
// elsewhere, reached here via an active agent_workspace_grants connection)
// and must tag it as a guest rather than silently identical to a home
// member. Before this fix, GetTeamDirectory listed agents strictly by
// agents.workspace_id (s.agentRepo.List), which can never see a grant row —
// the same asymmetry #7661fc5d found on the authentication side.
func TestGetTeamDirectory_GuestAgentVisibility(t *testing.T) {
	ctx := context.Background()
	wsRepo := NewMockWorkspaceRepository()
	agentRepo := NewMockAgentRepository()
	memberRepo := NewMockWorkspaceMemberRepository()
	grantRepo := NewMockAgentWorkspaceGrantRepository()

	targetWS := uuid.New()
	homeWS := uuid.New()
	require.NoError(t, wsRepo.Create(ctx, &domain.Workspace{ID: targetWS, Name: "Target"}))

	homeAgentID := uuid.New()
	agentRepo.items[homeAgentID] = &domain.Agent{ID: homeAgentID, WorkspaceID: targetWS, Slug: "native", Name: "Native"}

	guestAgentID := uuid.New()
	agentRepo.items[guestAgentID] = &domain.Agent{ID: guestAgentID, WorkspaceID: homeWS, Slug: "guest", Name: "Guest"}
	grantRepo.Seed(&domain.AgentWorkspaceGrant{ID: uuid.New(), AgentID: guestAgentID, WorkspaceID: targetWS})

	svc := NewRulesServiceWithOptions(
		nil, nil, nil, agentRepo, memberRepo, wsRepo, nil,
		WithRulesAgentGrantRepo(grantRepo),
	)

	dir, err := svc.GetTeamDirectory(ctx, targetWS)
	require.NoError(t, err)
	require.Len(t, dir.Agents, 2, "both the home agent and the guest must appear")

	byID := map[uuid.UUID]domain.TeamDirectoryAgent{}
	for _, a := range dir.Agents {
		byID[a.ID] = a
	}

	home, ok := byID[homeAgentID]
	require.True(t, ok, "home agent missing from directory")
	assert.True(t, home.IsHome, "home agent must be tagged is_home=true")

	guest, ok := byID[guestAgentID]
	require.True(t, ok, "guest agent missing from directory — this is AC3's core repro")
	assert.False(t, guest.IsHome, "guest agent must be tagged is_home=false, distinguishable from a native member")
}

// TestGetTeamDirectory_RevokedGuestGrantIsNotListed is AC3's negative half:
// once a grant is revoked, ListActiveByWorkspace no longer returns it (same
// query RequireWorkspaceMember and assertAssigneeInProjectWorkspace rely on
// for "is this connection still active"), so the directory drops the guest
// along with it — not a special case, just the same active-grants query.
func TestGetTeamDirectory_RevokedGuestGrantIsNotListed(t *testing.T) {
	ctx := context.Background()
	wsRepo := NewMockWorkspaceRepository()
	agentRepo := NewMockAgentRepository()
	memberRepo := NewMockWorkspaceMemberRepository()
	grantRepo := NewMockAgentWorkspaceGrantRepository()

	targetWS := uuid.New()
	require.NoError(t, wsRepo.Create(ctx, &domain.Workspace{ID: targetWS, Name: "Target"}))

	guestAgentID := uuid.New()
	agentRepo.items[guestAgentID] = &domain.Agent{ID: guestAgentID, WorkspaceID: uuid.New(), Slug: "guest"}
	grant := &domain.AgentWorkspaceGrant{ID: uuid.New(), AgentID: guestAgentID, WorkspaceID: targetWS}
	grantRepo.Seed(grant)
	grantRepo.SeedRevoke(grant.ID, timeNow())

	svc := NewRulesServiceWithOptions(
		nil, nil, nil, agentRepo, memberRepo, wsRepo, nil,
		WithRulesAgentGrantRepo(grantRepo),
	)

	dir, err := svc.GetTeamDirectory(ctx, targetWS)
	require.NoError(t, err)
	assert.Empty(t, dir.Agents, "a revoked connection must not list the agent as a guest")
}

// TestGetTeamDirectory_HomeAgentNeverDuplicatedAsGuest is the defensive
// dedup case named in GetTeamDirectory's own comment: an agent that is both
// home AND (pathologically) carries a distinct grant row into that same
// home workspace must still appear exactly once, as the fuller home record
// — not twice, and not as a guest.
func TestGetTeamDirectory_HomeAgentNeverDuplicatedAsGuest(t *testing.T) {
	ctx := context.Background()
	wsRepo := NewMockWorkspaceRepository()
	agentRepo := NewMockAgentRepository()
	memberRepo := NewMockWorkspaceMemberRepository()
	grantRepo := NewMockAgentWorkspaceGrantRepository()

	targetWS := uuid.New()
	require.NoError(t, wsRepo.Create(ctx, &domain.Workspace{ID: targetWS, Name: "Target"}))

	homeAgentID := uuid.New()
	agentRepo.items[homeAgentID] = &domain.Agent{ID: homeAgentID, WorkspaceID: targetWS, Slug: "native"}
	// Pathological: a grant row into the agent's own home workspace.
	grantRepo.Seed(&domain.AgentWorkspaceGrant{ID: uuid.New(), AgentID: homeAgentID, WorkspaceID: targetWS})

	svc := NewRulesServiceWithOptions(
		nil, nil, nil, agentRepo, memberRepo, wsRepo, nil,
		WithRulesAgentGrantRepo(grantRepo),
	)

	dir, err := svc.GetTeamDirectory(ctx, targetWS)
	require.NoError(t, err)
	require.Len(t, dir.Agents, 1, "must not duplicate the home agent as a second, guest-flavored row")
	assert.True(t, dir.Agents[0].IsHome)
}

// TestGetTeamDirectory_NilGrantRepoFallsBackToHomeOnly pins the optional-
// dependency contract WithRulesAgentGrantRepo's own doc comment promises:
// unwired, GetTeamDirectory behaves exactly as it did before #71627c5a —
// home agents only — rather than panicking on a nil repo.
func TestGetTeamDirectory_NilGrantRepoFallsBackToHomeOnly(t *testing.T) {
	ctx := context.Background()
	wsRepo := NewMockWorkspaceRepository()
	agentRepo := NewMockAgentRepository()
	memberRepo := NewMockWorkspaceMemberRepository()

	targetWS := uuid.New()
	require.NoError(t, wsRepo.Create(ctx, &domain.Workspace{ID: targetWS, Name: "Target"}))

	homeAgentID := uuid.New()
	agentRepo.items[homeAgentID] = &domain.Agent{ID: homeAgentID, WorkspaceID: targetWS, Slug: "native"}

	svc := NewRulesServiceWithOptions(nil, nil, nil, agentRepo, memberRepo, wsRepo, nil)

	dir, err := svc.GetTeamDirectory(ctx, targetWS)
	require.NoError(t, err)
	require.Len(t, dir.Agents, 1)
	assert.True(t, dir.Agents[0].IsHome)
}

// TestGetTeamDirectory_GrantLookupErrorPropagates: an unreadable grant
// directory must fail the whole call rather than silently rendering a
// home-only directory that looks complete but is not — the read-side
// analogue of assertAssigneeInProjectWorkspace's fail-closed write guard.
func TestGetTeamDirectory_GrantLookupErrorPropagates(t *testing.T) {
	ctx := context.Background()
	wsRepo := NewMockWorkspaceRepository()
	agentRepo := NewMockAgentRepository()
	memberRepo := NewMockWorkspaceMemberRepository()
	grantRepo := NewMockAgentWorkspaceGrantRepository()
	grantRepo.errToReturn = assert.AnError

	targetWS := uuid.New()
	require.NoError(t, wsRepo.Create(ctx, &domain.Workspace{ID: targetWS, Name: "Target"}))

	svc := NewRulesServiceWithOptions(
		nil, nil, nil, agentRepo, memberRepo, wsRepo, nil,
		WithRulesAgentGrantRepo(grantRepo),
	)

	_, err := svc.GetTeamDirectory(ctx, targetWS)
	require.Error(t, err)
}
