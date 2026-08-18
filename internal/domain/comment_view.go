package domain

import (
	"time"

	"github.com/google/uuid"
)

// CommentView is the enriched comment shape used for activity feed endpoints.
type CommentView struct {
	CommentID   uuid.UUID `json:"comment_id"   db:"comment_id"`
	TaskID      uuid.UUID `json:"task_id"      db:"task_id"`
	TaskTitle   string    `json:"task_title"   db:"task_title"`
	ProjectID   uuid.UUID `json:"project_id"   db:"project_id"`
	ProjectName string    `json:"project_name" db:"project_name"`
	CommentBody string    `json:"comment_body" db:"comment_body"`
	AuthorID    uuid.UUID `json:"author_id"    db:"author_id"`
	AuthorName  string    `json:"author_name"  db:"author_name"`
	AuthorKind  string    `json:"author_kind"  db:"author_kind"`
	IsInternal  bool      `json:"is_internal"  db:"is_internal"`
	CreatedAt   time.Time `json:"created_at"   db:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"   db:"updated_at"`
}

// CommentCursor identifies a page boundary as a (created_at, id) tuple rather
// than created_at alone, which is not unique — a bulk-insert (or any two
// comments landing in the same microsecond) can put dozens of rows on one
// timestamp, and a strict `created_at < cursor` comparison silently drops
// every unread member of that tie group at a page boundary. Same class as
// the task-list cursor fix in #a1012e55. See #c6dc694e.
type CommentCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// CommentViewPage is the cursor-paginated response for comment view endpoints.
// NextCursor/NextCursorID are nil together when there are no more pages.
// NextCursor alone (RFC3339) stays valid for old clients paginating with only
// a timestamp; NextCursorID is the tie-breaker new clients should also send
// back as before_id for a page boundary that lands inside a tie group.
type CommentViewPage struct {
	Items        []CommentView `json:"items"`
	NextCursor   *time.Time    `json:"next_cursor"`
	NextCursorID *uuid.UUID    `json:"next_cursor_id"`
}
