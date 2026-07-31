package service

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/config"
	"github.com/entire-vc/evc-mesh/internal/domain"
)

// stubEmailService lets a test choose exactly what the mail layer does.
type stubEmailService struct {
	enabled bool
	err     error
	calls   int
}

func (s *stubEmailService) Enabled() bool { return s.enabled }

func (s *stubEmailService) SendInvite(_ context.Context, _, _, _ string) error {
	s.calls++
	return s.err
}

func newInviteSvcWithEmail(t *testing.T, email EmailService) (WorkspaceInviteService, uuid.UUID) {
	t.Helper()

	wsID := uuid.New()
	return NewInviteService(
		&minimalInviteRepo{},
		NewMockUserRepository(),
		&minimalWorkspaceMemberRepo{},
		&fakeWorkspaceRepoForInvites{ws: &domain.Workspace{ID: wsID, Name: "Acme"}},
		email,
		nil,
		"https://mesh.example.com",
	), wsID
}

// TestCreateInvite_ReportsDeliveryOutcome is the regression for the reported
// defect: with no SMTP host configured the API answered 201 and the UI said
// "Invite sent", for an email that was never sent and could not have been.
//
// The three cases are deliberately distinguishable from one another — the old
// code collapsed all of them into "no error", because an unconfigured mailer
// returned nil and CreateInvite assigned the send result to _.
func TestCreateInvite_ReportsDeliveryOutcome(t *testing.T) {
	t.Run("email not configured: created, not sent, link returned", func(t *testing.T) {
		email := &stubEmailService{enabled: false, err: ErrEmailNotConfigured}
		svc, wsID := newInviteSvcWithEmail(t, email)

		res, err := svc.CreateInvite(context.Background(), CreateInviteInput{
			WorkspaceID: wsID,
			Email:       "teammate@example.com",
			Role:        domain.RoleMember,
		})

		// The invite itself must still be created — an absent mail server is a
		// supported configuration, not a reason to refuse the invitation.
		require.NoError(t, err)
		require.NotNil(t, res.Invite)
		assert.Equal(t, "teammate@example.com", res.Invite.Email)

		assert.False(t, res.Delivery.Sent(), "claimed the invitation email was sent with no mail server configured")
		assert.Equal(t, InviteDeliveryNotConfigured, res.Delivery.Status)
		assert.Equal(t, "https://mesh.example.com/accept-invite/"+res.Invite.Token, res.Delivery.URL,
			"the inviter has no other way to deliver the invitation")
	})

	t.Run("email configured and working: sent", func(t *testing.T) {
		email := &stubEmailService{enabled: true}
		svc, wsID := newInviteSvcWithEmail(t, email)

		res, err := svc.CreateInvite(context.Background(), CreateInviteInput{
			WorkspaceID: wsID,
			Email:       "teammate@example.com",
			Role:        domain.RoleMember,
		})

		require.NoError(t, err)
		assert.True(t, res.Delivery.Sent())
		assert.Equal(t, InviteDeliverySent, res.Delivery.Status)
		assert.Equal(t, 1, email.calls)
		assert.Empty(t, res.Delivery.Error)
	})

	t.Run("email configured but send fails: reported, invite still created", func(t *testing.T) {
		email := &stubEmailService{enabled: true, err: errors.New("dial tcp: connection refused")}
		svc, wsID := newInviteSvcWithEmail(t, email)

		res, err := svc.CreateInvite(context.Background(), CreateInviteInput{
			WorkspaceID: wsID,
			Email:       "teammate@example.com",
			Role:        domain.RoleMember,
		})

		// A broken mail server used to be indistinguishable from a delivered
		// email: the error went to _. It must now surface, without discarding
		// the invite that was already written.
		require.NoError(t, err, "a mail failure must not throw away a valid invite")
		require.NotNil(t, res.Invite)
		assert.False(t, res.Delivery.Sent())
		assert.Equal(t, InviteDeliveryFailed, res.Delivery.Status)
		assert.Contains(t, res.Delivery.Error, "connection refused")
		assert.NotEmpty(t, res.Delivery.URL)
	})
}

// TestResendInvite_ReportsDeliveryOutcome covers the same lie on the resend
// path, which answered a hardcoded {"status":"sent"} regardless of outcome.
func TestResendInvite_ReportsDeliveryOutcome(t *testing.T) {
	setup := func(t *testing.T, email EmailService) (WorkspaceInviteService, uuid.UUID, uuid.UUID) {
		t.Helper()
		svc, wsID := newInviteSvcWithEmail(t, email)
		res, err := svc.CreateInvite(context.Background(), CreateInviteInput{
			WorkspaceID: wsID,
			Email:       "teammate@example.com",
			Role:        domain.RoleMember,
		})
		require.NoError(t, err)
		return svc, wsID, res.Invite.ID
	}

	t.Run("not configured", func(t *testing.T) {
		email := &stubEmailService{enabled: false, err: ErrEmailNotConfigured}
		svc, wsID, inviteID := setup(t, email)

		delivery, err := svc.ResendInvite(context.Background(), wsID, inviteID)
		require.NoError(t, err)
		assert.False(t, delivery.Sent(), "resend claimed success with no mail server")
		assert.Equal(t, InviteDeliveryNotConfigured, delivery.Status)
		assert.NotEmpty(t, delivery.URL)
	})

	t.Run("send fails", func(t *testing.T) {
		email := &stubEmailService{enabled: true, err: errors.New("535 authentication failed")}
		svc, wsID, inviteID := setup(t, email)

		delivery, err := svc.ResendInvite(context.Background(), wsID, inviteID)
		require.NoError(t, err)
		assert.Equal(t, InviteDeliveryFailed, delivery.Status)
		assert.Contains(t, delivery.Error, "authentication failed")
	})

	t.Run("send succeeds", func(t *testing.T) {
		email := &stubEmailService{enabled: true}
		svc, wsID, inviteID := setup(t, email)

		delivery, err := svc.ResendInvite(context.Background(), wsID, inviteID)
		require.NoError(t, err)
		assert.True(t, delivery.Sent())
		assert.Equal(t, InviteDeliverySent, delivery.Status)
	})
}

// TestEmailService_UnconfiguredIsDistinguishableAndKeepsTokenOutOfLogs pins two
// properties of the mail layer itself.
//
// The token check is not cosmetic: the accept URL is a bearer credential for
// joining a workspace, and it used to be written to the API log in full. Logs
// get shipped to collectors and read by more people than the workspace has
// members, so anyone with log access could join.
func TestEmailService_UnconfiguredIsDistinguishableAndKeepsTokenOutOfLogs(t *testing.T) {
	svc := NewEmailService(config.EmailConfig{}) // no Host — the self-host default

	assert.False(t, svc.Enabled())

	var logged bytes.Buffer
	restore := log.Writer()
	log.SetOutput(&logged)
	log.SetFlags(0)
	defer log.SetOutput(restore)

	const token = "a122f31deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef0"
	inviteURL := "https://mesh.example.com/accept-invite/" + token

	err := svc.SendInvite(context.Background(), "teammate@example.com", "Acme", inviteURL)

	require.ErrorIs(t, err, ErrEmailNotConfigured,
		"an unconfigured mailer reported success, which is what made the API answer 201 for an email it never sent")

	out := logged.String()
	require.NotEmpty(t, out, "the operator gets no signal at all")
	assert.NotContains(t, out, token, "the invite token was written to the log in full")
	assert.NotContains(t, out, inviteURL, "the invite accept URL was written to the log")
	assert.True(t, strings.Contains(out, "teammate@example.com"),
		"the log should still say who was not emailed")
}
