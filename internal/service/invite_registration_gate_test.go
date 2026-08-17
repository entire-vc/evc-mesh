package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// This file proves — with a real run through inviteService.AcceptInvite, not
// just code reading — that a closed MESH_ALLOW_REGISTRATION policy does NOT
// block the invite-acceptance path. AcceptInvite creates the user directly
// via userRepo.Create and never calls auth.Service.Register/RegistrationOpen,
// so the two paths are structurally decoupled; this test pins that fact down
// so a future refactor that accidentally routes AcceptInvite through
// Register would fail loudly instead of silently locking operators out of
// their own invite flow.

// minimalRefreshTokenRepo is a no-op RefreshTokenRepository — only Create is
// exercised (by auth.Service.Login's token issuance at the end of
// AcceptInvite); the rest are never called on this path.
type minimalRefreshTokenRepo struct{}

func (minimalRefreshTokenRepo) Create(_ context.Context, _ uuid.UUID, _ string, _ time.Time) error {
	return nil
}
func (minimalRefreshTokenRepo) GetByHash(_ context.Context, _ string) (*repository.RefreshToken, error) {
	return nil, nil
}
func (minimalRefreshTokenRepo) RevokeByUserID(_ context.Context, _ uuid.UUID) error { return nil }
func (minimalRefreshTokenRepo) RevokeByHash(_ context.Context, _ string) (bool, error) {
	return true, nil
}
func (minimalRefreshTokenRepo) DeleteExpired(_ context.Context) error { return nil }

// minimalInviteRepo backs one pending invite, looked up by token.
type minimalInviteRepo struct {
	mu     sync.Mutex
	invite *domain.WorkspaceInvite
}

func (r *minimalInviteRepo) Create(_ context.Context, invite *domain.WorkspaceInvite) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invite = invite
	return nil
}
func (r *minimalInviteRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.WorkspaceInvite, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.invite != nil && r.invite.ID == id {
		return r.invite, nil
	}
	return nil, nil
}
func (r *minimalInviteRepo) GetByToken(_ context.Context, token string) (*domain.WorkspaceInvite, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.invite != nil && r.invite.Token == token {
		return r.invite, nil
	}
	return nil, nil
}
func (r *minimalInviteRepo) ListByWorkspace(_ context.Context, _ uuid.UUID) ([]domain.WorkspaceInvite, error) {
	return nil, nil
}
func (r *minimalInviteRepo) Accept(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.invite != nil && r.invite.ID == id {
		now := time.Now()
		r.invite.AcceptedAt = &now
	}
	return nil
}
func (r *minimalInviteRepo) Delete(_ context.Context, _ uuid.UUID) error { return nil }
func (r *minimalInviteRepo) DeleteExpired(_ context.Context) (int64, error) {
	return 0, nil
}

// minimalWorkspaceMemberRepo tracks memberships in-memory; only the two
// methods AcceptInvite calls (GetByWorkspaceAndUser, Create) do real work.
type minimalWorkspaceMemberRepo struct {
	mu      sync.Mutex
	members []*domain.WorkspaceMember
}

func (r *minimalWorkspaceMemberRepo) Create(_ context.Context, member *domain.WorkspaceMember) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.members = append(r.members, member)
	return nil
}
func (r *minimalWorkspaceMemberRepo) GetByWorkspaceAndUser(_ context.Context, workspaceID, userID uuid.UUID) (*domain.WorkspaceMember, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.members {
		if m.WorkspaceID == workspaceID && m.UserID == userID {
			return m, nil
		}
	}
	return nil, nil
}
func (r *minimalWorkspaceMemberRepo) GetRole(_ context.Context, _, _ uuid.UUID) (string, error) {
	return "", nil
}
func (r *minimalWorkspaceMemberRepo) List(_ context.Context, _ uuid.UUID) ([]domain.WorkspaceMemberWithUser, error) {
	return nil, nil
}
func (r *minimalWorkspaceMemberRepo) ListWithProjects(_ context.Context, _ uuid.UUID) ([]repository.HumanWithProjects, error) {
	return nil, nil
}
func (r *minimalWorkspaceMemberRepo) UpdateRole(_ context.Context, _, _ uuid.UUID, _ string) error {
	return nil
}
func (r *minimalWorkspaceMemberRepo) Delete(_ context.Context, _, _ uuid.UUID) error { return nil }
func (r *minimalWorkspaceMemberRepo) CountOwners(_ context.Context, _ uuid.UUID) (int, error) {
	return 1, nil
}

func TestAcceptInvite_SucceedsWhenRegistrationIsClosed(t *testing.T) {
	userRepo := NewMockUserRepository()
	// Seed one existing user so RegistrationOpen (if AcceptInvite wrongly
	// routed through it) would actually be closed — proving this isn't
	// passing by accident of the zero-users bootstrap exception.
	existingAdmin := &domain.User{ID: uuid.New(), Email: "admin@example.com", Username: "admin", IsActive: true}
	userRepo.AddUser(uuid.New(), existingAdmin)

	authSvc := auth.NewService(
		userRepo,
		minimalRefreshTokenRepo{},
		nil, // workspaceRepo: unused by Login, the only auth.Service method AcceptInvite calls
		nil, // workspaceMemberRepo: unused by Login
		testAuthJWTSecret,
		auth.WithAllowRegistration(false), // registration CLOSED
	)

	// Sanity: confirm the instance really is closed before proving invites
	// route around it — otherwise this test would prove nothing.
	open, err := authSvc.RegistrationOpen(context.Background())
	require.NoError(t, err)
	require.False(t, open, "test setup must start with registration closed")
	_, _, regErr := authSvc.Register(context.Background(), "walkup@example.com", "StrongP4ss", "Walkup")
	require.ErrorIs(t, regErr, auth.ErrRegistrationClosed, "direct self-registration must be blocked in this setup")

	inviteRepo := &minimalInviteRepo{}
	memberRepo := &minimalWorkspaceMemberRepo{}
	invites := NewInviteService(inviteRepo, userRepo, memberRepo, nil /* workspaceRepo: unused by AcceptInvite */, nil /* emailSvc: unused by AcceptInvite */, authSvc, "https://mesh.example.com")

	wsID := uuid.New()
	invite := &domain.WorkspaceInvite{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		Email:       "invited@example.com",
		Role:        domain.RoleMember,
		Token:       "test-token-abc123",
		ExpiresAt:   time.Now().Add(24 * time.Hour),
		CreatedAt:   time.Now(),
	}
	require.NoError(t, inviteRepo.Create(context.Background(), invite))

	accessToken, refreshToken, err := invites.AcceptInvite(context.Background(), AcceptInviteInput{
		Token:    "test-token-abc123",
		Name:     "Invited User",
		Password: "StrongP4ss",
	})

	require.NoError(t, err, "accepting an invite must succeed even when self-registration is closed")
	assert.NotEmpty(t, accessToken)
	assert.NotEmpty(t, refreshToken)

	created, err := userRepo.GetByEmail(context.Background(), "invited@example.com")
	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Equal(t, "Invited User", created.Name)

	member, err := memberRepo.GetByWorkspaceAndUser(context.Background(), wsID, created.ID)
	require.NoError(t, err)
	require.NotNil(t, member)
	assert.Equal(t, domain.RoleMember, member.Role)
}

const testAuthJWTSecret = "test-secret-key-for-jwt-signing-32b"
