package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// seedProfileMember puts one human with the column defaults into wsID.
func seedProfileMember(memberRepo *MockWorkspaceMemberRepository, wsID, userID uuid.UUID, name string) {
	memberRepo.Seed(wsID, domain.WorkspaceMemberWithUser{
		WorkspaceMember: domain.WorkspaceMember{
			ID: uuid.New(), UserID: userID, Role: domain.RoleMember,
			Capabilities: []byte(`[]`), Availability: "business-hours",
		},
		User: domain.UserBrief{ID: userID, Name: name},
	})
}

// A human in the imported YAML gets zone/capabilities/availability written,
// and the team directory hands them back. Before the fix the import updated
// the role only and the directory answered constants.
func TestImportTeam_HumanProfileReachesTeamDirectory(t *testing.T) {
	ctx := context.Background()
	wsRepo := NewMockWorkspaceRepository()
	memberRepo := NewMockWorkspaceMemberRepository()
	wsID, userID := uuid.New(), uuid.New()
	require.NoError(t, wsRepo.Create(ctx, &domain.Workspace{ID: wsID, Name: "W"}))
	seedProfileMember(memberRepo, wsID, userID, "Ann")

	svc := NewRulesService(nil, nil, nil, NewMockAgentRepository(), memberRepo, wsRepo, nil)
	result, err := svc.ImportTeam(ctx, wsID, []byte(`
humans:
  - name: Ann
    responsibility_zone: payments
    capabilities: [billing, sql]
    availability: on-call
`))
	require.NoError(t, err)
	assert.Empty(t, result.Errors)
	assert.Equal(t, 1, result.HumansUpdated)

	dir, err := svc.GetTeamDirectory(ctx, wsID)
	require.NoError(t, err)
	require.Len(t, dir.Humans, 1)
	assert.Equal(t, "payments", dir.Humans[0].ResponsibilityZone)
	assert.JSONEq(t, `["billing","sql"]`, string(dir.Humans[0].Capabilities))
	assert.Equal(t, "on-call", dir.Humans[0].Availability)

	// A second import naming only the role keeps the profile.
	_, err = svc.ImportTeam(ctx, wsID, []byte("humans:\n  - name: Ann\n    role: admin\n"))
	require.NoError(t, err)
	dir, err = svc.GetTeamDirectory(ctx, wsID)
	require.NoError(t, err)
	assert.Equal(t, "payments", dir.Humans[0].ResponsibilityZone)
}

func TestUpdateMemberProfile_Validation(t *testing.T) {
	ctx := context.Background()
	memberRepo := NewMockWorkspaceMemberRepository()
	wsID, userID := uuid.New(), uuid.New()
	seedProfileMember(memberRepo, wsID, userID, "Ann")
	svc := NewWorkspaceMemberService(memberRepo, nil, nil, nil, nil)

	err := svc.UpdateMemberProfile(ctx, wsID, userID, domain.MemberProfileUpdate{Capabilities: []byte(`{"go":true}`)})
	var apiErr *apierror.Error
	require.True(t, errors.As(err, &apiErr), "got %v", err)
	assert.Equal(t, 400, apiErr.Code)

	zone := "infra"
	require.NoError(t, svc.UpdateMemberProfile(ctx, wsID, userID, domain.MemberProfileUpdate{
		ResponsibilityZone: &zone, Capabilities: []byte(`null`),
	}))
	got, err := memberRepo.GetByWorkspaceAndUser(ctx, wsID, userID)
	require.NoError(t, err)
	assert.Equal(t, "infra", got.ResponsibilityZone)
	assert.JSONEq(t, `[]`, string(got.Capabilities), "null capabilities store as an empty array")
	assert.Equal(t, "business-hours", got.Availability)

	err = svc.UpdateMemberProfile(ctx, wsID, uuid.New(), domain.MemberProfileUpdate{ResponsibilityZone: &zone})
	require.True(t, errors.As(err, &apiErr), "got %v", err)
	assert.Equal(t, 404, apiErr.Code)
}

// The tree format reads ListWithProjects, a different query from the flat one;
// a member read without the capabilities column still renders [] not null.
func TestGetTeamDirectoryTree_HumanProfile(t *testing.T) {
	ctx := context.Background()
	wsRepo := NewMockWorkspaceRepository()
	memberRepo := NewMockWorkspaceMemberRepository()
	wsID, ann, bob := uuid.New(), uuid.New(), uuid.New()
	require.NoError(t, wsRepo.Create(ctx, &domain.Workspace{ID: wsID, Name: "W"}))
	memberRepo.Seed(wsID, domain.WorkspaceMemberWithUser{
		WorkspaceMember: domain.WorkspaceMember{
			ID: uuid.New(), UserID: ann, Role: domain.RoleMember,
			Capabilities: []byte(`["go"]`), ResponsibilityZone: "X", Availability: "async",
		},
		User: domain.UserBrief{ID: ann, Name: "Ann", Username: "ann"},
	})
	memberRepo.Seed(wsID, domain.WorkspaceMemberWithUser{
		WorkspaceMember: domain.WorkspaceMember{ID: uuid.New(), UserID: bob, Role: domain.RoleMember},
		User:            domain.UserBrief{ID: bob, Name: "Bob"},
	})

	svc := NewRulesService(nil, nil, nil, NewMockAgentRepository(), memberRepo, wsRepo, nil)
	tree, err := svc.GetTeamDirectoryTree(ctx, wsID)
	require.NoError(t, err)
	require.Len(t, tree.Humans, 2)
	assert.Equal(t, "X", tree.Humans[0].ResponsibilityZone)
	assert.Equal(t, "async", tree.Humans[0].Availability)
	assert.JSONEq(t, `["go"]`, string(tree.Humans[0].Capabilities))
	assert.Equal(t, "ann", tree.Humans[0].Username)
	assert.JSONEq(t, `[]`, string(tree.Humans[1].Capabilities))
}

