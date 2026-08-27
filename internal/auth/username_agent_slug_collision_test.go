package auth

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// mockAgentRepo is a minimal repository.AgentRepository stub — only GetBySlug
// does real work, everything else exists solely to satisfy the interface.
type mockAgentRepo struct {
	agents []*domain.Agent
}

func (m *mockAgentRepo) GetBySlug(_ context.Context, workspaceID uuid.UUID, slug string) (*domain.Agent, error) {
	for _, a := range m.agents {
		if a.WorkspaceID == workspaceID && a.Slug == slug {
			return a, nil
		}
	}
	return nil, nil
}

func (m *mockAgentRepo) Create(context.Context, *domain.Agent) error { return nil }
func (m *mockAgentRepo) GetByID(context.Context, uuid.UUID) (*domain.Agent, error) {
	return nil, nil
}
func (m *mockAgentRepo) GetByAPIKeyPrefix(context.Context, uuid.UUID, string) (*domain.Agent, error) {
	return nil, nil
}
func (m *mockAgentRepo) SetAPIKeySHA256(context.Context, uuid.UUID, string, string) error { return nil }
func (m *mockAgentRepo) Update(context.Context, *domain.Agent) error                      { return nil }
func (m *mockAgentRepo) Delete(context.Context, uuid.UUID) error                          { return nil }
func (m *mockAgentRepo) List(context.Context, uuid.UUID, repository.AgentFilter, pagination.Params) (*pagination.Page[domain.Agent], error) {
	return nil, nil
}
func (m *mockAgentRepo) UpdateHeartbeat(context.Context, uuid.UUID, *repository.UpdateHeartbeatParams) error {
	return nil
}
func (m *mockAgentRepo) UpdateStatus(context.Context, uuid.UUID, domain.AgentStatus) error {
	return nil
}
func (m *mockAgentRepo) GetSubAgentTree(context.Context, uuid.UUID) ([]domain.Agent, error) {
	return nil, nil
}
func (m *mockAgentRepo) ListWithProjects(context.Context, uuid.UUID) ([]repository.AgentWithProjects, error) {
	return nil, nil
}
func (m *mockAgentRepo) TouchLastSeenBatch(context.Context, []uuid.UUID) error { return nil }
func (m *mockAgentRepo) SearchByPrefix(context.Context, uuid.UUID, string, int) ([]domain.Agent, error) {
	return nil, nil
}

// seedCollisionFixture builds a service whose test user owns one workspace
// (mockWorkspaceRepo.ListForUser == ListByOwner, so ownership is what makes a
// workspace visible to the guard here) containing one agent with slug "hugh".
func seedCollisionFixture(t *testing.T) (svc *Service, userRepo *mockUserRepo, agentRepo *mockAgentRepo, userID, workspaceID uuid.UUID) {
	t.Helper()
	userRepo = newMockUserRepo()
	refreshRepo := newMockRefreshTokenRepo()
	wsRepo := newMockWorkspaceRepo()
	wsMemberRepo := newMockWorkspaceMemberRepo()
	agentRepo = &mockAgentRepo{}

	userID = uuid.New()
	require.NoError(t, userRepo.Create(context.Background(), &domain.User{
		ID: userID, Email: "pat@example.com", Name: "Pat", Username: "pat", IsActive: true,
	}))

	workspaceID = uuid.New()
	require.NoError(t, wsRepo.Create(context.Background(), &domain.Workspace{
		ID: workspaceID, Name: "Acme", Slug: "acme", OwnerID: userID,
	}))

	agentRepo.agents = append(agentRepo.agents, &domain.Agent{
		ID: uuid.New(), WorkspaceID: workspaceID, Name: "Hugh", Slug: "hugh",
	})

	svc = NewService(userRepo, refreshRepo, wsRepo, wsMemberRepo, testJWTSecret, WithAgentRepo(agentRepo))
	return svc, userRepo, agentRepo, userID, workspaceID
}

// TestUpdateProfile_RejectsUsernameMatchingAgentSlugInOwnWorkspace is the RED
// case from task fee35355: an agent "hugh" already exists in a workspace this
// user belongs to. Renaming to "hugh" must be refused, naming who already
// holds the handle — not a generic validation failure the caller has to guess
// at (the task's own AC1).
func TestUpdateProfile_RejectsUsernameMatchingAgentSlugInOwnWorkspace(t *testing.T) {
	svc, userRepo, _, userID, _ := seedCollisionFixture(t)

	_, err := svc.UpdateProfile(context.Background(), userID, "Pat Renamed", "hugh", "")
	require.Error(t, err)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 409, apiErr.StatusCode())
	assert.Contains(t, err.Error(), "hugh", "the refusal must name the agent that already holds the handle")
	assert.Contains(t, err.Error(), "Acme", "the refusal must name the workspace, so the caller isn't left guessing where the conflict is")

	// The username must not have changed.
	stored, getErr := userRepo.GetByID(context.Background(), userID)
	require.NoError(t, getErr)
	assert.Equal(t, "pat", stored.Username, "a rejected rename must not partially apply")
}

