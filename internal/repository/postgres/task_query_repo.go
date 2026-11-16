package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

var _ repository.TaskQueryRepository = (*TaskRepo)(nil)

// taskQueryOrder maps a sort onto ORDER BY. Every order ends on t.id so equal
// keys do not reshuffle between pages.
var taskQueryOrder = map[repository.TaskQuerySort]string{
	repository.TaskQuerySortUpdated: `t.updated_at DESC, t.id ASC`,
	repository.TaskQuerySortPriority: `CASE t.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END ASC,
		t.updated_at DESC, t.id ASC`,
	repository.TaskQuerySortDueDate: `t.due_date ASC NULLS LAST, t.updated_at DESC, t.id ASC`,
	repository.TaskQuerySortTriage: `(t.human_gate AND t.human_gate_class = 'hard') DESC,
		t.human_gate DESC,
		t.human_gate_armed_at ASC NULLS LAST,
		t.created_at DESC, t.id ASC`,
}

// QueryTasks runs a dashboard task query across the workspace's projects the
// viewer may read. The count and the page share one WHERE clause, so
// total_count is the number of matches, not the length of the page.
func (r *TaskRepo) QueryTasks(ctx context.Context, q repository.TaskQuery, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	pg.Normalize()
	order, ok := taskQueryOrder[q.Sort]
	if !ok {
		order = taskQueryOrder[repository.TaskQuerySortUpdated]
	}

	args := []any{q.WorkspaceID}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	conds := []string{"p.workspace_id = $1", "t.deleted_at IS NULL", "p.deleted_at IS NULL"}

	if !q.Viewer.AllProjects {
		col := "user_id"
		if q.Viewer.Agent {
			col = "agent_id"
		}
		conds = append(conds, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = p.id AND pm.%s = %s)", col, arg(q.Viewer.ActorID)))
	}
	if q.AssigneeUserID != nil {
		conds = append(conds, "t.assignee_type = 'user'", "t.assignee_id = "+arg(*q.AssigneeUserID))
	}
	if q.ProjectID != nil {
		conds = append(conds, "t.project_id = "+arg(*q.ProjectID))
	}
	closed := pq.Array([]string{string(domain.StatusCategoryDone), string(domain.StatusCategoryCancelled)})
	if len(q.StatusCategories) > 0 {
		cats := make([]string, len(q.StatusCategories))
		for i, c := range q.StatusCategories {
			cats[i] = string(c)
		}
		conds = append(conds, "ts.category::text = ANY("+arg(pq.Array(cats))+")")
	} else {
		conds = append(conds, "NOT (ts.category::text = ANY("+arg(closed)+"))")
	}
	if len(q.Priorities) > 0 {
		ps := make([]string, len(q.Priorities))
		for i, p := range q.Priorities {
			ps[i] = string(p)
		}
		conds = append(conds, "t.priority::text = ANY("+arg(pq.Array(ps))+")")
	}
	if q.DueBefore != nil {
		conds = append(conds, "t.due_date IS NOT NULL", "t.due_date < "+arg(*q.DueBefore))
	}
	if q.HardGatesOnly {
		conds = append(conds, "t.human_gate = true", "t.human_gate_class = 'hard'",
			"NOT (ts.category::text = ANY("+arg(closed)+"))")
	} else if q.Gated {
		conds = append(conds, "(ts.category = "+arg(domain.StatusCategoryTriage)+
			" OR (t.human_gate = true AND NOT (ts.category::text = ANY("+arg(closed)+"))))")
	}

	where := `
		FROM tasks t
		INNER JOIN task_statuses ts ON ts.id = t.status_id
		INNER JOIN projects p ON p.id = t.project_id
		WHERE ` + strings.Join(conds, "\n\t\t  AND ")

	var total int
	if err := r.db.GetContext(ctx, &total, `SELECT COUNT(t.id)`+where, args...); err != nil {
		return nil, err
	}

	limit, offset := arg(pg.Limit()), arg(pg.Offset())
	dataQ := `SELECT t.id, t.version, t.input_revision, t.project_id, t.status_id, t.title, t.description,
		t.assignee_id, t.assignee_type, t.priority, t.parent_task_id, t.position,
		t.due_date, t.start_after, t.start_after_origin, t.start_after_receipt_id, t.estimated_hours, t.custom_fields, t.labels,
		t.task_number, t.created_by, t.created_by_type, t.created_at, t.updated_at,
		t.completed_at, t.deleted_at,
		t.recurring_schedule_id, t.recurring_instance_number,
		t.delegation_level, t.human_gate, t.human_gate_class, t.human_gate_armed_at,
		t.gate_author, t.gate_author_type, t.gate_reason, t.recommended_default, t.gate_deadline,
		` + taskComputedColsAliased + where + `
		ORDER BY ` + order + `
		LIMIT ` + limit + ` OFFSET ` + offset
	var rows []taskRow
	if err := r.db.SelectContext(ctx, &rows, dataQ, args...); err != nil {
		return nil, err
	}
	return pagination.NewPage(taskRowsToSlice(rows), total, pg), nil
}
