package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
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

// A valid language is stored and marked as chosen, so the client stops
// suggesting one from the browser settings; a fresh read (another device, new
// session) sees the same value.
func TestSetLocale_PersistsAndMarksExplicit(t *testing.T) {
	svc, userRepo, id := seedProfileUser(t)

	updated, err := svc.SetLocale(context.Background(), id, "ru")
	require.NoError(t, err)
	assert.Equal(t, "ru", updated.Locale)
	assert.True(t, updated.LocaleSetExplicitly)

	stored, err := userRepo.GetByID(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, "ru", stored.Locale)
	assert.True(t, stored.LocaleSetExplicitly)
}

// An unsupported code is a 400 and must leave the stored language untouched.
func TestSetLocale_RejectsUnsupported(t *testing.T) {
	svc, userRepo, id := seedProfileUser(t)

	for _, bad := range []string{"", "de", "RU", "ru-RU"} {
		_, err := svc.SetLocale(context.Background(), id, bad)
		require.Error(t, err, "locale %q", bad)
		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusBadRequest, apiErr.Code)
	}

	stored, err := userRepo.GetByID(context.Background(), id)
	require.NoError(t, err)
	assert.NotEqual(t, "de", stored.Locale)
	assert.False(t, stored.LocaleSetExplicitly)
}

type failingUpdateRepo struct{ *mockUserRepo }

func (failingUpdateRepo) Update(context.Context, *domain.User) error { return assert.AnError }

func TestSetLocale_RepoFailuresSurface(t *testing.T) {
	svc, userRepo, id := seedProfileUser(t)

	_, err := svc.SetLocale(context.Background(), uuid.New(), "ru")
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.Code, "an unknown user is a 404, not a silent no-op")

	userRepo.getByIDErr = assert.AnError
	_, err = svc.SetLocale(context.Background(), id, "ru")
	require.Error(t, err, "a failing read must not look like success")
	userRepo.getByIDErr = nil

	failing := NewService(failingUpdateRepo{userRepo}, newMockRefreshTokenRepo(), newMockWorkspaceRepo(), newMockWorkspaceMemberRepo(), testJWTSecret)
	_, err = failing.SetLocale(context.Background(), id, "ru")
	require.ErrorIs(t, err, assert.AnError, "a failing write must not look like success")
}
