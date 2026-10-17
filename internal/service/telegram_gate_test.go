package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// The instance switch is asked at the moment of use: a poller whose gate is
// off makes no getUpdates call, and picks polling up again when the gate turns
// on, without a restart of the manager.
func TestTelegramBotManager_GateOffPausesPollingAndOnResumesIt(t *testing.T) {
	old := telegramGateIdle
	telegramGateIdle = 20 * time.Millisecond
	defer func() { telegramGateIdle = old }()

	var polls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rEndsWith(r.URL.Path, "/getUpdates") {
			atomic.AddInt32(&polls, 1)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []map[string]any{}})
	}))
	defer srv.Close()

	wsID, intID := uuid.New(), uuid.New()
	tgCfg, _ := json.Marshal(TelegramIntegrationConfig{BotToken: "test-token", BotUsername: "mesh_bot"})
	integration := &domain.IntegrationConfig{ID: intID, WorkspaceID: wsID, Provider: domain.IntegrationProviderTelegram, IsActive: true, Config: tgCfg}
	integrations := &fakeTelegramIntegrationSource{
		active: []domain.IntegrationConfig{*integration},
		byID:   map[uuid.UUID]*domain.IntegrationConfig{intID: integration},
	}

	var on atomic.Bool
	mgr := NewTelegramBotManager(NewTelegramClientWithBaseURL(srv.URL), integrations, &fakeNotificationRepo{},
		&fakeTelegramUserLookup{}, &fakeTelegramProjectLookup{}, &fakeWorkspaceNameLookup{}).
		WithGate(func(context.Context) bool { return on.Load() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Start(ctx)

	time.Sleep(200 * time.Millisecond)
	assert.Zero(t, atomic.LoadInt32(&polls), "a switched-off instance must not poll Telegram")

	on.Store(true)
	require.Eventually(t, func() bool { return atomic.LoadInt32(&polls) > 0 }, 2*time.Second, 10*time.Millisecond,
		"switching the gate back on did not resume polling")
}

func TestNotificationService_TelegramGateOffReportsChannelOff(t *testing.T) {
	var off atomic.Bool
	off.Store(true)
	integrations := &fakeTelegramIntegrationLookup{cfg: telegramIntegration(true, "bot-token")}
	svc := NewNotificationService(&fakeNotificationRepo{},
		WithTelegramService(&fakeTelegramClient{username: "mesh_bot"}, integrations, nil, nil),
		WithTelegramGate(func(context.Context) bool { return !off.Load() })).(*notificationService)

	ok, reason := svc.TelegramReachable(context.Background(), uuid.New())
	assert.False(t, ok)
	assert.Contains(t, reason, "not enabled on this instance")
	_, has := svc.TelegramBotInfo(context.Background(), uuid.New())
	assert.False(t, has)

	off.Store(false)
	ok, _ = svc.TelegramReachable(context.Background(), uuid.New())
	assert.True(t, ok)
	_, has = svc.TelegramBotInfo(context.Background(), uuid.New())
	assert.True(t, has)
}

// The poller takes its token from the extension resolver, not from the raw row,
// and pauses while the resolver says the channel is off for the workspace.
func TestTelegramBotManager_PollsWithResolverToken(t *testing.T) {
	old := telegramGateIdle
	telegramGateIdle = 20 * time.Millisecond
	defer func() { telegramGateIdle = old }()

	var viaResolver, viaRow int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "bot"+"resolver-token/getUpdates"):
			atomic.AddInt32(&viaResolver, 1)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			atomic.AddInt32(&viaRow, 1)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []map[string]any{}})
	}))
	defer srv.Close()

	wsID, intID := uuid.New(), uuid.New()
	tgCfg, _ := json.Marshal(TelegramIntegrationConfig{BotToken: "row-token", BotUsername: "mesh_bot"})
	integration := &domain.IntegrationConfig{ID: intID, WorkspaceID: wsID, Provider: domain.IntegrationProviderTelegram, IsActive: true, Config: tgCfg}
	integrations := &fakeTelegramIntegrationSource{
		active: []domain.IntegrationConfig{*integration},
		byID:   map[uuid.UUID]*domain.IntegrationConfig{intID: integration},
	}

	var on atomic.Bool
	mgr := NewTelegramBotManager(NewTelegramClientWithBaseURL(srv.URL), integrations, &fakeNotificationRepo{},
		&fakeTelegramUserLookup{}, &fakeTelegramProjectLookup{}, &fakeWorkspaceNameLookup{}).
		WithBotResolver(func(_ context.Context, ws uuid.UUID) (string, string, bool) {
			if ws != wsID || !on.Load() {
				return "", "", false
			}
			return "resolver-token", "mesh_bot", true
		})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Start(ctx)

	time.Sleep(200 * time.Millisecond)
	assert.Zero(t, atomic.LoadInt32(&viaResolver)+atomic.LoadInt32(&viaRow), "channel off for the workspace: no poll")

	on.Store(true)
	require.Eventually(t, func() bool { return atomic.LoadInt32(&viaResolver) > 0 }, 2*time.Second, 10*time.Millisecond)
	assert.Zero(t, atomic.LoadInt32(&viaRow), "the raw row token must not be used when a resolver is set")
}
