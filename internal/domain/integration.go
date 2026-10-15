package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// IntegrationProvider identifies the external service.
type IntegrationProvider string

const (
	IntegrationProviderGitHub   IntegrationProvider = "github"
	IntegrationProviderGitLab   IntegrationProvider = "gitlab"
	IntegrationProviderSpark    IntegrationProvider = "spark"
	IntegrationProviderMCP      IntegrationProvider = "mcp"
	IntegrationProviderTelegram IntegrationProvider = "telegram"
	// IntegrationProviderTeamRelay is the CONNECTION-layer row (where the relay
	// lives — its base URL) for provider="team_relay" in integration_configs.
	// Deliberately distinct from the per-project BINDING (which share, which
	// subfolder, the sync agent key) already stored in project_integrations —
	// see specsintegration-provider-contract §2. This constant only names the
	// connection-layer row; it does not change project_integrations at all.
	IntegrationProviderTeamRelay IntegrationProvider = "team_relay"
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

// ExtensionConsent is what a workspace admin accepted when connecting an
// extension (spec 03 §3): the capabilities, when, and by whom. A connection
// made before consent was recorded has none.
type ExtensionConsent struct {
	Capabilities []string   `json:"capabilities"`
	At           time.Time  `json:"at"`
	By           *uuid.UUID `json:"by,omitempty"`
}
