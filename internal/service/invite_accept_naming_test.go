package service

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func mustHash(t *testing.T, password string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	return string(h)
}

// A name given at acceptance is the person's own — they typed it about
// themselves — so it locks immediately and no workspace admin can rewrite it.
func TestAcceptInvite_NameGivenAtAcceptanceIsTheirOwn(t *testing.T) {
	invites, userRepo, _ := newInviteTestFixture(t, "frank@example.com")

	_, _, err := invites.AcceptInvite(context.Background(), AcceptInviteInput{
		Token:    "invite-token-for-test",
		Name:     "  Frank Booth  ",
		Password: "StrongP4ss",
	})
	require.NoError(t, err)

	created, err := userRepo.GetByEmail(context.Background(), "frank@example.com")
	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Equal(t, "Frank Booth", created.Name, "trimmed, and the address is not what gets stored")
	assert.True(t, created.DisplayNameSelfSet)
	assert.False(t, created.NameIsPlaceholder())
}

// The address fallback survives for API clients that omit the field, but the
// resulting account must be marked as un-named so it can still be corrected —
// this is the population that filled an entire instance with addresses where
// names belong.
func TestAcceptInvite_OmittedNameLeavesAnUnownedPlaceholder(t *testing.T) {
	invites, userRepo, _ := newInviteTestFixture(t, "frank@example.com")

	_, _, err := invites.AcceptInvite(context.Background(), AcceptInviteInput{
		Token:    "invite-token-for-test",
		Password: "StrongP4ss",
	})
	require.NoError(t, err)

	created, err := userRepo.GetByEmail(context.Background(), "frank@example.com")
	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Equal(t, "frank@example.com", created.Name)
	assert.True(t, created.NameIsPlaceholder())
	assert.False(t, created.DisplayNameSelfSet,
		"nobody chose this, so an admin must still be able to fill it in")
}

func TestAcceptInvite_RejectsAnOverlongName(t *testing.T) {
	invites, _, _ := newInviteTestFixture(t, "frank@example.com")

	_, _, err := invites.AcceptInvite(context.Background(), AcceptInviteInput{
		Token:    "invite-token-for-test",
		Name:     strings.Repeat("x", 101),
		Password: "StrongP4ss",
	})
	require.Error(t, err)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
}

// The second half of "one account, several workspaces": an invite sent to an
// address that already has an account must attach that account, not fork it.
func TestAcceptInvite_ExistingAccountJoinsWithoutDuplicating(t *testing.T) {
	invites, userRepo, _ := newInviteTestFixture(t, "frank@example.com")

	existing := &domain.User{
		ID: uuid.New(), Email: "frank@example.com", Name: "Frank Booth",
		Username: "frank", IsActive: true, DisplayNameSelfSet: true,
	}
	// The password has to match: AcceptInvite finishes by logging the person in.
	existing.PasswordHash = mustHash(t, "StrongP4ss")
	userRepo.AddUser(uuid.New(), existing)

	access, _, err := invites.AcceptInvite(context.Background(), AcceptInviteInput{
		Token:    "invite-token-for-test",
		Name:     "Renamed By The Invite",
		Password: "StrongP4ss",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, access)

	count, err := userRepo.Count(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, count, "the invite joined the existing account, it did not create a second")

	stored, err := userRepo.GetByID(context.Background(), existing.ID)
	require.NoError(t, err)
	assert.Equal(t, "Frank Booth", stored.Name,
		"accepting an invite must not let the form overwrite a name its owner already chose")
}
