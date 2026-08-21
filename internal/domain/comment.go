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
	//
	// No omitempty: a nil AuthorName (system-authored comment, or an agent
	// whose row is soft-deleted) must still serialize the key as JSON
	// `null`, not omit it entirely. omitempty on a *string treats a nil
	// pointer as "empty" and drops the field, which made system-authored
	// comments the only branch of this CASE (comment_repo.go's
	// commentEnrichedSelect) to come back with author_name missing from the
	// response altogether instead of null like every other unresolved
	// branch. Consumers already use optional chaining (comment.author_name?.),
	// which treats null and undefined identically, so this does not change
	// any rendering behavior — only the wire shape.
	AuthorName *string `json:"author_name" db:"author_name"`

	// Delivery is what became of each @-addressed handle on this comment:
	// who it reached, over which path, and — when it reached nobody — the
	// named reason. Populated by a separate lookup, never scanned from the
	// comments table, hence db:"-".
	//
	// omitempty is deliberate: a comment that addressed nobody carries no
	// delivery record, and an empty array next to every ordinary comment
	// would be noise on the one field whose whole value is that it only
	// appears when there is something to say.
	Delivery []CommentDeliveryOutcome `json:"delivery,omitempty" db:"-"`
}
