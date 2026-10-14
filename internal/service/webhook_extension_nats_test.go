package service

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/eventbus"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// mock: external boundary — NATS JetStream publish.
type fakeExtPublisher struct {
	mu   sync.Mutex
	msgs []publishedExtMsg
}

type publishedExtMsg struct {
	subject string
	body    []byte
}

func (f *fakeExtPublisher) PublishExtensionEvent(_ context.Context, subject string, data []byte, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, publishedExtMsg{subject, data})
	return nil
}

func (f *fakeExtPublisher) snapshot() []publishedExtMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]publishedExtMsg(nil), f.msgs...)
}

type fakeWorkspaces struct {
	repository.WorkspaceRepository
	slug string
}

func (f *fakeWorkspaces) GetByID(_ context.Context, id uuid.UUID) (*domain.Workspace, error) {
	return &domain.Workspace{ID: id, Slug: f.slug}, nil
}

type natsFixture struct {
	svc      *webhookService
	pub      *fakeExtPublisher
	ws       uuid.UUID
	connA    uuid.UUID
	connB    uuid.UUID
	connOff  uuid.UUID
	subsRepo *fakeExtSubs
}

func newNATSFixture() *natsFixture {
	ws := uuid.New()
	connA, connB, connOff := uuid.New(), uuid.New(), uuid.New()
	nats := func(conn uuid.UUID, event string) domain.ExtensionEventSubscription {
		return domain.ExtensionEventSubscription{ID: uuid.New(), ConnectionID: conn, Event: event, Delivery: domain.ExtensionDeliveryNATS}
	}
	subs := &fakeExtSubs{subs: []domain.ExtensionEventSubscription{
		nats(connA, "task.updated"),
		nats(connB, "task.updated"),
		nats(connB, "comment.created"),
		nats(connOff, "task.updated"),
		// A webhook subscription must not produce a NATS message.
		{ID: uuid.New(), ConnectionID: connA, Event: "task.updated", Delivery: domain.ExtensionDeliveryWebhook, Target: "https://x.invalid"},
	}}
	conns := &fakeExtIntegrations{conns: map[uuid.UUID]*domain.IntegrationConfig{
		connA:   {ID: connA, WorkspaceID: ws, IsActive: true},
		connB:   {ID: connB, WorkspaceID: ws, IsActive: true},
		connOff: {ID: connOff, WorkspaceID: ws, IsActive: false},
	}}
	pub := &fakeExtPublisher{}
	svc := &webhookService{
		repo:   &recordingWebhookRepo{},
		client: newWebhookHTTPClient(func(net.IP) bool { return false }),
		ext:    &extensionDelivery{subs: subs, integrations: conns, secrets: &fakeExtSecrets{}},
	}
	svc.SetExtensionNATS(pub, &fakeWorkspaces{slug: "acme"})
	return &natsFixture{svc: svc, pub: pub, ws: ws, connA: connA, connB: connB, connOff: connOff, subsRepo: subs}
}

func (f *natsFixture) waitMsgs(t *testing.T, n int) []publishedExtMsg {
	t.Helper()
	require.Eventually(t, func() bool { return len(f.pub.snapshot()) >= n }, 5*time.Second, 5*time.Millisecond)
	return f.pub.snapshot()
}

// task.updated reaches each subscribed active connection on its own subject and
// nothing else: not the inactive connection, not the webhook subscription.
func TestExtensionNATS_TaskUpdatedOnOwnSubject(t *testing.T) {
	f := newNATSFixture()
	f.svc.Dispatch(context.Background(), f.ws, "task.updated", map[string]any{"task_id": "t1"})

	f.waitMsgs(t, 2)
	time.Sleep(50 * time.Millisecond) // a wrong third message would land by now
	msgs := f.pub.snapshot()
	require.Len(t, msgs, 2)

	got := map[string]publishedExtMsg{}
	for _, m := range msgs {
		got[m.subject] = m
	}
	subjA := eventbus.ExtensionSubject("acme", f.connA, "task.updated")
	subjB := eventbus.ExtensionSubject("acme", f.connB, "task.updated")
	require.Contains(t, got, subjA)
	require.Contains(t, got, subjB)
	assert.Equal(t, "events.acme._ext."+f.connA.String()+".task.updated", subjA)

	var env extensionNATSEnvelope
	require.NoError(t, json.Unmarshal(got[subjA].body, &env))
	assert.Equal(t, "task.updated", env.Event)
	assert.Equal(t, f.ws, env.WorkspaceID)
	assert.JSONEq(t, `{"task_id":"t1"}`, string(env.Payload))
}

