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

// These are the fast, always-run counterpart to
// human_gate_decision_repo_db_test.go (build tag integration, real
// Postgres): they exercise the same repository methods against a mocked
// driver, so the query shape and result hydration are covered without a
// database — the DB-backed tests separately prove the CHECK constraints and
// trigger the mocked driver cannot express.

func provPtr(p domain.HumanGateProvenance) *domain.HumanGateProvenance { return &p }
func chanPtr(c domain.HumanGateChannel) *domain.HumanGateChannel       { return &c }

func newHGDRepoMock(t *testing.T) (*HumanGateDecisionRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewHumanGateDecisionRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

func TestHumanGateDecisionRepo_Create_InsertsAllFields(t *testing.T) {
	repo, mock := newHGDRepoMock(t)
	taskID, decidedBy, questionRef := uuid.New(), uuid.New(), uuid.New()

	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO human_gate_decisions")).
		WithArgs(sqlmock.AnyArg(), taskID, &questionRef, sqlmock.AnyArg(), decidedBy,
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	d := &domain.HumanGateDecision{
		TaskID: taskID, QuestionRef: &questionRef, DecidedBy: decidedBy,
		Provenance: provPtr(domain.HumanGateProvenanceDirect),
		Channel:    chanPtr(domain.HumanGateChannelMesh),
	}
	require.NoError(t, repo.Create(context.Background(), d))
	assert.NotEqual(t, uuid.Nil, d.ID, "Create must assign an id when none is set")
	assert.False(t, d.CreatedAt.IsZero(), "Create must stamp CreatedAt when unset")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestHumanGateDecisionRepo_Create_PreservesCallerSuppliedIDAndTimestamp(t *testing.T) {
	repo, mock := newHGDRepoMock(t)
	id, taskID := uuid.New(), uuid.New()
	createdAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO human_gate_decisions")).
		WithArgs(id, taskID, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), createdAt).
		WillReturnResult(sqlmock.NewResult(0, 1))

	key := "canonical-decision-test"
	d := &domain.HumanGateDecision{
		ID: id, TaskID: taskID, CanonicalKey: &key, DecidedBy: uuid.New(),
		Provenance: provPtr(domain.HumanGateProvenanceDirect),
		Channel:    chanPtr(domain.HumanGateChannelMesh),
		CreatedAt:  createdAt,
	}
	require.NoError(t, repo.Create(context.Background(), d))
	assert.Equal(t, id, d.ID)
	assert.Equal(t, createdAt, d.CreatedAt)
}

func TestHumanGateDecisionRepo_GetByID_HydratesRevocationFromJoin(t *testing.T) {
	repo, mock := newHGDRepoMock(t)
	id, taskID, decidedBy := uuid.New(), uuid.New(), uuid.New()
	revokedAt := time.Now().UTC()
	reason := "dispute"

	cols := []string{"id", "task_id", "question_ref", "canonical_key", "decided_by",
		"provenance", "channel", "quote", "channel_ref", "recorded_by",
		"revokes_id", "revoked_reason", "created_at",
		"revocation_created_at", "revocation_reason"}
	rows := sqlmock.NewRows(cols).AddRow(
		id, taskID, nil, "canonical-decision-x", decidedBy,
		"direct", "mesh", nil, []byte("null"), nil,
		nil, nil, time.Now().UTC(),
		revokedAt, reason,
	)
	mock.ExpectQuery(regexp.QuoteMeta("FROM human_gate_decisions d")).
		WithArgs(id).
		WillReturnRows(rows)

	got, err := repo.GetByID(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.RevokedAt)
	assert.Equal(t, reason, *got.RevokedReason)
	require.NotNil(t, got.Provenance)
	assert.Equal(t, domain.HumanGateProvenanceDirect, *got.Provenance)
}

func TestHumanGateDecisionRepo_GetByID_NotFound(t *testing.T) {
	repo, mock := newHGDRepoMock(t)
	id := uuid.New()
	mock.ExpectQuery(regexp.QuoteMeta("FROM human_gate_decisions d")).
		WithArgs(id).
		WillReturnError(sql.ErrNoRows)

	got, err := repo.GetByID(context.Background(), id)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestHumanGateDecisionRepo_FindLiveByRef_NoArgsReturnsNilWithoutQuerying(t *testing.T) {
	repo, mock := newHGDRepoMock(t)
	got, err := repo.FindLiveByRef(context.Background(), uuid.New(), nil, nil)
	require.NoError(t, err)
	assert.Nil(t, got)
	require.NoError(t, mock.ExpectationsWereMet(), "no query should run when both refs are nil")
}

func TestHumanGateDecisionRepo_FindLiveByRef_QueriesByQuestionRef(t *testing.T) {
	repo, mock := newHGDRepoMock(t)
	taskID, ref, decidedBy, id := uuid.New(), uuid.New(), uuid.New(), uuid.New()

	cols := []string{"id", "task_id", "question_ref", "canonical_key", "decided_by",
		"provenance", "channel", "quote", "channel_ref", "recorded_by",
		"revokes_id", "revoked_reason", "created_at",
		"revocation_created_at", "revocation_reason"}
	rows := sqlmock.NewRows(cols).AddRow(
		id, taskID, ref, nil, decidedBy,
		"direct", "mesh", nil, []byte("null"), nil,
		nil, nil, time.Now().UTC(),
		nil, nil,
	)
	mock.ExpectQuery(regexp.QuoteMeta("WHERE d.task_id = $1")).
		WithArgs(taskID, &ref, (*string)(nil)).
		WillReturnRows(rows)

	got, err := repo.FindLiveByRef(context.Background(), taskID, &ref, nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, id, got.ID)
	assert.Nil(t, got.RevokedAt)
}

func TestHumanGateDecisionRepo_ListByTask(t *testing.T) {
	repo, mock := newHGDRepoMock(t)
	taskID, id1, id2, decidedBy := uuid.New(), uuid.New(), uuid.New(), uuid.New()

	cols := []string{"id", "task_id", "question_ref", "canonical_key", "decided_by",
		"provenance", "channel", "quote", "channel_ref", "recorded_by",
		"revokes_id", "revoked_reason", "created_at",
		"revocation_created_at", "revocation_reason"}
	rows := sqlmock.NewRows(cols).
		AddRow(id2, taskID, nil, "k2", decidedBy, "direct", "mesh", nil, []byte("null"), nil, nil, nil, time.Now().UTC(), nil, nil).
		AddRow(id1, taskID, nil, "k1", decidedBy, "direct", "mesh", nil, []byte("null"), nil, nil, nil, time.Now().UTC().Add(-time.Hour), nil, nil)
	mock.ExpectQuery(regexp.QuoteMeta("WHERE d.task_id = $1\n\t\tORDER BY d.created_at DESC")).
		WithArgs(taskID).
		WillReturnRows(rows)

	list, err := repo.ListByTask(context.Background(), taskID)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, id2, list[0].ID)
	assert.Equal(t, id1, list[1].ID)
}

func TestHumanGateDecisionRepo_GetByID_PropagatesNonNoRowsError(t *testing.T) {
	repo, mock := newHGDRepoMock(t)
	id := uuid.New()
	dbErr := errors.New("connection reset")
	mock.ExpectQuery(regexp.QuoteMeta("FROM human_gate_decisions d")).
		WithArgs(id).
		WillReturnError(dbErr)

	got, err := repo.GetByID(context.Background(), id)
	require.Error(t, err)
	assert.Nil(t, got)
}

func TestHumanGateDecisionRepo_FindLiveByRef_PropagatesNonNoRowsError(t *testing.T) {
	repo, mock := newHGDRepoMock(t)
	taskID, ref := uuid.New(), uuid.New()
	dbErr := errors.New("connection reset")
	mock.ExpectQuery(regexp.QuoteMeta("WHERE d.task_id = $1")).
		WithArgs(taskID, &ref, (*string)(nil)).
		WillReturnError(dbErr)

	got, err := repo.FindLiveByRef(context.Background(), taskID, &ref, nil)
	require.Error(t, err)
	assert.Nil(t, got)
}

func TestHumanGateDecisionRepo_ListByTask_PropagatesError(t *testing.T) {
	repo, mock := newHGDRepoMock(t)
	taskID := uuid.New()
	dbErr := errors.New("connection reset")
	mock.ExpectQuery(regexp.QuoteMeta("WHERE d.task_id = $1\n\t\tORDER BY d.created_at DESC")).
		WithArgs(taskID).
		WillReturnError(dbErr)

	list, err := repo.ListByTask(context.Background(), taskID)
	require.Error(t, err)
	assert.Nil(t, list)
}
