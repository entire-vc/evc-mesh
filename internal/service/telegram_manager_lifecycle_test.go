package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// TestTelegramBotManager_PollLoopBindsThenStops is an end-to-end run of the
// real poll loop (not handleStart called directly): EnsureRunning starts a
// goroutine that calls the real HTTP client against a fake Telegram server,
// receives one /start update, binds it through the real notification repo
// fake, and Stop halts further polling.
func TestTelegramBotManager_PollLoopBindsThenStops(t *testing.T) {
	wsID, userID, intID := uuid.New(), uuid.New(), uuid.New()

	var pollCount int32
	var sentStart atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case rEndsWith(r.URL.Path, "/getUpdates"):
			atomic.AddInt32(&pollCount, 1)
			if sentStart.CompareAndSwap(false, true) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"ok": true,
					"result": []map[string]any{{
						"update_id": 1,
						"message": map[string]any{
							"message_id": 1,
							"text":       "/start good-token",
							"chat":       map[string]any{"id": 4242},
							"from":       map[string]any{"id": 1, "username": "alice"},
						},
					}},
				})
				return
			}
			// Every subsequent long-poll: no new updates. Real Telegram would
			// hold this open for the timeout; the fake returns immediately so
			// the test does not need to wait out a real long-poll.
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []map[string]any{}})
		case rEndsWith(r.URL.Path, "/sendMessage"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := NewTelegramClientWithBaseURL(srv.URL)
	tgCfg, _ := json.Marshal(TelegramIntegrationConfig{BotToken: "test-token", BotUsername: "mesh_bot"})
	integration := &domain.IntegrationConfig{ID: intID, WorkspaceID: wsID, Provider: domain.IntegrationProviderTelegram, IsActive: true, Config: tgCfg}
	integrations := &fakeTelegramIntegrationSource{
		active: []domain.IntegrationConfig{*integration},
		byID:   map[uuid.UUID]*domain.IntegrationConfig{intID: integration},
	}
	repo := &fakeNotificationRepo{
		prefs: []domain.NotificationPreference{telegramPrefWithBind(wsID, userID, "", "good-token", 15*time.Minute)},
	}
	users := &fakeTelegramUserLookup{users: map[uuid.UUID]*domain.User{userID: {ID: userID, Name: "Alice"}}}

	mgr := NewTelegramBotManager(client, integrations, repo, users, &fakeTelegramProjectLookup{}, &fakeWorkspaceNameLookup{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Start(ctx)

	require.Eventually(t, func() bool { return len(repo.upsertedPrefs()) > 0 }, 2*time.Second, 10*time.Millisecond,
		"the /start update sent through the real poll loop never bound")

	upserted := repo.upsertedPrefs()
	require.Len(t, upserted, 1)
	var cfg TelegramPreferenceConfig
	require.NoError(t, json.Unmarshal(upserted[0].Config, &cfg))
	assert.EqualValues(t, 4242, cfg.ChatID)

	mgr.Stop(intID)
	countAtStop := atomic.LoadInt32(&pollCount)
	time.Sleep(150 * time.Millisecond)
	assert.LessOrEqual(t, atomic.LoadInt32(&pollCount), countAtStop+1,
		"polling continued after Stop — at most one in-flight request should land after cancellation")
}

// TestTelegramBotManager_EnsureRunningIsIdempotent guards against a duplicate
// poller for the same integration — Telegram itself would answer a second
// concurrent getUpdates on the same token with a 409 Conflict.
func TestTelegramBotManager_EnsureRunningIsIdempotent(t *testing.T) {
	intID := uuid.New()

	var pollCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rEndsWith(r.URL.Path, "/getUpdates") {
			atomic.AddInt32(&pollCount, 1)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []map[string]any{}})
	}))
	defer srv.Close()

	client := NewTelegramClientWithBaseURL(srv.URL)
	tgCfg, _ := json.Marshal(TelegramIntegrationConfig{BotToken: "test-token", BotUsername: "mesh_bot"})
	integration := &domain.IntegrationConfig{ID: intID, Provider: domain.IntegrationProviderTelegram, IsActive: true, Config: tgCfg}
	integrations := &fakeTelegramIntegrationSource{byID: map[uuid.UUID]*domain.IntegrationConfig{intID: integration}}

	mgr := NewTelegramBotManager(client, integrations, &fakeNotificationRepo{}, &fakeTelegramUserLookup{}, &fakeTelegramProjectLookup{}, &fakeWorkspaceNameLookup{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr.rootCtx = ctx // simulate Start() having run, without its ListActiveByProvider call

	mgr.EnsureRunning(intID)
	mgr.EnsureRunning(intID)
	mgr.EnsureRunning(intID)

	require.Eventually(t, func() bool { return atomic.LoadInt32(&pollCount) > 0 }, time.Second, 10*time.Millisecond)

	mgr.mu.Lock()
	running := len(mgr.cancels)
	mgr.mu.Unlock()
	assert.Equal(t, 1, running, "three EnsureRunning calls started more than one poller")
}

func rEndsWith(path, suffix string) bool {
	return len(path) >= len(suffix) && path[len(path)-len(suffix):] == suffix
}