// An event the connection did not subscribe to is not delivered, and another
// connection's event never lands on this connection's subject.
func TestExtensionNATS_OnlySubscribedEvents(t *testing.T) {
	f := newNATSFixture()
	f.svc.Dispatch(context.Background(), f.ws, "task.created", map[string]any{})       // nobody subscribed
	f.svc.Dispatch(context.Background(), uuid.New(), "task.updated", map[string]any{}) // other workspace
	f.svc.Dispatch(context.Background(), f.ws, "comment.created", map[string]any{})    // only connB

	f.waitMsgs(t, 1)
	time.Sleep(50 * time.Millisecond)
	msgs := f.pub.snapshot()
	require.Len(t, msgs, 1)
	assert.Equal(t, eventbus.ExtensionSubject("acme", f.connB, "comment.created"), msgs[0].subject)
}

// task.status_changed goes out under its public name.
func TestExtensionNATS_PublicEventName(t *testing.T) {
	f := newNATSFixture()
	f.subsRepo.subs = []domain.ExtensionEventSubscription{{
		ID: uuid.New(), ConnectionID: f.connA, Event: "task.moved", Delivery: domain.ExtensionDeliveryNATS,
	}}
	f.svc.Dispatch(context.Background(), f.ws, "task.status_changed", map[string]any{})

	msgs := f.waitMsgs(t, 1)
	assert.Equal(t, eventbus.ExtensionSubject("acme", f.connA, "task.moved"), msgs[0].subject)
}

// Before the event bus is up there is no publisher: nats subscriptions are
// skipped, nothing panics.
func TestExtensionNATS_NoPublisherIsSkipped(t *testing.T) {
	f := newNATSFixture()
	f.svc.ext.nats = nil
	f.svc.Dispatch(context.Background(), f.ws, "task.updated", map[string]any{})
	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, f.pub.snapshot())
}

type errExtPublisher struct{}

func (errExtPublisher) PublishExtensionEvent(context.Context, string, []byte, string) error {
	return errors.New("stream unavailable")
}

type errWorkspaces struct{ repository.WorkspaceRepository }

func (errWorkspaces) GetByID(context.Context, uuid.UUID) (*domain.Workspace, error) {
	return nil, errors.New("db down")
}

// A failure on the way to the bus is logged and drops that one delivery; it
// never panics and never publishes a half-built message.
func TestExtensionNATS_FailuresPublishNothing(t *testing.T) {
	t.Run("workspace lookup fails", func(t *testing.T) {
		f := newNATSFixture()
		f.svc.SetExtensionNATS(f.pub, errWorkspaces{})
		f.svc.Dispatch(context.Background(), f.ws, "task.updated", map[string]any{})
		time.Sleep(50 * time.Millisecond)
		assert.Empty(t, f.pub.snapshot())
	})
	t.Run("payload is not valid json", func(t *testing.T) {
		f := newNATSFixture()
		sub := f.subsRepo.subs[0]
		f.svc.publishToSubscription(f.ws, sub, "task.updated", []byte("{not json"))
		assert.Empty(t, f.pub.snapshot())
	})
	t.Run("publish fails", func(t *testing.T) {
		f := newNATSFixture()
		f.svc.SetExtensionNATS(errExtPublisher{}, &fakeWorkspaces{slug: "acme"})
		// returns after logging; nothing to assert but that it completes
		f.svc.publishToSubscription(f.ws, f.subsRepo.subs[0], "task.updated", []byte(`{}`))
		assert.Empty(t, f.pub.snapshot())
	})
}

// Without the extensions feature wired there is nothing to attach a publisher
// to, and the call is a no-op.
func TestExtensionNATS_SetOnServiceWithoutExtensions(t *testing.T) {
	svc := &webhookService{repo: &recordingWebhookRepo{}}
	svc.SetExtensionNATS(&fakeExtPublisher{}, &fakeWorkspaces{slug: "acme"})
	assert.Nil(t, svc.ext)
}

// A subscription lookup failure drops the dispatch without touching the bus.
type errExtSubs struct {
	repository.ExtensionEventSubscriptionRepository
}

func (errExtSubs) ListByEvent(context.Context, string) ([]domain.ExtensionEventSubscription, error) {
	return nil, errors.New("db down")
}

func TestExtensionNATS_SubscriptionLookupFailure(t *testing.T) {
	f := newNATSFixture()
	f.svc.ext.subs = errExtSubs{}
	f.svc.Dispatch(context.Background(), f.ws, "task.updated", map[string]any{})
	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, f.pub.snapshot())
}
