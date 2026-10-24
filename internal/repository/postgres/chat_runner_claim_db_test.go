package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// DB-backed: the guarantees below (one winner per turn, fencing, expiry) live in
// SQL, so only a real Postgres can pin them. Like the other DATABASE_URL tests
// here they skip without a database and carry no build tag, so the default job
// (and its diff-coverage gate) runs them.

func (f *chatDeleteFixture) queueTurn(t *testing.T, ws, proj, agent, user uuid.UUID) (turn, msg uuid.UUID) {
	t.Helper()
	thread, err := f.insertThread(t, ws, proj, agent, user)
	require.NoError(t, err)
	msg, turn = uuid.New(), uuid.New()
	_, err = f.db.Exec(`INSERT INTO ext_agent_chat_messages (id, workspace_id, thread_id, role, body, status) VALUES ($1,$2,$3,'user','hello','queued')`, msg, ws, thread)
	require.NoError(t, err)
	_, err = f.db.Exec(`INSERT INTO ext_agent_chat_turns (id, workspace_id, thread_id, agent_id, user_message_id, expires_at) VALUES ($1,$2,$3,$4,$5, now() + interval '1 day')`, turn, ws, thread, agent, msg)
	require.NoError(t, err)
	return turn, msg
}

func (f *chatDeleteFixture) claimParams(ws, runner uuid.UUID, agents ...uuid.UUID) ChatClaimParams {
	return ChatClaimParams{WorkspaceID: ws, AgentIDs: agents, RunnerID: runner, Max: 5,
		LeaseSeconds: ChatLeaseSeconds, MaxAttempts: ChatMaxAttempts}
}

func protoErr(t *testing.T, err error) *apierror.Error {
	t.Helper()
	var e *apierror.Error
	require.ErrorAs(t, err, &e)
	return e
}

func TestChatRunnerClaim_ConcurrentClaimsOfOneTurnHaveExactlyOneWinner(t *testing.T) {
	f := newChatDeleteFixture(t)
	repo := NewChatRunnerRepo(f.db)

	// One race is won by luck often enough to hide a missing lock, so run many
	// independent rounds: without FOR UPDATE SKIP LOCKED some round hands the
	// same turn to two runners.
	const rounds, runners = 25, 8
	for round := 0; round < rounds; round++ {
		turn, _ := f.queueTurn(t, f.wsA, f.projA, f.agentA, f.userA)

		var wg sync.WaitGroup
		results := make([][]ChatClaimedTurn, runners)
		errs := make([]error, runners)
		start := make(chan struct{})
		for i := 0; i < runners; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				results[i], errs[i] = repo.ClaimTurns(context.Background(), f.claimParams(f.wsA, uuid.New(), f.agentA))
			}(i)
		}
		close(start)
		wg.Wait()

		winners := 0
		for i := range results {
			require.NoError(t, errs[i])
			for _, c := range results[i] {
				winners++
				assert.Equal(t, turn, c.TurnID)
				assert.NotEmpty(t, c.ClaimToken)
				assert.Equal(t, "hello", c.Body)
				assert.Equal(t, 1, c.Attempt)
				assert.Equal(t, int64(1), c.ExecutionEpoch)
			}
		}
		require.Equal(t, 1, winners, "round %d: a turn must reach exactly one of the concurrent claimers", round)

		var state string
		require.NoError(t, f.db.Get(&state, `SELECT state FROM ext_agent_chat_turns WHERE id=$1`, turn))
		assert.Equal(t, "claimed", state)
	}
}

func TestChatRunnerClaim_HeldTurnIsNotHandedOutAgain(t *testing.T) {
	f := newChatDeleteFixture(t)
	repo := NewChatRunnerRepo(f.db)
	runner := uuid.New()
	f.queueTurn(t, f.wsA, f.projA, f.agentA, f.userA)
	first, err := repo.ClaimTurns(context.Background(), f.claimParams(f.wsA, runner, f.agentA))
	require.NoError(t, err)
	require.Len(t, first, 1)
	again, err := repo.ClaimTurns(context.Background(), f.claimParams(f.wsA, runner, f.agentA))
	require.NoError(t, err)
	assert.Empty(t, again, "a runner re-claiming must not receive its own live turn (N1)")
}

