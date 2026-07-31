package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"

	"context"
)

// deliveryInviteService returns a fixed delivery outcome so the test is about
// the wire format the client sees, not about the mail layer.
type deliveryInviteService struct {
	service.WorkspaceInviteService
	invite   domain.WorkspaceInvite
	delivery service.InviteDelivery
}

func (d *deliveryInviteService) CreateInvite(context.Context, service.CreateInviteInput) (*service.CreateInviteResult, error) {
	inv := d.invite
	return &service.CreateInviteResult{Invite: &inv, Delivery: d.delivery}, nil
}

func (d *deliveryInviteService) ResendInvite(context.Context, uuid.UUID, uuid.UUID) (service.InviteDelivery, error) {
	return d.delivery, nil
}

func createInvite(t *testing.T, svc service.WorkspaceInviteService, wsID uuid.UUID) map[string]any {
	t.Helper()

	e := echo.New()
	h := NewInviteHandler(svc)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"email":"teammate@example.com","role":"member"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id/invites")
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())

	require.NoError(t, h.Create(c))
	require.Equal(t, http.StatusCreated, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body
}

// TestCreateInvite_ResponseDistinguishesSentFromCreated is the HTTP-level
// regression. The 201 body used to be the bare invite object, which carried no
// evidence about the email at all — so every client, including our own UI, read
// "created" as "sent" and told the inviter their teammate had been emailed.
func TestCreateInvite_ResponseDistinguishesSentFromCreated(t *testing.T) {
	wsID := uuid.New()
	invite := domain.WorkspaceInvite{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		Email:       "teammate@example.com",
		Role:        domain.RoleMember,
		Token:       "tok123",
	}
	const url = "https://mesh.example.com/accept-invite/tok123"

	t.Run("not configured", func(t *testing.T) {
		body := createInvite(t, &deliveryInviteService{
			invite: invite,
			delivery: service.InviteDelivery{
				Status: service.InviteDeliveryNotConfigured,
				URL:    url,
			},
		}, wsID)

		assert.Equal(t, false, body["email_sent"],
			"the response claimed the invitation was emailed with no mail server configured")
		assert.Equal(t, "not_configured", body["delivery_status"])
		assert.Equal(t, url, body["invite_url"],
			"without the link in the response the inviter has no way to deliver the invite")

		// Additive change: the invite object must keep its exact previous shape
		// at the top level, or every existing client breaks on upgrade.
		assert.Equal(t, invite.ID.String(), body["id"])
		assert.Equal(t, "teammate@example.com", body["email"])
		assert.Equal(t, "tok123", body["token"])
		assert.Equal(t, wsID.String(), body["workspace_id"])
	})

	t.Run("sent", func(t *testing.T) {
		body := createInvite(t, &deliveryInviteService{
			invite:   invite,
			delivery: service.InviteDelivery{Status: service.InviteDeliverySent, URL: url},
		}, wsID)

		assert.Equal(t, true, body["email_sent"])
		assert.Equal(t, "sent", body["delivery_status"])
		assert.NotContains(t, body, "delivery_error")
	})

	t.Run("failed carries the reason", func(t *testing.T) {
		body := createInvite(t, &deliveryInviteService{
			invite: invite,
			delivery: service.InviteDelivery{
				Status: service.InviteDeliveryFailed,
				URL:    url,
				Error:  "dial tcp: connection refused",
			},
		}, wsID)

		assert.Equal(t, false, body["email_sent"])
		assert.Equal(t, "failed", body["delivery_status"])
		assert.Contains(t, body["delivery_error"], "connection refused")
	})
}

// TestResendInvite_ResponseReportsRealOutcome pins the resend path, which
// answered a hardcoded {"status":"sent"} that depended on nothing.
func TestResendInvite_ResponseReportsRealOutcome(t *testing.T) {
	svc := &deliveryInviteService{
		delivery: service.InviteDelivery{
			Status: service.InviteDeliveryNotConfigured,
			URL:    "https://mesh.example.com/accept-invite/tok123",
		},
	}

	rec := inviteRequest(t, svc, http.MethodPost, "resend", uuid.New().String(), uuid.New().String())
	require.Equal(t, http.StatusOK, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	assert.Equal(t, false, body["email_sent"], `resend answered "sent" with no mail server configured`)
	assert.Equal(t, "not_configured", body["delivery_status"])
	assert.NotEmpty(t, body["invite_url"])
}
