package domain

import "github.com/google/uuid"

// Mentionable represents a workspace member (agent or user) that can be @-mentioned in a comment.
type Mentionable struct {
	ID          uuid.UUID `json:"id"`
	Kind        string    `json:"kind"`         // "agent" | "user"
	Slug        string    `json:"slug"`
	DisplayName string    `json:"display_name"`
	AvatarURL   *string   `json:"avatar_url"`
}
