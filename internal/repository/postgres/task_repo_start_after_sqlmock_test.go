package postgres

import (
	"context"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// Fast, always-run counterpart to the DB-backed round trip in
// integration_test.go / TestTaskRepo_StartAfter_RoundTrip (build tag
// integration): this file exists because the coverage-gate CI job measures
// diff coverage from a PLAIN `go test` run with NO `-tags=integration`
// (.gitlab-ci.yml coverage-gate script), so the integration-tagged round
// trip proves start_after works against real Postgres but counts for
// nothing in that gate. These pin the exact SQL text/arg wiring the mocked
// driver can express for every write/read path that #246b8fcc touched and
// that had zero non-integration coverage before this change (Update, Search,
// ListByStatusCategory, ListByUserActive, ListOpenByRecurringScheduleID all
// measured 0.0% in the coverage-gate run on MR !921, pipeline #2656).

func TestTaskRepo_Update_SendsStartAfterColumn(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	taskID := uuid.New()
	startAfter := time.Now().UTC().Truncate(time.Microsecond)

	mock.ExpectExec(regexp.QuoteMeta("start_after = $29")).
		WithArgs(
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			startAfter, // $29 — the field this test exists to pin
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	task := &domain.Task{ID: taskID, StartAfter: &startAfter}
	require.NoError(t, repo.Update(context.Background(), task))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTaskRepo_Update_NotFound(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	taskID := uuid.New()

	mock.ExpectExec(regexp.QuoteMeta("UPDATE tasks")).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.Update(context.Background(), &domain.Task{ID: taskID})
	require.Error(t, err, "zero rows affected must surface as an error, not a silent no-op")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTaskRepo_Search_SelectsStartAfterColumn(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	workspaceID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(t.id) FROM tasks t")).
		WithArgs(workspaceID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(regexp.QuoteMeta("t.due_date, t.start_after, t.estimated_hours")).
		WithArgs(workspaceID, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	page, err := repo.Search(context.Background(), workspaceID, repository.TaskFilter{}, pagination.Params{})
	require.NoError(t, err)
	assert.Equal(t, 0, page.TotalCount)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTaskRepo_ListByStatusCategory_SelectsStartAfterColumn(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	workspaceID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(t.id)")).
		WithArgs(workspaceID, domain.StatusCategoryTriage).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(regexp.QuoteMeta("t.due_date, t.start_after, t.estimated_hours")).
		WithArgs(workspaceID, domain.StatusCategoryTriage, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	page, err := repo.ListByStatusCategory(context.Background(), workspaceID, domain.StatusCategoryTriage, pagination.Params{})
	require.NoError(t, err)
	assert.Equal(t, 0, page.TotalCount)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTaskRepo_ListByUserActive_SelectsStartAfterColumn(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	workspaceID, userID := uuid.New(), uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(t.id)")).
		WithArgs(workspaceID, userID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(regexp.QuoteMeta("t.due_date, t.start_after, t.estimated_hours")).
		WithArgs(workspaceID, userID, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	page, err := repo.ListByUserActive(context.Background(), workspaceID, userID, pagination.Params{})
	require.NoError(t, err)
	assert.Equal(t, 0, page.TotalCount)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTaskRepo_ListOpenByRecurringScheduleID_SelectsStartAfterColumn(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	scheduleID, exceptTaskID := uuid.New(), uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta("t.due_date, t.start_after, t.estimated_hours")).
		WithArgs(scheduleID, exceptTaskID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	got, err := repo.ListOpenByRecurringScheduleID(context.Background(), scheduleID, exceptTaskID)
	require.NoError(t, err)
	assert.Empty(t, got)
	require.NoError(t, mock.ExpectationsWereMet())
}
