package service

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/eventbus"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// extensionMaxAttempts is the first try plus one retry per backoff step (1s, 5s,
// 25s).
const extensionMaxAttempts = 4

// publicEventName maps an event name used inside the services to its name on
// the public list (internal/extensions). Names not listed here are already
// public.
func publicEventName(internal string) string {
	if internal == "task.status_changed" {
		return "task.moved"
	}
	return internal
}

// extensionDelivery fans a public event out to extension_event_subscriptions
// with delivery=webhook, over the same signing, SSRF-guarded client and retry
// schedule as webhook_configs. The signing secret is the workspace secret named
// by the subscription's secret_ref, read at delivery time (never stored with the
// subscription).
type extensionDelivery struct {
	subs         repository.ExtensionEventSubscriptionRepository
	integrations repository.IntegrationRepository
	secrets      repository.SecretMaterializer
	// nats and workspaces serve delivery=nats; nil until the event bus is up
	// (it is created after the webhook service), in which case nats
	// subscriptions are skipped.
	nats       ExtensionEventPublisher
	workspaces repository.WorkspaceRepository
}

// ExtensionEventPublisher puts one serialised event on an extension
// connection's NATS subject. *eventbus.EventBus implements it.
type ExtensionEventPublisher interface {
	PublishExtensionEvent(ctx context.Context, subject string, data []byte, msgID string) error
}

// ExtensionNATSConfigurable is implemented by the webhook service so main can
// hand it the event bus once that is connected.
type ExtensionNATSConfigurable interface {
	SetExtensionNATS(pub ExtensionEventPublisher, workspaces repository.WorkspaceRepository)
}

// SetExtensionNATS enables delivery=nats once the event bus is connected. Call
// it before the server starts taking requests.
func (s *webhookService) SetExtensionNATS(pub ExtensionEventPublisher, workspaces repository.WorkspaceRepository) {
	if s.ext == nil {
		return
	}
	s.ext.nats = pub
	s.ext.workspaces = workspaces
}

// WithExtensionDelivery makes Dispatch also deliver to extension subscriptions.
func WithExtensionDelivery(
	subs repository.ExtensionEventSubscriptionRepository,
	integrations repository.IntegrationRepository,
	secrets repository.SecretMaterializer,
) WebhookServiceOption {
	return func(s *webhookService) {
		s.ext = &extensionDelivery{subs: subs, integrations: integrations, secrets: secrets}
	}
}

// dispatchExtensions delivers the event to every webhook or nats subscription of an
// active connection in workspaceID. One goroutine per subscription; a failure of
// one never affects another.
func (s *webhookService) dispatchExtensions(ctx context.Context, workspaceID uuid.UUID, eventType string, payloadBytes []byte) {
	if s.ext == nil {
		return
	}
	event := publicEventName(eventType)
	payloadBytes = withWorkspaceID(payloadBytes, workspaceID)
	subs, err := s.ext.subs.ListByEvent(ctx, event)
	if err != nil {
		log.Printf("[webhook] failed to list extension subscriptions for event %s: %v", event, err)
		return
	}
	// ListByEvent spans every workspace; a connection belongs to exactly one.
	inWorkspace := map[uuid.UUID]bool{}
	for i := range subs {
		sub := subs[i]
		if sub.Delivery != domain.ExtensionDeliveryWebhook && sub.Delivery != domain.ExtensionDeliveryNATS {
			continue
		}
		ok, seen := inWorkspace[sub.ConnectionID]
		if !seen {
			conn, err := s.ext.integrations.GetByID(ctx, sub.ConnectionID)
			ok = err == nil && conn != nil && conn.IsActive && conn.WorkspaceID == workspaceID
			inWorkspace[sub.ConnectionID] = ok
		}
		if !ok {
			continue
		}
		if sub.Delivery == domain.ExtensionDeliveryNATS {
			go s.publishToSubscription(workspaceID, sub, event, payloadBytes)
		} else {
			go s.deliverToSubscription(workspaceID, sub, event, payloadBytes)
		}
	}
}

// withWorkspaceID returns the payload with workspace_id set, as docs/webhooks.md
// promises every receiver: an extension picks its signing secret by it, so a body
// without it is refused before the signature is checked. A workspace_id the event
// already carries is kept; a payload that is not a JSON object is sent as is.
func withWorkspaceID(payloadBytes []byte, workspaceID uuid.UUID) []byte {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(payloadBytes, &m); err != nil || m == nil {
		return payloadBytes
	}
	if _, ok := m["workspace_id"]; ok {
		return payloadBytes
	}
	ws, _ := json.Marshal(workspaceID)
	m["workspace_id"] = ws
	out, err := json.Marshal(m)
	if err != nil {
		return payloadBytes
	}
	return out
}

