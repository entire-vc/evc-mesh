package domain

import (
	"encoding/json"

	"github.com/google/uuid"
)

// StatusCategory is a semantic category that agents use to understand task status
// regardless of the user-defined status name.
type StatusCategory string

const (
	StatusCategoryBacklog    StatusCategory = "backlog"
	StatusCategoryTodo       StatusCategory = "todo"
	StatusCategoryInProgress StatusCategory = "in_progress"
	StatusCategoryReview     StatusCategory = "review"
	StatusCategoryDone       StatusCategory = "done"
	StatusCategoryCancelled  StatusCategory = "cancelled"
)

// TaskStatus is a customizable status column within a project.
// Each project defines its own statuses with a semantic category.
type TaskStatus struct {
	ID             uuid.UUID       `json:"id" db:"id"`
	ProjectID      uuid.UUID       `json:"project_id" db:"project_id"`
	Name           string          `json:"name" db:"name"`
	Slug           string          `json:"slug" db:"slug"`
	Color          string          `json:"color" db:"color"`
	Position       int             `json:"position" db:"position"`
	Category       StatusCategory  `json:"category" db:"category"`
	IsDefault      bool            `json:"is_default" db:"is_default"`
	AutoTransition json.RawMessage `json:"auto_transition" db:"auto_transition"`
}
