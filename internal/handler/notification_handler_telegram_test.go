package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// putTelegramPreferences serves PUT /notifications/preferences the same way
// putPreferences does, but lets the test seed existingPrefs on the mock so the
// "already bound" path can be exercised.
func putTelegramPreferences(t *testing.T, db *sqlx.DB, userID uuid.UUID, existing []domain.NotificationPreference, body string) (*mockNotificationService, *httptest.ResponseRecorder) {
	t.Helper()

	svc := &mockNotificationService{existingPrefs: existing}
	h := NewNotificationHandler(svc, nil)

	e := echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(mw.ContextKeyAuthType, mw.AuthTypeUser)
			c.Set(mw.ContextKeyUserID, userID)
			c.Set("user_id", userID)
			return next(c)
		}
	})
	e.PUT("/api/v1/notifications/preferences", h.UpdatePreferences, mw.RequireBodyWorkspace(db))

	req := httptest.NewRequest(http.MethodPut, "/api/v1/notifications/preferences", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return svc, rec
}

func TestUpdatePreferences_TelegramEnableWithoutUsernameIsRejected(t *testing.T) {
	db, mock := newSQLMock(t)
	wsID, member := uuid.New(), uuid.New()
	mock.ExpectQuery(`SELECT wm\.role FROM workspace_members wm\s+JOIN workspaces w ON w\.id = wm\.workspace_id\s+WHERE wm\.workspace_id = \$1 AND wm\.user_id = \$2 AND w\.deleted_at IS NULL`).
		WithArgs(wsID, member).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("member"))

	svc, rec := putTelegramPreferences(t, db, member, nil,
		`{"workspace_id":"`+wsID.String()+`","channel":"telegram","is_enabled":true}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.False(t, svc.upsertCalled)
}

// TestUpdatePreferences_TelegramFirstEnableIssuesABindToken: enabling with a
// username and no existing binding gets a fresh, TTL'd bind token.
func TestUpdatePreferences_TelegramFirstEnableIssuesABindToken(t *testing.T) {
	db, mock := newSQLMock(t)
	wsID, member := uuid.New(), uuid.New()
	mock.ExpectQuery(`SELECT wm\.role FROM workspace_members wm\s+JOIN workspaces w ON w\.id = wm\.workspace_id\s+WHERE wm\.workspace_id = \$1 AND wm\.user_id = \$2 AND w\.deleted_at IS NULL`).
		WithArgs(wsID, member).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("member"))

	svc, rec := putTelegramPreferences(t, db, member, nil,
		`{"workspace_id":"`+wsID.String()+`","channel":"telegram","is_enabled":true,"config":{"telegram_username":"@alice"}}`)

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, svc.upsertCalled)
	require.NotNil(t, svc.upsertedPref)

	var cfg service.TelegramPreferenceConfig
	require.NoError(t, json.Unmarshal(svc.upsertedPref.Config, &cfg))
	assert.Equal(t, "alice", cfg.Username, "leading @ should be stripped")
	assert.NotEmpty(t, cfg.BindToken)
	require.NotNil(t, cfg.BindExpiresAt)
	assert.WithinDuration(t, time.Now().Add(telegramBindTokenTTL), *cfg.BindExpiresAt, time.Minute)
	assert.Zero(t, cfg.ChatID)
}

// TestUpdatePreferences_TelegramAlreadyBoundDoesNotReissueToken: a user who is
// already bound (chat_id set) and just adjusts their event toggles should not
// get a brand-new link on every save.
func TestUpdatePreferences_TelegramAlreadyBoundDoesNotReissueToken(t *testing.T) {
	db, mock := newSQLMock(t)
	wsID, member := uuid.New(), uuid.New()
	mock.ExpectQuery(`SELECT wm\.role FROM workspace_members wm\s+JOIN workspaces w ON w\.id = wm\.workspace_id\s+WHERE wm\.workspace_id = \$1 AND wm\.user_id = \$2 AND w\.deleted_at IS NULL`).
		WithArgs(wsID, member).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("member"))

	existingCfg, _ := json.Marshal(service.TelegramPreferenceConfig{Username: "alice", ChatID: 777})
	existing := []domain.NotificationPreference{{
		WorkspaceID: wsID, UserID: &member, Channel: "telegram", Config: existingCfg,
	}}

	svc, rec := putTelegramPreferences(t, db, member, existing,
		`{"workspace_id":"`+wsID.String()+`","channel":"telegram","is_enabled":true,"config":{"telegram_username":"alice"}}`)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, svc.upsertedPref)

	var cfg service.TelegramPreferenceConfig
	require.NoError(t, json.Unmarshal(svc.upsertedPref.Config, &cfg))
	assert.EqualValues(t, 777, cfg.ChatID, "an already-bound chat_id must survive a preference save")
	assert.Empty(t, cfg.BindToken, "a new bind token was issued for an already-bound subscriber")
}

// TestUpdatePreferences_TelegramDisableDoesNotRequireUsername: turning the
// channel off should not be blocked by validation meant for turning it on.
func TestUpdatePreferences_TelegramDisableDoesNotRequireUsername(t *testing.T) {
	db, mock := newSQLMock(t)
	wsID, member := uuid.New(), uuid.New()
	mock.ExpectQuery(`SELECT wm\.role FROM workspace_members wm\s+JOIN workspaces w ON w\.id = wm\.workspace_id\s+WHERE wm\.workspace_id = \$1 AND wm\.user_id = \$2 AND w\.deleted_at IS NULL`).
		WithArgs(wsID, member).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("member"))

	svc, rec := putTelegramPreferences(t, db, member, nil,
		`{"workspace_id":"`+wsID.String()+`","channel":"telegram","is_enabled":false}`)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, svc.upsertCalled)
}

// TestUpdatePreferences_TelegramDisableClearsChatID is the direct regression
// test for the unbind bug: chat_id used to be carried forward unconditionally
// regardless of is_enabled, so there was no way to stop delivery to a bound
// account or ever bind a different one. Disabling the channel must clear it.
func TestUpdatePreferences_TelegramDisableClearsChatID(t *testing.T) {
	db, mock := newSQLMock(t)
	wsID, member := uuid.New(), uuid.New()
	mock.ExpectQuery(`SELECT wm\.role FROM workspace_members wm\s+JOIN workspaces w ON w\.id = wm\.workspace_id\s+WHERE wm\.workspace_id = \$1 AND wm\.user_id = \$2 AND w\.deleted_at IS NULL`).
		WithArgs(wsID, member).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("member"))

	existingCfg, _ := json.Marshal(service.TelegramPreferenceConfig{Username: "alice", ChatID: 777})
	existing := []domain.NotificationPreference{{
		WorkspaceID: wsID, UserID: &member, Channel: "telegram", Config: existingCfg,
	}}

	svc, rec := putTelegramPreferences(t, db, member, existing,
		`{"workspace_id":"`+wsID.String()+`","channel":"telegram","is_enabled":false}`)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, svc.upsertedPref)

	var cfg service.TelegramPreferenceConfig
	require.NoError(t, json.Unmarshal(svc.upsertedPref.Config, &cfg))
	assert.Zero(t, cfg.ChatID, "disabling the channel must clear a prior chat_id, or the bot can never be unbound")
}

// TestUpdatePreferences_TelegramReenableAfterDisableIssuesFreshToken covers the
// full unbind-then-rebind flow: after a disable (chat_id cleared), re-enabling
// must issue a new bind token rather than staying permanently unbindable
// because a token is only ever generated when chat_id is already empty.
func TestUpdatePreferences_TelegramReenableAfterDisableIssuesFreshToken(t *testing.T) {
	db, mock := newSQLMock(t)
	wsID, member := uuid.New(), uuid.New()
	mock.ExpectQuery(`SELECT wm\.role FROM workspace_members wm\s+JOIN workspaces w ON w\.id = wm\.workspace_id\s+WHERE wm\.workspace_id = \$1 AND wm\.user_id = \$2 AND w\.deleted_at IS NULL`).
		WithArgs(wsID, member).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("member"))

	// Simulates the state right after a disable: username still on file, but
	// chat_id already cleared (ChatID intentionally left zero).
	existingCfg, _ := json.Marshal(service.TelegramPreferenceConfig{Username: "alice"})
	existing := []domain.NotificationPreference{{
		WorkspaceID: wsID, UserID: &member, Channel: "telegram", IsEnabled: false, Config: existingCfg,
	}}

	svc, rec := putTelegramPreferences(t, db, member, existing,
		`{"workspace_id":"`+wsID.String()+`","channel":"telegram","is_enabled":true,"config":{"telegram_username":"bob"}}`)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, svc.upsertedPref)

	var cfg service.TelegramPreferenceConfig
	require.NoError(t, json.Unmarshal(svc.upsertedPref.Config, &cfg))
	assert.Equal(t, "bob", cfg.Username, "re-enabling should accept a different account's username")
	assert.NotEmpty(t, cfg.BindToken, "re-enabling after an unbind must issue a fresh bind token")
	assert.Zero(t, cfg.ChatID)
}

// --- GET /notifications/telegram-bot-info ------------------------------------

// telegramBotInfoService is mockNotificationService with a fixed
// TelegramBotInfo answer — the outer method shadows the embedded stub.
type telegramBotInfoService struct {
	mockNotificationService
	botUsername string
	available   bool
}

func (s *telegramBotInfoService) TelegramBotInfo(context.Context, uuid.UUID) (string, bool) {
	return s.botUsername, s.available
}

func newTelegramBotInfoServer(userID uuid.UUID, members map[string]string, botUsername string, available bool) *echo.Echo {
	h := NewNotificationHandler(&telegramBotInfoService{botUsername: botUsername, available: available}, &mockWorkspaceMemberRepo{members: members})

	e := echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(mw.ContextKeyAuthType, mw.AuthTypeUser)
			c.Set(mw.ContextKeyUserID, userID)
			c.Set("user_id", userID)
			return next(c)
		}
	})
	e.GET("/api/v1/notifications/telegram-bot-info", h.GetTelegramBotInfo)
	return e
}

func TestGetTelegramBotInfo_MemberSeesIt(t *testing.T) {
	wsID, member := uuid.New(), uuid.New()
	e := newTelegramBotInfoServer(member, map[string]string{wsID.String() + "/" + member.String(): "member"}, "mesh_bot", true)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/telegram-bot-info?workspace_id="+wsID.String(), http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, true, body["available"])
	assert.Equal(t, "mesh_bot", body["bot_username"])
}

func TestGetTelegramBotInfo_NonMemberIsForbidden(t *testing.T) {
	wsID, stranger := uuid.New(), uuid.New()
	e := newTelegramBotInfoServer(stranger, map[string]string{}, "mesh_bot", true)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/telegram-bot-info?workspace_id="+wsID.String(), http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestGetTelegramBotInfo_InvalidWorkspaceID(t *testing.T) {
	e := newTelegramBotInfoServer(uuid.New(), map[string]string{}, "", false)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/telegram-bot-info?workspace_id=not-a-uuid", http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
