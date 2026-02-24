package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Workspace represents a top-level tenant that isolates all data (multi-tenancy).
type Workspace struct {
	ID                uuid.UUID       `json:"id" db:"id"`
	Name              string          `json:"name" db:"name"`
	Slug              string          `json:"slug" db:"slug"`
	OwnerID           uuid.UUID       `json:"owner_id" db:"owner_id"`
	Settings          json.RawMessage `json:"settings" db:"settings"`
	BillingPlanID     string          `json:"billing_plan_id" db:"billing_plan_id"`
	BillingCustomerID string          `json:"billing_customer_id" db:"billing_customer_id"`
	CreatedAt         time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at" db:"updated_at"`
}