// TestUpdateProfile_FreeUsernameStillWorks is the negative control: a handle
// with no collision at all must keep working exactly as before this fix.
func TestUpdateProfile_FreeUsernameStillWorks(t *testing.T) {
	svc, userRepo, _, userID, _ := seedCollisionFixture(t)

	updated, err := svc.UpdateProfile(context.Background(), userID, "Pat Renamed", "totally-free-handle", "")
	require.NoError(t, err)
	assert.Equal(t, "totally-free-handle", updated.Username)

	stored, getErr := userRepo.GetByID(context.Background(), userID)
	require.NoError(t, getErr)
	assert.Equal(t, "totally-free-handle", stored.Username)
}

// TestUpdateProfile_AgentSlugInUnrelatedWorkspaceIsNotACollision proves the
// guard is scoped to the user's OWN workspaces, per the task's explicit
// "область — воркспейс, а не глобально" requirement: an agent slug that exists
// only in a workspace this user has never joined can never actually collide
// with them, so it must not block the rename.
func TestUpdateProfile_AgentSlugInUnrelatedWorkspaceIsNotACollision(t *testing.T) {
	svc, userRepo, agentRepo, userID, _ := seedCollisionFixture(t)

	otherWorkspaceID := uuid.New()
	agentRepo.agents = append(agentRepo.agents, &domain.Agent{
		ID: uuid.New(), WorkspaceID: otherWorkspaceID, Name: "Ralph", Slug: "ralph",
	})

	updated, err := svc.UpdateProfile(context.Background(), userID, "Pat Renamed", "ralph", "")
	require.NoError(t, err, "an agent slug in a workspace this user never joined must not block the rename")
	assert.Equal(t, "ralph", updated.Username)

	stored, getErr := userRepo.GetByID(context.Background(), userID)
	require.NoError(t, getErr)
	assert.Equal(t, "ralph", stored.Username)
}

// TestUpdateProfile_NotChangingUsernameIgnoresExistingCollision is the
// regression guard named explicitly in the task's AC5: an EXISTING username↔
// slug collision (the "hugh"/"ralph" QA-lane accounts, deliberately never
// renamed — see the task description) must keep working for every OTHER
// profile edit. The guard only ever fires when username is being SET to
// something; leaving it blank (no change requested) must never trip it, even
// if the caller's CURRENT username already collides with an agent slug.
func TestUpdateProfile_NotChangingUsernameIgnoresExistingCollision(t *testing.T) {
	svc, userRepo, _, _, wsID := seedCollisionFixture(t)

	// This user's username IS "hugh" already (the pre-existing collision this
	// fix must never retroactively break) — added as a distinct user from the
	// fixture's "pat" to isolate the scenario.
	collidedUserID := uuid.New()
	require.NoError(t, userRepo.Create(context.Background(), &domain.User{
		ID: collidedUserID, Email: "hugh-human@example.com", Name: "Hugh Human", Username: "hugh", IsActive: true,
	}))
	// Make them a member (via ownership, per this mock's ListForUser) of the
	// same workspace as the colliding agent, so the guard is genuinely in play
	// for this user, not skipped for lack of any shared workspace.
	wsRepo := svc.workspaceRepo.(*mockWorkspaceRepo)
	require.NoError(t, wsRepo.Create(context.Background(), &domain.Workspace{
		ID: uuid.New(), Name: "Second Workspace", Slug: "second", OwnerID: collidedUserID,
	}))
	_ = wsID

	updated, err := svc.UpdateProfile(context.Background(), collidedUserID, "Hugh Human Renamed", "", "")
	require.NoError(t, err, "editing name only (no username change) must not be blocked by a pre-existing username↔slug collision")
	assert.Equal(t, "Hugh Human Renamed", updated.Name)
	assert.Equal(t, "hugh", updated.Username, "username must be untouched when the caller didn't ask to change it")
}

// TestCheckUsername_ReturnsFalseForAgentSlugCollision / _ReturnsTrueForFreeHandle
// cover the availability-probe endpoint the frontend calls before submitting a
// rename — it must agree with what UpdateProfile will actually accept,
// otherwise a user sees "available" and then gets rejected on submit.
func TestCheckUsername_ReturnsFalseForAgentSlugCollision(t *testing.T) {
	svc, _, _, userID, _ := seedCollisionFixture(t)

	available, err := svc.CheckUsername(context.Background(), userID, "hugh")
	require.NoError(t, err)
	assert.False(t, available)
}

func TestCheckUsername_ReturnsTrueForFreeHandle(t *testing.T) {
	svc, _, _, userID, _ := seedCollisionFixture(t)

	available, err := svc.CheckUsername(context.Background(), userID, "nobody-has-this")
	require.NoError(t, err)
	assert.True(t, available)
}
