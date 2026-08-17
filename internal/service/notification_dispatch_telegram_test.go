package service

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// --- fake Telegram client ----------------------------------------------------

type sentTelegramMessage struct {
	token  string
	chatID int64
	text   string
}

type fakeTelegramClient struct {
	mu       sync.Mutex
	sent     []sentTelegramMessage
	sendErr  error
	meErr    error
	username string
}

func (f *fakeTelegramClient) GetMe(context.Context, string) (string, error) {
	if f.meErr != nil {
		return "", f.meErr
	}
	return f.username, nil
}

func (f *fakeTelegramClient) SendMessage(_ context.Context, token string, chatID int64, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, sentTelegramMessage{token: token, chatID: chatID, text: text})
	return nil
}

func (f *fakeTelegramClient) GetUpdates(context.Context, string, int64, int) ([]TelegramUpdate, error) {
	return nil, nil
}

func (f *fakeTelegramClient) messages() []sentTelegramMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]sentTelegramMessage, len(f.sent))
	copy(out, f.sent)
	return out
}

var _ TelegramClient = (*fakeTelegramClient)(nil)

// --- fake integration/workspace/project lookups ------------------------------

type fakeTelegramIntegrationLookup struct {
	cfg *domain.IntegrationConfig
	err error
}

func (f *fakeTelegramIntegrationLookup) GetByProvider(context.Context, uuid.UUID, domain.IntegrationProvider) (*domain.IntegrationConfig, error) {
	return f.cfg, f.err
}

func telegramIntegration(active bool, token string) *domain.IntegrationConfig {
	cfg, _ := json.Marshal(TelegramIntegrationConfig{BotToken: token, BotUsername: "mesh_bot"})
	return &domain.IntegrationConfig{
		ID:       uuid.New(),
		Provider: domain.IntegrationProviderTelegram,
		IsActive: active,
		Config:   cfg,
	}
}

type fakeWorkspaceNameLookup struct {
	names map[uuid.UUID]string
}

func (f *fakeWorkspaceNameLookup) GetByID(_ context.Context, id uuid.UUID) (*domain.Workspace, error) {
	return &domain.Workspace{ID: id, Name: f.names[id]}, nil
}

type fakeProjectNameLookup struct {
	names map[uuid.UUID]string
}

func (f *fakeProjectNameLookup) GetByID(_ context.Context, id uuid.UUID) (*domain.Project, error) {
	return &domain.Project{ID: id, Name: f.names[id]}, nil
}

func telegramPref(wsID, userID uuid.UUID, chatID int64) domain.NotificationPreference {
	cfg, _ := json.Marshal(TelegramPreferenceConfig{Username: "member", ChatID: chatID})
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

// --- tests --------------------------------------------------------------------

// TestDispatch_TelegramDeliversToChatID: a bound (chat_id set) subscriber in the
// workspace receives the message, formatted per spec.
func TestDispatch_TelegramDeliversToChatID(t *testing.T) {
	wsID := uuid.New()
	projID := uuid.New()
	member := uuid.New()

	client := &fakeTelegramClient{}
	integrations := &fakeTelegramIntegrationLookup{cfg: telegramIntegration(true, "bot-token")}
	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{telegramPref(wsID, member, 555)},
		members: map[uuid.UUID]bool{member: true},
	}
	workspaces := &fakeWorkspaceNameLookup{names: map[uuid.UUID]string{wsID: "Acme"}}
	projects := &fakeProjectNameLookup{names: map[uuid.UUID]string{projID: "Mesh"}}
	svc := NewNotificationService(repo, WithTelegramService(client, integrations, workspaces, projects)).(*notificationService)

	event := commentEvent(wsID)
	event.ProjectID = &projID
	event.Labels = []string{"bug", "urgent"}
	svc.dispatch(event)

	require.Eventually(t, func() bool { return len(client.messages()) > 0 }, time.Second, 5*time.Millisecond)
	msgs := client.messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, "bot-token", msgs[0].token)
	assert.EqualValues(t, 555, msgs[0].chatID)
	assert.Equal(t, "Acme | Mesh\nbug urgent\n\nNew comment\n\nthe confidential contents of somebody else's comment", msgs[0].text)
}

