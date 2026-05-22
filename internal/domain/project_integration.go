package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ProjectIntegration represents a project-level integration configuration.
type ProjectIntegration struct {
	ID        uuid.UUID       `json:"id" db:"id"`
	ProjectID uuid.UUID       `json:"project_id" db:"project_id"`
	Type      string          `json:"type" db:"type"`
	Enabled   bool            `json:"enabled" db:"enabled"`
	Settings  json.RawMessage `json:"settings" db:"settings"`
	AgentKey  string          `json:"agent_key,omitempty" db:"agent_key"` // plaintext in memory; encrypted in DB
	CreatedAt time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt time.Time       `json:"updated_at" db:"updated_at"`
	CreatedBy *uuid.UUID      `json:"created_by,omitempty" db:"created_by"`
}

// TeamRelaySettings holds the Team Relay-specific configuration.
type TeamRelaySettings struct {
	ShareID            string `json:"share_id"`
	Subfolder          string `json:"subfolder"`
	IncludeProjectSlug bool   `json:"include_project_slug"`
}

// UpsertProjectIntegrationInput holds data for creating/updating a project integration.
type UpsertProjectIntegrationInput struct {
	ProjectID          uuid.UUID  `json:"project_id"`
	Enabled            bool       `json:"enabled"`
	ShareID            string     `json:"share_id"`
	AgentKey           string     `json:"agent_key,omitempty"` // omit to keep existing
	Subfolder          string     `json:"subfolder"`
	IncludeProjectSlug bool       `json:"include_project_slug"`
	CreatedBy          *uuid.UUID `json:"-"`
}
