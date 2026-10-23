package domain

import "github.com/google/uuid"

// Mentionable represents a workspace member (agent or user) that can be @-mentioned in a comment.
type Mentionable struct {
	ID          uuid.UUID `json:"id"`
	Kind        string    `json:"kind"`
	Slug        string    `json:"slug"`
	DisplayName string    `json:"display_name"`
	AvatarURL   *string   `json:"avatar_url"`
	// ShortTag is the agent's role label (Mesh an internal ticket), so a mention
	// picker can badge agents inline. Always nil for Kind == "user" — users
	// have no equivalent field, and the frontend distinguishes by Kind.
	ShortTag *string `json:"short_tag"`
	// IsServiceUser marks a user entry that acts on behalf of an agent; AgentID
	// then names that agent. Always false/null for people and for agent entries.
	IsServiceUser bool       `json:"is_service_user"`
	AgentID       *uuid.UUID `json:"agent_id"`
}
