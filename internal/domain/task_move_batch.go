package domain

import "github.com/google/uuid"

// Leaf modes of the atomic batch project move.
const (
	// MoveLeafDetachKeepBlocks detaches a moved task from a parent that stays
	// behind and makes that parent wait on it through a `blocks` dependency.
	MoveLeafDetachKeepBlocks = "detach_parent_keep_blocks"
	// MoveLeafRefuse refuses the whole batch when a moved task has a parent
	// that does not move with it.
	MoveLeafRefuse = "refuse"
)

// MoveBatchInput describes one all-or-nothing move of tasks into a project.
type MoveBatchInput struct {
	WorkspaceID   uuid.UUID
	ProjectID     uuid.UUID
	TaskIDs       []uuid.UUID
	StatusMapping map[uuid.UUID]uuid.UUID // source status id -> target status id
	LeafMode      string
	DryRun        bool
}

// MoveBatchDependency is a task_dependencies row as stored.
type MoveBatchDependency struct {
	ID              uuid.UUID `json:"id" db:"id"`
	TaskID          uuid.UUID `json:"task_id" db:"task_id"`
	DependsOnTaskID uuid.UUID `json:"depends_on_task_id" db:"depends_on_task_id"`
	DependencyType  string    `json:"dependency_type" db:"dependency_type"`
}

// MoveBatchSnapshot is the state of one moved task, taken inside the move
// transaction, with every dependency row that touches it.
type MoveBatchSnapshot struct {
	TaskID       uuid.UUID             `json:"task_id"`
	ProjectID    uuid.UUID             `json:"project_id"`
	StatusID     uuid.UUID             `json:"status_id"`
	TaskNumber   int                   `json:"task_number"`
	ParentTaskID *uuid.UUID            `json:"parent_task_id"`
	Dependencies []MoveBatchDependency `json:"dependencies"`
}

// MoveBatchDetach records a leaf that was separated from its old parent.
type MoveBatchDetach struct {
	TaskID       uuid.UUID  `json:"task_id"`
	OldParentID  uuid.UUID  `json:"old_parent_id"`
	BlocksDepID  *uuid.UUID `json:"blocks_dependency_id,omitempty"`
	BlocksAction string     `json:"blocks_action"` // created | upgraded | already_present | skipped_parent_deleted
}

// MoveBatchResult is returned for both real and dry runs.
type MoveBatchResult struct {
	DryRun   bool                `json:"dry_run"`
	Before   []MoveBatchSnapshot `json:"before"`
	After    []MoveBatchSnapshot `json:"after"`
	Detached []MoveBatchDetach   `json:"detached"`
	// SourceProjects lists the distinct projects the tasks left (for audit events).
	SourceProjects map[uuid.UUID]uuid.UUID `json:"-"` // task id -> source project id
}
