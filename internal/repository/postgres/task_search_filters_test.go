package postgres

import (
	"context"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

func TestTaskSearchFilters_CategoryTypeLabelsAndOrder(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	ws, agent := uuid.New(), uuid.New()
	cat, kind, priority := domain.StatusCategoryTodo, domain.AssigneeTypeAgent, domain.PriorityHigh
	humanGate := true
	filter := repository.TaskFilter{Search: "none", StatusCategory: &cat, AssigneeID: &agent, AssigneeType: &kind, Priority: &priority, Labels: []string{"gate"}, HumanGate: &humanGate}
	// Both count and rows must use the same filters, before limit/offset.
	predicate := `WHERE p.workspace_id = \$1 AND t.deleted_at IS NULL AND p.deleted_at IS NULL AND \(t.title ILIKE \$2 OR t.description ILIKE \$2\) AND t.status_id IN \(SELECT id FROM task_statuses WHERE category = \$3\) AND t.assignee_id = \$4 AND t.assignee_type = \$5 AND t.priority = \$6 AND t.labels && \$7 AND t.human_gate = \$8`
	mock.ExpectQuery(`SELECT COUNT\(t.id\).* `+predicate).
		WithArgs(ws, "%none%", cat, agent, kind, priority, pq.Array(filter.Labels), humanGate).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(predicate+` ORDER BY t.created_at ASC, t.id ASC LIMIT \$9 OFFSET \$10`).
		WithArgs(ws, "%none%", cat, agent, kind, priority, pq.Array(filter.Labels), humanGate, 20, 20).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	page, err := repo.Search(context.Background(), ws, filter, pagination.Params{Page: 2, PageSize: 20, SortBy: "created_at", SortDir: "asc"})
	require.NoError(t, err)
	require.Empty(t, page.Items)
	require.NoError(t, mock.ExpectationsWereMet())
}
