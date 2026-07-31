package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// User represents a registered human user who authenticates via JWT.
type User struct {
	ID           uuid.UUID `json:"id" db:"id"`
	Email        string    `json:"email" db:"email"`
	PasswordHash string    `json:"-" db:"password_hash"`
	Name         string    `json:"name" db:"display_name"`
	Username     string    `json:"username" db:"username"`
	AvatarURL    string    `json:"avatar_url" db:"avatar_url"`
	IsActive     bool      `json:"is_active" db:"is_active"`
	// DisplayNameSelfSet records that Name was chosen by this person rather than
	// derived or seeded on their behalf. A workspace admin may fill in an unowned
	// name; once this is true the name belongs to the account and no workspace
	// admin can overwrite it. See migration 20260731089 for why provenance rather
	// than role is the thing that makes an admin edit safe here.
	DisplayNameSelfSet bool      `json:"display_name_self_set" db:"display_name_self_set"`
	CreatedAt          time.Time `json:"created_at" db:"created_at"`
	UpdatedAt          time.Time `json:"updated_at" db:"updated_at"`
}

// NameIsPlaceholder reports whether the account has no real display name — the
// stored value is just the address echoed back, which is what every path that
// never asked for a name leaves behind. The UI uses this to show the address
// once with a "name not set" affordance instead of printing it twice as if it
// were a name.
func (u *User) NameIsPlaceholder() bool {
	return IsPlaceholderName(u.Name, u.Email)
}

// IsPlaceholderName reports whether name carries no information beyond email.
func IsPlaceholderName(name, email string) bool {
	return strings.TrimSpace(name) == "" || strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(email))
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
//
// Username is here so the UI has a stable secondary identifier — the thing that
// tells two people called "Alex" apart — that is not an email address. Before
// it existed the only disambiguator available to a template was the address,
// which is why addresses ended up printed in places that only needed a label.
type UserBrief struct {
	ID        uuid.UUID `json:"id" db:"id"`
	Email     string    `json:"email" db:"email"`
	Name      string    `json:"name" db:"display_name"`
	Username  string    `json:"username" db:"username"`
	AvatarURL string    `json:"avatar_url" db:"avatar_url"`
}

// NameIsPlaceholder reports whether this brief carries no real name.
func (u UserBrief) NameIsPlaceholder() bool {
	return IsPlaceholderName(u.Name, u.Email)
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
