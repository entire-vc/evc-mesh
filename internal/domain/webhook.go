package domain

import (
	"time"

	"github.com/google/uuid"
)

// WebhookConfig represents a registered outbound webhook endpoint.
type WebhookConfig struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	WorkspaceID   uuid.UUID  `json:"workspace_id" db:"workspace_id"`
	Name          string     `json:"name" db:"name"`
	URL           string     `json:"url" db:"url"`
	Secret        string     `json:"-" db:"secret"` // never exposed in API responses
	Events        []string   `json:"events" db:"events"`
	IsActive      bool       `json:"is_active" db:"is_active"`
	FailureCount  int        `json:"failure_count" db:"failure_count"`
	LastFailureAt *time.Time `json:"last_failure_at,omitempty" db:"last_failure_at"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty" db:"last_success_at"`
	CreatedBy     uuid.UUID  `json:"created_by" db:"created_by"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at" db:"updated_at"`
}

// WebhookDelivery records a single delivery attempt for a webhook.
type WebhookDelivery struct {
	ID             uuid.UUID `json:"id" db:"id"`
	WebhookID      uuid.UUID `json:"webhook_id" db:"webhook_id"`
	EventType      string    `json:"event_type" db:"event_type"`
	Payload        []byte    `json:"payload" db:"payload"`
	ResponseStatus *int      `json:"response_status,omitempty" db:"response_status"`
	ResponseBody   *string   `json:"response_body,omitempty" db:"response_body"`
	DurationMs     *int      `json:"duration_ms,omitempty" db:"duration_ms"`
	Success        bool      `json:"success" db:"success"`
	Attempt        int       `json:"attempt" db:"attempt"`
	CreatedAt      time.Time `json:"created_at" db:"created_at"`
}

// CreateWebhookInput holds parameters for creating a webhook.
type CreateWebhookInput struct {
	WorkspaceID uuid.UUID
	Name        string
	URL         string
	Events      []string
	CreatedBy   uuid.UUID
}

// UpdateWebhookInput holds parameters for partially updating a webhook.
type UpdateWebhookInput struct {
	Name     *string
	URL      *string
	Events   []string
	IsActive *bool
}