func TestChatRunnerClaim_OtherAgentsAndWorkspacesSeeNothing(t *testing.T) {
	f := newChatDeleteFixture(t)
	repo := NewChatRunnerRepo(f.db)
	turn, _ := f.queueTurn(t, f.wsA, f.projA, f.agentA, f.userA)

	got, err := repo.ClaimTurns(context.Background(), f.claimParams(f.wsA, uuid.New(), f.agentB))
	require.NoError(t, err)
	assert.Empty(t, got, "an agent that is not the turn's agent gets nothing")
	got, err = repo.ClaimTurns(context.Background(), f.claimParams(f.wsB, uuid.New(), f.agentA))
	require.NoError(t, err)
	assert.Empty(t, got, "the workspace pins the claim even for the right agent id")

	var state string
	require.NoError(t, f.db.Get(&state, `SELECT state FROM ext_agent_chat_turns WHERE id=$1`, turn))
	assert.Equal(t, "queued", state)
}

func TestChatRunnerLease_ExpiredLeaseRedeliversAndFencesTheOldToken(t *testing.T) {
	f := newChatDeleteFixture(t)
	repo := NewChatRunnerRepo(f.db)
	ctx := context.Background()
	runnerA, runnerB := uuid.New(), uuid.New()
	turn, _ := f.queueTurn(t, f.wsA, f.projA, f.agentA, f.userA)

	a, err := repo.ClaimTurns(ctx, f.claimParams(f.wsA, runnerA, f.agentA))
	require.NoError(t, err)
	require.Len(t, a, 1)
	lease, err := repo.RenewLease(ctx, f.wsA, []uuid.UUID{f.agentA}, runnerA, turn, a[0].ClaimToken, ChatLeaseSeconds)
	require.NoError(t, err)
	assert.Equal(t, "claimed", lease.State)

	_, err = f.db.Exec(`UPDATE ext_agent_chat_turns SET lease_until = now() - interval '1 second' WHERE id=$1`, turn)
	require.NoError(t, err)

	b, err := repo.ClaimTurns(ctx, f.claimParams(f.wsA, runnerB, f.agentA))
	require.NoError(t, err)
	require.Len(t, b, 1, "a lapsed lease before any start makes the turn deliverable again")
	assert.Equal(t, turn, b[0].TurnID)
	assert.Equal(t, 2, b[0].Attempt)
	assert.NotEqual(t, a[0].ClaimToken, b[0].ClaimToken)
	assert.Greater(t, b[0].ExecutionEpoch, a[0].ExecutionEpoch)

	_, err = repo.RenewLease(ctx, f.wsA, []uuid.UUID{f.agentA}, runnerB, turn, a[0].ClaimToken, ChatLeaseSeconds)
	e := protoErr(t, err)
	assert.Equal(t, 409, e.Code)
	assert.Equal(t, ChatErrStaleClaim, e.ErrorCode, "the old claim_token is fenced off")

	_, err = repo.RenewLease(ctx, f.wsA, []uuid.UUID{f.agentA}, runnerA, turn, a[0].ClaimToken, ChatLeaseSeconds)
	assert.Equal(t, ChatErrNotClaimHolder, protoErr(t, err).ErrorCode, "the previous holder is no longer the holder")

	_, err = repo.RenewLease(ctx, f.wsA, []uuid.UUID{f.agentA}, runnerB, turn, b[0].ClaimToken, ChatLeaseSeconds)
	require.NoError(t, err, "the new holder renews with the new token")
}

