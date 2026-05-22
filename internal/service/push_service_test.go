package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// ---------------------------------------------------------------------------
// In-memory mocks
// ---------------------------------------------------------------------------

type mockPushSubRepo struct {
	mu    sync.Mutex
	subs  map[string]*domain.PushSubscription // keyed by endpoint
	calls struct {
		upsert   int
		delete   int
		delByEP  int
		listUser int
		getByEP  int
	}
}

func newMockPushSubRepo() *mockPushSubRepo {
	return &mockPushSubRepo{subs: make(map[string]*domain.PushSubscription)}
}

func (m *mockPushSubRepo) Upsert(_ context.Context, sub *domain.PushSubscription) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls.upsert++
	if sub.ID == uuid.Nil {
		sub.ID = uuid.New()
	}
	cp := *sub
	m.subs[sub.Endpoint] = &cp
	return nil
}

func (m *mockPushSubRepo) Delete(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls.delete++
	for ep, s := range m.subs {
		if s.ID == id {
			delete(m.subs, ep)
			break
		}
	}
	return nil
}

func (m *mockPushSubRepo) DeleteByEndpoint(_ context.Context, _ uuid.UUID, endpoint string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls.delByEP++
	delete(m.subs, endpoint)
	return nil
}

func (m *mockPushSubRepo) ListByUser(_ context.Context, userID uuid.UUID) ([]domain.PushSubscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls.listUser++
	var out []domain.PushSubscription
	for _, s := range m.subs {
		if s.UserID == userID {
			out = append(out, *s)
		}
	}
	return out, nil
}

func (m *mockPushSubRepo) GetByEndpoint(_ context.Context, endpoint string) (*domain.PushSubscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls.getByEP++
	s := m.subs[endpoint]
	if s == nil {
		return nil, nil
	}
	cp := *s
	return &cp, nil
}

var _ repository.PushSubscriptionRepository = (*mockPushSubRepo)(nil)

type mockNotifPrefsGetter struct {
	prefs []domain.NotificationPreference
}

func (m *mockNotifPrefsGetter) GetPreferencesByWorkspace(_ context.Context, _ uuid.UUID) ([]domain.NotificationPreference, error) {
	return m.prefs, nil
}

