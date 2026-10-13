package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// localeUsers resolves users with an explicit or implicit language.
type localeUsers struct{ users map[uuid.UUID]*domain.User }

func (l *localeUsers) GetByID(_ context.Context, id uuid.UUID) (*domain.User, error) {
	return l.users[id], nil
}

type localeWorkspaces struct{ locale string }

func (l *localeWorkspaces) GetByID(_ context.Context, id uuid.UUID) (*domain.Workspace, error) {
	return &domain.Workspace{ID: id, Name: "Acme", DefaultLocale: l.locale}, nil
}

func localePref(ws, user uuid.UUID, channel string, cfg []byte) domain.NotificationPreference {
	return domain.NotificationPreference{
		ID: uuid.New(), WorkspaceID: ws, UserID: &user, Channel: channel,
		Events: pq.StringArray{"task.assigned"}, IsEnabled: true, Config: cfg,
	}
}

func assignedEvent(ws uuid.UUID) domain.NotificationEvent {
	taskID := uuid.New()
	return domain.NotificationEvent{
		WorkspaceID:   ws,
		TaskID:        &taskID,
		EventType:     "task.assigned",
		Title:         "Task assigned: Fix login",
		MessageCode:   "notif.task.assigned",
		MessageParams: map[string]any{"task": "Fix login"},
	}
}

// One event, three recipients — English by choice, Russian by choice, and one who
// never chose a language in a Russian workspace. In-app, email and Telegram must
// each arrive in the recipient's language.
func TestDispatch_RendersPerRecipientLocale(t *testing.T) {
	ws := uuid.New()
	en, ru, none := uuid.New(), uuid.New(), uuid.New()
	users := &localeUsers{users: map[uuid.UUID]*domain.User{
		en:   {ID: en, Email: "en@example.com", Locale: "en", LocaleSetExplicitly: true},
		ru:   {ID: ru, Email: "ru@example.com", Locale: "ru", LocaleSetExplicitly: true},
		none: {ID: none, Email: "none@example.com", Locale: "en", LocaleSetExplicitly: false},
	}}
	chats := map[uuid.UUID]int64{en: 1, ru: 2, none: 3}

	var prefs []domain.NotificationPreference
	for _, u := range []uuid.UUID{en, ru, none} {
		cfg, _ := json.Marshal(TelegramPreferenceConfig{Username: "x", ChatID: chats[u]})
		prefs = append(prefs, localePref(ws, u, "web_push", nil), localePref(ws, u, "email", nil), localePref(ws, u, "telegram", cfg))
	}
	repo := &fakeNotificationRepo{prefs: prefs, members: map[uuid.UUID]bool{en: true, ru: true, none: true}}
	mailer := &fakeEmailService{enabled: true}
	tg := &fakeTelegramClient{}
	svc := NewNotificationService(repo,
		WithEmailService(mailer, users, "https://mesh.example.com"),
		WithTelegramService(tg, &fakeTelegramIntegrationLookup{cfg: telegramIntegration(true, "tok")}, nil, nil),
		WithLocaleLookups(users, &localeWorkspaces{locale: "ru"}),
	).(*notificationService)

	svc.dispatch(assignedEvent(ws))

	require.Eventually(t, func() bool { return len(tg.messages()) == 3 && len(mailer.recipients()) == 3 }, 2*time.Second, 5*time.Millisecond)

	subjects := map[string]string{}
	bodies := map[string]string{}
	mailer.mu.Lock()
	for _, m := range mailer.sent {
		subjects[m.to], bodies[m.to] = m.subject, m.body
	}
	mailer.mu.Unlock()
	assert.Equal(t, "Task assigned: Fix login", subjects["en@example.com"])
	assert.Contains(t, bodies["en@example.com"], "View task")
	assert.Equal(t, "Назначена задача: Fix login", subjects["ru@example.com"])
	assert.Contains(t, bodies["ru@example.com"], "Открыть задачу")
	// Red control: no explicit locale -> the workspace's language, not the column default "en".
	assert.Equal(t, "Назначена задача: Fix login", subjects["none@example.com"])

	texts := map[int64]string{}
	for _, m := range tg.messages() {
		texts[m.chatID] = m.text
	}
	assert.Contains(t, texts[1], "Task assigned: Fix login")
	assert.Contains(t, texts[2], "Назначена задача: Fix login")
	assert.Contains(t, texts[3], "Назначена задача: Fix login")

	// In-app: the stored row keeps code + params and the English fallback ...
	require.Len(t, repo.created, 3)
	for _, n := range repo.created {
		require.NotNil(t, n.MessageCode)
		assert.Equal(t, "notif.task.assigned", *n.MessageCode)
		assert.JSONEq(t, `{"task":"Fix login"}`, string(n.MessageParams))
		assert.Equal(t, "Task assigned: Fix login", n.Title)
	}
	// ... and is shown in the viewer's language.
	byUser := map[uuid.UUID]domain.Notification{}
	for _, n := range repo.created {
		byUser[*n.UserID] = n
	}
	for u, want := range map[uuid.UUID]string{en: "Task assigned: Fix login", ru: "Назначена задача: Fix login", none: "Назначена задача: Fix login"} {
		n := byUser[u]
		renderStoredNotification(svc.recipientLocale(context.Background(), ws, u), &n)
		assert.Equal(t, want, n.Title)
	}
}

