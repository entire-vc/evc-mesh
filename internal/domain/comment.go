package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Comment is a threaded message on a task, authored by a user, agent, or system.
// Internal comments (is_internal=true) are visible only to agents for inter-agent communication.
type Comment struct {
	ID              uuid.UUID       `json:"id" db:"id"`
	TaskID          uuid.UUID       `json:"task_id" db:"task_id"`
	ParentCommentID *uuid.UUID      `json:"parent_comment_id" db:"parent_comment_id"`
	AuthorID        uuid.UUID       `json:"author_id" db:"author_id"`
	AuthorType      ActorType       `json:"author_type" db:"author_type"`
	Body            string          `json:"body" db:"body"`
	Metadata        json.RawMessage `json:"metadata" db:"metadata"`
	IsInternal      bool            `json:"is_internal" db:"is_internal"`
	CreatedAt       time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at" db:"updated_at"`

	// Computed (not a DB column — populated via subquery in SELECT).
	AuthorName *string `json:"author_name,omitempty" db:"author_name"`
}
