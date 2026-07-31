package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func newIdentityFixture() (WorkspaceMemberService, *MockUserRepository, *minimalWorkspaceMemberRepo) {
	userRepo := NewMockUserRepository()
	memberRepo := &minimalWorkspaceMemberRepo{}
	svc := NewWorkspaceMemberService(memberRepo, userRepo, NewMockProjectMemberRepository(), nil)
	return svc, userRepo, memberRepo
}

// ---------------------------------------------------------------------------
// One account, several workspaces
// ---------------------------------------------------------------------------

// The whole point of the feature: a person who is already on the instance joins
// a second workspace as the SAME account. A second account with the same
// address would split their tasks, mentions and history in two, and — since
// email is uniquely indexed — could not even be created without the first one
// being unreachable.
func TestAddMember_SameAccountJoinsSecondWorkspace(t *testing.T) {
	svc, userRepo, _ := newIdentityFixture()
	ctx := context.Background()

	maombi, prototypes := uuid.New(), uuid.New()

	// Provisioned into the first workspace, account created here.
	first, err := svc.AddMemberWithCreate(ctx, maombi, "sid@webs-company.ru", "Sid Vicious", domain.RoleMember, "StrongP4ss", uuid.Nil)
	require.NoError(t, err)

	// Added to the second by address, no password: the existing account is found.
	second, err := svc.AddMember(ctx, prototypes, "sid@webs-company.ru", domain.RoleAdmin, uuid.Nil)
	require.NoError(t, err)

	assert.Equal(t, first.User.ID, second.User.ID,
		"the second workspace must attach the SAME account, not create a parallel one")
	assert.NotEqual(t, first.ID, second.ID,
		"two memberships, one user")
	assert.Equal(t, maombi, first.WorkspaceID)
	assert.Equal(t, prototypes, second.WorkspaceID)
	assert.Equal(t, domain.RoleMember, first.Role)
	assert.Equal(t, domain.RoleAdmin, second.Role,
		"role is per membership — the same person can be a member here and an admin there")

	count, err := userRepo.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "exactly one account exists on the instance")
}

// Normalization is what makes the "same account" guarantee hold for a human
// typing the address by hand. "Sid@Webs-Company.RU " must not open a second
// identity.
func TestAddMember_SecondWorkspaceMatchesRegardlessOfSpelling(t *testing.T) {
	svc, userRepo, _ := newIdentityFixture()
	ctx := context.Background()

	created, err := svc.AddMemberWithCreate(ctx, uuid.New(), "sid@webs-company.ru", "Sid", domain.RoleMember, "StrongP4ss", uuid.Nil)
	require.NoError(t, err)

	for _, spelling := range []string{
		"Sid@Webs-Company.RU",
		"  SID@WEBS-COMPANY.RU  ",
		"sid@webs-company.ru",
	} {
		joined, addErr := svc.AddMember(ctx, uuid.New(), spelling, domain.RoleMember, uuid.Nil)
		require.NoError(t, addErr, "spelling %q must resolve to the existing account", spelling)
		assert.Equal(t, created.User.ID, joined.User.ID, "spelling %q", spelling)
	}

	count, err := userRepo.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "no spelling variant may fork the identity")
}

