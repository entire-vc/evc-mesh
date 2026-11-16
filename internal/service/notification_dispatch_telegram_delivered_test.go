package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// A successful Telegram send used to leave nothing in the log: only refusals
// were written. "Delivered" and "this branch never ran" were the same silence,
// so every check of the channel needed a person to confirm the message arrived.
// These tests drive the real HTTP client against a stand-in Bot API, so the
// only thing replaced is the network boundary.

const deliveredTestBotToken = "987654:AAHdeliveredTestSecretTokenValue" // pragma: allowlist secret

// dispatchTelegramAgainst runs one comment.created dispatch to a single bound
// subscriber through a Bot API stand-in that answers sendMessage with reply,
// and returns the log once the send has been answered and logged.
func dispatchTelegramAgainst(t *testing.T, status int, reply map[string]any, wantInLog string) string {
	t.Helper()
	readLog := captureLog(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(srv.Close)

	wsID := uuid.New()
	member := uuid.New()
	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{telegramPref(wsID, member, 777001)},
		members: map[uuid.UUID]bool{member: true},
	}
	integrations := &fakeTelegramIntegrationLookup{cfg: telegramIntegration(true, deliveredTestBotToken)}
	client := NewTelegramClientWithBaseURL(srv.URL)
	svc := NewNotificationService(repo, WithTelegramService(client, integrations, nil, nil)).(*notificationService)

	svc.dispatch(commentEvent(wsID))

	waitFor(t, func() bool { return strings.Contains(readLog(), wantInLog) })
	return readLog()
}

func TestDispatch_TelegramLogsDeliveredWithMessageID(t *testing.T) {
	out := dispatchTelegramAgainst(t, http.StatusOK,
		map[string]any{"ok": true, "result": map[string]any{"message_id": 42, "chat": map[string]any{"id": 777001}}},
		"[notification][telegram] delivered:")

	assert.Equal(t, 1, strings.Count(out, "[notification][telegram] delivered:"), "log:\n%s", out)
	assert.Contains(t, out, "event comment.created")
	assert.Contains(t, out, "message_id 42")
	assert.NotContains(t, out, "NOT DELIVERED")
	assert.NotContains(t, out, deliveredTestBotToken, "the bot token reached the log")
	assert.NotContains(t, out, "777001", "the chat id reached the delivered line")
}

func TestDispatch_TelegramRefusalLogsNoDeliveredLine(t *testing.T) {
	out := dispatchTelegramAgainst(t, http.StatusBadRequest,
		map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: chat not found"},
		"[notification][telegram] NOT DELIVERED")

	require.Contains(t, out, "[notification][telegram] NOT DELIVERED")
	assert.NotContains(t, out, "[notification][telegram] delivered:")
	assert.NotContains(t, out, deliveredTestBotToken, "the bot token reached the log")
}