// prefFor returns a pref with browser_push channel enabled for the given user and events.
func prefFor(userID uuid.UUID, events ...string) domain.NotificationPreference {
	return domain.NotificationPreference{
		UserID:    &userID,
		Channel:   "browser_push",
		Events:    pq.StringArray(events),
		IsEnabled: true,
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestPushService_Subscribe_NewEndpoint(t *testing.T) {
	repo := newMockPushSubRepo()
	prefs := &mockNotifPrefsGetter{}
	svc := NewPushService(repo, prefs, "pub", "priv", "mailto:test@test.com")

	userID := uuid.New()
	sub, err := svc.Subscribe(context.Background(), userID, "https://push.example.com/sub1", "p256dh==", "auth==", "TestBrowser/1.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sub.ID == uuid.Nil {
		t.Error("expected non-nil subscription ID")
	}
	if repo.calls.upsert != 1 {
		t.Errorf("expected 1 upsert call, got %d", repo.calls.upsert)
	}
}

func TestPushService_Subscribe_Upsert(t *testing.T) {
	repo := newMockPushSubRepo()
	prefs := &mockNotifPrefsGetter{}
	svc := NewPushService(repo, prefs, "pub", "priv", "mailto:test@test.com")

	userID := uuid.New()
	endpoint := "https://push.example.com/sub2"
	_, _ = svc.Subscribe(context.Background(), userID, endpoint, "key1", "auth1", "UA")
	_, _ = svc.Subscribe(context.Background(), userID, endpoint, "key2", "auth2", "UA")

	repo.mu.Lock()
	count := len(repo.subs)
	repo.mu.Unlock()

	if count != 1 {
		t.Errorf("upsert should not create duplicate: expected 1 sub, got %d", count)
	}
}

func TestPushService_SendToUser_PrefsDisabled(t *testing.T) {
	repo := newMockPushSubRepo()
	userID := uuid.New()
	// add a sub
	repo.subs["ep3"] = &domain.PushSubscription{ID: uuid.New(), UserID: userID, Endpoint: "ep3", P256DHKey: "k", AuthKey: "a"}

	disabledPref := domain.NotificationPreference{
		UserID:    &userID,
		Channel:   "browser_push",
		Events:    pq.StringArray{"task.assigned"},
		IsEnabled: false,
	}
	prefs := &mockNotifPrefsGetter{prefs: []domain.NotificationPreference{disabledPref}}
	svc := NewPushService(repo, prefs, "pub", "priv", "mailto:test@test.com")

	// SendToUser should be a no-op due to disabled pref (no real webpush call happens since we won't hit the server)
	// We verify ListByUser is NOT called (short-circuit on pref check)
	_ = svc.SendToUser(context.Background(), userID, uuid.New(), domain.PushPayload{EventType: "task.assigned"})
	// No panic = pass; also verify list was not called
	if repo.calls.listUser != 0 {
		t.Errorf("expected no ListByUser when prefs disabled, got %d calls", repo.calls.listUser)
	}
}

func TestPushService_SendToUser_NoSubs(t *testing.T) {
	repo := newMockPushSubRepo()
	userID := uuid.New()
	prefs := &mockNotifPrefsGetter{prefs: []domain.NotificationPreference{prefFor(userID, "task.assigned")}}
	svc := NewPushService(repo, prefs, "pub", "priv", "mailto:test@test.com")

	err := svc.SendToUser(context.Background(), userID, uuid.New(), domain.PushPayload{EventType: "task.assigned"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No subs → list called but no send
	if repo.calls.listUser != 1 {
		t.Errorf("expected 1 ListByUser call, got %d", repo.calls.listUser)
	}
}

func TestPushService_Unsubscribe(t *testing.T) {
	repo := newMockPushSubRepo()
	prefs := &mockNotifPrefsGetter{}
	svc := NewPushService(repo, prefs, "pub", "priv", "mailto:test@test.com")

	userID := uuid.New()
	endpoint := "https://push.example.com/sub4"
	_, _ = svc.Subscribe(context.Background(), userID, endpoint, "k", "a", "UA")
	if err := svc.Unsubscribe(context.Background(), userID, endpoint); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.calls.delByEP != 1 {
		t.Errorf("expected 1 DeleteByEndpoint call, got %d", repo.calls.delByEP)
	}
	repo.mu.Lock()
	_, exists := repo.subs[endpoint]
	repo.mu.Unlock()
	if exists {
		t.Error("subscription should have been deleted")
	}
}

func TestPushService_GetVAPIDPublicKey_NeverReturnsPrivate(t *testing.T) {
	repo := newMockPushSubRepo()
	prefs := &mockNotifPrefsGetter{}
	svc := NewPushService(repo, prefs, "my-pub-key", "my-priv-key", "mailto:test@test.com")

	key := svc.GetVAPIDPublicKey()
	if key != "my-pub-key" {
		t.Errorf("expected public key, got %q", key)
	}
	if key == "my-priv-key" {
		t.Error("GetVAPIDPublicKey must not return private key")
	}
}

func TestPushService_Disabled_WhenNoKeys(t *testing.T) {
	repo := newMockPushSubRepo()
	userID := uuid.New()
	repo.subs["ep5"] = &domain.PushSubscription{ID: uuid.New(), UserID: userID, Endpoint: "ep5"}
	prefs := &mockNotifPrefsGetter{prefs: []domain.NotificationPreference{prefFor(userID, "task.assigned")}}

	// No VAPID keys → push disabled silently
	svc := NewPushService(repo, prefs, "", "", "")

	err := svc.SendToUser(context.Background(), userID, uuid.New(), domain.PushPayload{EventType: "task.assigned"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No ListByUser should have been called (early return on missing keys)
	if repo.calls.listUser != 0 {
		t.Errorf("expected no calls when keys absent, got %d", repo.calls.listUser)
	}
}

func TestPushService_Subscribe_SetsUserAgent(t *testing.T) {
	repo := newMockPushSubRepo()
	prefs := &mockNotifPrefsGetter{}
	svc := NewPushService(repo, prefs, "pub", "priv", "mailto:test@test.com")

	userID := uuid.New()
	ua := "Mozilla/5.0 TestBrowser"
	sub, err := svc.Subscribe(context.Background(), userID, "https://push.example.com/sub6", "p256dh", "auth", ua)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sub.UserAgent != ua {
		t.Errorf("expected UserAgent %q, got %q", ua, sub.UserAgent)
	}
}

func TestPushService_pushEnabled_DefaultOnWhenNoPrefs(t *testing.T) {
	repo := newMockPushSubRepo()
	prefs := &mockNotifPrefsGetter{prefs: []domain.NotificationPreference{}} // no browser_push prefs
	svc := NewPushService(repo, prefs, "pub", "priv", "mailto:test@test.com")

	ps := svc.(*pushService)
	userID := uuid.New()
	if !ps.pushEnabled(nil, userID, "task.assigned") {
		t.Error("expected pushEnabled=true when no prefs (opt-in default)")
	}
	_ = time.Now() // suppress import warning
}
