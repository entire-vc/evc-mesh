package postgres

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// Stable codes of the start / session-token half of the protocol (§5.1, §5.4).
const (
	ChatErrNotStarted      = "chat_not_started"
	ChatErrCancelRequested = "chat_cancel_requested"
)

// ChatStarted is what an accepted start reports (§5.1).
type ChatStarted struct {
	State           string
	LeaseUntil      time.Time
	CancelRequested bool
	LastSeq         int
	ExecutionEpoch  int64
}

// StartTurn is the commit point T3 (§5.1): claimed → streaming. Checks run in
// the order of RenewLease (foreign turn 404, other runner 403, stale token 409,
// dead turn 409). Repeating it on a streaming turn with the same claim_token
// answers 200 with the current state. Like every lease touch, a start after
// lease_until bumps execution_epoch (N4) so no earlier permission is revived.
func (r *ChatRunnerRepo) StartTurn(ctx context.Context, workspaceID uuid.UUID, agentIDs []uuid.UUID, runnerID, turnID uuid.UUID,
	claimToken string, leaseSeconds int) (*ChatStarted, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	t, err := lockHeldChatTurn(ctx, tx, workspaceID, agentIDs, runnerID, turnID, claimToken)
	if err != nil {
		return nil, err
	}
	switch t.State {
	case "claimed", "streaming":
	case "failed":
		return nil, chatProtocolError(409, ChatErrLeaseExpired, "the lease of this turn has expired", map[string]any{"state": t.State})
	case "queued":
		return nil, chatProtocolError(409, ChatErrStaleClaim, "the turn is back in the queue", nil)
	default:
		return nil, chatProtocolError(409, ChatErrTurnTerminal, "this turn is already finished", map[string]any{"state": t.State})
	}

	out := &ChatStarted{State: "streaming", CancelRequested: t.CancelRequested, LastSeq: t.LastSeq}
	if err := tx.GetContext(ctx, &out.LeaseUntil, `
		UPDATE ext_agent_chat_turns
		   SET state = 'streaming', started_at = COALESCE(started_at, now()),
		       lease_until = now() + make_interval(secs => $2)
		 WHERE id = $1 RETURNING lease_until`, turnID, leaseSeconds); err != nil {
		return nil, err
	}
	out.LeaseUntil = out.LeaseUntil.UTC()
	// The accepted start is the delivery point: the model sees the user message
	// from here on, so it leaves 'queued'. Cancel paths only touch a queued
	// message of a queued turn, so this cannot race them.
	if _, err := tx.ExecContext(ctx, `
		UPDATE ext_agent_chat_messages m SET status = 'done'
		  FROM ext_agent_chat_turns t
		 WHERE t.id = $1 AND m.id = t.user_message_id AND m.workspace_id = $2 AND m.status = 'queued'`,
		turnID, workspaceID); err != nil {
		return nil, err
	}
	if t.Expired {
		if err := tx.GetContext(ctx, &out.ExecutionEpoch, `
			UPDATE ext_agent_chat_threads SET execution_epoch = execution_epoch + 1
			 WHERE id = $1 AND workspace_id = $2 RETURNING execution_epoch`, t.ThreadID, workspaceID); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ext_agent_chat_turns SET execution_epoch = $2 WHERE id = $1`,
			turnID, out.ExecutionEpoch); err != nil {
			return nil, err
		}
	} else {
		out.ExecutionEpoch = t.ExecutionEpoch.Int64
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// ChatSessionContext is the server-side record a session token is bound to.
type ChatSessionContext struct {
	UserID         uuid.UUID
	AgentID        uuid.UUID
	WorkspaceID    uuid.UUID
	ProjectID      uuid.UUID
	ThreadID       uuid.UUID
	TurnID         uuid.UUID
	ExecutionEpoch int64
}

// SessionContext validates the §5.4 preconditions for issuing a session token
// and returns the binding from the turn's own thread, never from the caller:
// holder with the current claim_token, turn streaming (accepted start), lease
// alive, no cancel, and the runner's epoch equal to the turn's.
func (r *ChatRunnerRepo) SessionContext(ctx context.Context, workspaceID uuid.UUID, agentIDs []uuid.UUID, runnerID, turnID uuid.UUID,
	claimToken string, epoch int64) (*ChatSessionContext, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	t, err := lockHeldChatTurn(ctx, tx, workspaceID, agentIDs, runnerID, turnID, claimToken)
	if err != nil {
		return nil, err
	}
	switch t.State {
	case "streaming":
	case "claimed":
		return nil, chatProtocolError(409, ChatErrNotStarted, "the turn has not been started", nil)
	case "failed":
		return nil, chatProtocolError(409, ChatErrLeaseExpired, "the lease of this turn has expired", map[string]any{"state": t.State})
	case "queued":
		return nil, chatProtocolError(409, ChatErrStaleClaim, "the turn is back in the queue", nil)
	default:
		return nil, chatProtocolError(409, ChatErrTurnTerminal, "this turn is already finished", map[string]any{"state": t.State})
	}
	if t.Expired {
		return nil, chatProtocolError(409, ChatErrLeaseExpired, "the lease of this turn has expired", nil)
	}
	if t.CancelRequested {
		return nil, chatProtocolError(409, ChatErrCancelRequested, "the turn was cancelled", nil)
	}
	if t.ExecutionEpoch.Int64 != epoch {
		return nil, chatProtocolError(409, ChatErrStaleClaim, "execution epoch is not the turn's current one",
			map[string]any{"execution_epoch": t.ExecutionEpoch.Int64})
	}
	var th struct {
		UserID    uuid.UUID `db:"user_id"`
		ProjectID uuid.UUID `db:"project_id"`
	}
	if err := tx.GetContext(ctx, &th, `SELECT user_id, project_id FROM ext_agent_chat_threads
		WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, t.ThreadID, workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, chatProtocolError(409, ChatErrTurnTerminal, "the chat was deleted", nil)
		}
		return nil, err
	}
	return &ChatSessionContext{UserID: th.UserID, AgentID: t.AgentID, WorkspaceID: workspaceID, ProjectID: th.ProjectID,
		ThreadID: t.ThreadID, TurnID: turnID, ExecutionEpoch: t.ExecutionEpoch.Int64}, nil
}