func TestChatRunnerLease_ForeignAgentAndUnknownTurnAreIndistinguishable(t *testing.T) {
	f := newChatDeleteFixture(t)
	repo := NewChatRunnerRepo(f.db)
	ctx := context.Background()
	runner := uuid.New()
	turn, _ := f.queueTurn(t, f.wsA, f.projA, f.agentA, f.userA)
	c, err := repo.ClaimTurns(ctx, f.claimParams(f.wsA, runner, f.agentA))
	require.NoError(t, err)
	require.Len(t, c, 1)

	_, errForeign := repo.RenewLease(ctx, f.wsA, []uuid.UUID{f.agentB}, runner, turn, c[0].ClaimToken, ChatLeaseSeconds)
	_, errUnknown := repo.RenewLease(ctx, f.wsA, []uuid.UUID{f.agentB}, runner, uuid.New(), c[0].ClaimToken, ChatLeaseSeconds)
	_, errOtherWS := repo.RenewLease(ctx, f.wsB, []uuid.UUID{f.agentA}, runner, turn, c[0].ClaimToken, ChatLeaseSeconds)
	for _, e := range []error{errForeign, errUnknown, errOtherWS} {
		pe := protoErr(t, e)
		assert.Equal(t, 404, pe.Code)
		assert.Equal(t, ChatErrTurnNotFound, pe.ErrorCode)
	}
}

func TestChatRunnerLease_LateButLiveRunnerRenewsAndBumpsTheEpoch(t *testing.T) {
	f := newChatDeleteFixture(t)
	repo := NewChatRunnerRepo(f.db)
	ctx := context.Background()
	runner := uuid.New()
	turn, _ := f.queueTurn(t, f.wsA, f.projA, f.agentA, f.userA)
	c, err := repo.ClaimTurns(ctx, f.claimParams(f.wsA, runner, f.agentA))
	require.NoError(t, err)
	require.Len(t, c, 1)

	_, err = f.db.Exec(`UPDATE ext_agent_chat_turns SET lease_until = now() - interval '1 second' WHERE id=$1`, turn)
	require.NoError(t, err)
	lease, err := repo.RenewLease(ctx, f.wsA, []uuid.UUID{f.agentA}, runner, turn, c[0].ClaimToken, ChatLeaseSeconds)
	require.NoError(t, err, "N4: the sweeper has not acted, a late runner is not punished")
	assert.Greater(t, lease.ExecutionEpoch, c[0].ExecutionEpoch, "a renewal after the lapse must not revive earlier session permissions")
	assert.True(t, lease.LeaseUntil.After(time.Now()))
}

func TestChatRunnerClaim_AttemptCapFailsTheTurnAndQueuedExpiryMarksNotDelivered(t *testing.T) {
	f := newChatDeleteFixture(t)
	repo := NewChatRunnerRepo(f.db)
	ctx := context.Background()

	capped, cappedMsg := f.queueTurn(t, f.wsA, f.projA, f.agentA, f.userA)
	_, err := f.db.Exec(`UPDATE ext_agent_chat_turns SET state='claimed', attempt=$2, runner_id=$3, claim_token='ct_old', lease_until = now() - interval '1 second' WHERE id=$1`,
		capped, ChatMaxAttempts, uuid.New())
	require.NoError(t, err)
	late, lateMsg := f.queueTurn(t, f.wsA, f.projA, f.agentA, f.userA)
	_, err = f.db.Exec(`UPDATE ext_agent_chat_turns SET expires_at = now() - interval '1 second' WHERE id=$1`, late)
	require.NoError(t, err)

	got, err := repo.ClaimTurns(ctx, f.claimParams(f.wsA, uuid.New(), f.agentA))
	require.NoError(t, err)
	assert.Empty(t, got, "neither a capped nor an expired turn is handed out")

	var state, code, mstate string
	require.NoError(t, f.db.QueryRow(`SELECT state, error_code FROM ext_agent_chat_turns WHERE id=$1`, capped).Scan(&state, &code))
	assert.Equal(t, "failed", state)
	assert.Equal(t, "runner_lost", code)
	require.NoError(t, f.db.Get(&mstate, `SELECT status FROM ext_agent_chat_messages WHERE id=$1`, cappedMsg))
	assert.Equal(t, "failed", mstate)

	require.NoError(t, f.db.QueryRow(`SELECT state, error_code FROM ext_agent_chat_turns WHERE id=$1`, late).Scan(&state, &code))
	assert.Equal(t, "expired", state)
	assert.Equal(t, "not_delivered", code)
	require.NoError(t, f.db.Get(&code, `SELECT error_code FROM ext_agent_chat_messages WHERE id=$1`, lateMsg))
	assert.Equal(t, "not_delivered", code)
}
