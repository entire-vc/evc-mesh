package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// IntegrationProvider identifies the external service.
type IntegrationProvider string

const (
	IntegrationProviderSlack  IntegrationProvider = "slack"
	IntegrationProviderGitHub IntegrationProvider = "github"
)

// IntegrationConfig stores provider-specific configuration for a workspace integration.
type IntegrationConfig struct {
	ID          uuid.UUID           `json:"id" db:"id"`
	WorkspaceID uuid.UUID           `json:"workspace_id" db:"workspace_id"`
	Provider    IntegrationProvider `json:"provider" db:"provider"`
	Config      json.RawMessage     `json:"config" db:"config"`
	IsActive    bool                `json:"is_active" db:"is_active"`
	CreatedAt   time.Time           `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at" db:"updated_at"`
}

// CreateIntegrationInput holds data for creating an integration config.
type CreateIntegrationInput struct {
	WorkspaceID uuid.UUID           `json:"workspace_id"`
	Provider    IntegrationProvider `json:"provider"`
	Config      json.RawMessage     `json:"config"`
	IsActive    bool                `json:"is_active"`
}

// UpdateIntegrationInput holds partial update data for an integration config.
type UpdateIntegrationInput struct {
	Config   json.RawMessage `json:"config"`
	IsActive *bool           `json:"is_active"`
}
