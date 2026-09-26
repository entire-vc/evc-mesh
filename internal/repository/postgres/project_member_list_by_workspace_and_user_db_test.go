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

// TestProjectMemberRepo_ListByWorkspaceAndUser_ScopesToWorkspace exercises the
// query mirrorProjectMemberships (oauth_service.go) relies on to onboard a
// fresh connector agent with the consenting human's own project access: it
// must return every project membership a user holds INSIDE the given
// workspace, and must never leak a membership the same user holds in a
// project that belongs to a different workspace — a workspace-agnostic query
// would mirror a human's access in one workspace onto a connector agent
// scoped to another (task ec0bc566).
func TestProjectMemberRepo_ListByWorkspaceAndUser_ScopesToWorkspace(t *testing.T) {
	db := pmFKTestDB(t)
	fx := newPMFKFixture(t, db)
	ctx := context.Background()
	repo := NewProjectMemberRepo(db)

	require.NoError(t, repo.Create(ctx, &domain.ProjectMember{
		ID: uuid.New(), ProjectID: fx.nativeProjectID, UserID: &fx.nativeUserID,
		Role: "admin", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	secondNativeProject := &domain.Project{
		ID: uuid.New(), WorkspaceID: fx.nativeWorkspaceID, Name: "pmfk-native-proj-2",
		Slug: "pmfk-native-proj-2-" + uuid.New().String()[:8], DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	require.NoError(t, NewProjectRepo(db).Create(ctx, secondNativeProject))
	require.NoError(t, repo.Create(ctx, &domain.ProjectMember{
		ID: uuid.New(), ProjectID: secondNativeProject.ID, UserID: &fx.nativeUserID,
		Role: "member", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	// A THIRD workspace, unrelated to the fixture's native/foreign pair, that
	// the same native user also happens to belong to — this is the case a
	// query filtering on user_id alone would wrongly include.
	otherOwner := &domain.User{
		ID: uuid.New(), Email: "pmfk-other-" + uuid.New().String()[:8] + "@example.com", PasswordHash: "x",
		Name: "Other Owner", Username: "pmfk-other-" + uuid.New().String()[:8], IsActive: true,
	}
	require.NoError(t, NewUserRepo(db).Create(ctx, otherOwner))
	otherWS := &domain.Workspace{
		ID: uuid.New(), Name: "pmfk-other-ws", Slug: "pmfk-other-ws-" + uuid.New().String()[:8], OwnerID: otherOwner.ID,
	}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, otherWS))
	require.NoError(t, NewWorkspaceMemberRepo(db).Create(ctx, &domain.WorkspaceMember{
		ID: uuid.New(), WorkspaceID: otherWS.ID, UserID: fx.nativeUserID, Role: "member",
	}))
	otherProject := &domain.Project{
		ID: uuid.New(), WorkspaceID: otherWS.ID, Name: "pmfk-other-proj",
		Slug: "pmfk-other-proj-" + uuid.New().String()[:8], DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	require.NoError(t, NewProjectRepo(db).Create(ctx, otherProject))
	require.NoError(t, repo.Create(ctx, &domain.ProjectMember{
		ID: uuid.New(), ProjectID: otherProject.ID, UserID: &fx.nativeUserID,
		Role: "admin", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	members, err := repo.ListByWorkspaceAndUser(ctx, fx.nativeWorkspaceID, fx.nativeUserID)
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
	assert.NotContains(t, byProject, otherProject.ID, "a membership in a different workspace's project must never be returned")
}

// TestProjectMemberRepo_ListByWorkspaceAndUser_NoMemberships proves the empty
// case returns an empty slice and no error, rather than erroring on the zero
// rows — mirrorProjectMemberships treats an error as a hard stop, so a user
// with no project memberships must not look like a failed lookup.
func TestProjectMemberRepo_ListByWorkspaceAndUser_NoMemberships(t *testing.T) {
	db := pmFKTestDB(t)
	fx := newPMFKFixture(t, db)
	ctx := context.Background()

	members, err := NewProjectMemberRepo(db).ListByWorkspaceAndUser(ctx, fx.nativeWorkspaceID, fx.nativeUserID)
	require.NoError(t, err)
	assert.Empty(t, members)
}

// TestProjectMemberRepo_ListByWorkspaceAndUser_QueryErrorPropagates proves the
// query failure is returned, not swallowed — mirrorProjectMemberships relies
// on getting a real error back so it can log and skip mirroring rather than
// silently proceeding with an incomplete membership list.
func TestProjectMemberRepo_ListByWorkspaceAndUser_QueryErrorPropagates(t *testing.T) {
	db := pmFKTestDB(t)
	require.NoError(t, db.Close())

	_, err := NewProjectMemberRepo(db).ListByWorkspaceAndUser(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err)
}
