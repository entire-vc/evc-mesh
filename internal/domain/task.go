package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// AssignmentSource records who or what set the current task assignee.
// Used to enforce pin-assignee invariant (human pin cannot be overridden by rules or system).
type AssignmentSource string

const (
	AssignmentSourceHuman  AssignmentSource = "human"
	AssignmentSourceRule   AssignmentSource = "rule"
	AssignmentSourceSystem AssignmentSource = "system"
)

// AssigneeType determines whether a task is assigned to a user, agent, or nobody.
type AssigneeType string

const (
	AssigneeTypeUser       AssigneeType = "user"
	AssigneeTypeAgent      AssigneeType = "agent"
	AssigneeTypeUnassigned AssigneeType = "unassigned"
)

// Priority represents the urgency level of a task.
type Priority string

const (
	PriorityUrgent Priority = "urgent"
	PriorityHigh   Priority = "high"
	PriorityMedium Priority = "medium"
	PriorityLow    Priority = "low"
	PriorityNone   Priority = "none"
)

// DelegationLevel controls the autonomy level of an agent working on a task.
type DelegationLevel string

const (
	DelegationLevelAuto       DelegationLevel = "auto"
	DelegationLevelReview     DelegationLevel = "review"
	DelegationLevelSupervised DelegationLevel = "supervised"
)

// ActorType represents who performed an action (creating tasks, comments, etc.).
type ActorType string

const (
	ActorTypeUser   ActorType = "user"
	ActorTypeAgent  ActorType = "agent"
	ActorTypeSystem ActorType = "system"
)

// Task is the central entity -- a unit of work that can be assigned to users or agents.
type Task struct {
	ID           uuid.UUID    `json:"id" db:"id"`
	ProjectID    uuid.UUID    `json:"project_id" db:"project_id"`
	StatusID     uuid.UUID    `json:"status_id" db:"status_id"`
	Title        string       `json:"title" db:"title"`
	Description  string       `json:"description" db:"description"`
	AssigneeID   *uuid.UUID   `json:"assignee_id" db:"assignee_id"`
	AssigneeType AssigneeType `json:"assignee_type" db:"assignee_type"`
	// AssignedBy records whether the current assignee was set by a human, a rule engine, or the system.
	// When set to AssignmentSourceHuman, only another human-source call may override the assignment.
	AssignedBy     AssignmentSource `json:"assigned_by" db:"assigned_by"`
	Priority       Priority         `json:"priority" db:"priority"`
	ParentTaskID   *uuid.UUID       `json:"parent_task_id" db:"parent_task_id"`
	Position       float64          `json:"position" db:"position"`
	DueDate        *time.Time       `json:"due_date" db:"due_date"`
	EstimatedHours *float64         `json:"estimated_hours" db:"estimated_hours"`
	CustomFields   json.RawMessage  `json:"custom_fields" db:"custom_fields"`
	Labels         pq.StringArray   `json:"labels" db:"labels"`
	ThreadID       *string          `json:"thread_id,omitempty" db:"thread_id"`
	CreatedBy      uuid.UUID        `json:"created_by" db:"created_by"`
	CreatedByType  ActorType        `json:"created_by_type" db:"created_by_type"`
	CreatedAt      time.Time        `json:"created_at" db:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at" db:"updated_at"`
	CompletedAt    *time.Time       `json:"completed_at" db:"completed_at"`

	// Recurring series fields — populated when the task is an instance of a recurring schedule.
	RecurringScheduleID     *uuid.UUID `json:"recurring_schedule_id,omitempty" db:"recurring_schedule_id"`
	RecurringInstanceNumber *int       `json:"recurring_instance_number,omitempty" db:"recurring_instance_number"`

	DelegationLevel DelegationLevel `json:"delegation_level" db:"delegation_level"`
	// HumanGate is a sticky flag set when a human (Pavel) sign-off is required.
	// Once set, only a human actor may move the task to backlog/done/cancelled.
	HumanGate bool `json:"human_gate" db:"human_gate"`
	// IsShipped marks the task as terminally shipped. Once set, MoveTask to any
	// non-done category returns 422. Cleared only by an explicit PATCH /tasks/:id/unship.
	IsShipped bool `json:"is_shipped" db:"is_shipped"`

	// Checkout fields — set when an agent has an exclusive application-level lock on the task.
	CheckedOutBy       *uuid.UUID `json:"checked_out_by,omitempty" db:"checked_out_by"`
	CheckoutToken      *uuid.UUID `json:"checkout_token,omitempty" db:"checkout_token"`
	CheckoutExpires    *time.Time `json:"checkout_expires,omitempty" db:"checkout_expires"`
	CheckoutAcquiredAt *time.Time `json:"checkout_acquired_at,omitempty" db:"checkout_acquired_at"`

	// Computed fields — populated by enriched list/get queries, not stored columns.
	SubtaskCount  int     `json:"subtask_count"`
	AssigneeName  *string `json:"assignee_name,omitempty"`
	CreatedByName *string `json:"created_by_name,omitempty"`
	ArtifactCount int     `json:"artifact_count"`
	VCSLinkCount  int     `json:"vcs_link_count"`
	// URL is the canonical short deep-link, e.g. https://mesh.entire.host/t/<id>.
	// Populated by HTTP handlers from request scheme+host. Empty in non-HTTP contexts.
	URL string `json:"url,omitempty"`
}

// CheckoutInfo carries checkout state for API responses on GET task endpoints.
// If the task has no active checkout, the field is omitted (nil pointer in the response struct).
type CheckoutInfo struct {
	CheckedOutBy uuid.UUID `json:"checked_out_by"`
	AgentName    string    `json:"agent_name,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	IsExpired    bool      `json:"is_expired"`
}

// DependencyType represents the relationship between two tasks.
type DependencyType string

const (
	DependencyTypeBlocks    DependencyType = "blocks"
	DependencyTypeRelatesTo DependencyType = "relates_to"
	DependencyTypeIsChildOf DependencyType = "is_child_of"
)

// TaskDependency represents a dependency relationship between two tasks.
type TaskDependency struct {
	ID              uuid.UUID      `json:"id" db:"id"`
	TaskID          uuid.UUID      `json:"task_id" db:"task_id"`
	DependsOnTaskID uuid.UUID      `json:"depends_on_task_id" db:"depends_on_task_id"`
	DependencyType  DependencyType `json:"dependency_type" db:"dependency_type"`
	CreatedAt       time.Time      `json:"created_at" db:"created_at"`
}
