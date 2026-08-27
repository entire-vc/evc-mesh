package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func newMemberServiceForEmailTests() (WorkspaceMemberService, *MockUserRepository) {
	userRepo := NewMockUserRepository()
	return NewWorkspaceMemberService(
		&minimalWorkspaceMemberRepo{},
		userRepo,
		NewMockProjectMemberRepository(),
		nil, // activityRepo: only touched on the success path
		nil, // agentRepo: not exercised by these tests
	), userRepo
}

// Adding a member is a lookup by email, so it has to agree with how the address
// was stored — otherwise inviting "Carol@Example.COM" to a workspace reports
// "User not found" for an account that plainly exists.
func TestAddMember_MatchesEmailCaseInsensitively(t *testing.T) {
	svc, userRepo := newMemberServiceForEmailTests()

	existing := &domain.User{
		ID: uuid.New(), Email: "carol@example.com", Name: "Carol",
		Username: "carol", IsActive: true,
	}
	userRepo.AddUser(uuid.New(), existing)

	for _, spelling := range []string{
		"carol@example.com",
		"Carol@Example.COM",
		"  CAROL@EXAMPLE.COM  ",
	} {
		member, err := svc.AddMember(context.Background(), uuid.New(), spelling, domain.RoleMember, uuid.Nil)
		require.NoError(t, err, "AddMember with %q must find the existing account", spelling)
		assert.Equal(t, existing.ID, member.User.ID)
	}
}

func TestAddMember_RejectsBlankEmail(t *testing.T) {
	svc, _ := newMemberServiceForEmailTests()

	// "   " is not an address — after trimming there is nothing left, and it
	// must be rejected the same way "" is rather than reaching the repository.
	for _, blank := range []string{"", "   ", "\t\n"} {
		_, err := svc.AddMember(context.Background(), uuid.New(), blank, domain.RoleMember, uuid.Nil)
		require.Error(t, err, "AddMember(%q) must be rejected", blank)

		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode())
	}
}

func TestAddMemberWithCreate_RejectsBlankEmail(t *testing.T) {
	svc, _ := newMemberServiceForEmailTests()

	for _, blank := range []string{"", "   "} {
		_, err := svc.AddMemberWithCreate(context.Background(), uuid.New(), blank, "", domain.RoleMember, "StrongP4ss", uuid.Nil)
		require.Error(t, err, "AddMemberWithCreate(%q) must be rejected", blank)

		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode())
	}
}

func TestAddMemberWithCreate_NormalizesEmailOnCreate(t *testing.T) {
	svc, userRepo := newMemberServiceForEmailTests()

	member, err := svc.AddMemberWithCreate(context.Background(), uuid.New(),
		"  Dave@Example.COM ", "", domain.RoleMember, "StrongP4ss", uuid.Nil)
	require.NoError(t, err)
	assert.Equal(t, "dave@example.com", member.User.Email)

	created, err := userRepo.GetByEmail(context.Background(), "dave@example.com")
	require.NoError(t, err)
	require.NotNil(t, created, "the account must be stored under the canonical address")
	assert.Equal(t, "dave", created.Username)
}
