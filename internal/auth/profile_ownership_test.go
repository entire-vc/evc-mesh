package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func seedProfileUser(t *testing.T) (*Service, *mockUserRepo, uuid.UUID) {
	t.Helper()
	svc, userRepo, _, _, _ := newTestService()
	id := uuid.New()
	require.NoError(t, userRepo.Create(context.Background(), &domain.User{
		ID: id, Email: "sid@webs-company.ru", Name: "sid@webs-company.ru",
		Username: "sid", IsActive: true,
	}))
	return svc, userRepo, id
}

// Editing your own name is what makes the admin-side edit safe to allow at all:
// it is the act that transfers ownership of the field to the person it
// describes. Without this flag being written, WorkspaceMemberService could
// never tell an unowned name from a chosen one.
func TestUpdateProfile_MarksTheNameAsSelfChosen(t *testing.T) {
	svc, userRepo, id := seedProfileUser(t)

	before, err := userRepo.GetByID(context.Background(), id)
	require.NoError(t, err)
	require.False(t, before.DisplayNameSelfSet)
	require.True(t, before.NameIsPlaceholder())

	updated, err := svc.UpdateProfile(context.Background(), id, "Sid Vicious", "", "")
	require.NoError(t, err)
	assert.Equal(t, "Sid Vicious", updated.Name)
	assert.True(t, updated.DisplayNameSelfSet)

	stored, err := userRepo.GetByID(context.Background(), id)
	require.NoError(t, err)
	assert.True(t, stored.DisplayNameSelfSet, "the flag has to be persisted, not just returned")
	assert.False(t, stored.NameIsPlaceholder())
}

// Saving only a name must not disturb the username. The Postgres column is NOT
// NULL, so a blank one reaching the UPDATE is not a cosmetic slip — it aborts
// the statement and the whole edit fails.
func TestUpdateProfile_LeavesUsernameAloneWhenNotSupplied(t *testing.T) {
	svc, userRepo, id := seedProfileUser(t)

	updated, err := svc.UpdateProfile(context.Background(), id, "Sid Vicious", "", "")
	require.NoError(t, err)
	assert.Equal(t, "sid", updated.Username, "an omitted username means 'unchanged', never 'clear it'")

	stored, err := userRepo.GetByID(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, "sid", stored.Username)
}

func TestUpdateProfile_RejectsABlankOrOverlongName(t *testing.T) {
	svc, _, id := seedProfileUser(t)

	for _, bad := range []string{"", "   ", strings.Repeat("x", 101)} {
		_, err := svc.UpdateProfile(context.Background(), id, bad, "", "")
		require.Error(t, err, "name %q must be rejected", bad)
	}
}
