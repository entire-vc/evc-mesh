package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// --- fakes specific to the bind flow ------------------------------------------

type fakeTelegramIntegrationSource struct {
	active []domain.IntegrationConfig
	byID   map[uuid.UUID]*domain.IntegrationConfig
}

func (f *fakeTelegramIntegrationSource) ListActiveByProvider(context.Context, domain.IntegrationProvider) ([]domain.IntegrationConfig, error) {
	return f.active, nil
}

func (f *fakeTelegramIntegrationSource) GetByID(_ context.Context, id uuid.UUID) (*domain.IntegrationConfig, error) {
	return f.byID[id], nil
}

type fakeTelegramUserLookup struct {
	users map[uuid.UUID]*domain.User
}

func (f *fakeTelegramUserLookup) GetByID(_ context.Context, id uuid.UUID) (*domain.User, error) {
	return f.users[id], nil
}

type fakeTelegramProjectLookup struct {
	byWorkspaceUser map[uuid.UUID][]domain.Project // keyed by userID; test scenarios use one workspace
}

func (f *fakeTelegramProjectLookup) GetByID(context.Context, uuid.UUID) (*domain.Project, error) {
	return nil, nil
}

func (f *fakeTelegramProjectLookup) ListForUserInWorkspace(_ context.Context, _, userID uuid.UUID) ([]domain.Project, error) {
	return f.byWorkspaceUser[userID], nil
}

func newTestManager(notifRepo notificationRepository, client TelegramClient, users userEmailLookup, projects projectLookup, workspaces workspaceNameLookup) *TelegramBotManager {
	return NewTelegramBotManager(client, &fakeTelegramIntegrationSource{}, notifRepo, users, projects, workspaces)
}

func telegramPrefWithBind(wsID, userID uuid.UUID, username, bindToken string, expiresIn time.Duration) domain.NotificationPreference {
	var expires *time.Time
	if bindToken != "" {
		t := time.Now().Add(expiresIn)
		expires = &t
	}
	cfg, _ := json.Marshal(TelegramPreferenceConfig{Username: username, BindToken: bindToken, BindExpiresAt: expires})
	return domain.NotificationPreference{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		UserID:      &userID,
		Channel:     "telegram",
		Events:      pq.StringArray{"comment.created"},
		IsEnabled:   true,
		Config:      cfg,
	}
}

// --- tests: handleStart -------------------------------------------------------

func TestHandleStart_ValidTokenBinds(t *testing.T) {
	wsID, userID := uuid.New(), uuid.New()
	repo := &fakeNotificationRepo{
		prefs: []domain.NotificationPreference{telegramPrefWithBind(wsID, userID, "", "good-token", 15*time.Minute)},
	}
	client := &fakeTelegramClient{}
	users := &fakeTelegramUserLookup{users: map[uuid.UUID]*domain.User{userID: {ID: userID, Name: "Alice"}}}
	workspaces := &fakeWorkspaceNameLookup{names: map[uuid.UUID]string{wsID: "Acme"}}
	projects := &fakeTelegramProjectLookup{byWorkspaceUser: map[uuid.UUID][]domain.Project{
		userID: {{Name: "Website"}, {Name: "Mobile"}},
	}}
	mgr := newTestManager(repo, client, users, projects, workspaces)

	mgr.handleStart(context.Background(), wsID, "bot-token", 999, "whoever", "good-token")

	upserted := repo.upsertedPrefs()
	require.Len(t, upserted, 1)
	var cfg TelegramPreferenceConfig
	require.NoError(t, json.Unmarshal(upserted[0].Config, &cfg))
	assert.EqualValues(t, 999, cfg.ChatID)
	assert.Empty(t, cfg.BindToken, "bind token was not cleared after binding")
	assert.Nil(t, cfg.BindExpiresAt)

	msgs := client.messages()
	require.Len(t, msgs, 1)
	assert.EqualValues(t, 999, msgs[0].chatID)
	assert.Contains(t, msgs[0].text, "Alice")
	assert.Contains(t, msgs[0].text, "- Acme / Website")
	assert.Contains(t, msgs[0].text, "- Acme / Mobile")
}

func TestHandleStart_NoProjectsSaysSo(t *testing.T) {
	wsID, userID := uuid.New(), uuid.New()
	repo := &fakeNotificationRepo{
		prefs: []domain.NotificationPreference{telegramPrefWithBind(wsID, userID, "", "good-token", 15*time.Minute)},
	}
	client := &fakeTelegramClient{}
	users := &fakeTelegramUserLookup{users: map[uuid.UUID]*domain.User{userID: {ID: userID, Name: "Alice"}}}
	mgr := newTestManager(repo, client, users, &fakeTelegramProjectLookup{}, &fakeWorkspaceNameLookup{})

	mgr.handleStart(context.Background(), wsID, "bot-token", 999, "whoever", "good-token")

	msgs := client.messages()
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0].text, "пока нет проектов")
}

