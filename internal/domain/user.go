package domain

import (
	"encoding/json"
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
	DisplayNameSelfSet bool `json:"display_name_self_set" db:"display_name_self_set"`
	// Locale is the interface language ("en"|"ru"). LocaleSetExplicitly is false
	// until the person picks a language themselves; the web client uses it to
	// offer (not force) a switch when the browser language differs.
	Locale              string `json:"locale" db:"locale"`
	LocaleSetExplicitly bool   `json:"locale_set_explicitly" db:"locale_set_explicitly"`
	// ServiceAgentID is set when this account is a service user acting on behalf
	// of an agent (e.g. "Hana (QA Mesh)"); nil for people.
	ServiceAgentID *uuid.UUID `json:"service_agent_id,omitempty" db:"service_agent_id"`
	CreatedAt      time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at" db:"updated_at"`
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
	// The person's team profile in this workspace — the human counterpart of
	// an agent's capabilities/responsibility_zone/working_hours. Written by
	// PUT /workspaces/:ws_id/members/:user_id/profile and by config import,
	// read by the team directory.
	Capabilities       json.RawMessage `json:"capabilities" db:"capabilities"`
	ResponsibilityZone string          `json:"responsibility_zone" db:"responsibility_zone"`
	Availability       string          `json:"availability" db:"availability"`
	CreatedAt          time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at" db:"updated_at"`
}

// MemberProfileUpdate is a partial update of a member's team profile, with the
// same semantics as AgentProfileUpdate: a field omitted from the request is
// left untouched; send it explicitly (e.g. "responsibility_zone": "") to clear it.
type MemberProfileUpdate struct {
	Capabilities       json.RawMessage `json:"capabilities"`
	ResponsibilityZone *string         `json:"responsibility_zone,omitempty"`
	Availability       *string         `json:"availability,omitempty"`
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