type heldChatTurn struct {
	ThreadID        uuid.UUID      `db:"thread_id"`
	AgentID         uuid.UUID      `db:"agent_id"`
	State           string         `db:"state"`
	RunnerID        uuid.NullUUID  `db:"runner_id"`
	ClaimToken      sql.NullString `db:"claim_token"`
	ExecutionEpoch  sql.NullInt64  `db:"execution_epoch"`
	Expired         bool           `db:"expired"`
	CancelRequested bool           `db:"cancel_requested"`
	LastSeq         int            `db:"last_seq"`
}

// lockHeldChatTurn locks the turn row and applies the identity checks every
// holder-only call shares: served agent (else 404, no existence leak) → holder
// runner (403) → current claim_token (409, the fence).
func lockHeldChatTurn(ctx context.Context, tx chatTxGetter, workspaceID uuid.UUID, agentIDs []uuid.UUID,
	runnerID, turnID uuid.UUID, claimToken string) (*heldChatTurn, error) {
	var t heldChatTurn
	err := tx.GetContext(ctx, &t, `
		SELECT thread_id, agent_id, state, runner_id, claim_token, execution_epoch,
		       COALESCE(lease_until <= now(), false) AS expired, cancel_requested, last_seq
		  FROM ext_agent_chat_turns WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, turnID, workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errChatTurnNotFound()
	}
	if err != nil {
		return nil, err
	}
	served := false
	for _, a := range agentIDs {
		served = served || a == t.AgentID
	}
	if !served {
		return nil, errChatTurnNotFound()
	}
	if !t.RunnerID.Valid || t.RunnerID.UUID != runnerID {
		return nil, chatProtocolError(403, ChatErrNotClaimHolder, "this turn is held by another runner", nil)
	}
	if !t.ClaimToken.Valid || claimToken == "" || subtle.ConstantTimeCompare([]byte(t.ClaimToken.String), []byte(claimToken)) != 1 {
		return nil, chatProtocolError(409, ChatErrStaleClaim, "claim_token is not the current one for this turn", nil)
	}
	return &t, nil
}

// SessionGeneration is the 1-based generation of one issued token: its rank among
// the session's tokens by (issued_at, id) (§5.4 rotation). It depends only on
// that token and the ones issued before it, so a request that overlaps another
// issue still reports its own generation, not the final count.
func (r *ChatRunnerRepo) SessionGeneration(ctx context.Context, sessionID, tokenID uuid.UUID) (int, error) {
	var n int
	err := r.db.GetContext(ctx, &n, `SELECT COUNT(*) FROM chat_session_tokens t, chat_session_tokens me
		WHERE me.id = $2 AND t.chat_session_id = $1 AND (t.issued_at, t.id) <= (me.issued_at, me.id)`, sessionID, tokenID)
	return n, err
}

// ChatActiveTurn is a turn a runner still holds: what it needs to recover after a restart.
type ChatActiveTurn struct {
	TurnID         uuid.UUID `db:"id"`
	ThreadID       uuid.UUID `db:"thread_id"`
	AgentID        uuid.UUID `db:"agent_id"`
	State          string    `db:"state"`
	Attempt        int       `db:"attempt"`
	ClaimToken     string    `db:"claim_token"`
	LeaseUntil     time.Time `db:"lease_until"`
	ExecutionEpoch int64     `db:"execution_epoch"`
}

// ActiveTurns lists the claimed or streaming turns held by runnerID for the
// given agents, oldest first. The claim_token is returned: the caller is the
// holder (agent key + runner id), and it is what lease/start/events are fenced on.
func (r *ChatRunnerRepo) ActiveTurns(ctx context.Context, workspaceID uuid.UUID, agentIDs []uuid.UUID, runnerID uuid.UUID) ([]ChatActiveTurn, error) {
	out := []ChatActiveTurn{}
	if len(agentIDs) == 0 {
		return out, nil
	}
	err := r.db.SelectContext(ctx, &out, `
		SELECT id, thread_id, agent_id, state, attempt, claim_token, lease_until,
		       COALESCE(execution_epoch, 0) AS execution_epoch
		  FROM ext_agent_chat_turns
		 WHERE workspace_id = $1 AND agent_id = ANY($2) AND runner_id = $3
		   AND state IN ('claimed', 'streaming')
		 ORDER BY created_at, id`, workspaceID, pq.Array(agentIDs), runnerID)
	return out, err
}
