package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

type fakeExtSubs struct {
	repository.ExtensionEventSubscriptionRepository
	subs []domain.ExtensionEventSubscription
}

func (f *fakeExtSubs) ListByEvent(_ context.Context, event string) ([]domain.ExtensionEventSubscription, error) {
	var out []domain.ExtensionEventSubscription
	for _, s := range f.subs {
		if s.Event == event {
			out = append(out, s)
		}
	}
	return out, nil
}

type fakeExtIntegrations struct {
	repository.IntegrationRepository
	conns map[uuid.UUID]*domain.IntegrationConfig
}

func (f *fakeExtIntegrations) GetByID(_ context.Context, id uuid.UUID) (*domain.IntegrationConfig, error) {
	return f.conns[id], nil
}

type fakeExtSecrets struct {
	repository.SecretMaterializer
	values []domain.MaterializedSecret
}

func (f *fakeExtSecrets) ResolveCurrentValues(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID) ([]domain.MaterializedSecret, error) {
	return f.values, nil
}

type extFixture struct {
	svc      *webhookService
	repo     *recordingWebhookRepo
	ws       uuid.UUID
	subID    uuid.UUID
	subsRepo *fakeExtSubs
}

func newExtFixture(t *testing.T, target string, secrets []domain.MaterializedSecret) *extFixture {
	t.Helper()
	ws, connID, subID := uuid.New(), uuid.New(), uuid.New()
	subs := &fakeExtSubs{subs: []domain.ExtensionEventSubscription{{
		ID: subID, ConnectionID: connID, Event: "task.updated",
		Delivery: domain.ExtensionDeliveryWebhook, Target: target, SecretRef: "ext-hook-secret",
	}}}
	conns := &fakeExtIntegrations{conns: map[uuid.UUID]*domain.IntegrationConfig{
		connID: {ID: connID, WorkspaceID: ws, IsActive: true},
	}}
	repo := &recordingWebhookRepo{}
	svc := &webhookService{
		repo:     repo,
		client:   newWebhookHTTPClient(func(net.IP) bool { return true }),
		backoffs: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond},
		ext:      &extensionDelivery{subs: subs, integrations: conns, secrets: &fakeExtSecrets{values: secrets}},
	}
	return &extFixture{svc: svc, repo: repo, ws: ws, subID: subID, subsRepo: subs}
}

func (f *extFixture) waitDeliveries(t *testing.T, n int) []*domain.WebhookDelivery {
	t.Helper()
	require.Eventually(t, func() bool {
		f.repo.mu.Lock()
		defer f.repo.mu.Unlock()
		return len(f.repo.deliveries) >= n
	}, 5*time.Second, 5*time.Millisecond)
	f.repo.mu.Lock()
	defer f.repo.mu.Unlock()
	return append([]*domain.WebhookDelivery(nil), f.repo.deliveries...)
}