func TestExportConfig_CarriesHumanProfile(t *testing.T) {
	ctx := context.Background()
	wsRepo := NewMockWorkspaceRepository()
	memberRepo := NewMockWorkspaceMemberRepository()
	ws := &domain.Workspace{ID: uuid.New(), Name: "Acme", Slug: "acme"}
	wsRepo.items[ws.ID] = ws
	userID := uuid.New()
	memberRepo.Seed(ws.ID, domain.WorkspaceMemberWithUser{
		WorkspaceMember: domain.WorkspaceMember{
			ID: uuid.New(), UserID: userID, Role: domain.RoleMember,
			Capabilities: []byte(`["billing"]`), ResponsibilityZone: "payments", Availability: "on-call",
		},
		User: domain.UserBrief{ID: userID, Name: "Ann"},
	})

	svc := NewRulesService(noopWsRuleRepo{}, nil, nil, NewMockAgentRepository(), memberRepo, wsRepo, nil)
	out, err := svc.ExportConfig(ctx, ws.ID)
	require.NoError(t, err)
	assert.Contains(t, string(out), "responsibility_zone: payments")
	assert.Contains(t, string(out), "availability: on-call")
	assert.Contains(t, string(out), "- billing")
}

// failingProfileRepo refuses the profile write and nothing else.
type failingProfileRepo struct {
	*MockWorkspaceMemberRepository
}

func (failingProfileRepo) UpdateProfile(context.Context, uuid.UUID, uuid.UUID, domain.MemberProfileUpdate) (bool, error) {
	return false, errors.New("db down")
}

func TestImportTeam_HumanProfileWriteError_Recorded(t *testing.T) {
	ctx := context.Background()
	memberRepo := NewMockWorkspaceMemberRepository()
	wsID := uuid.New()
	seedProfileMember(memberRepo, wsID, uuid.New(), "Ann")

	svc := NewRulesService(nil, nil, nil, NewMockAgentRepository(), failingProfileRepo{memberRepo}, nil, nil)
	result, err := svc.ImportTeam(ctx, wsID, []byte("humans:\n  - name: Ann\n    responsibility_zone: payments\n"))
	require.NoError(t, err)
	assert.Equal(t, 0, result.HumansUpdated)
	require.Len(t, result.Errors, 1)
	assert.Contains(t, result.Errors[0], "update profile error")
}

func TestUpdateMemberProfile_AvailabilityTooLong_AndRepoError(t *testing.T) {
	ctx := context.Background()
	memberRepo := NewMockWorkspaceMemberRepository()
	wsID, userID := uuid.New(), uuid.New()
	seedProfileMember(memberRepo, wsID, userID, "Ann")

	long := strings.Repeat("a", 51)
	err := NewWorkspaceMemberService(memberRepo, nil, nil, nil, nil).
		UpdateMemberProfile(ctx, wsID, userID, domain.MemberProfileUpdate{Availability: &long})
	var apiErr *apierror.Error
	require.True(t, errors.As(err, &apiErr), "got %v", err)
	assert.Contains(t, apiErr.Validation, "availability")

	// The column is VARCHAR(50): the limit is characters, not UTF-8 bytes.
	cyrillic := strings.Repeat("я", 50)
	require.NoError(t, NewWorkspaceMemberService(memberRepo, nil, nil, nil, nil).
		UpdateMemberProfile(ctx, wsID, userID, domain.MemberProfileUpdate{Availability: &cyrillic}))
	cyrillic += "я"
	err = NewWorkspaceMemberService(memberRepo, nil, nil, nil, nil).
		UpdateMemberProfile(ctx, wsID, userID, domain.MemberProfileUpdate{Availability: &cyrillic})
	require.True(t, errors.As(err, &apiErr), "got %v", err)
	assert.Contains(t, apiErr.Validation, "availability")

	ok := "async"
	err = NewWorkspaceMemberService(failingProfileRepo{memberRepo}, nil, nil, nil, nil).
		UpdateMemberProfile(ctx, wsID, userID, domain.MemberProfileUpdate{Availability: &ok})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "db down")
}
