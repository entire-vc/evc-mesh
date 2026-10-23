package domain

import (
	"time"

	"github.com/google/uuid"
)

// ChatSessionRevokedSuperseded marks a token replaced by a renewal. Any other
// revoked_reason ends the chat session for good (see ChatSessionToken.SessionEnded).
const ChatSessionRevokedSuperseded = "superseded"

// ChatSessionRevokedStop is the reason recorded when a turn is stopped.
const ChatSessionRevokedStop = "stop"

// ChatSessionToken is the server-side record behind a cst_ bearer: the
// immutable binding of (user, agent, workspace, project, chat, session, epoch).
// The bearer itself is never stored, only its hash.
type ChatSessionToken struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	TokenHash      string     `json:"-" db:"token_hash"`
	UserID         uuid.UUID  `json:"user_id" db:"user_id"`
	AgentID        uuid.UUID  `json:"agent_id" db:"agent_id"`
	WorkspaceID    uuid.UUID  `json:"workspace_id" db:"workspace_id"`
	ProjectID      uuid.UUID  `json:"project_id" db:"project_id"`
	ChatThreadID   uuid.UUID  `json:"chat_thread_id" db:"chat_thread_id"`
	ChatSessionID  uuid.UUID  `json:"chat_session_id" db:"chat_session_id"`
	RunnerID       string     `json:"runner_id" db:"runner_id"`
	ExecutionEpoch int64      `json:"execution_epoch" db:"execution_epoch"`
	IssuedAt       time.Time  `json:"issued_at" db:"issued_at"`
	ExpiresAt      time.Time  `json:"expires_at" db:"expires_at"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty" db:"revoked_at"`
	RevokedReason  *string    `json:"revoked_reason,omitempty" db:"revoked_reason"`
}

// IsUsable reports whether the token may authenticate right now: not revoked
// and not past expires_at (server time decides, never the client's).
func (t *ChatSessionToken) IsUsable(now time.Time) bool {
	if t.RevokedAt != nil && !t.RevokedAt.After(now) {
		return false
	}
	return now.Before(t.ExpiresAt)
}

// SessionEnded reports whether the token was revoked for a reason that ends the
// whole session (stop, manual revoke) rather than by a renewal replacing it.
func (t *ChatSessionToken) SessionEnded() bool {
	return t.RevokedAt != nil && (t.RevokedReason == nil || *t.RevokedReason != ChatSessionRevokedSuperseded)
}
