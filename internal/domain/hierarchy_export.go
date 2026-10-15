package domain

import (
	"time"

	"github.com/google/uuid"
)

// HierarchyExport is the snapshot behind GET /workspaces/{ws}/export/hierarchy.
type HierarchyExport struct {
	Workspace HierarchyWorkspace `json:"workspace"`
	Projects  []HierarchyProject `json:"projects"`
	Members   []HierarchyMember  `json:"members"`
	Agents    []HierarchyAgent   `json:"agents"`
}

// HierarchyWorkspace identifies the exported workspace.
type HierarchyWorkspace struct {
	ID   uuid.UUID `json:"id" db:"id"`
	Slug string    `json:"slug" db:"slug"`
	Name string    `json:"name" db:"name"`
}

// HierarchyProject is one live project.
type HierarchyProject struct {
	ID         uuid.UUID `json:"id" db:"id"`
	Slug       string    `json:"slug" db:"slug"`
	Name       string    `json:"name" db:"name"`
	IsArchived bool      `json:"is_archived" db:"is_archived"`
	CreatedAt  time.Time `json:"created_at" db:"created_at"`
	UpdatedAt  time.Time `json:"updated_at" db:"updated_at"`
}

// HierarchyMember is one workspace member with the workspace role.
type HierarchyMember struct {
	UserID uuid.UUID `json:"user_id" db:"user_id"`
	Email  string    `json:"email" db:"email"`
	Name   string    `json:"name" db:"name"`
	Role   string    `json:"role" db:"role"`
}

// HierarchyAgent is one live agent. Keys carry metadata only: the prefix is the
// public lookup part of the key, never the secret.
type HierarchyAgent struct {
	ID            uuid.UUID           `json:"id" db:"id"`
	Slug          string              `json:"slug" db:"slug"`
	Name          string              `json:"name" db:"name"`
	AgentType     string              `json:"agent_type" db:"agent_type"`
	Role          string              `json:"role" db:"role"`
	ParentAgentID *uuid.UUID          `json:"parent_agent_id,omitempty" db:"parent_agent_id"`
	SponsorUserID *uuid.UUID          `json:"sponsor_user_id" db:"sponsor_user_id"`
	CreatedAt     time.Time           `json:"created_at" db:"created_at"`
	ActiveKeys    []HierarchyAgentKey `json:"active_keys"`
}

// HierarchyAgentKey is the metadata of one active key of an agent.
type HierarchyAgentKey struct {
	// KeyID is domain.AgentPrimaryKeyID for the agent's original key, a UUID for
	// the additional ones.
	KeyID        string     `json:"key_id"`
	KeyPrefix    string     `json:"key_prefix"`
	Label        string     `json:"label,omitempty"`
	IssuedByType string     `json:"issued_by_type,omitempty"`
	IssuedByID   *uuid.UUID `json:"issued_by_id,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}
