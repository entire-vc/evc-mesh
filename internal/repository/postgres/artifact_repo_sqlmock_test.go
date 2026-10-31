package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// The concurrent behaviour of the per-task artifact limit is proven against a
// live Postgres in artifact_repo_db_test.go (build tag `integration`). These
// pin the statement order it relies on: the advisory lock is taken before the
// sum, nothing is reserved when the sum leaves no room, and the artifact takes
// over its reservation in one transaction.

func newArtifactRepoMock(t *testing.T) (*ArtifactRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewArtifactRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

func limitTestArtifact(size int64) *domain.Artifact {
	return &domain.Artifact{
		ID: uuid.New(), TaskID: uuid.New(), Name: "bundle.tar.gz",
		ArtifactType: domain.ArtifactTypeFile, MimeType: "application/gzip",
		StorageKey: "test/bundle.tar.gz", SizeBytes: size,
		Metadata: json.RawMessage(`{}`), UploadedBy: uuid.New(),
		UploadedByType: domain.UploaderTypeAgent, CreatedAt: time.Now().UTC(),
	}
}

func expectLockPurgeSum(mock sqlmock.Sqlmock, taskID uuid.UUID, used int64) {
	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").WithArgs(taskID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM artifact_reservations WHERE task_id = \\$1 AND expires_at <= now\\(\\)").
		WithArgs(taskID).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("FROM artifacts WHERE task_id = \\$1\\)\\s+\\+ \\(SELECT COALESCE\\(SUM\\(size_bytes\\), 0\\) FROM artifact_reservations").
		WithArgs(taskID).WillReturnRows(sqlmock.NewRows([]string{"sum"}).AddRow(used))
}

func TestArtifactRepo_ReserveTaskBytes_LockSumReserve(t *testing.T) {
	repo, mock := newArtifactRepoMock(t)
	id, taskID := uuid.New(), uuid.New()

	expectLockPurgeSum(mock, taskID, 15)
	mock.ExpectExec("INSERT INTO artifact_reservations").
		WithArgs(id, taskID, int64(10), float64(900)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	reserved, used, err := repo.ReserveTaskBytes(context.Background(), id, taskID, 10, 25, 15*time.Minute)
	require.NoError(t, err)
	assert.True(t, reserved)
	assert.EqualValues(t, 15, used)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestArtifactRepo_ReserveTaskBytes_NoRoomNoReservation(t *testing.T) {
	repo, mock := newArtifactRepoMock(t)
	taskID := uuid.New()

	expectLockPurgeSum(mock, taskID, 15)
	mock.ExpectRollback()

	reserved, used, err := repo.ReserveTaskBytes(context.Background(), uuid.New(), taskID, 11, 25, time.Minute)
	require.NoError(t, err)
	assert.False(t, reserved)
	assert.EqualValues(t, 15, used)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestArtifactRepo_ReserveTaskBytes_Errors(t *testing.T) {
	boom := errors.New("boom")
	cases := map[string]func(mock sqlmock.Sqlmock, taskID uuid.UUID){
		"begin": func(mock sqlmock.Sqlmock, _ uuid.UUID) {
			mock.ExpectBegin().WillReturnError(boom)
		},
		"lock": func(mock sqlmock.Sqlmock, taskID uuid.UUID) {
			mock.ExpectBegin()
			mock.ExpectExec("pg_advisory_xact_lock").WithArgs(taskID).WillReturnError(boom)
			mock.ExpectRollback()
		},
		"purge": func(mock sqlmock.Sqlmock, taskID uuid.UUID) {
			mock.ExpectBegin()
			mock.ExpectExec("pg_advisory_xact_lock").WithArgs(taskID).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("DELETE FROM artifact_reservations").WithArgs(taskID).WillReturnError(boom)
			mock.ExpectRollback()
		},
		"sum": func(mock sqlmock.Sqlmock, taskID uuid.UUID) {
			mock.ExpectBegin()
			mock.ExpectExec("pg_advisory_xact_lock").WithArgs(taskID).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("DELETE FROM artifact_reservations").WithArgs(taskID).WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectQuery("SUM\\(size_bytes\\)").WithArgs(taskID).WillReturnError(boom)
			mock.ExpectRollback()
		},
		"insert": func(mock sqlmock.Sqlmock, taskID uuid.UUID) {
			expectLockPurgeSum(mock, taskID, 0)
			mock.ExpectExec("INSERT INTO artifact_reservations").WillReturnError(boom)
			mock.ExpectRollback()
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			repo, mock := newArtifactRepoMock(t)
			taskID := uuid.New()
			setup(mock, taskID)

			reserved, _, err := repo.ReserveTaskBytes(context.Background(), uuid.New(), taskID, 10, 25, time.Minute)
			require.ErrorIs(t, err, boom)
			assert.False(t, reserved)
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestArtifactRepo_CreateFromReservation_TakesLiveReservationThenInserts(t *testing.T) {
	repo, mock := newArtifactRepoMock(t)
	a := limitTestArtifact(10)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM artifact_reservations WHERE id = \\$1 AND expires_at > now\\(\\)").
		WithArgs(a.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO artifacts").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.CreateFromReservation(context.Background(), a.ID, a))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestArtifactRepo_CreateFromReservation_ExpiredWritesNothing(t *testing.T) {
	repo, mock := newArtifactRepoMock(t)
	a := limitTestArtifact(10)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM artifact_reservations").WithArgs(a.ID).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	err := repo.CreateFromReservation(context.Background(), a.ID, a)
	require.ErrorIs(t, err, repository.ErrArtifactReservationExpired)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestArtifactRepo_CreateFromReservation_Errors(t *testing.T) {
	boom := errors.New("boom")
	cases := map[string]func(mock sqlmock.Sqlmock, a *domain.Artifact){
		"begin": func(mock sqlmock.Sqlmock, _ *domain.Artifact) {
			mock.ExpectBegin().WillReturnError(boom)
		},
		"take reservation": func(mock sqlmock.Sqlmock, a *domain.Artifact) {
			mock.ExpectBegin()
			mock.ExpectExec("DELETE FROM artifact_reservations").WithArgs(a.ID).WillReturnError(boom)
			mock.ExpectRollback()
		},
		"insert": func(mock sqlmock.Sqlmock, a *domain.Artifact) {
			mock.ExpectBegin()
			mock.ExpectExec("DELETE FROM artifact_reservations").WithArgs(a.ID).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("INSERT INTO artifacts").WillReturnError(boom)
			mock.ExpectRollback()
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			repo, mock := newArtifactRepoMock(t)
			a := limitTestArtifact(10)
			setup(mock, a)

			require.ErrorIs(t, repo.CreateFromReservation(context.Background(), a.ID, a), boom)
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestArtifactRepo_ReleaseReservation(t *testing.T) {
	repo, mock := newArtifactRepoMock(t)
	id := uuid.New()
	mock.ExpectExec("DELETE FROM artifact_reservations WHERE id = \\$1").WithArgs(id).WillReturnResult(sqlmock.NewResult(0, 0))

	require.NoError(t, repo.ReleaseReservation(context.Background(), id), "an already released reservation is not an error")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestArtifactRepo_Create_InsertsWithoutLimit(t *testing.T) {
	repo, mock := newArtifactRepoMock(t)
	a := limitTestArtifact(10)
	a.Metadata = nil
	mock.ExpectExec("INSERT INTO artifacts").WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.Create(context.Background(), a))
	assert.NoError(t, mock.ExpectationsWereMet())
}