// TestDispatch_TelegramSkipsUnboundSubscriber: enabled + username set is not
// enough — chat_id is only ever written by a successful /start.
func TestDispatch_TelegramSkipsUnboundSubscriber(t *testing.T) {
	wsID := uuid.New()
	member := uuid.New()

	client := &fakeTelegramClient{}
	integrations := &fakeTelegramIntegrationLookup{cfg: telegramIntegration(true, "bot-token")}
	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{telegramPref(wsID, member, 0)}, // chat_id unset
		members: map[uuid.UUID]bool{member: true},
	}
	svc := NewNotificationService(repo, WithTelegramService(client, integrations, nil, nil)).(*notificationService)

	svc.dispatch(commentEvent(wsID))

	time.Sleep(20 * time.Millisecond)
	assert.Empty(t, client.messages(), "message sent to a subscriber who never completed /start")
}

// TestDispatch_TelegramSkipsStrangers: same membership rule as every other
// channel — a preference row is a claim, not an entitlement.
func TestDispatch_TelegramSkipsStrangers(t *testing.T) {
	wsID := uuid.New()
	member := uuid.New()
	stranger := uuid.New()

	client := &fakeTelegramClient{}
	integrations := &fakeTelegramIntegrationLookup{cfg: telegramIntegration(true, "bot-token")}
	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{telegramPref(wsID, member, 111), telegramPref(wsID, stranger, 222)},
		members: map[uuid.UUID]bool{member: true},
	}
	svc := NewNotificationService(repo, WithTelegramService(client, integrations, nil, nil)).(*notificationService)

	svc.dispatch(commentEvent(wsID))

	require.Eventually(t, func() bool { return len(client.messages()) > 0 }, time.Second, 5*time.Millisecond)
	msgs := client.messages()
	require.Len(t, msgs, 1)
	assert.EqualValues(t, 111, msgs[0].chatID)
}

// TestDispatch_TelegramSkippedWhenNoActiveBot: the channel is silent when the
// workspace has no active bot configured — no token, wrong provider, inactive,
// or the lookup errored.
func TestDispatch_TelegramSkippedWhenNoActiveBot(t *testing.T) {
	wsID := uuid.New()
	member := uuid.New()

	client := &fakeTelegramClient{}
	integrations := &fakeTelegramIntegrationLookup{cfg: telegramIntegration(false, "bot-token")} // inactive
	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{telegramPref(wsID, member, 555)},
		members: map[uuid.UUID]bool{member: true},
	}
	svc := NewNotificationService(repo, WithTelegramService(client, integrations, nil, nil)).(*notificationService)

	svc.dispatch(commentEvent(wsID))

	time.Sleep(20 * time.Millisecond)
	assert.Empty(t, client.messages(), "message sent despite the workspace's bot being inactive")
}

// TestDispatch_TelegramTargetUserIDGatesDeliveryToo: same targeted-event rule as
// the other three channels.
func TestDispatch_TelegramTargetUserIDGatesDeliveryToo(t *testing.T) {
	wsID := uuid.New()
	reviewer := uuid.New()
	bystander := uuid.New()

	reviewerPref := telegramPref(wsID, reviewer, 1)
	reviewerPref.Events = pq.StringArray{"task.reviewer_assigned"}
	bystanderPref := telegramPref(wsID, bystander, 2)
	bystanderPref.Events = pq.StringArray{"task.reviewer_assigned"}

	client := &fakeTelegramClient{}
	integrations := &fakeTelegramIntegrationLookup{cfg: telegramIntegration(true, "bot-token")}
	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{reviewerPref, bystanderPref},
		members: map[uuid.UUID]bool{reviewer: true, bystander: true},
	}
	svc := NewNotificationService(repo, WithTelegramService(client, integrations, nil, nil)).(*notificationService)

	event := domain.NotificationEvent{
		WorkspaceID:  wsID,
		EventType:    "task.reviewer_assigned",
		Title:        "Review requested",
		TargetUserID: &reviewer,
	}
	svc.dispatch(event)

	require.Eventually(t, func() bool { return len(client.messages()) > 0 }, time.Second, 5*time.Millisecond)
	msgs := client.messages()
	require.Len(t, msgs, 1)
	assert.EqualValues(t, 1, msgs[0].chatID)
}

