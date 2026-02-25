package domain

import (
	"time"

	"github.com/google/uuid"
)

// InitiativeStatus represents the lifecycle state of an initiative.
type InitiativeStatus string

const (
	InitiativeStatusActive    InitiativeStatus = "active"
	InitiativeStatusCompleted InitiativeStatus = "completed"
	InitiativeStatusArchived  InitiativeStatus = "archived"
)

// Initiative is a strategic objective grouping multiple projects.
type Initiative struct {
	ID          uuid.UUID        `json:"id" db:"id"`
	WorkspaceID uuid.UUID        `json:"workspace_id" db:"workspace_id"`
	Name        string           `json:"name" db:"name"`
	Description string           `json:"description" db:"description"`
	Status      InitiativeStatus `json:"status" db:"status"`
	TargetDate  *time.Time       `json:"target_date" db:"target_date"`
	CreatedBy   uuid.UUID        `json:"created_by" db:"created_by"`
	CreatedAt   time.Time        `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at" db:"updated_at"`

	// LinkedProjects is populated by the service layer on GetByID.
	LinkedProjects []Project `json:"linked_projects,omitempty" db:"-"`
}
