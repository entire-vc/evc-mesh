package domain

import (
	"time"

	"github.com/google/uuid"
)

// AgentWorkspaceGrant is a connection between an agent and a workspace: the
// agent's role and API key material for that specific workspace.
//
// Introduced additively by migration 20260909001 (task U1), backfilled with
// exactly one row per pre-existing agent pointing at its home workspace. U2
// makes this table — not agents.workspace_id/api_key_* — the primary source
// an agent authenticates against; agents.* remains a transitional fallback
// for a connection-less agent (see agentService.Authenticate).
type AgentWorkspaceGrant struct {
	ID          uuid.UUID `json:"id" db:"id"`
	AgentID     uuid.UUID `json:"agent_id" db:"agent_id"`
	WorkspaceID uuid.UUID `json:"workspace_id" db:"workspace_id"`
	// Role is the workspace_role enum (owner/admin/member/viewer) — the
	// agent's permission level IN THIS WORKSPACE, distinct from
	// Agent.Role (the team role, e.g. "developer").
	Role         string     `json:"role" db:"role"`
	APIKeyPrefix string     `json:"api_key_prefix" db:"api_key_prefix"`
	APIKeyHash   string     `json:"-" db:"api_key_hash"`
	InvitedBy    *uuid.UUID `json:"invited_by,omitempty" db:"invited_by"`
	CreatedAt    time.Time  `json:"created_at" db:"created_at"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty" db:"revoked_at"`
}

// IsRevoked reports whether this connection has been revoked.
func (g *AgentWorkspaceGrant) IsRevoked() bool {
	return g.RevokedAt != nil
}
