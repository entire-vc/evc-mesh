package domain

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// SystemParams is the JSON object of values a system comment's text is built
// from (comments.system_params, nullable jsonb). A SQL NULL scans to nil —
// json.RawMessage alone cannot take NULL — and a nil value marshals as null and
// is stored as NULL.
type SystemParams json.RawMessage

// Scan implements sql.Scanner.
func (p *SystemParams) Scan(src interface{}) error {
	switch v := src.(type) {
	case nil:
		*p = nil
	case []byte:
		*p = append((*p)[:0], v...)
	case string:
		*p = SystemParams(v)
	default:
		return fmt.Errorf("unsupported scan type %T for system_params", src)
	}
	return nil
}

// Value implements driver.Valuer.
func (p SystemParams) Value() (driver.Value, error) {
	if len(p) == 0 {
		return nil, nil
	}
	return []byte(p), nil
}

// MarshalJSON emits the stored document, or null when unset.
func (p SystemParams) MarshalJSON() ([]byte, error) {
	if len(p) == 0 {
		return []byte("null"), nil
	}
	return []byte(p), nil
}

// UnmarshalJSON keeps the raw document; JSON null becomes nil.
func (p *SystemParams) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*p = nil
		return nil
	}
	*p = append((*p)[:0], b...)
	return nil
}

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
	// SystemCode names the kind of a server-generated comment (the constants in
	// internal/service/system_codes.go); SystemParams holds the values its text
	// is built from. Both are NULL for human/agent comments and for every
	// comment written before the columns existed. No omitempty: the keys are
	// always present, null when unset.
	SystemCode   *string      `json:"system_code" db:"system_code"`
	SystemParams SystemParams `json:"system_params" db:"system_params"`
	IsInternal   bool         `json:"is_internal" db:"is_internal"`
	CreatedAt    time.Time    `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at" db:"updated_at"`

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

	// URL is the canonical deep-link straight to this comment: the task's
	// /t/<taskID> URL plus the ?comment=<id> focus parameter the web client
	// already understands (task-panel.tsx opens the comments tab on it,
	// comment-list.tsx scrolls to the [data-comment-id] element and
	// highlights it). Computed per-response by the handler, never stored,
	// hence db:"-" and omitempty.
	URL string `json:"url,omitempty" db:"-"`
}
