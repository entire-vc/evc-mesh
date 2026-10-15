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

// Real Postgres via DATABASE_URL, skipped when unreachable (see agentDigestTestDB).

func TestWorkspaceExport_HierarchySponsorAndRemovedMember(t *testing.T) {
	db := agentDigestTestDB(t)
	ctx := context.Background()
	agent := newDigestAgent(t, db, "digest-"+uuid.New().String())
	wsID := agent.WorkspaceID
	repo := NewWorkspaceExportRepo(db)

	var owner uuid.UUID
	require.NoError(t, db.GetContext(ctx, &owner, `SELECT owner_id FROM workspaces WHERE id = $1`, wsID))
	members := NewWorkspaceMemberRepo(db)
	now := time.Now()
	require.NoError(t, members.Create(ctx, &domain.WorkspaceMember{
		ID: uuid.New(), WorkspaceID: wsID, UserID: owner, Role: domain.RoleOwner, CreatedAt: now, UpdatedAt: now}))
	other := &domain.User{ID: uuid.New(), Email: "x-" + uuid.NewString()[:8] + "@example.com", PasswordHash: "x",
		Name: "Other", Username: "other-" + uuid.NewString()[:8], IsActive: true}
	require.NoError(t, NewUserRepo(db).Create(ctx, other))
	require.NoError(t, members.Create(ctx, &domain.WorkspaceMember{
		ID: uuid.New(), WorkspaceID: wsID, UserID: other.ID, Role: domain.RoleMember, CreatedAt: now, UpdatedAt: now}))
	proj := &domain.Project{ID: uuid.New(), WorkspaceID: wsID, Name: "P", Slug: "p-" + uuid.NewString()[:8],
		Settings: []byte(`{}`), DefaultAssigneeType: domain.DefaultAssigneeUser}
	require.NoError(t, NewProjectRepo(db).Create(ctx, proj))

	// An additional key issued by the owner, and a revoked one that must not show.
	keys := NewAgentAPIKeyRepo(db)
	live := &domain.AgentAPIKey{ID: uuid.New(), AgentID: agent.ID, WorkspaceID: wsID, KeyPrefix: "live" + uuid.NewString()[:6],
		KeySHA256: "live-" + uuid.NewString(), Label: "rotator", IssuedByType: domain.ActorTypeUser, IssuedByID: owner}
	dead := &domain.AgentAPIKey{ID: uuid.New(), AgentID: agent.ID, WorkspaceID: wsID, KeyPrefix: "dead" + uuid.NewString()[:6],
		KeySHA256: "dead-" + uuid.NewString(), IssuedByType: domain.ActorTypeUser, IssuedByID: owner}
	require.NoError(t, keys.Create(ctx, live))
	require.NoError(t, keys.Create(ctx, dead))
	_, err := keys.Revoke(ctx, agent.ID, dead.ID)
	require.NoError(t, err)

	require.NoError(t, repo.SetAgentSponsor(ctx, agent.ID, owner))

	snap, ok, err := repo.Hierarchy(ctx, wsID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, wsID, snap.Workspace.ID)
	require.Len(t, snap.Projects, 1)
	assert.Equal(t, proj.ID, snap.Projects[0].ID)
	require.Len(t, snap.Members, 2)
	require.Len(t, snap.Agents, 1)
	a := snap.Agents[0]
	require.NotNil(t, a.SponsorUserID)
	assert.Equal(t, owner, *a.SponsorUserID)
	var ids []string
	for _, k := range a.ActiveKeys {
		ids = append(ids, k.KeyID)
	}
	assert.ElementsMatch(t, []string{domain.AgentPrimaryKeyID, live.ID.String()}, ids, "primary + live additional key; the revoked one is absent")

	// Red control: a member removed behind the extension's back is gone from the next export.
	require.NoError(t, members.Delete(ctx, wsID, other.ID))
	after, _, err := repo.Hierarchy(ctx, wsID)
	require.NoError(t, err)
	require.Len(t, after.Members, 1)
	assert.Equal(t, owner, after.Members[0].UserID)

	// A workspace that does not exist is a miss, not an empty export.
	_, ok, err = repo.Hierarchy(ctx, uuid.New())
	require.NoError(t, err)
	assert.False(t, ok)
}
