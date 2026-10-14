package service

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
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

// dispatchExtensions delivers the event to every webhook subscription of an
// active connection in workspaceID. One goroutine per subscription; a failure of
// one never affects another.
func (s *webhookService) dispatchExtensions(ctx context.Context, workspaceID uuid.UUID, eventType string, payloadBytes []byte) {
	if s.ext == nil {
		return
	}
	event := publicEventName(eventType)
	subs, err := s.ext.subs.ListByEvent(ctx, event)
	if err != nil {
		log.Printf("[webhook] failed to list extension subscriptions for event %s: %v", event, err)
		return
	}
	// ListByEvent spans every workspace; a connection belongs to exactly one.
	inWorkspace := map[uuid.UUID]bool{}
	for i := range subs {
		sub := subs[i]
		if sub.Delivery != domain.ExtensionDeliveryWebhook {
			continue
		}
		ok, seen := inWorkspace[sub.ConnectionID]
		if !seen {
			conn, err := s.ext.integrations.GetByID(ctx, sub.ConnectionID)
			ok = err == nil && conn != nil && conn.IsActive && conn.WorkspaceID == workspaceID
			inWorkspace[sub.ConnectionID] = ok
		}
		if ok {
			go s.deliverToSubscription(workspaceID, sub, event, payloadBytes)
		}
	}
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
	res := s.deliver(sub.ID, sub.Target, secret, event, payloadBytes, extensionMaxAttempts)
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
