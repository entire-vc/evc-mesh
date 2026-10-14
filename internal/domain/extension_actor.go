package domain

import (
	"time"

	"github.com/google/uuid"
)

// ActorTypeExtension is the principal type of an extension's service actor
// (`ext:<id>@<workspace>`). It is neither a user nor an agent: it authenticates
// with its own token and is limited to the capabilities consented to. Its value
// was added to the actor_type enum by migration 20261014004.
const ActorTypeExtension ActorType = "extension"

// ExtensionActor is a row of extension_actors: the service principal of one
// extension connection. TokenHash/PrevTokenHash are digests; the token itself
// exists only in the response that issued it.
type ExtensionActor struct {
	ConnectionID       uuid.UUID  `json:"connection_id" db:"connection_id"`
	WorkspaceID        uuid.UUID  `json:"workspace_id" db:"workspace_id"`
	ActorID            uuid.UUID  `json:"actor_id" db:"actor_id"`
	ExtensionID        string     `json:"extension_id" db:"extension_id"`
	DisplayName        string     `json:"display_name" db:"display_name"`
	TokenHash          string     `json:"-" db:"token_hash"`
	PrevTokenHash      *string    `json:"-" db:"prev_token_hash"`
	PrevTokenExpiresAt *time.Time `json:"-" db:"prev_token_expires_at"`
	Capabilities       []byte     `json:"-" db:"capabilities"`
	ViolationCount     int64      `json:"violation_count" db:"violation_count"`
	LastViolationAt    *time.Time `json:"last_violation_at,omitempty" db:"last_violation_at"`
	LastViolation      string     `json:"last_violation,omitempty" db:"last_violation"`
	IssuedAt           time.Time  `json:"issued_at" db:"issued_at"`
	RotatedAt          *time.Time `json:"rotated_at,omitempty" db:"rotated_at"`
	RevokedAt          *time.Time `json:"revoked_at,omitempty" db:"revoked_at"`
}

// Principal returns the stable principal name `ext:<id>@<workspace>`.
func (a *ExtensionActor) Principal() string {
	return "ext:" + a.ExtensionID + "@" + a.WorkspaceID.String()
}

// AgentAPIKey is an additional API key of an agent (agent_api_keys). The agent's
// original key is not a row here; it is addressed as AgentPrimaryKeyID.
type AgentAPIKey struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	AgentID      uuid.UUID  `json:"agent_id" db:"agent_id"`
	WorkspaceID  uuid.UUID  `json:"workspace_id" db:"workspace_id"`
	KeyPrefix    string     `json:"-" db:"key_prefix"`
	KeySHA256    string     `json:"-" db:"key_sha256"`
	Label        string     `json:"label" db:"label"`
	IssuedByType ActorType  `json:"issued_by_type" db:"issued_by_type"`
	IssuedByID   uuid.UUID  `json:"issued_by_id" db:"issued_by_id"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty" db:"expires_at"`
	CreatedAt    time.Time  `json:"created_at" db:"created_at"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty" db:"revoked_at"`
}

// AgentPrimaryKeyID is the key_id that addresses the agent's original key in
// DELETE /api/v1/agents/{id}/keys/{key_id}.
const AgentPrimaryKeyID = "primary"