// Even with a password supplied, an address that already has an account must
// attach that account rather than try to create a rival one.
func TestAddMemberWithCreate_DoesNotDuplicateAnExistingAccount(t *testing.T) {
	svc, userRepo, _ := newIdentityFixture()
	ctx := context.Background()

	existing := &domain.User{
		ID: uuid.New(), Email: "kmv@yandex.ru", Name: "Konstantin",
		Username: "kmv", IsActive: true, DisplayNameSelfSet: true,
	}
	userRepo.AddUser(uuid.New(), existing)

	member, err := svc.AddMemberWithCreate(ctx, uuid.New(), "kmv@yandex.ru", "Someone Else", domain.RoleMember, "AnotherP4ss", uuid.Nil)
	require.NoError(t, err)
	assert.Equal(t, existing.ID, member.User.ID)
	assert.Equal(t, "Konstantin", member.User.Name,
		"an existing account keeps its own name — the inviter does not get to rename it through the add form")

	count, err := userRepo.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

// Adding the same person to the same workspace twice is a conflict, not a
// second membership row.
func TestAddMember_RejectsDuplicateMembership(t *testing.T) {
	svc, userRepo, _ := newIdentityFixture()
	ctx := context.Background()
	ws := uuid.New()

	userRepo.AddUser(ws, &domain.User{
		ID: uuid.New(), Email: "dup@example.com", Name: "Dup", Username: "dup", IsActive: true,
	})

	_, err := svc.AddMember(ctx, ws, "dup@example.com", domain.RoleMember, uuid.Nil)
	require.NoError(t, err)

	_, err = svc.AddMember(ctx, ws, "dup@example.com", domain.RoleMember, uuid.Nil)
	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusConflict, apiErr.StatusCode())
}

// An unknown address is a fork in the flow (invite, or provision with a
// password), and the error has to say so — "User not found" alone is what left
// the operator with no next step.
func TestAddMember_UnknownAddressExplainsTheAlternatives(t *testing.T) {
	svc, _, _ := newIdentityFixture()

	_, err := svc.AddMember(context.Background(), uuid.New(), "nobody@example.com", domain.RoleMember, uuid.Nil)
	require.Error(t, err)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.StatusCode())
	assert.Contains(t, strings.ToLower(apiErr.Details), "invite")
	assert.Contains(t, strings.ToLower(apiErr.Details), "password")
}

// ---------------------------------------------------------------------------
// Names
// ---------------------------------------------------------------------------

func TestAddMemberWithCreate_StoresTheGivenName(t *testing.T) {
	svc, userRepo, _ := newIdentityFixture()
	ctx := context.Background()

	member, err := svc.AddMemberWithCreate(ctx, uuid.New(), "jane@example.com", "  Jane Cooper  ", domain.RoleMember, "StrongP4ss", uuid.Nil)
	require.NoError(t, err)
	assert.Equal(t, "Jane Cooper", member.User.Name, "the name is trimmed and stored, not the address")
	assert.False(t, member.User.NameIsPlaceholder())

	stored, err := userRepo.GetByEmail(ctx, "jane@example.com")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "Jane Cooper", stored.Name)
	assert.False(t, stored.DisplayNameSelfSet,
		"a name typed by whoever provisioned the account is not the account holder's own choice yet")
}

// The address fallback still exists for a client that omits the field, but it
// must be recognisable as "no name" rather than pass for one.
func TestAddMemberWithCreate_FallsBackToAddressAndMarksItUnowned(t *testing.T) {
	svc, userRepo, _ := newIdentityFixture()
	ctx := context.Background()

	member, err := svc.AddMemberWithCreate(ctx, uuid.New(), "noname@example.com", "", domain.RoleMember, "StrongP4ss", uuid.Nil)
	require.NoError(t, err)
	assert.Equal(t, "noname@example.com", member.User.Name)
	assert.True(t, member.User.NameIsPlaceholder(),
		"the UI has to be able to tell this apart from a real name")

	stored, err := userRepo.GetByEmail(ctx, "noname@example.com")
	require.NoError(t, err)
	assert.False(t, stored.DisplayNameSelfSet)
}

func TestAddMemberWithCreate_RejectsAnOverlongName(t *testing.T) {
	svc, _, _ := newIdentityFixture()

	_, err := svc.AddMemberWithCreate(context.Background(), uuid.New(), "long@example.com",
		strings.Repeat("x", 101), domain.RoleMember, "StrongP4ss", uuid.Nil)
	require.Error(t, err)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode())
}

