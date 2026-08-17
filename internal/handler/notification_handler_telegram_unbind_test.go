package handler

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// Disabling the channel is what revokes a Telegram binding (#581): chat_id is
// carried forward only while is_enabled is true, so a disabled save comes back
// with no chat and no token.
//
// TestUpdatePreferences_TelegramDisableClearsChatID and its rebind twin, both
// in notification_handler_telegram_test.go, cover that rule directly. What is
// here is the surrounding behaviour a revocation depends on and which those two
// do not assert: that an unopened bind link does not survive the revocation,
// that an ordinary save does not revoke anything by accident, and that a
// revocation cannot reach anybody else's row.

// expectMembership seeds the RequireBodyWorkspace guard's membership lookup.
func expectMembership(mock sqlmock.Sqlmock, wsID, userID uuid.UUID) {
	mock.ExpectQuery(`SELECT wm\.role FROM workspace_members wm\s+JOIN workspaces w ON w\.id = wm\.workspace_id\s+WHERE wm\.workspace_id = \$1 AND wm\.user_id = \$2 AND w\.deleted_at IS NULL`).
		WithArgs(wsID, userID).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("member"))
}

// boundTelegramPref is an existing, connected subscription — the state every
// revocation test starts from.
func boundTelegramPref(wsID, userID uuid.UUID, chatID int64) []domain.NotificationPreference {
	cfg, _ := json.Marshal(service.TelegramPreferenceConfig{Username: "alice", ChatID: chatID})
	return []domain.NotificationPreference{{
		WorkspaceID: wsID, UserID: &userID, Channel: "telegram", Config: cfg,
	}}
}

func upsertedTelegramConfig(t *testing.T, svc *mockNotificationService) service.TelegramPreferenceConfig {
	t.Helper()
	require.NotNil(t, svc.upsertedPref)
	var cfg service.TelegramPreferenceConfig
	require.NoError(t, json.Unmarshal(svc.upsertedPref.Config, &cfg))
	return cfg
}

// TestUpdatePreferences_DisableSpendsTheOutstandingBindToken: a link that was
// issued but never opened must not survive revocation. Otherwise a user who
// disconnects *because* the link leaked has revoked nothing — the URL is still
// live and the next person to open it binds their chat to this account.
func TestUpdatePreferences_DisableSpendsTheOutstandingBindToken(t *testing.T) {
	db, mock := newSQLMock(t)
	wsID, member := uuid.New(), uuid.New()
	expectMembership(mock, wsID, member)

	expires := time.Now().Add(telegramBindTokenTTL)
	existingCfg, _ := json.Marshal(service.TelegramPreferenceConfig{
		Username: "alice", BindToken: "an-outstanding-token", BindExpiresAt: &expires,
	})
	existing := []domain.NotificationPreference{{
		WorkspaceID: wsID, UserID: &member, Channel: "telegram", Config: existingCfg,
	}}

	svc, rec := putTelegramPreferences(t, db, member, existing,
		`{"workspace_id":"`+wsID.String()+`","channel":"telegram","is_enabled":false}`)

	require.Equal(t, http.StatusOK, rec.Code)
	cfg := upsertedTelegramConfig(t, svc)
	assert.Empty(t, cfg.BindToken, "the link the user is revoking is still valid after revocation")
	assert.Nil(t, cfg.BindExpiresAt)
	assert.Zero(t, cfg.ChatID)
}

// TestUpdatePreferences_EnabledSaveKeepsTheBinding is the regression guard on
// the other side of the same rule. Carrying chat_id forward exists so that
// adjusting event toggles cannot silently disconnect a connected account;
// scoping that carry-forward to is_enabled must not have cost it.
func TestUpdatePreferences_EnabledSaveKeepsTheBinding(t *testing.T) {
	db, mock := newSQLMock(t)
	wsID, member := uuid.New(), uuid.New()
	expectMembership(mock, wsID, member)

	svc, rec := putTelegramPreferences(t, db, member, boundTelegramPref(wsID, member, 777),
		`{"workspace_id":"`+wsID.String()+`","channel":"telegram","is_enabled":true,"config":{"telegram_username":"alice"}}`)

	require.Equal(t, http.StatusOK, rec.Code)
	cfg := upsertedTelegramConfig(t, svc)
	assert.EqualValues(t, 777, cfg.ChatID, "an ordinary save disconnected a connected account")
	assert.Empty(t, cfg.BindToken, "a still-bound account was handed a pointless new connection link")
}

// TestUpdatePreferences_DisableOnlyAffectsTheCallersOwnRow: the row written is
// always keyed on the authenticated caller, so no request shape can revoke
// somebody else's binding — including one naming a workspace where another
// user is bound.
func TestUpdatePreferences_DisableOnlyAffectsTheCallersOwnRow(t *testing.T) {
	db, mock := newSQLMock(t)
	wsID, member, other := uuid.New(), uuid.New(), uuid.New()
	expectMembership(mock, wsID, member)

	existing := append(boundTelegramPref(wsID, member, 777), boundTelegramPref(wsID, other, 888)...)

	svc, rec := putTelegramPreferences(t, db, member, existing,
		`{"workspace_id":"`+wsID.String()+`","channel":"telegram","is_enabled":false}`)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, svc.upsertedPref.UserID)
	assert.Equal(t, member, *svc.upsertedPref.UserID)
}
