package domain

import (
	"time"

	"github.com/google/uuid"
)

// CommentMention is a row in comment_mentions — one recipient per comment.
type CommentMention struct {
	CommentID     uuid.UUID  `db:"comment_id"`
	MentionedID   uuid.UUID  `db:"mentioned_id"`
	MentionedKind string     `db:"mentioned_kind"` // "agent" | "user"
	MentionedSlug string     `db:"mentioned_slug"`
	ExtractedAt   time.Time  `db:"extracted_at"`
	SeenAt        *time.Time `db:"seen_at"`
}

// CommentMentionView is the enriched shape returned by GET /me/mentions.
type CommentMentionView struct {
	CommentID     uuid.UUID  `json:"comment_id"    db:"comment_id"`
	MentionedID   uuid.UUID  `json:"mentioned_id"  db:"mentioned_id"`
	MentionedKind string     `json:"mentioned_kind" db:"mentioned_kind"`
	MentionedSlug string     `json:"mentioned_slug" db:"mentioned_slug"`
	ExtractedAt   time.Time  `json:"extracted_at"  db:"extracted_at"`
	SeenAt        *time.Time `json:"seen_at"       db:"seen_at"`

	// Enriched from joins
	TaskID      uuid.UUID `json:"task_id"      db:"task_id"`
	TaskTitle   string    `json:"task_title"   db:"task_title"`
	ProjectID   uuid.UUID `json:"project_id"   db:"project_id"`
	CommentBody string    `json:"comment_body" db:"comment_body"`
	AuthorID    uuid.UUID `json:"author_id"    db:"author_id"`
	AuthorName  string    `json:"author_name"  db:"author_name"`
}
