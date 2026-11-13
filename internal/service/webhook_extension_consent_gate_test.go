package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// gateConsents is the consent column of one connection, changed by the test.
type gateConsents struct {
	repository.ExtensionConsentRepository
	mu sync.Mutex
	c  *domain.ExtensionConsent
}

func (g *gateConsents) GetConsent(context.Context, uuid.UUID, string) (*domain.ExtensionConsent, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.c == nil {
		return nil, nil
	}
	v := *g.c
	return &v, nil
}

func (g *gateConsents) set(c *domain.ExtensionConsent) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.c = c
}

// The hole of an internal ticket: after an instance update adds an event, the handshake
// (or an API call) leaves a subscription to it, but the workspace never agreed
// to it. Delivery must follow consent, not the subscription table: nothing is
// sent before the consent, the event is sent after it. The event the
// workspace did consent to is delivered throughout (positive control), and an
// event the installed manifest no longer declares is not, even if consented.
func TestExtensionDelivery_EventFollowsConsentNotSubscription(t *testing.T) {
	var mu sync.Mutex
	got := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got[r.Header.Get("X-Mesh-Event")]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	count := func(ev string) int {
		mu.Lock()
		defer mu.Unlock()
		return got[ev]
	}

	f := newExtFixture(t, srv.URL, []domain.MaterializedSecret{{Name: "ext-hook-secret", Value: "v"}})
	connID := f.subsRepo.subs[0].ConnectionID
	for id := range f.svc.ext.integrations.(*fakeExtIntegrations).conns {
		f.svc.ext.integrations.(*fakeExtIntegrations).conns[id].Provider = "memory-service"
	}
	// Manifest 0.4 is installed: it declares task.created in addition to
	// task.updated, and the subscription to task.created already exists.
	f.subsRepo.subs = append(f.subsRepo.subs, domain.ExtensionEventSubscription{
		ID: uuid.New(), ConnectionID: connID, Event: "task.created",
		Delivery: domain.ExtensionDeliveryWebhook, Target: srv.URL, SecretRef: "ext-hook-secret",
	})
	declared := []string{"api:tasks:read", "events:task.created", "events:task.updated"}
	consents := &gateConsents{c: &domain.ExtensionConsent{Capabilities: []string{"api:tasks:read", "events:task.updated"}, Version: "0.3.0"}}
	var declMu sync.Mutex
	setDeclared := func(d []string) {
		declMu.Lock()
		defer declMu.Unlock()
		declared = d
	}
	WithExtensionConsentGate(consents, func(id string) ([]string, bool) {
		declMu.Lock()
		defer declMu.Unlock()
		if id != "memory-service" {
			return nil, false
		}
		return declared, true
	})(f.svc)

	ctx := context.Background()
	f.svc.Dispatch(ctx, f.ws, "task.created", map[string]any{"task_id": "t1"})
	f.svc.Dispatch(ctx, f.ws, "task.updated", map[string]any{"task_id": "t1"})
	f.waitDeliveries(t, 1)
	time.Sleep(100 * time.Millisecond) // a wrongly sent task.created would land by now
	assert.Equal(t, 1, count("task.updated"), "the consented event is delivered (positive control)")
	assert.Equal(t, 0, count("task.created"), "no delivery of the added event before the workspace consents")

	consents.set(&domain.ExtensionConsent{Capabilities: []string{"api:tasks:read", "events:task.created", "events:task.updated"}, Version: "0.4.0"})
	f.svc.Dispatch(ctx, f.ws, "task.created", map[string]any{"task_id": "t2"})
	f.waitDeliveries(t, 2)
	assert.Equal(t, 1, count("task.created"), "delivered once the workspace consented")

	// Rolled back to a manifest that no longer declares task.created: the
	// consent still has it, but it does not come back.
	setDeclared([]string{"api:tasks:read", "events:task.updated"})
	f.svc.Dispatch(ctx, f.ws, "task.created", map[string]any{"task_id": "t3"})
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 1, count("task.created"), "an event the installed manifest dropped is not delivered")

	// A connection with no consent record receives nothing.
	consents.set(nil)
	f.svc.Dispatch(ctx, f.ws, "task.updated", map[string]any{"task_id": "t4"})
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 1, count("task.updated"), "no consent record, no delivery")
}

// Subscribing over the API follows consent too: an event the manifest declares
// but the workspace has not agreed to is refused with 403; once consented it is
// accepted (positive control).
func TestExtEventSvc_SubscribeNeedsConsent(t *testing.T) {
	ctx := context.Background()
	svc, _, conn := newExtEventSvc([]string{extensions.EventTaskCreated, extensions.EventTaskUpdated})
	consents := &gateConsents{c: &domain.ExtensionConsent{Capabilities: []string{"events:task.updated"}}}
	svc = WithSubscriptionConsents(svc, consents)

	_, err := svc.Subscribe(ctx, webhookSub(conn, extensions.EventTaskCreated))
	assert.Equal(t, 403, apiStatus(t, err), "declared but not consented")
	_, err = svc.Subscribe(ctx, webhookSub(conn, extensions.EventTaskUpdated))
	assert.NoError(t, err, "consented")

	consents.set(&domain.ExtensionConsent{Capabilities: []string{"events:task.created", "events:task.updated"}})
	_, err = svc.Subscribe(ctx, webhookSub(conn, extensions.EventTaskCreated))
	assert.NoError(t, err, "accepted after the consent")

	consents.set(nil)
	_, err = svc.Subscribe(ctx, webhookSub(conn, extensions.EventTaskUpdated))
	assert.Equal(t, 403, apiStatus(t, err), "no consent record")
}
