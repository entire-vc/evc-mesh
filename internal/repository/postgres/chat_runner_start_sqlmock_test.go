package postgres

import (
	"context"
	"net/http"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// The row races (one holder, fencing, epoch) are pinned by the DB-backed tests in
// chat_runner_start_db_test.go; these walk the same branches against a scripted
// driver so they run without PostgreSQL.

type startFixture struct {
	mock          sqlmock.Sqlmock
	repo          *ChatRunnerRepo
	ws, agent     uuid.UUID
	runner, turn  uuid.UUID
	thread, owner uuid.UUID
}

func newStartFixture(t *testing.T) *startFixture {
	t.Helper()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	return &startFixture{mock: mock, repo: NewChatRunnerRepo(sqlx.NewDb(raw, "postgres")),
		ws: uuid.New(), agent: uuid.New(), runner: uuid.New(), turn: uuid.New(), thread: uuid.New(), owner: uuid.New()}
}

// lock scripts the FOR UPDATE read of the turn row.
func (f *startFixture) lock(state string, runner uuid.UUID, token string, epoch int64, expired, cancel bool) {
	f.mock.ExpectBegin()
	f.mock.ExpectQuery("SELECT thread_id, agent_id, state, runner_id").WithArgs(f.turn, f.ws).WillReturnRows(
		sqlmock.NewRows([]string{"thread_id", "agent_id", "state", "runner_id", "claim_token", "execution_epoch", "expired", "cancel_requested", "last_seq"}).
			AddRow(f.thread, f.agent, state, runner, token, epoch, expired, cancel, 3))
}

func errStatus(t *testing.T, err error) (status int, code string) {
	t.Helper()
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	return ae.Code, ae.ErrorCode
}

func TestStartTurn_ClaimedBecomesStreaming(t *testing.T) {
	f := newStartFixture(t)
	f.lock("claimed", f.runner, "ct", 4, false, false)
	f.mock.ExpectQuery("UPDATE ext_agent_chat_turns").WithArgs(f.turn, ChatLeaseSeconds).
		WillReturnRows(sqlmock.NewRows([]string{"lease_until"}).AddRow(time.Now()))
	f.mock.ExpectExec("UPDATE ext_agent_chat_messages m SET status = 'done'").WithArgs(f.turn, f.ws).WillReturnResult(sqlmock.NewResult(0, 1))
	f.mock.ExpectCommit()

	out, err := f.repo.StartTurn(context.Background(), f.ws, []uuid.UUID{f.agent}, f.runner, f.turn, "ct", ChatLeaseSeconds)
	require.NoError(t, err)
	assert.Equal(t, "streaming", out.State)
	assert.EqualValues(t, 4, out.ExecutionEpoch)
	assert.Equal(t, 3, out.LastSeq)
	require.NoError(t, f.mock.ExpectationsWereMet())
}

func TestStartTurn_ExpiredLeaseBumpsTheEpoch(t *testing.T) {
	f := newStartFixture(t)
	f.lock("streaming", f.runner, "ct", 4, true, false)
	f.mock.ExpectQuery("UPDATE ext_agent_chat_turns").WillReturnRows(sqlmock.NewRows([]string{"lease_until"}).AddRow(time.Now()))
	f.mock.ExpectExec("UPDATE ext_agent_chat_messages m SET status = 'done'").WithArgs(f.turn, f.ws).WillReturnResult(sqlmock.NewResult(0, 1))
	f.mock.ExpectQuery("UPDATE ext_agent_chat_threads SET execution_epoch").WithArgs(f.thread, f.ws).
		WillReturnRows(sqlmock.NewRows([]string{"execution_epoch"}).AddRow(5))
	f.mock.ExpectExec("UPDATE ext_agent_chat_turns SET execution_epoch").WithArgs(f.turn, int64(5)).WillReturnResult(sqlmock.NewResult(0, 1))
	f.mock.ExpectCommit()

	out, err := f.repo.StartTurn(context.Background(), f.ws, []uuid.UUID{f.agent}, f.runner, f.turn, "ct", ChatLeaseSeconds)
	require.NoError(t, err)
	assert.EqualValues(t, 5, out.ExecutionEpoch, "a late start must not revive a permission issued before the lapse")
	require.NoError(t, f.mock.ExpectationsWereMet())
}

func TestStartTurn_Refusals(t *testing.T) {
	cases := []struct {
		name   string
		state  string
		runner func(f *startFixture) uuid.UUID
		token  string
		agents func(f *startFixture) []uuid.UUID
		status int
		code   string
	}{
		{"other runner", "claimed", func(*startFixture) uuid.UUID { return uuid.New() }, "ct", nil, 403, ChatErrNotClaimHolder},
		{"stale token", "claimed", nil, "old", nil, 409, ChatErrStaleClaim},
		{"empty token", "claimed", nil, "", nil, 409, ChatErrStaleClaim},
		{"foreign agent", "claimed", nil, "ct", func(*startFixture) []uuid.UUID { return []uuid.UUID{uuid.New()} }, 404, ChatErrTurnNotFound},
		{"lease failed", "failed", nil, "ct", nil, 409, ChatErrLeaseExpired},
		{"requeued", "queued", nil, "ct", nil, 409, ChatErrStaleClaim},
		{"finished", "done", nil, "ct", nil, 409, ChatErrTurnTerminal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newStartFixture(t)
			f.lock(tc.state, f.runner, "ct", 1, false, false)
			f.mock.ExpectRollback()
			runner, agents := f.runner, []uuid.UUID{f.agent}
			if tc.runner != nil {
				runner = tc.runner(f)
			}
			if tc.agents != nil {
				agents = tc.agents(f)
			}
			_, err := f.repo.StartTurn(context.Background(), f.ws, agents, runner, f.turn, tc.token, ChatLeaseSeconds)
			status, code := errStatus(t, err)
			assert.Equal(t, tc.status, status)
			assert.Equal(t, tc.code, code)
		})
	}

	t.Run("unknown turn", func(t *testing.T) {
		f := newStartFixture(t)
		f.mock.ExpectBegin()
		f.mock.ExpectQuery("SELECT thread_id").WillReturnRows(sqlmock.NewRows([]string{"thread_id"}))
		f.mock.ExpectRollback()
		_, err := f.repo.StartTurn(context.Background(), f.ws, []uuid.UUID{f.agent}, f.runner, f.turn, "ct", 60)
		status, _ := errStatus(t, err)
		assert.Equal(t, http.StatusNotFound, status)
	})
}