// TestDispatch_TelegramBlockedBotDisablesChannel: a 403 from Telegram means the
// recipient blocked the bot — dispatch must turn the channel off for them
// rather than retry a dead chat_id on every future event.
func TestDispatch_TelegramBlockedBotDisablesChannel(t *testing.T) {
	wsID := uuid.New()
	member := uuid.New()

	client := &fakeTelegramClient{sendErr: &TelegramAPIError{StatusCode: http.StatusForbidden, Description: "bot was blocked by the user"}}
	integrations := &fakeTelegramIntegrationLookup{cfg: telegramIntegration(true, "bot-token")}
	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{telegramPref(wsID, member, 555)},
		members: map[uuid.UUID]bool{member: true},
	}
	svc := NewNotificationService(repo, WithTelegramService(client, integrations, nil, nil)).(*notificationService)

	svc.dispatch(commentEvent(wsID))

	require.Eventually(t, func() bool { return len(repo.upsertedPrefs()) > 0 }, time.Second, 5*time.Millisecond)
	upserted := repo.upsertedPrefs()
	require.Len(t, upserted, 1)
	assert.Equal(t, "telegram", upserted[0].Channel)
	assert.Equal(t, member, *upserted[0].UserID)
	assert.False(t, upserted[0].IsEnabled, "channel was not disabled after a 403 from Telegram")
}

// TestDispatch_TelegramNonForbiddenErrorDoesNotDisableChannel: a transient
// failure (not a block) must not silently unsubscribe the user.
func TestDispatch_TelegramNonForbiddenErrorDoesNotDisableChannel(t *testing.T) {
	wsID := uuid.New()
	member := uuid.New()

	client := &fakeTelegramClient{sendErr: &TelegramAPIError{StatusCode: http.StatusTooManyRequests, Description: "flood control"}}
	integrations := &fakeTelegramIntegrationLookup{cfg: telegramIntegration(true, "bot-token")}
	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{telegramPref(wsID, member, 555)},
		members: map[uuid.UUID]bool{member: true},
	}
	svc := NewNotificationService(repo, WithTelegramService(client, integrations, nil, nil)).(*notificationService)

	svc.dispatch(commentEvent(wsID))

	time.Sleep(20 * time.Millisecond)
	assert.Empty(t, repo.upsertedPrefs(), "a transient 429 disabled the channel — only a 403 (blocked) should")
}

// TestTelegramBotInfo reflects the workspace's configured bot back to the
// settings page, or reports unavailable when there is none.
func TestTelegramBotInfo(t *testing.T) {
	wsID := uuid.New()

	t.Run("active bot", func(t *testing.T) {
		integrations := &fakeTelegramIntegrationLookup{cfg: telegramIntegration(true, "bot-token")}
		svc := NewNotificationService(&fakeNotificationRepo{}, WithTelegramService(&fakeTelegramClient{}, integrations, nil, nil))

		username, available := svc.TelegramBotInfo(context.Background(), wsID)
		assert.True(t, available)
		assert.Equal(t, "mesh_bot", username)
	})

	t.Run("no telegram service wired", func(t *testing.T) {
		svc := NewNotificationService(&fakeNotificationRepo{})

		_, available := svc.TelegramBotInfo(context.Background(), wsID)
		assert.False(t, available)
	})

	t.Run("inactive integration", func(t *testing.T) {
		integrations := &fakeTelegramIntegrationLookup{cfg: telegramIntegration(false, "bot-token")}
		svc := NewNotificationService(&fakeNotificationRepo{}, WithTelegramService(&fakeTelegramClient{}, integrations, nil, nil))

		_, available := svc.TelegramBotInfo(context.Background(), wsID)
		assert.False(t, available)
	})
}
