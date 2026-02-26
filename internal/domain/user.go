package domain

import (
	"time"

	"github.com/google/uuid"
)

// User represents a registered human user who authenticates via JWT.
type User struct {
	ID           uuid.UUID `json:"id" db:"id"`
	Email        string    `json:"email" db:"email"`
	PasswordHash string    `json:"-" db:"password_hash"`
	Name         string    `json:"name" db:"display_name"`
	AvatarURL    string    `json:"avatar_url" db:"avatar_url"`
	IsActive     bool      `json:"is_active" db:"is_active"`
	CreatedAt    time.Time `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time `json:"updated_at" db:"updated_at"`
}

// WorkspaceMember represents a user's membership in a workspace with a specific role.
type WorkspaceMember struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	WorkspaceID uuid.UUID  `json:"workspace_id" db:"workspace_id"`
	UserID      uuid.UUID  `json:"user_id" db:"user_id"`
	Role        string     `json:"role" db:"role"`
	InvitedBy   *uuid.UUID `json:"invited_by,omitempty" db:"invited_by"`
	CreatedAt   time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at" db:"updated_at"`
}

// Workspace member roles.
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
	RoleViewer = "viewer"
)

// UserBrief holds minimal public user information for embedding in list responses.
type UserBrief struct {
	ID        uuid.UUID `json:"id" db:"id"`
	Email     string    `json:"email" db:"email"`
	Name      string    `json:"name" db:"display_name"`
	AvatarURL string    `json:"avatar_url" db:"avatar_url"`
}

// WorkspaceMemberWithUser embeds WorkspaceMember with the associated user's brief info.
type WorkspaceMemberWithUser struct {
	WorkspaceMember
	User UserBrief `json:"user"`
}

// UserWithMemberStatus combines a UserBrief with a flag indicating workspace membership.
type UserWithMemberStatus struct {
	UserBrief
	IsMember bool `json:"is_member"`
}