func TestSessionContext_BindingComesFromTheThread(t *testing.T) {
	f := newStartFixture(t)
	project := uuid.New()
	f.lock("streaming", f.runner, "ct", 4, false, false)
	f.mock.ExpectQuery("SELECT user_id, project_id FROM ext_agent_chat_threads").WithArgs(f.thread, f.ws).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "project_id"}).AddRow(f.owner, project))
	f.mock.ExpectRollback()

	sc, err := f.repo.SessionContext(context.Background(), f.ws, []uuid.UUID{f.agent}, f.runner, f.turn, "ct", 4)
	require.NoError(t, err)
	assert.Equal(t, f.owner, sc.UserID)
	assert.Equal(t, project, sc.ProjectID)
	assert.Equal(t, f.thread, sc.ThreadID)
	assert.Equal(t, f.turn, sc.TurnID)
}

func TestSessionContext_Preconditions(t *testing.T) {
	cases := []struct {
		name            string
		state           string
		epoch           int64
		expired, cancel bool
		code            string
	}{
		{"not started", "claimed", 4, false, false, ChatErrNotStarted},
		{"lease lapsed", "streaming", 4, true, false, ChatErrLeaseExpired},
		{"cancelled", "streaming", 4, false, true, ChatErrCancelRequested},
		{"stale epoch", "streaming", 3, false, false, ChatErrStaleClaim},
		{"failed", "failed", 4, false, false, ChatErrLeaseExpired},
		{"requeued", "queued", 4, false, false, ChatErrStaleClaim},
		{"finished", "done", 4, false, false, ChatErrTurnTerminal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newStartFixture(t)
			f.lock(tc.state, f.runner, "ct", 4, tc.expired, tc.cancel)
			f.mock.ExpectRollback()
			_, err := f.repo.SessionContext(context.Background(), f.ws, []uuid.UUID{f.agent}, f.runner, f.turn, "ct", tc.epoch)
			status, code := errStatus(t, err)
			assert.Equal(t, http.StatusConflict, status)
			assert.Equal(t, tc.code, code)
		})
	}

	t.Run("deleted chat", func(t *testing.T) {
		f := newStartFixture(t)
		f.lock("streaming", f.runner, "ct", 4, false, false)
		f.mock.ExpectQuery("SELECT user_id, project_id").WillReturnRows(sqlmock.NewRows([]string{"user_id", "project_id"}))
		f.mock.ExpectRollback()
		_, err := f.repo.SessionContext(context.Background(), f.ws, []uuid.UUID{f.agent}, f.runner, f.turn, "ct", 4)
		_, code := errStatus(t, err)
		assert.Equal(t, ChatErrTurnTerminal, code)
	})
}

