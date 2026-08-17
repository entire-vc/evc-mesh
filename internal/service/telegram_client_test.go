package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestTelegramServer(t *testing.T, handler http.HandlerFunc) (client TelegramClient, closeFn func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	return NewTelegramClientWithBaseURL(srv.URL), srv.Close
}

func TestTelegramClient_GetMe(t *testing.T) {
	client, closeFn := newTestTelegramServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/bottest-token/getMe", r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"id": 1, "username": "mesh_bot"},
		})
	})
	defer closeFn()

	username, err := client.GetMe(t.Context(), "test-token")
	require.NoError(t, err)
	assert.Equal(t, "mesh_bot", username)
}

func TestTelegramClient_GetMe_InvalidTokenReturnsError(t *testing.T) {
	client, closeFn := newTestTelegramServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": false, "error_code": 401, "description": "Unauthorized",
		})
	})
	defer closeFn()

	_, err := client.GetMe(t.Context(), "bad-token")
	require.Error(t, err)
	var apiErr *TelegramAPIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusUnauthorized, apiErr.StatusCode)
	assert.False(t, apiErr.Forbidden())
}

func TestTelegramClient_SendMessage(t *testing.T) {
	var gotBody map[string]any
	client, closeFn := newTestTelegramServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/bottest-token/sendMessage", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{}})
	})
	defer closeFn()

	err := client.SendMessage(t.Context(), "test-token", 12345, "hello")
	require.NoError(t, err)
	assert.EqualValues(t, 12345, gotBody["chat_id"])
	assert.Equal(t, "hello", gotBody["text"])
}

func TestTelegramClient_SendMessage_ForbiddenWhenBlocked(t *testing.T) {
	client, closeFn := newTestTelegramServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": false, "error_code": 403, "description": "Forbidden: bot was blocked by the user",
		})
	})
	defer closeFn()

	err := client.SendMessage(t.Context(), "test-token", 1, "hi")
	require.Error(t, err)
	var apiErr *TelegramAPIError
	require.ErrorAs(t, err, &apiErr)
	assert.True(t, apiErr.Forbidden())
}

func TestTelegramClient_GetUpdates(t *testing.T) {
	var gotBody map[string]any
	client, closeFn := newTestTelegramServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/bottest-token/getUpdates", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": []map[string]any{
				{
					"update_id": 42,
					"message": map[string]any{
						"message_id": 1,
						"text":       "/start abc123",
						"chat":       map[string]any{"id": 999},
						"from":       map[string]any{"id": 7, "username": "alice"},
					},
				},
			},
		})
	})
	defer closeFn()

	updates, err := client.GetUpdates(t.Context(), "test-token", 5, 30)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.EqualValues(t, 42, updates[0].UpdateID)
	require.NotNil(t, updates[0].Message)
	assert.Equal(t, "/start abc123", updates[0].Message.Text)
	assert.EqualValues(t, 999, updates[0].Message.Chat.ID)
	require.NotNil(t, updates[0].Message.From)
	assert.Equal(t, "alice", updates[0].Message.From.Username)
	assert.EqualValues(t, 5, gotBody["offset"])
	assert.EqualValues(t, 30, gotBody["timeout"])
}

// TestTelegramClient_TokenIsPathEscaped guards against a token containing a
// character that would otherwise change the URL's path shape — tokens are
// admin-supplied and validated via GetMe before being trusted, but the HTTP
// layer should not assume that has already happened.
func TestTelegramClient_TokenIsPathEscaped(t *testing.T) {
	client, closeFn := newTestTelegramServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/bota%2Fb/getMe", r.URL.EscapedPath())
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"username": "x"}})
	})
	defer closeFn()

	_, err := client.GetMe(t.Context(), "a/b")
	require.NoError(t, err)
}
