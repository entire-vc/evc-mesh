package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// TaskTemplate defines a reusable template for creating tasks within a project.
type TaskTemplate struct {
	ID                  uuid.UUID       `json:"id" db:"id"`
	ProjectID           uuid.UUID       `json:"project_id" db:"project_id"`
	Name                string          `json:"name" db:"name"`
	Description         string          `json:"description" db:"description"`
	TitleTemplate       string          `json:"title_template" db:"title_template"`
	DescriptionTemplate string          `json:"description_template" db:"description_template"`
	Priority            Priority        `json:"priority" db:"priority"`
	Labels              pq.StringArray  `json:"labels" db:"labels"`
	EstimatedHours      *float64        `json:"estimated_hours" db:"estimated_hours"`
	CustomFields        json.RawMessage `json:"custom_fields" db:"custom_fields"`
	AssigneeID          *uuid.UUID      `json:"assignee_id" db:"assignee_id"`
	AssigneeType        *AssigneeType   `json:"assignee_type" db:"assignee_type"`
	StatusID            *uuid.UUID      `json:"status_id" db:"status_id"`
	CreatedBy           *uuid.UUID      `json:"created_by" db:"created_by"`
	CreatedAt           time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at" db:"updated_at"`
}

// CreateTemplateInput holds the fields required to create a task template.
type CreateTemplateInput struct {
	ProjectID           uuid.UUID
	Name                string
	Description         string
	TitleTemplate       string
	DescriptionTemplate string
	Priority            Priority
	Labels              []string
	EstimatedHours      *float64
	CustomFields        json.RawMessage
	AssigneeID          *uuid.UUID
	AssigneeType        *AssigneeType
	StatusID            *uuid.UUID
	CreatedBy           *uuid.UUID
}

// UpdateTemplateInput holds the optional fields for updating a task template.
// Nil pointer fields are ignored (no update applied).
type UpdateTemplateInput struct {
	Name                *string
	Description         *string
	TitleTemplate       *string
	DescriptionTemplate *string
	Priority            *Priority
	Labels              *[]string
	EstimatedHours      *float64
	CustomFields        json.RawMessage
	AssigneeID          *uuid.UUID
	AssigneeType        *AssigneeType
	StatusID            *uuid.UUID
}
