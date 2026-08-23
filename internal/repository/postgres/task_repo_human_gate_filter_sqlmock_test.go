package postgres

import (
	"context"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// Fast, always-run counterpart to TestTaskRepo_List_HumanGateFilter in
// integration_test.go (build tag integration, real Postgres, which proves the
// filter actually discriminates rows). This one pins the exact SQL/args List()
// sends for the new HumanGate predicate — the affected-set coverage gate runs
// `go test` with no build tag, so the integration test's coverage of these
// lines does not count there; this file is what does.

func taskListRowColumns() []string {
	return []string{
		"id", "project_id", "status_id", "title", "description",
		"assignee_id", "assignee_type", "priority", "parent_task_id", "position",
		"due_date", "estimated_hours", "custom_fields", "labels",
		"task_number", "created_by", "created_by_type", "created_at", "updated_at",
		"completed_at", "deleted_at",
		"recurring_schedule_id", "recurring_instance_number",
		"checked_out_by", "checkout_token", "checkout_expires", "checkout_acquired_at",
		"delegation_level", "thread_id", "human_gate", "is_shipped", "assigned_by", "dod_checks",
		"completion_signal", "status_changed_at", "pre_review_assignee_id", "pre_review_assignee_type",
		"reviewer_id", "reviewer_type", "human_gate_class", "human_gate_armed_at",
		"subtask_count", "artifact_count", "vcs_link_count", "assignee_name", "reviewer_name",
	}
}

func TestTaskRepo_List_HumanGateFilter_True_SendsExpectedSQL(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	projectID := uuid.New()
	trueFlag := true

	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM tasks")).
		WithArgs(projectID, true).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("human_gate = \\$2").
		WithArgs(projectID, true).
		WillReturnRows(sqlmock.NewRows(taskListRowColumns()))

	page, err := repo.List(context.Background(), projectID,
		repository.TaskFilter{HumanGate: &trueFlag},
		pagination.Params{Page: 1, PageSize: 10})
	require.NoError(t, err)
	assert.Equal(t, 0, page.TotalCount)
	assert.Empty(t, page.Items)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTaskRepo_List_HumanGateFilter_False_SendsExpectedSQL(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	projectID := uuid.New()
	falseFlag := false

	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM tasks")).
		WithArgs(projectID, false).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("human_gate = \\$2").
		WithArgs(projectID, false).
		WillReturnRows(sqlmock.NewRows(taskListRowColumns()))

	page, err := repo.List(context.Background(), projectID,
		repository.TaskFilter{HumanGate: &falseFlag},
		pagination.Params{Page: 1, PageSize: 10})
	require.NoError(t, err)
	assert.Equal(t, 0, page.TotalCount)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestTaskRepo_List_HumanGateFilter_Nil_OmitsThePredicate is the negative
// control: an unset filter must NOT add a human_gate condition to the WHERE
// clause — proven by pinning the args list to exactly [projectID] (no bool
// value appended). Column selection always includes human_gate/human_gate_class
// (they are part of the base column list regardless of filter), so this checks
// the predicate, not the projection.
func TestTaskRepo_List_HumanGateFilter_Nil_OmitsThePredicate(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	projectID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM tasks")).
		WithArgs(projectID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("FROM tasks").
		WithArgs(projectID).
		WillReturnRows(sqlmock.NewRows(taskListRowColumns()))

	page, err := repo.List(context.Background(), projectID,
		repository.TaskFilter{},
		pagination.Params{Page: 1, PageSize: 10})
	require.NoError(t, err)
	assert.Equal(t, 0, page.TotalCount)
	require.NoError(t, mock.ExpectationsWereMet())
}
