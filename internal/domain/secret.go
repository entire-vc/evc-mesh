package domain

import (
	"time"

	"github.com/google/uuid"
)

// SecretScope identifies which resource a secret is materialized for.
type SecretScope string

const (
	SecretScopeWorkspace SecretScope = "workspace"
	SecretScopeProject   SecretScope = "project"
	SecretScopeAgent     SecretScope = "agent"
)

// Secret is the read-side view of a row in the secrets table. It is
// deliberately structured so that a decrypted or encrypted value has no
// field to occupy: this type is what every read path in the repository and
// service layers returns, so "the value isn't in here" is a property of the
// Go type, not just of the query that happens to have populated it. The
// negative test in S6 exists to prove that no other path exists; this type
// is what makes that provable at all rather than just tested-for.
type Secret struct {
	ID                uuid.UUID   `json:"id" db:"id"`
	WorkspaceID       uuid.UUID   `json:"workspace_id" db:"workspace_id"`
	ProjectID         *uuid.UUID  `json:"project_id,omitempty" db:"project_id"`
	AgentID           *uuid.UUID  `json:"agent_id,omitempty" db:"agent_id"`
	Scope             SecretScope `json:"scope" db:"scope"`
	Name              string      `json:"name" db:"name"`
	ValueSHA256Prefix string      `json:"value_sha256_prefix" db:"value_sha256_prefix"`
	ValueLength       int         `json:"value_length" db:"value_length"`
	ValueCharClass    string      `json:"value_char_class" db:"value_char_class"`
	ExpiresAt         *time.Time  `json:"expires_at,omitempty" db:"expires_at"`
	CreatedBy         uuid.UUID   `json:"created_by" db:"created_by"`
	CreatedByType     ActorType   `json:"created_by_type" db:"created_by_type"`
	CreatedAt         time.Time   `json:"created_at" db:"created_at"`
	RotatedAt         *time.Time  `json:"rotated_at,omitempty" db:"rotated_at"`
}

// CreateSecretInput carries a plaintext value from the API boundary down to
// the repository, which encrypts it on the way in. Value never gets a `db`
// tag and is never embedded in Secret — it exists only long enough to be
// encrypted and must not be logged, and must not survive past the Create
// call that consumes it.
type CreateSecretInput struct {
	WorkspaceID   uuid.UUID
	ProjectID     *uuid.UUID
	AgentID       *uuid.UUID
	Scope         SecretScope
	Name          string
	Value         string
	ExpiresAt     *time.Time
	CreatedBy     uuid.UUID
	CreatedByType ActorType
}

// MaterializedSecret is the one place in this codebase a decrypted value is
// allowed to exist as a Go value. It exists only for the spawn-time
// materializer (S4) to write into a lane's env file; nothing else should
// construct or hold one. Expired carries the expiry state explicitly so the
// materializer can name the secret in a loud spawn error instead of writing
// a silently empty variable.
type MaterializedSecret struct {
	Name    string
	Value   string
	Expired bool
}
