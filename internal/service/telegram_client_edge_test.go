package service

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateTelegramBindToken_ReturnsDistinctNonEmptyValues(t *testing.T) {
	a, err := GenerateTelegramBindToken()
	require.NoError(t, err)
	b, err := GenerateTelegramBindToken()
	require.NoError(t, err)

	assert.NotEmpty(t, a)
	assert.NotEmpty(t, b)
	assert.NotEqual(t, a, b, "two calls produced the same token")
}

func TestNewTelegramClient_ReturnsAUsableClient(t *testing.T) {
	client := NewTelegramClient()
	require.NotNil(t, client)
	httpClient, ok := client.(*httpTelegramClient)
	require.True(t, ok)
	assert.Equal(t, TelegramAPIBaseURL, httpClient.baseURL)
}

func TestTelegramClient_GetMe_NoUsernameIsAnError(t *testing.T) {
	client, closeFn := newTestTelegramServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"id": 1}})
	})
	defer closeFn()

	_, err := client.GetMe(t.Context(), "test-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no username")
}

func TestTelegramClient_NetworkErrorIsWrapped(t *testing.T) {
	// An unroutable address fails at Do(), before any response exists.
	client := NewTelegramClientWithBaseURL("http://127.0.0.1:1")

	_, err := client.GetMe(t.Context(), "test-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "telegram getMe")
}

func TestTelegramClient_MalformedResponseBodyIsAnError(t *testing.T) {
	client, closeFn := newTestTelegramServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	})
	defer closeFn()

	_, err := client.GetMe(t.Context(), "test-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode response")
}

func TestTelegramClient_ResultShapeMismatchIsAnError(t *testing.T) {
	client, closeFn := newTestTelegramServer(t, func(w http.ResponseWriter, r *http.Request) {
		// getMe's target is a struct — a string result can't unmarshal into it.
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": "not-an-object"})
	})
	defer closeFn()

	_, err := client.GetMe(t.Context(), "test-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal result")
}

func TestDecodeTelegramIntegration_EdgeCases(t *testing.T) {
	t.Run("nil config", func(t *testing.T) {
		_, _, ok := decodeTelegramIntegration(nil)
		assert.False(t, ok)
	})

	t.Run("wrong provider", func(t *testing.T) {
		cfg := telegramIntegration(true, "tok")
		cfg.Provider = "slack"
		_, _, ok := decodeTelegramIntegration(cfg)
		assert.False(t, ok)
	})

	t.Run("no config bytes at all", func(t *testing.T) {
		cfg := telegramIntegration(true, "tok")
		cfg.Config = nil
		_, _, ok := decodeTelegramIntegration(cfg)
		assert.False(t, ok, "no config means no bot_token, same as an empty one")
	})

	t.Run("malformed config JSON", func(t *testing.T) {
		cfg := telegramIntegration(true, "tok")
		cfg.Config = []byte("not json")
		_, _, ok := decodeTelegramIntegration(cfg)
		assert.False(t, ok)
	})

	t.Run("empty bot token", func(t *testing.T) {
		cfg := telegramIntegration(true, "")
		_, _, ok := decodeTelegramIntegration(cfg)
		assert.False(t, ok)
	})
}