func TestSessionGeneration_CountsIssues(t *testing.T) {
	f := newStartFixture(t)
	token := uuid.New()
	f.mock.ExpectQuery("SELECT COUNT").WithArgs(f.turn, token).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	n, err := f.repo.SessionGeneration(context.Background(), f.turn, token)
	require.NoError(t, err)
	assert.Equal(t, 3, n)
}

// Stop writes the tombstone for the thread's live turns and for sessions that
// already have a token, each under that session's advisory lock, then revokes the tokens.
func TestRevokeThreadSessions_TombstonesLiveTurnsBeforeAnyTokenExists(t *testing.T) {
	f := newStartFixture(t)
	now := time.Now().UTC()
	f.mock.ExpectBegin()
	f.mock.ExpectQuery("SELECT id FROM ext_agent_chat_turns").WithArgs(f.thread).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(f.turn))
	f.mock.ExpectExec("pg_advisory_xact_lock").WithArgs("chat_session:" + f.turn.String()).WillReturnResult(sqlmock.NewResult(0, 0))
	f.mock.ExpectExec("INSERT INTO chat_session_stops").WithArgs(f.turn, now).WillReturnResult(sqlmock.NewResult(0, 1))
	f.mock.ExpectExec("UPDATE chat_session_tokens").WithArgs(f.thread, now).WillReturnResult(sqlmock.NewResult(0, 0))
	f.mock.ExpectCommit()

	tx, err := f.repo.db.BeginTxx(context.Background(), nil)
	require.NoError(t, err)
	require.NoError(t, revokeThreadSessions(context.Background(), tx, f.thread, now))
	require.NoError(t, tx.Commit())
	require.NoError(t, f.mock.ExpectationsWereMet())
}

func TestStartTurn_MessageDeliveryFailureRollsBack(t *testing.T) {
	f := newStartFixture(t)
	f.lock("claimed", f.runner, "ct", 4, false, false)
	f.mock.ExpectQuery("UPDATE ext_agent_chat_turns").WillReturnRows(sqlmock.NewRows([]string{"lease_until"}).AddRow(time.Now()))
	f.mock.ExpectExec("UPDATE ext_agent_chat_messages m SET status = 'done'").WillReturnError(assert.AnError)
	f.mock.ExpectRollback()
	_, err := f.repo.StartTurn(context.Background(), f.ws, []uuid.UUID{f.agent}, f.runner, f.turn, "ct", ChatLeaseSeconds)
	require.ErrorIs(t, err, assert.AnError)
	require.NoError(t, f.mock.ExpectationsWereMet())
}

func TestActiveTurns_ReadsOnlyTheCallersHeldTurns(t *testing.T) {
	f := newStartFixture(t)
	got, err := f.repo.ActiveTurns(context.Background(), f.ws, nil, f.runner)
	require.NoError(t, err)
	require.Empty(t, got, "no agents, no query")

	f.mock.ExpectQuery("FROM ext_agent_chat_turns").WithArgs(f.ws, sqlmock.AnyArg(), f.runner).WillReturnRows(
		sqlmock.NewRows([]string{"id", "thread_id", "agent_id", "state", "attempt", "claim_token", "lease_until", "execution_epoch"}).
			AddRow(f.turn, f.thread, f.agent, "claimed", 1, "ct", time.Now(), 4))
	got, err = f.repo.ActiveTurns(context.Background(), f.ws, []uuid.UUID{f.agent}, f.runner)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, f.turn, got[0].TurnID)
	assert.Equal(t, "ct", got[0].ClaimToken)
	require.NoError(t, f.mock.ExpectationsWereMet())

	f.mock.ExpectQuery("FROM ext_agent_chat_turns").WillReturnError(assert.AnError)
	_, err = f.repo.ActiveTurns(context.Background(), f.ws, []uuid.UUID{f.agent}, f.runner)
	require.ErrorIs(t, err, assert.AnError)
}
