package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
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

// ActorType represents who performed an action (creating tasks, comments, etc.).
type ActorType string

const (
	ActorTypeUser   ActorType = "user"
	ActorTypeAgent  ActorType = "agent"
	ActorTypeSystem ActorType = "system"
)

// Task is the central entity -- a unit of work that can be assigned to users or agents.
type Task struct {
	ID             uuid.UUID       `json:"id" db:"id"`
	ProjectID      uuid.UUID       `json:"project_id" db:"project_id"`
	StatusID       uuid.UUID       `json:"status_id" db:"status_id"`
	Title          string          `json:"title" db:"title"`
	Description    string          `json:"description" db:"description"`
	AssigneeID     *uuid.UUID      `json:"assignee_id" db:"assignee_id"`
	AssigneeType   AssigneeType    `json:"assignee_type" db:"assignee_type"`
	Priority       Priority        `json:"priority" db:"priority"`
	ParentTaskID   *uuid.UUID      `json:"parent_task_id" db:"parent_task_id"`
	Position       float64         `json:"position" db:"position"`
	DueDate        *time.Time      `json:"due_date" db:"due_date"`
	EstimatedHours *float64        `json:"estimated_hours" db:"estimated_hours"`
	CustomFields   json.RawMessage `json:"custom_fields" db:"custom_fields"`
	Labels         pq.StringArray  `json:"labels" db:"labels"`
	CreatedBy      uuid.UUID       `json:"created_by" db:"created_by"`
	CreatedByType  ActorType       `json:"created_by_type" db:"created_by_type"`
	CreatedAt      time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at" db:"updated_at"`
	CompletedAt    *time.Time      `json:"completed_at" db:"completed_at"`

	// Computed fields — populated by enriched list/get queries, not stored columns.
	SubtaskCount  int     `json:"subtask_count"`
	AssigneeName  *string `json:"assignee_name,omitempty"`
	ArtifactCount int     `json:"artifact_count"`
	VCSLinkCount  int     `json:"vcs_link_count"`
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