// The member and search payloads carry username so the UI can disambiguate two
// people with the same display name without printing an address.
func TestAddMember_ResponseCarriesUsername(t *testing.T) {
	svc, userRepo, _ := newIdentityFixture()
	ws := uuid.New()

	userRepo.AddUser(ws, &domain.User{
		ID: uuid.New(), Email: "alex@example.com", Name: "Alex",
		Username: "alex-p", IsActive: true,
	})

	member, err := svc.AddMember(context.Background(), ws, "alex@example.com", domain.RoleMember, uuid.Nil)
	require.NoError(t, err)
	assert.Equal(t, "alex-p", member.User.Username)
}

func TestIsPlaceholderName(t *testing.T) {
	cases := []struct {
		name, email string
		want        bool
	}{
		{"Jane Cooper", "jane@example.com", false},
		{"jane@example.com", "jane@example.com", true},
		{"JANE@EXAMPLE.COM", "jane@example.com", true},
		{"  jane@example.com  ", "jane@example.com", true},
		{"", "jane@example.com", true},
		{"   ", "jane@example.com", true},
		{"Jane", "", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, domain.IsPlaceholderName(tc.name, tc.email),
			"IsPlaceholderName(%q, %q)", tc.name, tc.email)
	}
}

// ---------------------------------------------------------------------------
// Editing a member's name
// ---------------------------------------------------------------------------

func TestSetMemberDisplayName_FillsInAnUnownedName(t *testing.T) {
	svc, userRepo, _ := newIdentityFixture()
	ctx := context.Background()
	ws := uuid.New()

	// Exactly Pavel's population: provisioned, never named.
	member, err := svc.AddMemberWithCreate(ctx, ws, "mail-kmv21@yandex.ru", "", domain.RoleMember, "StrongP4ss", uuid.Nil)
	require.NoError(t, err)
	require.True(t, member.User.NameIsPlaceholder())

	require.NoError(t, svc.SetMemberDisplayName(ctx, ws, member.User.ID, "  Konstantin M.  "))

	stored, err := userRepo.GetByID(ctx, member.User.ID)
	require.NoError(t, err)
	assert.Equal(t, "Konstantin M.", stored.Name, "trimmed and written")
	assert.False(t, stored.DisplayNameSelfSet,
		"an admin filling the name in does not make it the account holder's own choice")

	reloaded, err := svc.GetMember(ctx, ws, member.User.ID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Equal(t, "Konstantin M.", reloaded.User.Name)
	assert.False(t, reloaded.User.NameIsPlaceholder())
}

// The cross-tenant edge of this feature. display_name is one row read by every
// workspace the person belongs to, so once they have chosen it, an admin of one
// workspace must not be able to change how they appear in the others.
func TestSetMemberDisplayName_RefusesANameItsOwnerChose(t *testing.T) {
	svc, userRepo, memberRepo := newIdentityFixture()
	ctx := context.Background()
	ws := uuid.New()

	self := &domain.User{
		ID: uuid.New(), Email: "pavel@venture-crew.com", Name: "Pavel",
		Username: "pavel", IsActive: true, DisplayNameSelfSet: true,
	}
	userRepo.AddUser(ws, self)
	require.NoError(t, memberRepo.Create(ctx, &domain.WorkspaceMember{
		ID: uuid.New(), WorkspaceID: ws, UserID: self.ID, Role: domain.RoleMember,
	}))

	err := svc.SetMemberDisplayName(ctx, ws, self.ID, "Not Pavel")
	require.Error(t, err)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusForbidden, apiErr.StatusCode())

	stored, err := userRepo.GetByID(ctx, self.ID)
	require.NoError(t, err)
	assert.Equal(t, "Pavel", stored.Name, "the refusal must leave the name untouched")
}