func TestHandleStart_ExpiredTokenDoesNotBind(t *testing.T) {
	wsID, userID := uuid.New(), uuid.New()
	repo := &fakeNotificationRepo{
		prefs: []domain.NotificationPreference{telegramPrefWithBind(wsID, userID, "", "stale-token", -time.Minute)},
	}
	client := &fakeTelegramClient{}
	mgr := newTestManager(repo, client, &fakeTelegramUserLookup{}, &fakeTelegramProjectLookup{}, &fakeWorkspaceNameLookup{})

	mgr.handleStart(context.Background(), wsID, "bot-token", 999, "whoever", "stale-token")

	assert.Empty(t, repo.upsertedPrefs(), "an expired token bound a chat_id")
	msgs := client.messages()
	require.Len(t, msgs, 1, "expired token should still get the neutral reply")
	assert.NotContains(t, msgs[0].text, "999")
}

func TestHandleStart_UnknownTokenDoesNotBind(t *testing.T) {
	wsID, userID := uuid.New(), uuid.New()
	repo := &fakeNotificationRepo{
		prefs: []domain.NotificationPreference{telegramPrefWithBind(wsID, userID, "", "real-token", 15*time.Minute)},
	}
	client := &fakeTelegramClient{}
	mgr := newTestManager(repo, client, &fakeTelegramUserLookup{}, &fakeTelegramProjectLookup{}, &fakeWorkspaceNameLookup{})

	mgr.handleStart(context.Background(), wsID, "bot-token", 999, "stranger", "wrong-token")

	assert.Empty(t, repo.upsertedPrefs())
	require.Len(t, client.messages(), 1)
}

func TestHandleStart_NoTokenGetsNeutralReplyOnly(t *testing.T) {
	wsID := uuid.New()
	repo := &fakeNotificationRepo{} // GetPreferencesByWorkspace should not even be needed
	client := &fakeTelegramClient{}
	mgr := newTestManager(repo, client, &fakeTelegramUserLookup{}, &fakeTelegramProjectLookup{}, &fakeWorkspaceNameLookup{})

	mgr.handleStart(context.Background(), wsID, "bot-token", 999, "stranger", "")

	assert.Empty(t, repo.upsertedPrefs())
	require.Len(t, client.messages(), 1)
}

func TestHandleStart_UsernameMismatchRefusesEvenWithRightToken(t *testing.T) {
	wsID, userID := uuid.New(), uuid.New()
	repo := &fakeNotificationRepo{
		prefs: []domain.NotificationPreference{telegramPrefWithBind(wsID, userID, "alice", "good-token", 15*time.Minute)},
	}
	client := &fakeTelegramClient{}
	mgr := newTestManager(repo, client, &fakeTelegramUserLookup{}, &fakeTelegramProjectLookup{}, &fakeWorkspaceNameLookup{})

	// Right token, but the Telegram account sending /start claims a different
	// username than the one recorded in Notification Settings.
	mgr.handleStart(context.Background(), wsID, "bot-token", 999, "mallory", "good-token")

	assert.Empty(t, repo.upsertedPrefs(), "bound despite a username mismatch")
}

func TestHandleStart_UsernameMatchIsCaseAndAtInsensitive(t *testing.T) {
	wsID, userID := uuid.New(), uuid.New()
	repo := &fakeNotificationRepo{
		prefs: []domain.NotificationPreference{telegramPrefWithBind(wsID, userID, "@Alice", "good-token", 15*time.Minute)},
	}
	client := &fakeTelegramClient{}
	users := &fakeTelegramUserLookup{users: map[uuid.UUID]*domain.User{userID: {ID: userID}}}
	mgr := newTestManager(repo, client, users, &fakeTelegramProjectLookup{}, &fakeWorkspaceNameLookup{})

	mgr.handleStart(context.Background(), wsID, "bot-token", 999, "alice", "good-token")

	require.Len(t, repo.upsertedPrefs(), 1, "case/@ differences should not block a legitimate match")
}

// --- tests: handleUpdate -------------------------------------------------------

func TestHandleUpdate_IgnoresEverythingButStart(t *testing.T) {
	wsID := uuid.New()
	client := &fakeTelegramClient{}
	mgr := newTestManager(&fakeNotificationRepo{}, client, &fakeTelegramUserLookup{}, &fakeTelegramProjectLookup{}, &fakeWorkspaceNameLookup{})

	mgr.handleUpdate(context.Background(), wsID, "bot-token", TelegramUpdate{
		Message: &TelegramMessage{Text: "hello there", Chat: TelegramChat{ID: 1}, From: &TelegramUser{Username: "someone"}},
	})

	assert.Empty(t, client.messages(), "a non-/start message got a reply — spec requires silence")
}

func TestHandleUpdate_IgnoresMessagesWithNoSender(t *testing.T) {
	wsID := uuid.New()
	client := &fakeTelegramClient{}
	mgr := newTestManager(&fakeNotificationRepo{}, client, &fakeTelegramUserLookup{}, &fakeTelegramProjectLookup{}, &fakeWorkspaceNameLookup{})

	mgr.handleUpdate(context.Background(), wsID, "bot-token", TelegramUpdate{
		Message: &TelegramMessage{Text: "/start abc", Chat: TelegramChat{ID: 1}, From: nil},
	})

	assert.Empty(t, client.messages())
}
