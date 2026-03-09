package domain

import (
	"time"

	"github.com/google/uuid"
)

// AutoTransitionTrigger defines what event activates an auto-transition rule.
type AutoTransitionTrigger string

const (
	// TriggerAllSubtasksDone fires when all direct subtasks reach the "done" category.
	TriggerAllSubtasksDone AutoTransitionTrigger = "all_subtasks_done"
	// TriggerBlockingDepResolved fires when all blocking dependencies are in the "done" category.
	TriggerBlockingDepResolved AutoTransitionTrigger = "blocking_dep_resolved"
)

// AutoTransitionRule defines a project-level rule for automatic task transitions.
type AutoTransitionRule struct {
	ID             uuid.UUID             `json:"id" db:"id"`
	ProjectID      uuid.UUID             `json:"project_id" db:"project_id"`
	Trigger        AutoTransitionTrigger `json:"trigger" db:"trigger"`
	TargetStatusID uuid.UUID             `json:"target_status_id" db:"target_status_id"`
	IsEnabled      bool                  `json:"is_enabled" db:"is_enabled"`
	CreatedAt      time.Time             `json:"created_at" db:"created_at"`
	UpdatedAt      time.Time             `json:"updated_at" db:"updated_at"`
}
