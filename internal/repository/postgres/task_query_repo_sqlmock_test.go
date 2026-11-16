package postgres

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// Always-run counterpart to task_query_repo_db_test.go (build tag integration):
// the coverage gate runs a plain `go test`, so these pin the SQL QueryTasks
// composes without a live Postgres.

var closedCats = pq.Array([]string{"done", "cancelled"})

func TestTaskRepo_QueryTasks_MemberViewerIsLimitedToTheirProjects(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	ws, viewer := uuid.New(), uuid.New()
	member := regexp.QuoteMeta("EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = p.id AND pm.user_id = $2)")

	mock.ExpectQuery(member).WithArgs(ws, viewer, closedCats).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectQuery(member).WithArgs(ws, viewer, closedCats, 50, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	page, err := repo.QueryTasks(context.Background(), repository.TaskQuery{WorkspaceID: ws, Viewer: repository.TaskViewer{ActorID: viewer}}, pagination.Params{})
	require.NoError(t, err)
	assert.Equal(t, 3, page.TotalCount, "total_count comes from COUNT, not the page")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTaskRepo_QueryTasks_AgentViewerUsesAgentMembership(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	ws, agent := uuid.New(), uuid.New()
	mock.ExpectQuery(regexp.QuoteMeta("pm.agent_id = $2")).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(regexp.QuoteMeta("pm.agent_id = $2")).WillReturnRows(sqlmock.NewRows([]string{"id"}))

	_, err := repo.QueryTasks(context.Background(), repository.TaskQuery{WorkspaceID: ws, Viewer: repository.TaskViewer{ActorID: agent, Agent: true}}, pagination.Params{})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTaskRepo_QueryTasks_AllFiltersInSQL(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	ws, me, proj := uuid.New(), uuid.New(), uuid.New()
	due := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	q := repository.TaskQuery{
		WorkspaceID:      ws,
		Viewer:           repository.TaskViewer{AllProjects: true},
		AssigneeUserID:   &me,
		ProjectID:        &proj,
		StatusCategories: []domain.StatusCategory{domain.StatusCategoryTodo, domain.StatusCategoryReview},
		Priorities:       []domain.Priority{domain.PriorityUrgent},
		DueBefore:        &due,
		Gated:            true,
		Sort:             repository.TaskQuerySortDueDate,
	}
	where := regexp.QuoteMeta("t.assignee_type = 'user'") + `[\s\S]*` +
		regexp.QuoteMeta("t.assignee_id = $2") + `[\s\S]*` +
		regexp.QuoteMeta("t.project_id = $3") + `[\s\S]*` +
		regexp.QuoteMeta("ts.category::text = ANY($4)") + `[\s\S]*` +
		regexp.QuoteMeta("t.priority::text = ANY($5)") + `[\s\S]*` +
		regexp.QuoteMeta("t.due_date < $6") + `[\s\S]*` +
		regexp.QuoteMeta("(ts.category = $7 OR (t.human_gate = true AND NOT (ts.category::text = ANY($8))))")
	args := []driver.Value{ws, me, proj, pq.Array([]string{"todo", "review"}), pq.Array([]string{"urgent"}), due, domain.StatusCategoryTriage, closedCats}

	mock.ExpectQuery(where).WithArgs(args...).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(where + `[\s\S]*` + regexp.QuoteMeta("ORDER BY t.due_date ASC NULLS LAST")).
		WithArgs(append(args, 10, 10)...).WillReturnRows(sqlmock.NewRows([]string{"id"}))

	_, err := repo.QueryTasks(context.Background(), q, pagination.Params{Page: 2, PageSize: 10})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTaskRepo_QueryTasks_AdminViewerHasNoMembershipPredicate(t *testing.T) {
	var queries []string
	rawDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(_, actual string) error {
		queries = append(queries, actual)
		return nil
	})))
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	repo := NewTaskRepo(sqlx.NewDb(rawDB, "postgres"))
	mock.ExpectQuery("count").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("page").WillReturnRows(sqlmock.NewRows([]string{"id"}))

	_, err = repo.QueryTasks(context.Background(), repository.TaskQuery{WorkspaceID: uuid.New(), Viewer: repository.TaskViewer{AllProjects: true}}, pagination.Params{})
	require.NoError(t, err)
	require.Len(t, queries, 2)
	for _, q := range queries {
		assert.NotContains(t, q, "pm.user_id", "an owner/admin reads every project")
	}
}

func TestTaskRepo_QueryTasks_HardGatesOnlyAndTriageOrder(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	hard := regexp.QuoteMeta("t.human_gate = true\n\t\t  AND t.human_gate_class = 'hard'")
	mock.ExpectQuery(hard).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(hard + `[\s\S]*` + regexp.QuoteMeta("ORDER BY (t.human_gate AND t.human_gate_class = 'hard') DESC")).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	_, err := repo.QueryTasks(context.Background(), repository.TaskQuery{WorkspaceID: uuid.New(), Viewer: repository.TaskViewer{AllProjects: true}, HardGatesOnly: true, Sort: repository.TaskQuerySortTriage}, pagination.Params{})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTaskRepo_QueryTasks_Errors(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	mock.ExpectQuery("SELECT COUNT").WillReturnError(errors.New("boom"))
	_, err := repo.QueryTasks(context.Background(), repository.TaskQuery{WorkspaceID: uuid.New()}, pagination.Params{})
	require.Error(t, err)

	mock.ExpectQuery("SELECT COUNT").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("ORDER BY").WillReturnError(errors.New("boom"))
	_, err = repo.QueryTasks(context.Background(), repository.TaskQuery{WorkspaceID: uuid.New()}, pagination.Params{})
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
