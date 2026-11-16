package repository

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// TaskQuerySort orders a cross-project task read.
type TaskQuerySort string

const (
	// TaskQuerySortUpdated: most recently updated first.
	TaskQuerySortUpdated TaskQuerySort = "updated"
	// TaskQuerySortPriority: urgent first, then most recently updated.
	TaskQuerySortPriority TaskQuerySort = "priority"
	// TaskQuerySortDueDate: earliest due date first, undated last.
	TaskQuerySortDueDate TaskQuerySort = "due_date"
	// TaskQuerySortTriage: live hard gates, then other gates, longest waiting
	// first, then plain triage cards newest first (the triage inbox order).
	TaskQuerySortTriage TaskQuerySort = "triage"
)

// TaskViewer is whose rights a cross-project read runs under. A workspace-wide
// list used to return every project's tasks to any workspace member; the
// project rule (middleware.UserMayAccessProject) is that only members of a
// project, or workspace owners and admins, see its tasks.
type TaskViewer struct {
	ActorID uuid.UUID
	// Agent selects project_members.agent_id instead of user_id.
	Agent bool
	// AllProjects skips the membership check: workspace owner or admin.
	AllProjects bool
}

// TaskQuery is the closed filter set of dashboard task widgets (SDK TaskQuery).
// Every field is applied in SQL so total_count describes the whole match, not
// the page that came back.
type TaskQuery struct {
	WorkspaceID uuid.UUID
	Viewer      TaskViewer
	// AssigneeUserID keeps tasks assigned to this human (scope "mine").
	AssigneeUserID *uuid.UUID
	ProjectID      *uuid.UUID
	// StatusCategories keeps tasks in any of these categories. Empty: every
	// category except done and cancelled.
	StatusCategories []domain.StatusCategory
	Priorities       []domain.Priority
	// DueBefore keeps tasks with a due date strictly before it.
	DueBefore *time.Time
	// Gated keeps the human-attention queue: triage-category tasks and tasks
	// with a live human gate that are not done or cancelled.
	Gated bool
	// HardGatesOnly keeps tasks with a live hard human gate, not done or cancelled.
	HardGatesOnly bool
	Sort          TaskQuerySort
}

// TaskQueryRepository runs TaskQuery. Kept apart from TaskRepository so its
// many test doubles do not all have to grow a method.
type TaskQueryRepository interface {
	QueryTasks(ctx context.Context, q TaskQuery, pg pagination.Params) (*pagination.Page[domain.Task], error)
}
