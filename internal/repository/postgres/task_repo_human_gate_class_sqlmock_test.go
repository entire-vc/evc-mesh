package postgres

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Fast, always-run counterpart to task_repo_human_gate_class_db_test.go (build
// tag integration, real Postgres): these exercise the same TaskRepo methods
// against a mocked driver, pinning the exact SQL text and error/hydration paths
// the mocked driver CAN express — the DB-backed tests separately prove the CHECK
// constraint and the class predicate's real structural exclusion.

func newTaskRepoMock(t *testing.T) (*TaskRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewTaskRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

func TestTaskRepo_SetHumanGate_Arm_SendsExpectedSQL(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	taskID := uuid.New()

	mock.ExpectExec(regexp.QuoteMeta("UPDATE tasks SET")).
		WithArgs(taskID, true).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.SetHumanGate(context.Background(), taskID, true))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTaskRepo_SetHumanGate_Release_SendsExpectedSQL(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	taskID := uuid.New()

	mock.ExpectExec(regexp.QuoteMeta("UPDATE tasks SET")).
		WithArgs(taskID, false).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.SetHumanGate(context.Background(), taskID, false))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTaskRepo_SetHumanGate_NotFound(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	taskID := uuid.New()

	mock.ExpectExec(regexp.QuoteMeta("UPDATE tasks SET")).
		WithArgs(taskID, true).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.SetHumanGate(context.Background(), taskID, true)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTaskRepo_SetHumanGate_DBError_Propagates(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	taskID := uuid.New()
	dbErr := errors.New("connection reset")

	mock.ExpectExec(regexp.QuoteMeta("UPDATE tasks SET")).
		WithArgs(taskID, true).
		WillReturnError(dbErr)

	err := repo.SetHumanGate(context.Background(), taskID, true)
	require.ErrorIs(t, err, dbErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTaskRepo_SetHumanGateClass_SendsExpectedSQL(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	taskID := uuid.New()

	mock.ExpectExec(regexp.QuoteMeta("UPDATE tasks SET human_gate_class = $2")).
		WithArgs(taskID, string(domain.HumanGateClassSoft)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.SetHumanGateClass(context.Background(), taskID, domain.HumanGateClassSoft))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTaskRepo_SetHumanGateClass_NotFound(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	taskID := uuid.New()

	mock.ExpectExec(regexp.QuoteMeta("UPDATE tasks SET human_gate_class = $2")).
		WithArgs(taskID, string(domain.HumanGateClassHard)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.SetHumanGateClass(context.Background(), taskID, domain.HumanGateClassHard)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTaskRepo_SetHumanGateClass_DBError_Propagates(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	taskID := uuid.New()
	dbErr := errors.New("write conflict")

	mock.ExpectExec(regexp.QuoteMeta("UPDATE tasks SET human_gate_class = $2")).
		WithArgs(taskID, string(domain.HumanGateClassSoft)).
		WillReturnError(dbErr)

	err := repo.SetHumanGateClass(context.Background(), taskID, domain.HumanGateClassSoft)
	require.ErrorIs(t, err, dbErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestTaskRepo_FindSoftTimedOutGates_QueryPinsClassLiteral is the sqlmock half of
// the structural proof: it asserts the exact query text sent to Postgres contains
// the fixed literal `human_gate_class = 'soft'` — the real-Postgres test in
// task_repo_human_gate_class_db_test.go then proves that literal actually excludes
// a hard row from a live database, not just that this string was sent.
func TestTaskRepo_FindSoftTimedOutGates_QueryPinsClassLiteral(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	expectedQuery := regexp.QuoteMeta("human_gate_class = 'soft'")
	rows := sqlmock.NewRows([]string{"id", "human_gate_armed_at"})
	mock.ExpectQuery(expectedQuery).WithArgs(cutoff).WillReturnRows(rows)

	got, err := repo.FindSoftTimedOutGates(context.Background(), cutoff)
	require.NoError(t, err)
	assert.Empty(t, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTaskRepo_FindSoftTimedOutGates_HydratesRows(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	cutoff := time.Now().UTC()
	id1, id2 := uuid.New(), uuid.New()
	armedAt1 := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	armedAt2 := time.Date(2010, 6, 15, 12, 0, 0, 0, time.UTC)

	rows := sqlmock.NewRows([]string{"id", "human_gate_armed_at"}).
		AddRow(id1, armedAt1).
		AddRow(id2, armedAt2)
	mock.ExpectQuery(regexp.QuoteMeta("human_gate_class = 'soft'")).
		WithArgs(cutoff).WillReturnRows(rows)

	got, err := repo.FindSoftTimedOutGates(context.Background(), cutoff)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, id1, got[0].TaskID)
	assert.True(t, armedAt1.Equal(got[0].ArmedAt))
	assert.Equal(t, id2, got[1].TaskID)
	assert.True(t, armedAt2.Equal(got[1].ArmedAt))
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestTaskRepo_FindSoftTimedOutGates_SkipsNullArmedAt proves the defensive nil-check
// in the hydration loop: a row that somehow has a NULL human_gate_armed_at (should be
// unreachable given the query's own `IS NOT NULL` predicate, but the Go code does not
// trust that blindly) is skipped rather than producing a candidate with a zero-value
// ArmedAt that would read as "armed at the Unix epoch".
func TestTaskRepo_FindSoftTimedOutGates_SkipsNullArmedAt(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	cutoff := time.Now().UTC()
	id := uuid.New()

	rows := sqlmock.NewRows([]string{"id", "human_gate_armed_at"}).
		AddRow(id, sql.NullTime{Valid: false})
	mock.ExpectQuery(regexp.QuoteMeta("human_gate_class = 'soft'")).
		WithArgs(cutoff).WillReturnRows(rows)

	got, err := repo.FindSoftTimedOutGates(context.Background(), cutoff)
	require.NoError(t, err)
	assert.Empty(t, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTaskRepo_FindSoftTimedOutGates_DBError_Propagates(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	cutoff := time.Now().UTC()
	dbErr := errors.New("connection reset")

	mock.ExpectQuery(regexp.QuoteMeta("human_gate_class = 'soft'")).
		WithArgs(cutoff).WillReturnError(dbErr)

	_, err := repo.FindSoftTimedOutGates(context.Background(), cutoff)
	require.ErrorIs(t, err, dbErr)
	require.NoError(t, mock.ExpectationsWereMet())
}