// deliverToSubscription signs and sends one event and records the outcome in
// webhook_deliveries (subscription_id set, webhook_id empty). An unresolvable
// secret is recorded as a failed delivery rather than dropped silently.
func (s *webhookService) deliverToSubscription(workspaceID uuid.UUID, sub domain.ExtensionEventSubscription, event string, payloadBytes []byte) {
	bgCtx := context.Background()
	subID := sub.ID

	secret, reason := s.ext.resolveSecret(bgCtx, workspaceID, sub.SecretRef)
	if secret == "" {
		failure := reason
		d := &domain.WebhookDelivery{
			ID: uuid.New(), SubscriptionID: &subID, EventType: event, Payload: payloadBytes,
			ResponseBody: &failure, Success: false, Attempt: 1, CreatedAt: time.Now(),
		}
		if err := s.repo.CreateDelivery(bgCtx, d); err != nil {
			log.Printf("[webhook] failed to record delivery for subscription %s: %v", sub.ID, err)
		}
		return
	}

	// Extension receivers get the full 1s/5s/25s schedule: first try plus three
	// retries.
	res := s.deliver(sub.ID, sub.Target, secret, event, payloadBytes, extensionMaxAttempts, true)
	d := &domain.WebhookDelivery{
		ID: res.deliveryID, SubscriptionID: &subID, EventType: event, Payload: payloadBytes,
		ResponseStatus: res.status, ResponseBody: res.body, DurationMs: res.durationMs,
		Success: res.success, Attempt: res.attempts, CreatedAt: time.Now(),
	}
	if err := s.repo.CreateDelivery(bgCtx, d); err != nil {
		log.Printf("[webhook] failed to record delivery for subscription %s: %v", sub.ID, err)
	}
}

// resolveSecret returns the current workspace-scope secret named ref, or "" and
// a reason (never the value) when there is none usable.
func (e *extensionDelivery) resolveSecret(ctx context.Context, workspaceID uuid.UUID, ref string) (secret, reason string) {
	all, err := e.secrets.ResolveCurrentValues(ctx, workspaceID, nil, nil)
	if err != nil {
		return "", "secret lookup failed"
	}
	for i := range all {
		if all[i].Name != ref {
			continue
		}
		if all[i].Expired || all[i].Value == "" {
			return "", "secret " + ref + " is expired"
		}
		return all[i].Value, ""
	}
	return "", "secret " + ref + " not found"
}

// extensionNATSEnvelope is the body published to an extension's subject.
type extensionNATSEnvelope struct {
	Event          string          `json:"event"`
	SubscriptionID uuid.UUID       `json:"subscription_id"`
	WorkspaceID    uuid.UUID       `json:"workspace_id"`
	Payload        json.RawMessage `json:"payload"`
	CreatedAt      time.Time       `json:"created_at"`
}

// publishToSubscription publishes one event to the connection's own subject
// (eventbus.ExtensionSubject). Other connections and the workspace's regular
// event subjects never see it. A failure is logged, not retried: JetStream
// owns durability once the publish is acknowledged.
func (s *webhookService) publishToSubscription(workspaceID uuid.UUID, sub domain.ExtensionEventSubscription, event string, payloadBytes []byte) {
	if s.ext.nats == nil || s.ext.workspaces == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, err := s.ext.workspaces.GetByID(ctx, workspaceID)
	if err != nil || ws == nil {
		log.Printf("[webhook] nats delivery for subscription %s: workspace lookup failed", sub.ID)
		return
	}
	id := uuid.New()
	body, err := json.Marshal(extensionNATSEnvelope{
		Event: event, SubscriptionID: sub.ID, WorkspaceID: workspaceID,
		Payload: payloadBytes, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		log.Printf("[webhook] nats delivery for subscription %s: marshal failed: %v", sub.ID, err)
		return
	}
	subject := eventbus.ExtensionSubject(ws.Slug, sub.ConnectionID, event)
	if err := s.ext.nats.PublishExtensionEvent(ctx, subject, body, id.String()); err != nil {
		log.Printf("[webhook] nats delivery for subscription %s failed: %v", sub.ID, err)
	}
}