// The route is /workspaces/:ws_id/members/:user_id and the middleware has
// already proven the target is a member of that workspace. The service restates
// it so the rule survives being called from anywhere else: an admin cannot aim
// this at a user outside their own workspace.
func TestSetMemberDisplayName_RefusesANonMember(t *testing.T) {
	svc, userRepo, _ := newIdentityFixture()
	ctx := context.Background()

	outsider := &domain.User{
		ID: uuid.New(), Email: "outsider@other.example", Name: "outsider@other.example",
		Username: "outsider", IsActive: true,
	}
	userRepo.AddUser(uuid.New(), outsider)

	err := svc.SetMemberDisplayName(ctx, uuid.New(), outsider.ID, "Renamed By A Stranger")
	require.Error(t, err)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.StatusCode())

	stored, err := userRepo.GetByID(ctx, outsider.ID)
	require.NoError(t, err)
	assert.Equal(t, "outsider@other.example", stored.Name, "no write may reach a non-member")
}

func TestSetMemberDisplayName_ValidatesTheName(t *testing.T) {
	svc, userRepo, memberRepo := newIdentityFixture()
	ctx := context.Background()
	ws := uuid.New()

	u := &domain.User{ID: uuid.New(), Email: "v@example.com", Name: "v@example.com", Username: "v", IsActive: true}
	userRepo.AddUser(ws, u)
	require.NoError(t, memberRepo.Create(ctx, &domain.WorkspaceMember{
		ID: uuid.New(), WorkspaceID: ws, UserID: u.ID, Role: domain.RoleMember,
	}))

	for _, bad := range []string{"", "   ", strings.Repeat("x", 101)} {
		err := svc.SetMemberDisplayName(ctx, ws, u.ID, bad)
		require.Error(t, err, "name %q must be rejected", bad)

		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode())
	}
}

func TestGetMember_ReturnsNilForANonMember(t *testing.T) {
	svc, _, _ := newIdentityFixture()

	member, err := svc.GetMember(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	assert.Nil(t, member)
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

// A blank query must not reach the repository at all: an empty pattern is the
// one input that would match every row the caller is allowed to see.
func TestSearchUsers_BlankQueryReturnsNothing(t *testing.T) {
	svc, userRepo, _ := newIdentityFixture()
	userRepo.searchResults = []domain.User{{ID: uuid.New(), Email: "leak@example.com"}}

	for _, blank := range []string{"", "   ", "\t\n"} {
		got, err := svc.SearchUsers(context.Background(), uuid.New(), uuid.New(), blank)
		require.NoError(t, err)
		assert.Empty(t, got, "query %q must short-circuit", blank)
	}
}

// Results are annotated so the dialog can grey out people who are already in,
// and they carry the username for the same reason member rows do.
func TestSearchUsers_AnnotatesMembershipAndCarriesUsername(t *testing.T) {
	svc, userRepo, memberRepo := newIdentityFixture()
	ctx := context.Background()
	ws := uuid.New()

	inWorkspace := domain.User{ID: uuid.New(), Email: "in@example.com", Name: "In Already", Username: "in-already"}
	outside := domain.User{ID: uuid.New(), Email: "out@example.com", Name: "Not In Yet", Username: "not-in-yet"}
	userRepo.searchResults = []domain.User{inWorkspace, outside}

	require.NoError(t, memberRepo.Create(ctx, &domain.WorkspaceMember{
		ID: uuid.New(), WorkspaceID: ws, UserID: inWorkspace.ID, Role: domain.RoleMember,
	}))

	got, err := svc.SearchUsers(ctx, ws, uuid.New(), "example")
	require.NoError(t, err)
	require.Len(t, got, 2)

	assert.True(t, got[0].IsMember)
	assert.Equal(t, "in-already", got[0].Username)
	assert.False(t, got[1].IsMember, "the person not in this workspace is the one worth adding")
	assert.Equal(t, "not-in-yet", got[1].Username)
	assert.False(t, got[1].NameIsPlaceholder())
}

// A repository fault must surface as an error rather than as a silent success
// that leaves the operator believing a name was saved.
func TestSetMemberDisplayName_PropagatesRepositoryFailures(t *testing.T) {
	ctx := context.Background()
	ws, target := uuid.New(), uuid.New()

	t.Run("member lookup fails", func(t *testing.T) {
		userRepo := NewMockUserRepository()
		memberRepo := &failingMemberLookupRepo{err: errors.New("connection reset by peer")}
		svc := NewWorkspaceMemberService(memberRepo, userRepo, NewMockProjectMemberRepository(), nil)

		err := svc.SetMemberDisplayName(ctx, ws, target, "Whoever")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "connection reset by peer")
	})

	t.Run("the membership exists but the account does not", func(t *testing.T) {
		userRepo := NewMockUserRepository()
		memberRepo := &minimalWorkspaceMemberRepo{}
		require.NoError(t, memberRepo.Create(ctx, &domain.WorkspaceMember{
			ID: uuid.New(), WorkspaceID: ws, UserID: target, Role: domain.RoleMember,
		}))
		svc := NewWorkspaceMemberService(memberRepo, userRepo, NewMockProjectMemberRepository(), nil)

		err := svc.SetMemberDisplayName(ctx, ws, target, "Whoever")
		require.Error(t, err)

		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusNotFound, apiErr.StatusCode())
	})

	t.Run("the write fails", func(t *testing.T) {
		userRepo := NewMockUserRepository()
		u := &domain.User{ID: target, Email: "w@example.com", Name: "w@example.com", Username: "w", IsActive: true}
		userRepo.AddUser(ws, u)
		memberRepo := &minimalWorkspaceMemberRepo{}
		require.NoError(t, memberRepo.Create(ctx, &domain.WorkspaceMember{
			ID: uuid.New(), WorkspaceID: ws, UserID: target, Role: domain.RoleMember,
		}))
		svc := NewWorkspaceMemberService(memberRepo, userRepo, NewMockProjectMemberRepository(), nil)

		userRepo.errToReturn = errors.New("disk full")
		err := svc.SetMemberDisplayName(ctx, ws, target, "Whoever")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "disk full")
	})
}

