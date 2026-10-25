package postgres

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// D1: a claimed turn starts (claimed → streaming), idempotently for the same
// claim_token; the fence and the holder checks answer as the protocol says.
func TestChatRunnerStartTurn(t *testing.T) {
	f := newChatDeleteFixture(t)
	ctx := context.Background()
	thread, _, _ := f.historyThread(t, 1)
	repo := NewChatRunnerRepo(f.db)
	agents := []uuid.UUID{f.agentA}
	var turn uuid.UUID
	require.NoError(t, f.db.Get(&turn, `UPDATE ext_agent_chat_turns SET claim_token = 'tok-1', execution_epoch = 4
		WHERE thread_id = $1 RETURNING id`, thread))

	_, err := repo.StartTurn(ctx, f.wsA, agents, uuid.New(), turn, "tok-1", ChatLeaseSeconds)
	requireChatStatus(t, err, http.StatusForbidden)
	_, err = repo.StartTurn(ctx, f.wsA, agents, historyRunner, turn, "old", ChatLeaseSeconds)
	requireChatStatus(t, err, http.StatusConflict)
	_, err = repo.StartTurn(ctx, f.wsA, []uuid.UUID{f.agentB}, historyRunner, turn, "tok-1", ChatLeaseSeconds)
	requireChatStatus(t, err, http.StatusNotFound)

	out, err := repo.StartTurn(ctx, f.wsA, agents, historyRunner, turn, "tok-1", ChatLeaseSeconds)
	require.NoError(t, err)
	require.Equal(t, "streaming", out.State)
	require.EqualValues(t, 4, out.ExecutionEpoch)
	require.False(t, out.CancelRequested)
	var state string
	require.NoError(t, f.db.Get(&state, `SELECT state FROM ext_agent_chat_turns WHERE id = $1`, turn))
	require.Equal(t, "streaming", state)

	again, err := repo.StartTurn(ctx, f.wsA, agents, historyRunner, turn, "tok-1", ChatLeaseSeconds)
	require.NoError(t, err, "a lost response is safe to retry")
	require.Equal(t, "streaming", again.State)

	_, err = f.db.Exec(`UPDATE ext_agent_chat_turns SET state = 'done' WHERE id = $1`, turn)
	require.NoError(t, err)
	_, err = repo.StartTurn(ctx, f.wsA, agents, historyRunner, turn, "tok-1", ChatLeaseSeconds)
	requireChatStatus(t, err, http.StatusConflict)
}

// D2: the session binding is read from the turn, and only for the live holder of a started turn.
func TestChatRunnerSessionContext(t *testing.T) {
	f := newChatDeleteFixture(t)
	ctx := context.Background()
	thread, _, _ := f.historyThread(t, 1)
	repo := NewChatRunnerRepo(f.db)
	agents := []uuid.UUID{f.agentA}
	var turn uuid.UUID
	require.NoError(t, f.db.Get(&turn, `UPDATE ext_agent_chat_turns SET claim_token = 'tok-1', execution_epoch = 4
		WHERE thread_id = $1 RETURNING id`, thread))

	_, err := repo.SessionContext(ctx, f.wsA, agents, historyRunner, turn, "tok-1", 4)
	requireChatStatus(t, err, http.StatusConflict) // chat_not_started: claimed only

	_, err = repo.StartTurn(ctx, f.wsA, agents, historyRunner, turn, "tok-1", ChatLeaseSeconds)
	require.NoError(t, err)

	sc, err := repo.SessionContext(ctx, f.wsA, agents, historyRunner, turn, "tok-1", 4)
	require.NoError(t, err)
	require.Equal(t, f.userA, sc.UserID, "the user comes from the chat's own thread")
	require.Equal(t, f.projA, sc.ProjectID)
	require.Equal(t, thread, sc.ThreadID)
	require.EqualValues(t, 4, sc.ExecutionEpoch)

	_, err = repo.SessionContext(ctx, f.wsA, agents, uuid.New(), turn, "tok-1", 4)
	requireChatStatus(t, err, http.StatusForbidden)
	_, err = repo.SessionContext(ctx, f.wsA, agents, historyRunner, turn, "tok-1", 3)
	requireChatStatus(t, err, http.StatusConflict)

	_, err = f.db.Exec(`UPDATE ext_agent_chat_turns SET cancel_requested = true WHERE id = $1`, turn)
	require.NoError(t, err)
	_, err = repo.SessionContext(ctx, f.wsA, agents, historyRunner, turn, "tok-1", 4)
	requireChatStatus(t, err, http.StatusConflict)

	_, err = f.db.Exec(`UPDATE ext_agent_chat_turns SET cancel_requested = false, lease_until = now() - interval '1 second' WHERE id = $1`, turn)
	require.NoError(t, err)
	_, err = repo.SessionContext(ctx, f.wsA, agents, historyRunner, turn, "tok-1", 4)
	requireChatStatus(t, err, http.StatusConflict)
}

