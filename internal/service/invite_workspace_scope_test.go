package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Resend and Revoke used to take an invite id and nothing else. The workspace in
// the route was parsed by the handler and dropped, so the service had no way to
// tell whose invite it was holding and could not have refused a foreign one even
// in principle: a workspace admin naming their own workspace and another tenant's
// invite id revoked or re-sent that tenant's pending invitation.
//
// These tests run the real service, so a future refactor that drops the workspace
// argument again fails here rather than in production.

// seedInvite returns a repo holding one pending invite in wsID.
func seedInvite(t *testing.T, wsID, inviteID uuid.UUID) *minimalInviteRepo {
	t.Helper()
	repo := &minimalInviteRepo{}
	require.NoError(t, repo.Create(context.Background(), &domain.WorkspaceInvite{
		ID:          inviteID,
		WorkspaceID: wsID,
		Email:       "invitee@example.com",
		Role:        domain.RoleMember,
		Token:       "tok-" + inviteID.String(),
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	}))
	return repo
}

func TestRevokeInvite_RefusesInviteOfAnotherWorkspace(t *testing.T) {
	ownerWS := uuid.New()
	inviteID := uuid.New()
	repo := seedInvite(t, ownerWS, inviteID)

	svc := NewInviteService(repo, nil, nil, nil, nil, nil, "http://localhost")

	err := svc.RevokeInvite(context.Background(), uuid.New(), inviteID)
	require.Error(t, err, "an invite belonging to another workspace was revoked")

	// Not found rather than forbidden: a caller authorized for one workspace
	// should not learn that an id exists in another.
	assert.Contains(t, err.Error(), "not found")

	// And it is still there.
	still, getErr := repo.GetByID(context.Background(), inviteID)
	require.NoError(t, getErr)
	assert.NotNil(t, still, "another workspace's invite was deleted")
}

func TestRevokeInvite_AllowsOwnWorkspace(t *testing.T) {
	ownerWS := uuid.New()
	inviteID := uuid.New()
	repo := seedInvite(t, ownerWS, inviteID)

	svc := NewInviteService(repo, nil, nil, nil, nil, nil, "http://localhost")

	require.NoError(t, svc.RevokeInvite(context.Background(), ownerWS, inviteID),
		"the owning workspace was refused its own invite")
}

func TestRevokeInvite_UnknownInvite(t *testing.T) {
	svc := NewInviteService(&minimalInviteRepo{}, nil, nil, nil, nil, nil, "http://localhost")
	require.Error(t, svc.RevokeInvite(context.Background(), uuid.New(), uuid.New()))
}

func TestResendInvite_RefusesInviteOfAnotherWorkspace(t *testing.T) {
	ownerWS := uuid.New()
	inviteID := uuid.New()
	repo := seedInvite(t, ownerWS, inviteID)

	// The refusal happens before the workspace lookup and the email send, so
	// leaving those nil also proves nothing downstream was reached.
	svc := NewInviteService(repo, nil, nil, nil, nil, nil, "http://localhost")

	err := svc.ResendInvite(context.Background(), uuid.New(), inviteID)
	require.Error(t, err, "another workspace's invitation email was re-sent")
	assert.Contains(t, err.Error(), "not found")
}

func TestResendInvite_AllowsOwnWorkspace(t *testing.T) {
	ownerWS := uuid.New()
	inviteID := uuid.New()
	repo := seedInvite(t, ownerWS, inviteID)

	wsRepo := NewMockWorkspaceRepository()
	require.NoError(t, wsRepo.Create(context.Background(), &domain.Workspace{
		ID: ownerWS, Name: "Owner Workspace", Slug: "owner-workspace",
	}))

	svc := NewInviteService(repo, nil, nil, wsRepo, noopEmailService{}, nil, "http://localhost")

	require.NoError(t, svc.ResendInvite(context.Background(), ownerWS, inviteID),
		"the owning workspace was refused re-sending its own invite")
}
