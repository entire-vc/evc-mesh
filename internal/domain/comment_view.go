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
	CreatedAt   time.Time `json:"created_at"   db:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"   db:"updated_at"`
}

// CommentViewPage is the cursor-paginated response for comment view endpoints.
// NextCursor is nil when there are no more pages.
type CommentViewPage struct {
	Items      []CommentView `json:"items"`
	NextCursor *time.Time    `json:"next_cursor"`
}