// Review finding: a Stop that lands after SessionContext validated the turn but
// before the token is issued must leave no usable token behind. The turn has no
// token yet, so the stop has to be recorded for the live turn itself.
func TestChatStopBeforeFirstTokenBlocksTheIssue(t *testing.T) {
	f := newChatDeleteFixture(t)
	ctx := context.Background()
	thread, _, _ := f.historyThread(t, 1)
	repo := NewChatRunnerRepo(f.db)
	var turn uuid.UUID
	require.NoError(t, f.db.Get(&turn, `UPDATE ext_agent_chat_turns SET claim_token = 'tok-1', execution_epoch = 4
		WHERE thread_id = $1 RETURNING id`, thread))
	_, err := repo.StartTurn(ctx, f.wsA, []uuid.UUID{f.agentA}, historyRunner, turn, "tok-1", ChatLeaseSeconds)
	require.NoError(t, err)
	sc, err := repo.SessionContext(ctx, f.wsA, []uuid.UUID{f.agentA}, historyRunner, turn, "tok-1", 4)
	require.NoError(t, err, "the validation passed")

	_, err = NewChatTurnEventsRepo(f.db).RequestCancel(ctx, f.userA, f.projA, thread)
	require.NoError(t, err)

	now := time.Now().UTC()
	err = NewChatSessionTokenRepo(f.db).Issue(ctx, &domain.ChatSessionToken{
		ID: uuid.New(), TokenHash: uuid.NewString(), UserID: sc.UserID, AgentID: sc.AgentID, WorkspaceID: sc.WorkspaceID,
		ProjectID: sc.ProjectID, ChatThreadID: sc.ThreadID, ChatSessionID: sc.TurnID, RunnerID: historyRunner.String(),
		ExecutionEpoch: sc.ExecutionEpoch, IssuedAt: now, ExpiresAt: now.Add(5 * time.Minute),
	}, now.Add(10*time.Second))
	require.ErrorIs(t, err, repository.ErrChatEpochStale, "no token may be issued after the stop")
}

// The generation of a token is its own rank, not the session's final count.
func TestChatSessionGenerationIsPerToken(t *testing.T) {
	f := newChatDeleteFixture(t)
	ctx := context.Background()
	thread, _, _ := f.historyThread(t, 1)
	repo := NewChatRunnerRepo(f.db)
	var turn uuid.UUID
	require.NoError(t, f.db.Get(&turn, `SELECT id FROM ext_agent_chat_turns WHERE thread_id = $1`, thread))
	tokens := NewChatSessionTokenRepo(f.db)

	var ids []uuid.UUID
	base := time.Now().UTC()
	for i := 0; i < 3; i++ {
		id := uuid.New()
		ids = append(ids, id)
		at := base.Add(time.Duration(i) * time.Second)
		require.NoError(t, tokens.Issue(ctx, &domain.ChatSessionToken{
			ID: id, TokenHash: uuid.NewString(), UserID: f.userA, AgentID: f.agentA, WorkspaceID: f.wsA, ProjectID: f.projA,
			ChatThreadID: thread, ChatSessionID: turn, RunnerID: historyRunner.String(), ExecutionEpoch: 1,
			IssuedAt: at, ExpiresAt: at.Add(5 * time.Minute),
		}, at.Add(10*time.Second)))
	}
	for i, id := range ids {
		n, err := repo.SessionGeneration(ctx, turn, id)
		require.NoError(t, err)
		require.Equal(t, i+1, n, "token %d keeps its own generation after later issues", i)
	}
}

// The accepted start is the delivery point of the user message: queued -> done.
func TestChatRunnerStartTurnDeliversUserMessage(t *testing.T) {
	f := newChatDeleteFixture(t)
	ctx := context.Background()
	repo := NewChatRunnerRepo(f.db)
	turn, msg := f.queueTurn(t, f.wsA, f.projA, f.agentA, f.userA)
	claimed, err := repo.ClaimTurns(ctx, f.claimParams(f.wsA, historyRunner, f.agentA))
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	var status string
	require.NoError(t, f.db.Get(&status, `SELECT status FROM ext_agent_chat_messages WHERE id = $1`, msg))
	require.Equal(t, "queued", status, "a claim alone does not deliver")

	_, err = repo.StartTurn(ctx, f.wsA, []uuid.UUID{f.agentA}, historyRunner, turn, claimed[0].ClaimToken, ChatLeaseSeconds)
	require.NoError(t, err)
	require.NoError(t, f.db.Get(&status, `SELECT status FROM ext_agent_chat_messages WHERE id = $1`, msg))
	require.Equal(t, "done", status)
}

// A restarted runner finds what it still holds: only its own claimed/streaming turns.
func TestChatRunnerActiveTurns(t *testing.T) {
	f := newChatDeleteFixture(t)
	ctx := context.Background()
	repo := NewChatRunnerRepo(f.db)
	turn, _ := f.queueTurn(t, f.wsA, f.projA, f.agentA, f.userA)

	got, err := repo.ActiveTurns(ctx, f.wsA, []uuid.UUID{f.agentA}, historyRunner)
	require.NoError(t, err)
	require.Empty(t, got, "a queued turn is not held by anyone")

	claimed, err := repo.ClaimTurns(ctx, f.claimParams(f.wsA, historyRunner, f.agentA))
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	got, err = repo.ActiveTurns(ctx, f.wsA, []uuid.UUID{f.agentA}, historyRunner)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, turn, got[0].TurnID)
	require.Equal(t, "claimed", got[0].State)
	require.Equal(t, claimed[0].ClaimToken, got[0].ClaimToken)

	other, err := repo.ActiveTurns(ctx, f.wsA, []uuid.UUID{f.agentA}, uuid.New())
	require.NoError(t, err)
	require.Empty(t, other, "another runner id does not see it")
	foreign, err := repo.ActiveTurns(ctx, f.wsA, []uuid.UUID{f.agentB}, historyRunner)
	require.NoError(t, err)
	require.Empty(t, foreign, "another agent does not see it")

	_, err = f.db.Exec(`UPDATE ext_agent_chat_turns SET state = 'done' WHERE id = $1`, turn)
	require.NoError(t, err)
	got, err = repo.ActiveTurns(ctx, f.wsA, []uuid.UUID{f.agentA}, historyRunner)
	require.NoError(t, err)
	require.Empty(t, got, "a finished turn is not active")
}
