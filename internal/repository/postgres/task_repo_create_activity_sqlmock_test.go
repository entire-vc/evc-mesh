package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// The real-Postgres proof that TaskRepo.Create's activity_log insert is
// genuinely atomic with the task insert lives in the sibling file
// task_repo_create_activity_atomicity_db_test.go (build tag `integration`,
// FK-violation rollback control). That file is invisible to the default
// (non-integration) `go test ./...` run the CI diff-coverage gate uses, so
// these sqlmock tests exist purely to give the new branches in TaskRepo.Create
// (activity != nil, activity's Changes == nil, and the activity-insert error
// path) coverage under the build the gate actually measures. They pin the
// STATEMENT SEQUENCE (lock -> task insert -> [activity insert] -> commit/
// rollback), not query text — that's the sqlmock tests' job elsewhere.

func newTaskRepoSQLMock(t *testing.T) (*TaskRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewTaskRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

func sampleCreateTask() *domain.Task {
	now := time.Now().UTC()
	return &domain.Task{
		ID: uuid.New(), ProjectID: uuid.New(), StatusID: uuid.New(),
		Title: "sqlmock task", AssigneeType: domain.AssigneeTypeUnassigned,
		Priority: domain.PriorityMedium, CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
		CreatedAt: now, UpdatedAt: now,
	}
}

func sampleActivity(taskID uuid.UUID, changes []byte) *domain.ActivityLog {
	return &domain.ActivityLog{
		ID: uuid.New(), WorkspaceID: uuid.New(), EntityType: "task", EntityID: taskID,
		Action: "task.created", ActorID: uuid.New(), ActorType: domain.ActorTypeAgent,
		Changes: changes, CreatedAt: time.Now().UTC(),
	}
}

// TestTaskRepo_Create_ActivityInsertRunsInsideTheTransaction pins the
// statement ORDER: lock, task insert, activity insert, commit — all inside
// one Begin/Commit pair. sqlmock's default MatchExpectationsInOrder(true)
// means this test fails if the code ever moves the activity insert outside
// the transaction (e.g. after Commit, against r.db instead of tx) — exactly
// the shape of the original #819e7b29 bug.
func TestTaskRepo_Create_ActivityInsertRunsInsideTheTransaction(t *testing.T) {
	repo, mock := newTaskRepoSQLMock(t)
	task := sampleCreateTask()
	activity := sampleActivity(task.ID, nil) // nil Changes exercises the json.RawMessage(`{}`) fallback

	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO tasks").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO activity_log").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.Create(context.Background(), task, activity))
	require.NoError(t, mock.ExpectationsWereMet(),
		"every expected statement (including the activity insert, in order, before commit) must have run")
}

// TestTaskRepo_Create_NilActivitySkipsTheInsertStatement proves activity=nil
// takes a genuinely different path — no activity_log statement is sent at
// all, not merely one bound to NULL-ish values. If the code sent it anyway,
// mock.ExpectCommit() would receive an unexpected statement and fail.
func TestTaskRepo_Create_NilActivitySkipsTheInsertStatement(t *testing.T) {
	repo, mock := newTaskRepoSQLMock(t)
	task := sampleCreateTask()

	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO tasks").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.Create(context.Background(), task, nil))
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestTaskRepo_Create_ActivityInsertErrorRollsBackNotCommits is the sqlmock
// sibling of the real-Postgres FK-violation rollback test: when the activity
// insert's driver call errors, Create must return that error AND the
// transaction must be ROLLED BACK, never committed. mock.ExpectRollback()
// (not ExpectCommit()) makes an accidental commit-on-error fail this test.
func TestTaskRepo_Create_ActivityInsertErrorRollsBackNotCommits(t *testing.T) {
	repo, mock := newTaskRepoSQLMock(t)
	task := sampleCreateTask()
	driverErr := errors.New("fk violation: workspace does not exist")
	activity := sampleActivity(task.ID, []byte(`{"k":"v"}`)) // non-nil Changes: skips the {} fallback branch

	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO tasks").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO activity_log").WillReturnError(driverErr)
	mock.ExpectRollback()

	err := repo.Create(context.Background(), task, activity)
	require.Error(t, err)
	require.ErrorIs(t, err, driverErr)
	require.NoError(t, mock.ExpectationsWereMet(),
		"a failed activity insert must roll back, not commit — this fails if the code path "+
			"tries to commit anyway or skips the rollback")
}