// Notifications written before message codes existed are displayed as stored.
func TestRenderStoredNotification_LegacyRowUnchanged(t *testing.T) {
	n := domain.Notification{Title: "Old title", Body: "Old body"}
	renderStoredNotification("ru", &n)
	assert.Equal(t, "Old title", n.Title)
	assert.Equal(t, "Old body", n.Body)
}

// A code the catalog does not know keeps the event's own text instead of
// showing a key to a person.
func TestRenderEventText_UnknownCodeKeepsFallback(t *testing.T) {
	e := assignedEvent(uuid.New())
	e.MessageCode = "notif.does.not.exist"
	title, _ := renderEventText("ru", e)
	assert.Equal(t, "Task assigned: Fix login", title)
}

// User-authored bodies are never translated.
func TestRenderEventText_UserBodyPreserved(t *testing.T) {
	e := domain.NotificationEvent{
		Title: "New comment on: T", Body: "hello <b>world</b>",
		MessageCode: "notif.comment.created", MessageParams: map[string]any{"task": "T"},
	}
	title, body := renderEventText("ru", e)
	assert.True(t, strings.HasPrefix(title, "Новый комментарий"))
	assert.Equal(t, "hello <b>world</b>", body)
}

func TestBuildInviteEmail_Localized(t *testing.T) {
	assert.Contains(t, buildInviteEmail("ru", "a@b.c", "Acme", "https://x/i"), "Принять приглашение")
	assert.Contains(t, buildInviteEmail("en", "a@b.c", "Acme", "https://x/i"), "Accept Invitation")
}

type unreadRepo struct {
	*fakeNotificationRepo
	rows []domain.Notification
}

func (r *unreadRepo) ListUnread(context.Context, uuid.UUID, int) ([]domain.Notification, error) {
	return r.rows, nil
}

// ListUnread shows coded rows in the viewer's language and leaves legacy rows alone.
func TestListUnread_RendersInViewerLocale(t *testing.T) {
	ws, u := uuid.New(), uuid.New()
	code := "notif.task.assigned"
	repo := &unreadRepo{fakeNotificationRepo: &fakeNotificationRepo{}, rows: []domain.Notification{
		{WorkspaceID: ws, Title: "Task assigned: A", MessageCode: &code, MessageParams: json.RawMessage(`{"task":"A"}`)},
		{WorkspaceID: ws, Title: "Legacy title"},
	}}
	users := &localeUsers{users: map[uuid.UUID]*domain.User{u: {ID: u, Locale: "ru", LocaleSetExplicitly: true}}}
	svc := NewNotificationService(repo, WithLocaleLookups(users, &localeWorkspaces{locale: "en"}))

	items, err := svc.ListUnread(context.Background(), u)
	require.NoError(t, err)
	assert.Equal(t, "Назначена задача: A", items[0].Title)
	assert.Equal(t, "Legacy title", items[1].Title)
}
