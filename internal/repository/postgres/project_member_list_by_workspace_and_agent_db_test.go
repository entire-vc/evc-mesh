package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// TestProjectMemberRepo_ListByWorkspaceAndAgent_ScopesToWorkspace exercises
// the query ResyncConnectorMemberships (oauth_service.go, task cf226500)
// relies on to read a connector agent's OWN current project memberships
// before diffing them against its human supervisor's: it must return every
// membership the agent holds inside the given workspace, and must never leak
// a membership the same agent holds in a project of a different workspace —
// even one reached only through an active cross-workspace guest grant, which
// is exactly the shape a query filtering on agent_id alone would wrongly
// include.
func TestProjectMemberRepo_ListByWorkspaceAndAgent_ScopesToWorkspace(t *testing.T) {
	db := pmFKTestDB(t)
	fx := newPMFKFixture(t, db)
	ctx := context.Background()
	repo := NewProjectMemberRepo(db)
	suffix := uuid.New().String()[:8]

	require.NoError(t, repo.Create(ctx, &domain.ProjectMember{
		ID: uuid.New(), ProjectID: fx.nativeProjectID, AgentID: &fx.nativeAgentID,
		Role: "admin", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	secondNativeProject := &domain.Project{
		ID: uuid.New(), WorkspaceID: fx.nativeWorkspaceID, Name: "pmfk-agent-native-proj-2",
		Slug: "pmfk-agent-native-proj-2-" + suffix, DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	require.NoError(t, NewProjectRepo(db).Create(ctx, secondNativeProject))
	require.NoError(t, repo.Create(ctx, &domain.ProjectMember{
		ID: uuid.New(), ProjectID: secondNativeProject.ID, AgentID: &fx.nativeAgentID,
		Role: "member", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	// A THIRD, unrelated workspace that the same native agent also holds a
	// membership in, reached only via an active agent_workspace_grants row —
	// this must never come back from a call scoped to the native workspace.
	thirdOwner := &domain.User{
		ID: uuid.New(), Email: "pmfk-agent-third-" + suffix + "@example.com", PasswordHash: "x",
		Name: "Third Owner", Username: "pmfk-agent-third-" + suffix, IsActive: true,
	}
	require.NoError(t, NewUserRepo(db).Create(ctx, thirdOwner))
	thirdWS := &domain.Workspace{
		ID: uuid.New(), Name: "pmfk-agent-third-ws", Slug: "pmfk-agent-third-ws-" + suffix, OwnerID: thirdOwner.ID,
	}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, thirdWS))
	thirdProject := &domain.Project{
		ID: uuid.New(), WorkspaceID: thirdWS.ID, Name: "pmfk-agent-third-proj",
		Slug: "pmfk-agent-third-proj-" + suffix, DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	require.NoError(t, NewProjectRepo(db).Create(ctx, thirdProject))
	grant := &domain.AgentWorkspaceGrant{
		ID: uuid.New(), AgentID: fx.nativeAgentID, WorkspaceID: thirdWS.ID,
		Role: "member", APIKeyPrefix: "tg-" + suffix, APIKeyHash: "$2a$12$" + suffix,
	}
	require.NoError(t, NewAgentWorkspaceGrantRepo(db).Create(ctx, grant))
	require.NoError(t, repo.Create(ctx, &domain.ProjectMember{
		ID: uuid.New(), ProjectID: thirdProject.ID, AgentID: &fx.nativeAgentID,
		Role: "admin", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	members, err := repo.ListByWorkspaceAndAgent(ctx, fx.nativeWorkspaceID, fx.nativeAgentID)
	require.NoError(t, err)

	byProject := make(map[uuid.UUID]domain.ProjectMember, len(members))
	for _, m := range members {
		byProject[m.ProjectID] = m
	}
	assert.Len(t, members, 2, "must return exactly the two native-workspace memberships, no more, no less")
	require.Contains(t, byProject, fx.nativeProjectID)
	assert.Equal(t, "admin", byProject[fx.nativeProjectID].Role)
	require.Contains(t, byProject, secondNativeProject.ID)
	assert.Equal(t, "member", byProject[secondNativeProject.ID].Role)
	assert.NotContains(t, byProject, thirdProject.ID, "a membership reached only via a guest grant into another workspace must never be returned")
}

// TestProjectMemberRepo_ListByWorkspaceAndAgent_NoMemberships proves the
// empty case returns an empty slice and no error — ResyncConnectorMemberships
// treats an error as a hard stop for that grant's row, so a freshly
// registered connector agent with no memberships yet must not look like a
// failed lookup.
func TestProjectMemberRepo_ListByWorkspaceAndAgent_NoMemberships(t *testing.T) {
	db := pmFKTestDB(t)
	fx := newPMFKFixture(t, db)

	members, err := NewProjectMemberRepo(db).ListByWorkspaceAndAgent(context.Background(), fx.nativeWorkspaceID, fx.nativeAgentID)
	require.NoError(t, err)
	assert.Empty(t, members)
}

// TestProjectMemberRepo_UpdateRoleAgent_ChangesRole proves the role
// correction ResyncConnectorMemberships applies when a connector agent's
// mirrored role has drifted from its human supervisor's current one actually
// lands — verified through a follow-up ListByWorkspaceAndAgent read, not just
// a nil error, since UpdateRoleAgent itself never reports rows affected.
func TestProjectMemberRepo_UpdateRoleAgent_ChangesRole(t *testing.T) {
	db := pmFKTestDB(t)
	fx := newPMFKFixture(t, db)
	ctx := context.Background()
	repo := NewProjectMemberRepo(db)

	require.NoError(t, repo.Create(ctx, &domain.ProjectMember{
		ID: uuid.New(), ProjectID: fx.nativeProjectID, AgentID: &fx.nativeAgentID,
		Role: "member", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	require.NoError(t, repo.UpdateRoleAgent(ctx, fx.nativeProjectID, fx.nativeAgentID, "admin"))

	members, err := repo.ListByWorkspaceAndAgent(ctx, fx.nativeWorkspaceID, fx.nativeAgentID)
	require.NoError(t, err)
	require.Len(t, members, 1)
	assert.Equal(t, "admin", members[0].Role)
}

// TestProjectMemberRepo_ListByWorkspaceAndAgent_QueryErrorPropagates and its
// UpdateRoleAgent sibling below prove neither query failure is swallowed —
// ResyncConnectorMemberships relies on getting a real error back so it can
// log and skip that grant rather than silently proceeding with a stale or
// incomplete membership diff.
func TestProjectMemberRepo_ListByWorkspaceAndAgent_QueryErrorPropagates(t *testing.T) {
	db := pmFKTestDB(t)
	require.NoError(t, db.Close())

	_, err := NewProjectMemberRepo(db).ListByWorkspaceAndAgent(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err)
}

func TestProjectMemberRepo_UpdateRoleAgent_QueryErrorPropagates(t *testing.T) {
	db := pmFKTestDB(t)
	require.NoError(t, db.Close())

	err := NewProjectMemberRepo(db).UpdateRoleAgent(context.Background(), uuid.New(), uuid.New(), "admin")
	require.Error(t, err)
}