// GetMember must distinguish "not a member of this workspace" (nil, nil) from
// "the membership row points at an account that is gone" (404) — the handler
// turns the first into a 404 of its own and must not mistake the second for it.
func TestGetMember_MembershipWithoutAnAccountIs404(t *testing.T) {
	ctx := context.Background()
	ws, target := uuid.New(), uuid.New()

	memberRepo := &minimalWorkspaceMemberRepo{}
	require.NoError(t, memberRepo.Create(ctx, &domain.WorkspaceMember{
		ID: uuid.New(), WorkspaceID: ws, UserID: target, Role: domain.RoleMember,
	}))
	svc := NewWorkspaceMemberService(memberRepo, NewMockUserRepository(), NewMockProjectMemberRepository(), nil)

	_, err := svc.GetMember(ctx, ws, target)
	require.Error(t, err)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.StatusCode())
}

func TestGetMember_PropagatesLookupFailures(t *testing.T) {
	svc := NewWorkspaceMemberService(
		&failingMemberLookupRepo{err: errors.New("connection reset by peer")},
		NewMockUserRepository(), NewMockProjectMemberRepository(), nil)

	_, err := svc.GetMember(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection reset by peer")
}

func TestSearchUsers_PropagatesRepositoryFailures(t *testing.T) {
	svc, userRepo, _ := newIdentityFixture()
	userRepo.errToReturn = errors.New("connection reset by peer")

	_, err := svc.SearchUsers(context.Background(), uuid.New(), uuid.New(), "anything")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection reset by peer")
}

// failingMemberLookupRepo fails GetByWorkspaceAndUser and nothing else.
type failingMemberLookupRepo struct {
	minimalWorkspaceMemberRepo
	err error
}

func (r *failingMemberLookupRepo) GetByWorkspaceAndUser(context.Context, uuid.UUID, uuid.UUID) (*domain.WorkspaceMember, error) {
	return nil, r.err
}
