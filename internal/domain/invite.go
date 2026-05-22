package domain

import (
	"time"

	"github.com/google/uuid"
)

// WorkspaceInvite represents a pending email invitation to a workspace.
type WorkspaceInvite struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	WorkspaceID uuid.UUID  `json:"workspace_id" db:"workspace_id"`
	Email       string     `json:"email" db:"email"`
	Role        string     `json:"role" db:"role"`
	Token       string     `json:"token" db:"token"`
	InvitedBy   *uuid.UUID `json:"invited_by,omitempty" db:"invited_by"`
	ExpiresAt   time.Time  `json:"expires_at" db:"expires_at"`
	AcceptedAt  *time.Time `json:"accepted_at,omitempty" db:"accepted_at"`
	CreatedAt   time.Time  `json:"created_at" db:"created_at"`
}

// IsPending returns true if the invite has not been accepted and has not expired.
func (i *WorkspaceInvite) IsPending() bool {
	return i.AcceptedAt == nil && time.Now().Before(i.ExpiresAt)
}
