package domain

import (
	"time"

	"github.com/google/uuid"
)

// ExtensionEventDelivery is how a subscribed event reaches the extension.
type ExtensionEventDelivery string

const (
	ExtensionDeliveryWebhook ExtensionEventDelivery = "webhook"
	ExtensionDeliveryNATS    ExtensionEventDelivery = "nats"
)

// ExtensionEventSubscription is a row of extension_event_subscriptions: one
// event an extension connection (integration_configs row) wants delivered.
// SecretRef names the secret that signs a webhook; the secret itself is never
// stored here.
type ExtensionEventSubscription struct {
	ID           uuid.UUID              `json:"id" db:"id"`
	ConnectionID uuid.UUID              `json:"connection_id" db:"connection_id"`
	Event        string                 `json:"event" db:"event"`
	Delivery     ExtensionEventDelivery `json:"delivery" db:"delivery"`
	Target       string                 `json:"target" db:"target"`
	SecretRef    string                 `json:"secret_ref" db:"secret_ref"`
	CreatedAt    time.Time              `json:"created_at" db:"created_at"`
}
