package domain

import (
	"time"

	"github.com/google/uuid"
)

// GraphNode is a read-only compact projection. Restricted/missing nodes retain
// identity and connectivity, but none of the task or project metadata.
type GraphNode struct {
	ID                 uuid.UUID      `json:"id" db:"id"`
	ProjectID          *uuid.UUID     `json:"project_id,omitempty" db:"project_id"`
	ProjectName        string         `json:"project_name,omitempty" db:"project_name"`
	ProjectSlug        string         `json:"project_slug,omitempty" db:"project_slug"`
	Title              string         `json:"title,omitempty" db:"title"`
	StatusID           *uuid.UUID     `json:"status_id,omitempty" db:"status_id"`
	StatusName         string         `json:"status_name,omitempty" db:"status_name"`
	Category           StatusCategory `json:"status_category,omitempty" db:"status_category"`
	Priority           Priority       `json:"priority,omitempty" db:"priority"`
	AssigneeName       string         `json:"assignee_name,omitempty" db:"assignee_name"`
	ParentTaskID       *uuid.UUID     `json:"parent_task_id,omitempty" db:"parent_task_id"`
	HumanGate          bool           `json:"human_gate,omitempty" db:"human_gate"`
	StartAfter         *time.Time     `json:"start_after,omitempty" db:"start_after"`
	Availability       string         `json:"availability" db:"availability"`
	Blockers           int            `json:"blockers" db:"blockers"`
	UnfinishedBlockers int            `json:"unfinished_blockers" db:"unfinished_blockers"`
	UnknownBlockers    int            `json:"unknown_blockers" db:"unknown_blockers"`
	Dependents         int            `json:"dependents" db:"dependents"`
	Cycle              bool           `json:"cycle" db:"-"`
	UpdatedAt          time.Time      `json:"-" db:"updated_at"`
}

func (n GraphNode) Terminal() bool {
	return n.Category == StatusCategoryDone || n.Category == StatusCategoryCancelled
}

// GraphScope mirrors RequireProjectMember. Admin bypass is human-only.
type GraphScope struct {
	WorkspaceID uuid.UUID
	ActorID     uuid.UUID
	Agent       bool
	Admin       bool
}

type GraphSnapshot struct {
	Nodes        []GraphNode
	Dependencies []TaskDependency
	// Topology contains only actor-authorized IDs, never metadata, and is not
	// serialized to callers. Restricted edges must not affect cycle diagnostics.
	// Whole-workspace topology detects cycles that leave and re-enter a project.
	Topology []TaskDependency
	Revision string
}
