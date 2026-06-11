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
	// ShareID is the relay share UUID (shares.id = folder GUID). Preferred over ShareSlug
	// because it works for private sync folders that have web_published=false.
	// When non-empty, transport() sends this UUID to the relay upload endpoint.
	ShareID            string `json:"share_id,omitempty"`
	ShareSlug          string `json:"share_slug"`
	Subfolder          string `json:"subfolder"`
	IncludeProjectSlug bool   `json:"include_project_slug"`
}

// RelayFileItem represents a file entry returned by the Team Relay share file-list API.
type RelayFileItem struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// UpsertProjectIntegrationInput holds data for creating/updating a project integration.
type UpsertProjectIntegrationInput struct {
	ProjectID          uuid.UUID  `json:"project_id"`
	Enabled            bool       `json:"enabled"`
	ShareID            string     `json:"share_id,omitempty"`
	ShareSlug          string     `json:"share_slug"`
	AgentKey           string     `json:"agent_key,omitempty"` // omit to keep existing
	Subfolder          string     `json:"subfolder"`
	IncludeProjectSlug bool       `json:"include_project_slug"`
	CreatedBy          *uuid.UUID `json:"-"`
}
