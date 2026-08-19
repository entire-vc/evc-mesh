package domain

import (
	"time"

	"github.com/google/uuid"
)

// DocumentCommentMention is a row in document_comment_mentions — one recipient
// of one @-mention in one document comment.
//
// The task-comment equivalent is CommentMention, and the shape is deliberately
// identical: the two live in separate tables only because their parent comments
// do (see migrations/20260820102_create_document_comment_mentions.sql).
type DocumentCommentMention struct {
	CommentID     uuid.UUID  `db:"comment_id"`
	MentionedID   uuid.UUID  `db:"mentioned_id"`
	MentionedKind string     `db:"mentioned_kind"` // "agent" | "user"
	MentionedSlug string     `db:"mentioned_slug"`
	ExtractedAt   time.Time  `db:"extracted_at"`
	SeenAt        *time.Time `db:"seen_at"`
}

// DocumentCommentMentionView is the enriched shape returned by
// GET /me/document-mentions.
//
// It names a document where CommentMentionView names a task, which is the whole
// difference between them and the reason the two are not one type: a caller has
// to know whether to open /t/<id> or the document tree, and a nullable task id
// on a shared struct would make that a guess.
type DocumentCommentMentionView struct {
	CommentID     uuid.UUID  `json:"comment_id"     db:"comment_id"`
	MentionedID   uuid.UUID  `json:"mentioned_id"   db:"mentioned_id"`
	MentionedKind string     `json:"mentioned_kind" db:"mentioned_kind"`
	MentionedSlug string     `json:"mentioned_slug" db:"mentioned_slug"`
	ExtractedAt   time.Time  `json:"extracted_at"   db:"extracted_at"`
	SeenAt        *time.Time `json:"seen_at"        db:"seen_at"`

	// Enriched from joins.
	DocumentID    uuid.UUID `json:"document_id"    db:"document_id"`
	DocumentTitle string    `json:"document_title" db:"document_title"`
	DocumentSlug  string    `json:"document_slug"  db:"document_slug"`
	ProjectID     uuid.UUID `json:"project_id"     db:"project_id"`
	CommentBody   string    `json:"comment_body"   db:"comment_body"`
	AuthorID      uuid.UUID `json:"author_id"      db:"author_id"`
	AuthorName    string    `json:"author_name"    db:"author_name"`
}
