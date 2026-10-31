package service

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/extsig"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// denyProjects is a project scope that refuses the listed projects.
type denyProjects struct {
	repository.ExtensionProjectScopeRepository
	denied map[uuid.UUID]bool
}

func (d denyProjects) AllowsProject(_ context.Context, _, projectID uuid.UUID) (bool, error) {
	return !d.denied[projectID], nil
}

type replayFixture struct {
	svc       *webhookService
	repo      *postgres.WebhookRepo
	subs      *postgres.ExtensionEventSubscriptionRepo
	principal *ExtensionPrincipal
	other     *ExtensionPrincipal
	sub       *domain.ExtensionEventSubscription

	mu       sync.Mutex
	status   int
	received []*http.Request
	bodies   [][]byte
}

// newReplayFixture builds a workspace with two connections of one extension
// that consents to task.updated; the first has a webhook subscription to a
// receiver whose answer the test sets.
func newReplayFixture(t *testing.T) *replayFixture {
	t.Helper()
	db := extEventSubTestDB(t)
	ctx := context.Background()

	suffix := uuid.New().String()[:8]
	extID := "evreplay-" + suffix
	owner := &domain.User{
		ID: uuid.New(), Email: "evreplay-" + suffix + "@example.com", PasswordHash: "x",
		Name: "Ext Replay Owner", Username: "evreplay-" + suffix, IsActive: true,
	}
	require.NoError(t, postgres.NewUserRepo(db).Create(ctx, owner))
	ws := &domain.Workspace{ID: uuid.New(), Name: "evreplay " + suffix, Slug: "evreplay-" + suffix, OwnerID: owner.ID}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(ctx, ws))
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM workspaces WHERE id = $1`, ws.ID)
		_, _ = db.Exec(`DELETE FROM installed_extensions WHERE extension_id = $1`, extID)
	})

	manifest, err := json.Marshal(map[string]any{
		"id": extID, "version": "1.0.0", "runtime": "builtin",
		"capabilities": map[string]any{"events": []string{extensions.EventTaskUpdated}},
	})
	require.NoError(t, err)
	installed := postgres.NewInstalledExtensionRepo(db)
	require.NoError(t, installed.UpsertBuiltin(ctx, &domain.InstalledExtension{
		ExtensionID: extID, Version: "1.0.0", Manifest: manifest, TrustLevel: domain.ExtensionTrustBuiltin,
	}))
	integrations := postgres.NewIntegrationRepo(db)
	conn := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws.ID, Provider: domain.IntegrationProvider(extID), Config: []byte("{}"), IsActive: true}
	require.NoError(t, integrations.Upsert(ctx, conn))

	f := &replayFixture{status: http.StatusInternalServerError}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.received = append(f.received, r)
		f.bodies = append(f.bodies, b)
		status := f.status
		f.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)

	f.subs = postgres.NewExtensionEventSubscriptionRepo(db)
	f.sub, err = NewExtensionEventSubscriptionService(f.subs, integrations, installed).Subscribe(ctx, CreateExtensionEventSubscriptionInput{
		ConnectionID: conn.ID, Event: extensions.EventTaskUpdated,
		Delivery: domain.ExtensionDeliveryWebhook, Target: srv.URL, SecretRef: "ext-hook-secret",
	})
	require.NoError(t, err)

	f.repo = postgres.NewWebhookRepo(db)
	f.svc = &webhookService{
		repo:   f.repo,
		client: newWebhookHTTPClient(func(net.IP) bool { return true }),
		ext: &extensionDelivery{
			subs: f.subs, integrations: integrations,
			secrets: &fakeExtSecrets{values: []domain.MaterializedSecret{{Name: "ext-hook-secret", Value: "whsec-replay"}}},
		},
	}
	consented := map[string][]string{"events": {extensions.EventTaskUpdated}}
	f.principal = &ExtensionPrincipal{ConnectionID: conn.ID, WorkspaceID: ws.ID, ExtensionID: extID, Capabilities: consented}
	f.other = &ExtensionPrincipal{ConnectionID: uuid.New(), WorkspaceID: ws.ID, ExtensionID: extID, Capabilities: consented}
	return f
}

// record writes an original, failed delivery of task.updated for project at
// the given age, as the dispatcher would after its retries ran out.
func (f *replayFixture) record(t *testing.T, project uuid.UUID, age time.Duration) uuid.UUID {
	t.Helper()
	status := http.StatusInternalServerError
	d := &domain.WebhookDelivery{
		ID: uuid.New(), SubscriptionID: &f.sub.ID, EventType: extensions.EventTaskUpdated,
		Payload:        []byte(`{"task_id":"` + uuid.NewString() + `","project_id":"` + project.String() + `"}`),
		ResponseStatus: &status, Success: false, Attempt: extensionMaxAttempts,
		CreatedAt: time.Now().Add(-age),
	}
	require.NoError(t, f.repo.CreateDelivery(context.Background(), d))
	return d.ID
}

func (f *replayFixture) list(t *testing.T, p *ExtensionPrincipal, q ExtensionEventsQuery) *ExtensionEventPage {
	t.Helper()
	page, err := f.svc.ListExtensionEvents(context.Background(), p, q)
	require.NoError(t, err)
	return page
}

func deliveryIDs(page *ExtensionEventPage) []uuid.UUID {
	out := []uuid.UUID{}
	for _, it := range page.Items {
		out = append(out, it.DeliveryID)
	}
	return out
}

// A connection that missed events reads them back in order, page by page, and
// a cursor resumes exactly after the last row it saw.
func TestExtensionEvents_FeedPagesInOrder_DB(t *testing.T) {
	f := newReplayFixture(t)
	proj := uuid.New()
	first := f.record(t, proj, 3*time.Minute)
	second := f.record(t, proj, 2*time.Minute)
	third := f.record(t, proj, time.Minute)
	// Inside the settle window: not listed yet, so no cursor can pass it while
	// an older row may still be committing.
	fresh := f.record(t, proj, 0)

	all := f.list(t, f.principal, ExtensionEventsQuery{})
	assert.Equal(t, []uuid.UUID{first, second, third}, deliveryIDs(all))
	assert.NotContains(t, deliveryIDs(all), fresh)
	assert.False(t, all.Items[0].Delivered)
	assert.Equal(t, f.sub.ID, all.Items[0].SubscriptionID)

	p1 := f.list(t, f.principal, ExtensionEventsQuery{Limit: 2})
	assert.Equal(t, []uuid.UUID{first, second}, deliveryIDs(p1))
	assert.True(t, p1.HasMore)
	after, err := ParseExtensionEventSince(p1.NextCursor)
	require.NoError(t, err)
	p2 := f.list(t, f.principal, ExtensionEventsQuery{After: after, Limit: 2})
	assert.Equal(t, []uuid.UUID{third}, deliveryIDs(p2))
	assert.False(t, p2.HasMore)

	// A page that holds exactly the last rows has nothing more after it.
	exact := f.list(t, f.principal, ExtensionEventsQuery{Limit: 3})
	assert.Equal(t, []uuid.UUID{first, second, third}, deliveryIDs(exact))
	assert.False(t, exact.HasMore)

	// since as a time: events created at or after it.
	at, err := ParseExtensionEventSince(time.Now().Add(-150 * time.Second).UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{second, third}, deliveryIDs(f.list(t, f.principal, ExtensionEventsQuery{After: at})))
}

// Rights are applied when the feed is read, not when the event was sent: an
// event no longer consented, a project outside the scope, another connection
// and a removed subscription all hide the rows. The cursor still moves past
// hidden rows so a page of them never stalls the reader.
func TestExtensionEvents_FeedReappliesCurrentRights_DB(t *testing.T) {
	f := newReplayFixture(t)
	allowed, denied := uuid.New(), uuid.New()
	a := f.record(t, allowed, 2*time.Minute)
	b := f.record(t, denied, time.Minute)

	// Positive control: without a scope both projects are listed.
	require.Len(t, f.list(t, f.principal, ExtensionEventsQuery{}).Items, 2)

	f.svc.extScope = denyProjects{denied: map[uuid.UUID]bool{denied: true}}
	assert.Equal(t, []uuid.UUID{a}, deliveryIDs(f.list(t, f.principal, ExtensionEventsQuery{})))

	revoked := *f.principal
	revoked.Capabilities = map[string][]string{"events": {}}
	page := f.list(t, &revoked, ExtensionEventsQuery{Limit: 1})
	assert.Empty(t, page.Items)
	assert.False(t, page.HasMore, "every remaining row is hidden, so nothing readable follows")
	moved, err := ParseExtensionEventSince(page.NextCursor)
	require.NoError(t, err)
	assert.Equal(t, b, moved.ID, "the cursor advances past every hidden row")

	assert.Empty(t, f.list(t, f.other, ExtensionEventsQuery{}).Items, "another connection sees nothing")

	require.NoError(t, f.subs.Delete(context.Background(), f.sub.ID))
	f.svc.extScope = nil
	assert.Empty(t, f.list(t, f.principal, ExtensionEventsQuery{}).Items, "unsubscribed events are gone")
}

// A redelivery goes to the subscription's receiver with the ORIGINAL
// X-Mesh-Delivery and a valid signature, is recorded as a new row pointing at
// the original, and marks the event delivered in the feed without listing the
// resend as an event of its own.
func TestExtensionEvents_Redeliver_DB(t *testing.T) {
	f := newReplayFixture(t)
	ctx := context.Background()
	proj := uuid.New()
	orig := f.record(t, proj, time.Minute)

	// Receiver still failing: the resend is recorded, the event stays undelivered.
	res, err := f.svc.RedeliverExtensionEvent(ctx, f.principal, orig)
	require.NoError(t, err)
	assert.False(t, res.Success)
	require.NotNil(t, res.ResponseStatus)
	assert.Equal(t, http.StatusInternalServerError, *res.ResponseStatus)
	assert.Equal(t, orig, res.DeliveryID)
	assert.Equal(t, []uuid.UUID{orig}, deliveryIDs(f.list(t, f.principal, ExtensionEventsQuery{Undelivered: true})))

	f.mu.Lock()
	f.status = http.StatusOK
	f.mu.Unlock()
	// Resending the resend still carries the original id.
	res2, err := f.svc.RedeliverExtensionEvent(ctx, f.principal, res.ID)
	require.NoError(t, err)
	assert.True(t, res2.Success)
	assert.Equal(t, orig, res2.DeliveryID)

	f.mu.Lock()
	require.Len(t, f.received, 2)
	for i, r := range f.received {
		assert.Equal(t, orig.String(), r.Header.Get("X-Mesh-Delivery"))
		assert.Equal(t, orig.String(), r.Header.Get("X-Mesh-Event-Id"), "a row without event_id falls back to the delivery id")
		assert.Equal(t, extensions.EventTaskUpdated, r.Header.Get("X-Mesh-Event"))
		assert.Equal(t, validSignature("whsec-replay", r.Header.Get("X-Mesh-Timestamp"), f.bodies[i]), r.Header.Get("X-Mesh-Signature"))
	}
	f.mu.Unlock()

	row, err := f.repo.GetDelivery(ctx, res2.ID)
	require.NoError(t, err)
	require.NotNil(t, row)
	require.NotNil(t, row.RedeliveryOf)
	assert.Equal(t, orig, *row.RedeliveryOf)
	assert.True(t, row.Success)

	feed := f.list(t, f.principal, ExtensionEventsQuery{})
	assert.Equal(t, []uuid.UUID{orig}, deliveryIDs(feed), "resends are not events of their own")
	assert.True(t, feed.Items[0].Delivered)
	assert.Empty(t, f.list(t, f.principal, ExtensionEventsQuery{Undelivered: true}).Items)
}

// A resend to a connection that negotiated v2 is signed with v2 and keeps the
// original delivery id: it must never fall back to a v1 signature.
func TestExtensionEvents_RedeliverV2_DB(t *testing.T) {
	f := newReplayFixture(t)
	ctx := context.Background()
	f.mu.Lock()
	f.status = http.StatusOK
	f.mu.Unlock()
	f.svc.ext.signatureVersion = func(string) int { return extsig.Version }
	orig := f.record(t, uuid.New(), time.Minute)

	res, err := f.svc.RedeliverExtensionEvent(ctx, f.principal, orig)
	require.NoError(t, err)
	assert.True(t, res.Success)

	f.mu.Lock()
	defer f.mu.Unlock()
	require.Len(t, f.received, 1)
	r := f.received[0]
	assert.Equal(t, "2", r.Header.Get("X-Mesh-Signature-Version"))
	got, err := extsig.Verifier{Secret: "whsec-replay", MaxSkew: time.Minute}.Verify(r.Header, f.bodies[0])
	require.NoError(t, err)
	assert.Equal(t, orig.String(), got.DeliveryID)
	assert.Equal(t, extensions.EventTaskUpdated, got.Event)
}

// Redelivery checks the same rights as the feed, and another connection cannot
// tell a foreign delivery from a missing one.
func TestExtensionEvents_RedeliverRefusals_DB(t *testing.T) {
	f := newReplayFixture(t)
	ctx := context.Background()
	denied := uuid.New()
	orig := f.record(t, denied, time.Minute)

	_, err := f.svc.RedeliverExtensionEvent(ctx, f.other, orig)
	assertAPIStatus(t, err, http.StatusNotFound)
	_, err = f.svc.RedeliverExtensionEvent(ctx, f.principal, uuid.New())
	assertAPIStatus(t, err, http.StatusNotFound)

	revoked := *f.principal
	revoked.Capabilities = map[string][]string{"events": {}}
	_, err = f.svc.RedeliverExtensionEvent(ctx, &revoked, orig)
	assertAPIStatus(t, err, http.StatusForbidden)

	f.svc.extScope = denyProjects{denied: map[uuid.UUID]bool{denied: true}}
	_, err = f.svc.RedeliverExtensionEvent(ctx, f.principal, orig)
	assertAPIStatus(t, err, http.StatusForbidden)

	f.mu.Lock()
	assert.Empty(t, f.received, "nothing was sent for a refused redelivery")
	f.mu.Unlock()
}

func assertAPIStatus(t *testing.T, err error, status int) {
	t.Helper()
	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, status, apiErr.StatusCode())
}

type failingScope struct {
	repository.ExtensionProjectScopeRepository
}

func (failingScope) AllowsProject(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return false, apierror.ServiceUnavailable("scope lookup failed")
}

// The edges of the feed and of redelivery: a scope that cannot answer fails
// the read instead of skipping the event, an event without a project is not
// held to the project scope, an oversized limit is clamped, a missing secret
// and an unreachable receiver are recorded outcomes, and a nats subscription
// has nothing to resend.
func TestExtensionEvents_Edges_DB(t *testing.T) {
	f := newReplayFixture(t)
	ctx := context.Background()
	orig := f.record(t, uuid.New(), 2*time.Minute)
	noProject := &domain.WebhookDelivery{
		ID: uuid.New(), SubscriptionID: &f.sub.ID, EventType: extensions.EventTaskUpdated,
		Payload: []byte(`{"task_id":"t"}`), Attempt: 1, CreatedAt: time.Now().Add(-time.Minute),
	}
	require.NoError(t, f.repo.CreateDelivery(ctx, noProject))

	f.svc.extScope = failingScope{}
	_, err := f.svc.ListExtensionEvents(ctx, f.principal, ExtensionEventsQuery{})
	assertAPIStatus(t, err, http.StatusServiceUnavailable)
	_, err = f.svc.RedeliverExtensionEvent(ctx, f.principal, orig)
	assertAPIStatus(t, err, http.StatusServiceUnavailable)

	f.svc.extScope = denyProjects{}
	page := f.list(t, f.principal, ExtensionEventsQuery{Limit: 10_000})
	assert.Equal(t, []uuid.UUID{orig, noProject.ID}, deliveryIDs(page))
	assert.False(t, page.HasMore)

	f.svc.ext.secrets = &fakeExtSecrets{}
	res, err := f.svc.RedeliverExtensionEvent(ctx, f.principal, orig)
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "not found")

	f.svc.ext.secrets = &fakeExtSecrets{values: []domain.MaterializedSecret{{Name: "ext-hook-secret", Value: "v"}}}
	f.svc.client = newWebhookHTTPClient(func(net.IP) bool { return false })
	res, err = f.svc.RedeliverExtensionEvent(ctx, f.principal, orig)
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.NotEmpty(t, res.Error, "a refused dial is reported, not swallowed")

	natsSub, err := f.subs.Create(ctx, &domain.ExtensionEventSubscription{
		ID: uuid.New(), ConnectionID: f.principal.ConnectionID, Event: extensions.EventTaskUpdated,
		Delivery: domain.ExtensionDeliveryNATS,
	})
	require.NoError(t, err)
	onNATS := &domain.WebhookDelivery{
		ID: uuid.New(), SubscriptionID: &natsSub.ID, EventType: extensions.EventTaskUpdated,
		Payload: []byte(`{}`), Attempt: 1, CreatedAt: time.Now().Add(-time.Minute),
	}
	require.NoError(t, f.repo.CreateDelivery(ctx, onNATS))
	_, err = f.svc.RedeliverExtensionEvent(ctx, f.principal, onNATS.ID)
	assertAPIStatus(t, err, http.StatusUnprocessableEntity)
}
