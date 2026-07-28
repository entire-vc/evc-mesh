package service

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/domain"
)

// usernameConstraintRe is the CHECK constraint from migration
// 20260520046_add_users_username, verbatim. users.username is also NOT NULL —
// an empty string fails both, which is exactly how accepting an invite as a
// brand-new user used to blow up: AcceptInvite built a domain.User with no
// Username at all, and Postgres rejected the INSERT.
var usernameConstraintRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$`)

// newInviteTestFixture wires an inviteService over the in-memory mocks with a
// single pending invite for the given email, and returns the pieces a test
// needs to assert on.
func newInviteTestFixture(t *testing.T, inviteEmail string) (WorkspaceInviteService, *MockUserRepository, uuid.UUID) {
	t.Helper()

	userRepo := NewMockUserRepository()
	authSvc := auth.NewService(userRepo, minimalRefreshTokenRepo{}, nil, nil, testAuthJWTSecret)

	inviteRepo := &minimalInviteRepo{}
	memberRepo := &minimalWorkspaceMemberRepo{}
	invites := NewInviteService(inviteRepo, userRepo, memberRepo, nil, nil, authSvc, "https://mesh.example.com")

	wsID := uuid.New()
	require.NoError(t, inviteRepo.Create(context.Background(), &domain.WorkspaceInvite{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		Email:       inviteEmail,
		Role:        domain.RoleMember,
		Token:       "invite-token-for-test",
		ExpiresAt:   time.Now().Add(24 * time.Hour),
		CreatedAt:   time.Now(),
	}))

	return invites, userRepo, wsID
}

func TestAcceptInvite_DerivesUsernameForNewUser(t *testing.T) {
	invites, userRepo, _ := newInviteTestFixture(t, "frank@example.com")

	access, refresh, err := invites.AcceptInvite(context.Background(), AcceptInviteInput{
		Token:    "invite-token-for-test",
		Name:     "Frank",
		Password: "StrongP4ss",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, access)
	assert.NotEmpty(t, refresh)

	created, err := userRepo.GetByEmail(context.Background(), "frank@example.com")
	require.NoError(t, err)
	require.NotNil(t, created)

	require.NotEmpty(t, created.Username, "a new invited user must get a username, not the empty string")
	assert.Regexp(t, usernameConstraintRe, created.Username,
		"username must satisfy chk_users_username or the INSERT is rejected by Postgres")
	assert.Equal(t, "frank", created.Username,
		"derivation must match self-registration: the email local-part, slugified")
}

// TestAcceptInvite_UsernameMatchesRegistration pins the two paths together. If
// someone reintroduces a second derivation implementation and it drifts, this
// fails.
func TestAcceptInvite_UsernameMatchesRegistration(t *testing.T) {
	for _, email := range []string{
		"frank@example.com",
		"first.last@example.com",
		"weird+tag_name@example.com",
		"x@example.com", // shorter than the 2-char minimum before padding
		"a-very-long-local-part-that-exceeds-the-thirty-eight-char-clamp@example.com",
	} {
		t.Run(email, func(t *testing.T) {
			invites, userRepo, _ := newInviteTestFixture(t, email)

			_, _, err := invites.AcceptInvite(context.Background(), AcceptInviteInput{
				Token:    "invite-token-for-test",
				Name:     "Invited",
				Password: "StrongP4ss",
			})
			require.NoError(t, err)

			created, err := userRepo.GetByEmail(context.Background(), auth.NormalizeEmail(email))
			require.NoError(t, err)
			require.NotNil(t, created)
			assert.Regexp(t, usernameConstraintRe, created.Username)

			// Same input through the registration path must produce the same
			// username — one implementation, one result.
			regRepo := NewMockUserRepository()
			regAuth := auth.NewService(regRepo, minimalRefreshTokenRepo{}, nil, nil, testAuthJWTSecret)
			want, deriveErr := regAuth.DeriveUsername(context.Background(), email)
			require.NoError(t, deriveErr)
			assert.Equal(t, want, created.Username)
		})
	}
}

func TestAcceptInvite_NormalizesInviteEmail(t *testing.T) {
	// An invite addressed with mixed case must land on the canonical address,
	// otherwise the invitee creates an account they cannot log into.
	invites, userRepo, _ := newInviteTestFixture(t, "  Frank@Example.COM  ")

	_, _, err := invites.AcceptInvite(context.Background(), AcceptInviteInput{
		Token:    "invite-token-for-test",
		Name:     "Frank",
		Password: "StrongP4ss",
	})
	require.NoError(t, err)

	created, err := userRepo.GetByEmail(context.Background(), "frank@example.com")
	require.NoError(t, err)
	require.NotNil(t, created, "the account must be stored under the canonical address")
	assert.Equal(t, "frank@example.com", created.Email)
	assert.Equal(t, "frank", created.Username)
}

// noopEmailService swallows the invite email; CreateInvite ignores its error
// anyway, this just keeps the call from panicking on a nil interface.
type noopEmailService struct{}

func (noopEmailService) SendInvite(_ context.Context, _, _, _ string) error { return nil }

func TestCreateInvite_NormalizesEmail(t *testing.T) {
	userRepo := NewMockUserRepository()
	authSvc := auth.NewService(userRepo, minimalRefreshTokenRepo{}, nil, nil, testAuthJWTSecret)
	inviteRepo := &minimalInviteRepo{}

	wsRepo := NewMockWorkspaceRepository()
	wsID := uuid.New()
	require.NoError(t, wsRepo.Create(context.Background(), &domain.Workspace{
		ID: wsID, Name: "Test WS", Slug: "test-ws", OwnerID: uuid.New(),
	}))

	invites := NewInviteService(inviteRepo, userRepo, &minimalWorkspaceMemberRepo{}, wsRepo, noopEmailService{}, authSvc, "https://mesh.example.com")

	invite, err := invites.CreateInvite(context.Background(), CreateInviteInput{
		WorkspaceID: wsID,
		Email:       "  Frank@Example.COM ",
		Role:        domain.RoleMember,
	})
	require.NoError(t, err)
	assert.Equal(t, "frank@example.com", invite.Email)

	// A whitespace-only address is not an address.
	_, err = invites.CreateInvite(context.Background(), CreateInviteInput{
		WorkspaceID: wsID,
		Email:       "   ",
		Role:        domain.RoleMember,
	})
	require.Error(t, err)
}
