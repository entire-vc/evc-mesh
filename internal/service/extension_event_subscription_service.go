package service

import (
	"context"
	"net/url"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// CreateExtensionEventSubscriptionInput is the request to subscribe a
// connection to one public event.
type CreateExtensionEventSubscriptionInput struct {
	ConnectionID uuid.UUID
	Event        string
	Delivery     domain.ExtensionEventDelivery
	Target       string
	SecretRef    string
}

// ExtensionEventSubscriptionService manages which public events an extension
// connection receives. A subscription is allowed only for an event on the
// public list that the extension's manifest declares in capabilities.events.
type ExtensionEventSubscriptionService interface {
	Subscribe(ctx context.Context, input CreateExtensionEventSubscriptionInput) (*domain.ExtensionEventSubscription, error)
	ListByConnection(ctx context.Context, connectionID uuid.UUID) ([]domain.ExtensionEventSubscription, error)
	// Unsubscribe deletes the subscription, NotFound for an unknown id.
	Unsubscribe(ctx context.Context, id uuid.UUID) error
}

type extensionEventSubscriptionService struct {
	subs         repository.ExtensionEventSubscriptionRepository
	integrations repository.IntegrationRepository
	installed    repository.InstalledExtensionRepository
}

// NewExtensionEventSubscriptionService creates the service.
func NewExtensionEventSubscriptionService(
	subs repository.ExtensionEventSubscriptionRepository,
	integrations repository.IntegrationRepository,
	installed repository.InstalledExtensionRepository,
) ExtensionEventSubscriptionService {
	return &extensionEventSubscriptionService{subs: subs, integrations: integrations, installed: installed}
}

func (s *extensionEventSubscriptionService) Subscribe(ctx context.Context, in CreateExtensionEventSubscriptionInput) (*domain.ExtensionEventSubscription, error) {
	if !extensions.IsPublicEvent(in.Event) {
		return nil, apierror.BadRequestWithDetails("unknown event", in.Event)
	}
	if err := validateExtensionDelivery(in); err != nil {
		return nil, err
	}

	conn, err := s.integrations.GetByID(ctx, in.ConnectionID)
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, apierror.NotFound("Connection")
	}
	ext, err := s.installed.Get(ctx, string(conn.Provider))
	if err != nil {
		return nil, err
	}
	if ext == nil {
		return nil, apierror.NotFound("Extension")
	}
	manifest, err := extensions.ParseManifest(ext.Manifest)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(manifest.Capabilities.Events, in.Event) {
		return nil, apierror.ForbiddenWithDetails("the extension manifest does not declare this event in capabilities.events", in.Event)
	}

	return s.subs.Create(ctx, &domain.ExtensionEventSubscription{
		ID:           uuid.New(),
		ConnectionID: in.ConnectionID,
		Event:        in.Event,
		Delivery:     in.Delivery,
		Target:       in.Target,
		SecretRef:    in.SecretRef,
	})
}

func validateExtensionDelivery(in CreateExtensionEventSubscriptionInput) *apierror.Error {
	switch in.Delivery {
	case domain.ExtensionDeliveryWebhook:
		u, err := url.Parse(in.Target)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return apierror.BadRequest("a webhook subscription needs an http(s) target URL")
		}
		if strings.TrimSpace(in.SecretRef) == "" {
			return apierror.BadRequest("a webhook subscription needs a secret_ref to sign deliveries")
		}
	case domain.ExtensionDeliveryNATS:
		// The subject is derived from the extension actor at delivery time.
		if in.Target != "" || in.SecretRef != "" {
			return apierror.BadRequest("a nats subscription takes no target or secret_ref")
		}
	default:
		return apierror.BadRequest("delivery must be webhook or nats")
	}
	return nil
}

func (s *extensionEventSubscriptionService) ListByConnection(ctx context.Context, connectionID uuid.UUID) ([]domain.ExtensionEventSubscription, error) {
	return s.subs.ListByConnection(ctx, connectionID)
}

func (s *extensionEventSubscriptionService) Unsubscribe(ctx context.Context, id uuid.UUID) error {
	sub, err := s.subs.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if sub == nil {
		return apierror.NotFound("Subscription")
	}
	return s.subs.Delete(ctx, id)
}
