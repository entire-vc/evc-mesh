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
	// IconStorageKey holds the S3 object key for the workspace icon.
	// Not exposed in JSON — handlers derive icon_url from this via the redirect endpoint.
	IconStorageKey *string `json:"-" db:"-"`
	// IsBench marks the single dedicated LME-bench workspace. Set only by the
	// migration backfill (see is_bench migration) — there is deliberately no
	// API path to toggle it. Gates the reserved `lme-bench` memory tag in
	// memoryService.Remember (task #0104878c): a workspace where this is false
	// can never accept a write carrying that tag, regardless of which key a
	// client process happens to be holding.
	IsBench bool `json:"is_bench" db:"is_bench"`
}
