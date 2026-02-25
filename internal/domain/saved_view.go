package domain

import (
	"time"

	"github.com/google/uuid"
)

// SavedView represents a stored filter/sort configuration for a project view.
type SavedView struct {
	ID        uuid.UUID              `json:"id" db:"id"`
	ProjectID uuid.UUID              `json:"project_id" db:"project_id"`
	Name      string                 `json:"name" db:"name"`
	ViewType  string                 `json:"view_type" db:"view_type"`
	Filters   map[string]interface{} `json:"filters" db:"filters"`
	SortBy    *string                `json:"sort_by,omitempty" db:"sort_by"`
	SortOrder *string                `json:"sort_order,omitempty" db:"sort_order"`
	Columns   []string               `json:"columns,omitempty" db:"columns"`
	IsShared  bool                   `json:"is_shared" db:"is_shared"`
	CreatedBy uuid.UUID              `json:"created_by" db:"created_by"`
	CreatedAt time.Time              `json:"created_at" db:"created_at"`
	UpdatedAt time.Time              `json:"updated_at" db:"updated_at"`
}

// CreateSavedViewInput holds parameters for creating a saved view.
type CreateSavedViewInput struct {
	ProjectID uuid.UUID
	Name      string
	ViewType  string
	Filters   map[string]interface{}
	SortBy    *string
	SortOrder *string
	Columns   []string
	IsShared  bool
	CreatedBy uuid.UUID
}

// UpdateSavedViewInput holds parameters for partially updating a saved view.
type UpdateSavedViewInput struct {
	Name      *string
	ViewType  *string
	Filters   map[string]interface{}
	SortBy    *string
	SortOrder *string
	Columns   []string
	IsShared  *bool
}