// validSignature is the documented scheme (docs/webhooks.md): hex HMAC-SHA256
// of "{X-Mesh-Timestamp}.{body}" with Unix-seconds timestamp.
func validSignature(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// A receiver that verifies the signature the way an extension would: Mesh's
// task.updated delivery passes it, and the same receiver rejects a request
// with a corrupted signature (red control, so a pass is not a receiver that
// accepts anything).
func TestExtensionDelivery_SignedTaskUpdated(t *testing.T) {
	const secret = "whsec-test-value"
	var mu sync.Mutex
	var events []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !hmac.Equal([]byte(r.Header.Get("X-Mesh-Signature")), []byte(validSignature(secret, r.Header.Get("X-Mesh-Timestamp"), b))) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		events = append(events, r.Header.Get("X-Mesh-Event"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Red control: a corrupted signature is refused by this receiver.
	body := []byte(`{"task_id":"t1"}`)
	bad := validSignature(secret, "1700000000", body)
	bad = bad[:len(bad)-1] + map[bool]string{true: "1", false: "0"}[bad[len(bad)-1] == '0']
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
	req.Header.Set("X-Mesh-Signature", bad)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	f := newExtFixture(t, srv.URL, []domain.MaterializedSecret{{Name: "ext-hook-secret", Value: secret}})
	f.svc.Dispatch(context.Background(), f.ws, "task.updated", map[string]any{"task_id": "t1"})

	ds := f.waitDeliveries(t, 1)
	require.Len(t, ds, 1)
	assert.True(t, ds[0].Success, "the receiver accepted the signature")
	assert.Equal(t, 1, ds[0].Attempt)
	require.NotNil(t, ds[0].SubscriptionID)
	assert.Equal(t, f.subID, *ds[0].SubscriptionID)
	assert.Equal(t, uuid.Nil, ds[0].WebhookID)
	assert.Equal(t, "task.updated", ds[0].EventType)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"task.updated"}, events)
}

// Every body an extension receives names its workspace (docs/webhooks.md):
// receivers such as Keep pick the signing secret by workspace_id and refuse a
// body without it. A payload that already carries workspace_id keeps it.
func TestExtensionDelivery_BodyCarriesWorkspaceID(t *testing.T) {
	bodies := make(chan map[string]any, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		bodies <- m
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	f := newExtFixture(t, srv.URL, []domain.MaterializedSecret{{Name: "ext-hook-secret", Value: "v"}})
	f.svc.Dispatch(context.Background(), f.ws, "task.updated", map[string]any{"task_id": "t1"})
	got := <-bodies
	assert.Equal(t, f.ws.String(), got["workspace_id"], "workspace_id added to a payload without it")
	assert.Equal(t, "t1", got["task_id"], "the event's own fields are unchanged")

	f.svc.Dispatch(context.Background(), f.ws, "task.updated", map[string]any{"task_id": "t2", "workspace_id": "kept"})
	got = <-bodies
	assert.Equal(t, "kept", got["workspace_id"], "a workspace_id set by the event is not overwritten")
}

// A redelivered event keeps its id (docs/webhooks.md "Event id"): every attempt
// carries the same X-Mesh-Event-Id and the same event_id in the body, so a
// receiver that lost its answer to the first attempt dedups the retry. Two
// different events get different ids.
func TestExtensionDelivery_EventIDStableAcrossRetries(t *testing.T) {
	type seen struct{ header, body string }
	var mu sync.Mutex
	var got []seen
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		bodyID, _ := m["event_id"].(string)
		mu.Lock()
		got = append(got, seen{r.Header.Get("X-Mesh-Event-Id"), bodyID})
		mu.Unlock()
		if hits.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	f := newExtFixture(t, srv.URL, []domain.MaterializedSecret{{Name: "ext-hook-secret", Value: "v"}})
	f.svc.Dispatch(context.Background(), f.ws, "task.updated", map[string]any{"task_id": "t1"})
	ds := f.waitDeliveries(t, 1)
	require.True(t, ds[0].Success)

	mu.Lock()
	first := append([]seen(nil), got...)
	mu.Unlock()
	require.Len(t, first, 3)
	id, err := uuid.Parse(first[0].header)
	require.NoError(t, err, "X-Mesh-Event-Id is a UUID")
	for i, s := range first {
		assert.Equal(t, id.String(), s.header, "attempt %d header", i+1)
		assert.Equal(t, id.String(), s.body, "attempt %d body", i+1)
	}

	f.svc.Dispatch(context.Background(), f.ws, "task.updated", map[string]any{"task_id": "t1"})
	f.waitDeliveries(t, 2)
	mu.Lock()
	defer mu.Unlock()
	assert.NotEqual(t, id.String(), got[len(got)-1].header, "another event gets another id")
}

// 500 from the receiver is retried; webhook_deliveries ends with the delivery
// that finally went through, at attempt 2.
func TestExtensionDelivery_RetriesOn500(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	f := newExtFixture(t, srv.URL, []domain.MaterializedSecret{{Name: "ext-hook-secret", Value: "v"}})
	f.svc.Dispatch(context.Background(), f.ws, "task.updated", map[string]any{"task_id": "t1"})

	ds := f.waitDeliveries(t, 1)
	require.Len(t, ds, 1)
	assert.EqualValues(t, 2, hits.Load())
	assert.True(t, ds[0].Success)
	assert.Equal(t, 2, ds[0].Attempt)
	require.NotNil(t, ds[0].ResponseStatus)
	assert.Equal(t, http.StatusOK, *ds[0].ResponseStatus)
}

func TestExtensionDelivery_AlwaysFailing_RecordedAfterAllAttempts(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	f := newExtFixture(t, srv.URL, []domain.MaterializedSecret{{Name: "ext-hook-secret", Value: "v"}})
	f.svc.Dispatch(context.Background(), f.ws, "task.updated", map[string]any{})

	ds := f.waitDeliveries(t, 1)
	assert.EqualValues(t, 4, hits.Load(), "first try plus the 1s, 5s and 25s retries")
	assert.False(t, ds[0].Success)
	assert.Equal(t, 4, ds[0].Attempt)
}

// A subscription whose secret cannot be resolved is not sent unsigned: it is
// recorded as a failed delivery naming the reason, without the value.
func TestExtensionDelivery_MissingOrExpiredSecret(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer srv.Close()

	for name, secrets := range map[string][]domain.MaterializedSecret{
		"missing": nil,
		"expired": {{Name: "ext-hook-secret", Expired: true}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newExtFixture(t, srv.URL, secrets)
			f.svc.Dispatch(context.Background(), f.ws, "task.updated", map[string]any{})
			ds := f.waitDeliveries(t, 1)
			assert.False(t, ds[0].Success)
			require.NotNil(t, ds[0].ResponseBody)
			assert.Contains(t, *ds[0].ResponseBody, "ext-hook-secret")
		})
	}
	assert.Zero(t, hits.Load(), "nothing is sent without a signing secret")
}

// Another workspace's connection, an inactive one, a nats subscription and a
// different event all get nothing.
func TestExtensionDelivery_OnlyMatchingSubscriptions(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer srv.Close()
	secrets := []domain.MaterializedSecret{{Name: "ext-hook-secret", Value: "v"}}

	f := newExtFixture(t, srv.URL, secrets)
	f.svc.Dispatch(context.Background(), uuid.New(), "task.updated", map[string]any{}) // other workspace
	f.svc.Dispatch(context.Background(), f.ws, "task.created", map[string]any{})       // not subscribed

	f.subsRepo.subs[0].Delivery = domain.ExtensionDeliveryNATS
	f.svc.Dispatch(context.Background(), f.ws, "task.updated", map[string]any{})

	time.Sleep(100 * time.Millisecond)
	f.repo.mu.Lock()
	defer f.repo.mu.Unlock()
	assert.Empty(t, f.repo.deliveries)
	assert.Zero(t, hits.Load())
}

func TestPublicEventName(t *testing.T) {
	assert.Equal(t, "task.moved", publicEventName("task.status_changed"))
	assert.Equal(t, "task.updated", publicEventName("task.updated"))
	assert.Equal(t, "comment.created", publicEventName("comment.created"))
}

// The id minted for the dispatch is the event's id on every transport; an
// event_id the payload already carries is overwritten, so the body never
// disagrees with X-Mesh-Event-Id or the NATS envelope and two events never
// share an identity.
func TestWithEventID(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	want := `{"event_id":"` + id.String() + `","a":1}`

	for _, in := range []string{
		`{"a":1}`,
		`{"event_id":"evt_1","a":1}`,
		`{"event_id":"` + uuid.New().String() + `","a":1}`,
		`{"event_id":"` + uuid.Must(uuid.NewV7()).String() + `","a":1}`,
	} {
		assert.JSONEq(t, want, string(withEventID([]byte(in), id)), in)
	}
	assert.Equal(t, `[1,2]`, string(withEventID([]byte(`[1,2]`), id)))
}

type fakeExtScope struct {
	repository.ExtensionProjectScopeRepository
	allowed map[uuid.UUID]bool
}

func (f *fakeExtScope) AllowsProject(_ context.Context, _, projectID uuid.UUID) (bool, error) {
	return f.allowed[projectID], nil
}

// A project-bound connection receives the events of its bound projects only:
// an event of another project and an event with no project are not delivered,
// and the bound project's event is (the positive control that the filter is not
// simply dropping everything).
func TestExtensionDelivery_ProjectBoundConnection(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer srv.Close()
	secrets := []domain.MaterializedSecret{{Name: "ext-hook-secret", Value: "v"}}

	f := newExtFixture(t, srv.URL, secrets)
	connID := f.subsRepo.subs[0].ConnectionID
	f.svc.ext.integrations.(*fakeExtIntegrations).conns[connID].ProjectScope = domain.ExtensionProjectScopeBound
	bound, other := uuid.New(), uuid.New()

	// Without a scope checker a bound connection gets nothing: fail closed.
	f.svc.Dispatch(context.Background(), f.ws, "task.updated", map[string]any{"project_id": bound})
	time.Sleep(100 * time.Millisecond)
	assert.Zero(t, hits.Load())

	f.svc.extScope = &fakeExtScope{allowed: map[uuid.UUID]bool{bound: true}}
	f.svc.Dispatch(context.Background(), f.ws, "task.updated", map[string]any{"project_id": other})
	f.svc.Dispatch(context.Background(), f.ws, "task.updated", map[string]any{"task_id": "no-project"})
	time.Sleep(100 * time.Millisecond)
	assert.Zero(t, hits.Load(), "foreign-project and project-less events are not delivered")

	f.svc.Dispatch(context.Background(), f.ws, "task.updated", map[string]any{"project_id": bound})
	ds := f.waitDeliveries(t, 1)
	require.Len(t, ds, 1)
	assert.Equal(t, int32(1), hits.Load())

	// Unbinding (the checker now says no) stops deliveries again.
	f.svc.extScope = &fakeExtScope{allowed: map[uuid.UUID]bool{}}
	f.svc.Dispatch(context.Background(), f.ws, "task.updated", map[string]any{"project_id": bound})
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(1), hits.Load())
}

func TestEventProjectID(t *testing.T) {
	id := uuid.New()
	got, ok := eventProjectID([]byte(`{"project_id":"` + id.String() + `"}`))
	assert.True(t, ok)
	assert.Equal(t, id, got)
	for _, body := range []string{`{}`, `{"project_id":null}`, `{"project_id":"00000000-0000-0000-0000-000000000000"}`, `[1]`, `x`} {
		_, ok := eventProjectID([]byte(body))
		assert.False(t, ok, body)
	}
}
