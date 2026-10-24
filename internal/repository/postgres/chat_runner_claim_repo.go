package postgres

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Runner protocol defaults (Mesh Doc agent-chat-runner-protocol §11).
const (
	ChatLeaseSeconds = 60
	ChatMaxAttempts  = 3
)

// Stable codes of the runner protocol (§10). Clients branch on these.
const (
	ChatErrStaleClaim     = "chat_stale_claim"
	ChatErrLeaseExpired   = "chat_lease_expired"
	ChatErrTurnTerminal   = "chat_turn_terminal"
	ChatErrNotClaimHolder = "chat_not_claim_holder"
	ChatErrTurnNotFound   = "chat_turn_not_found"
)

func chatProtocolError(status int, code, message string, ctx map[string]any) *apierror.Error {
	return &apierror.Error{Code: status, ErrorCode: code, Message: message, Context: ctx}
}

// ChatClaimParams says who claims what. AgentIDs is already authorised by the
// caller; the repo still pins every row to WorkspaceID.
type ChatClaimParams struct {
	WorkspaceID  uuid.UUID
	AgentIDs     []uuid.UUID
	RunnerID     uuid.UUID
	Max          int
	LeaseSeconds int
	MaxAttempts  int
}

type ChatClaimedAttachment struct {
	ID       uuid.UUID `db:"id"`
	FileName string    `db:"file_name"`
	Mime     string    `db:"mime"`
	Size     int64     `db:"size"`
	Width    *int      `db:"width"`
	Height   *int      `db:"height"`
}

// ChatClaimedTurn is a freshly claimed turn with what the runner needs to run it.
type ChatClaimedTurn struct {
	TurnID         uuid.UUID
	ThreadID       uuid.UUID
	AgentID        uuid.UUID
	ProjectID      uuid.UUID
	WorkspaceID    uuid.UUID
	Attempt        int
	ClaimToken     string
	ExecutionEpoch int64
	LeaseUntil     time.Time
	CreatedAt      time.Time
	ExpiresAt      time.Time
	RequestedBy    uuid.UUID
	DisplayName    string
	MessageID      uuid.UUID
	Body           string
	Attachments    []ChatClaimedAttachment
	Runtime        []byte
}

type chatClaimRow struct {
	ID             uuid.UUID     `db:"id"`
	ThreadID       uuid.UUID     `db:"thread_id"`
	AgentID        uuid.UUID     `db:"agent_id"`
	UserMessageID  uuid.NullUUID `db:"user_message_id"`
	Attempt        int           `db:"attempt"`
	ClaimToken     string        `db:"claim_token"`
	LeaseUntil     time.Time     `db:"lease_until"`
	CreatedAt      time.Time     `db:"created_at"`
	ExpiresAt      time.Time     `db:"expires_at"`
	ExecutionEpoch int64         `db:"execution_epoch"`
}

// ClaimTurns hands out up to p.Max turns of the listed agents. Deliverable is a
// queued turn that has not expired, or a claimed one whose lease ran out before
// any accepted start (T7: the model never saw it, so re-handing is safe). The
// claim_token is rotated on every claim, which is what fences the previous
// holder. FOR UPDATE SKIP LOCKED makes concurrent claims disjoint.
func (r *ChatRunnerRepo) ClaimTurns(ctx context.Context, p ChatClaimParams) ([]ChatClaimedTurn, error) {
	if len(p.AgentIDs) == 0 || p.Max < 1 {
		return nil, nil
	}
	agents := pq.Array(p.AgentIDs)
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// T13, lazily: a queued turn past its deadline is never handed out.
	// T8, lazily: a lease that ran out with no accepted start, at the attempt cap.
	if _, err = tx.ExecContext(ctx, `
		WITH ended AS (
			UPDATE ext_agent_chat_turns
			   SET state = CASE WHEN state = 'queued' THEN 'expired' ELSE 'failed' END,
			       error_code = CASE WHEN state = 'queued' THEN 'not_delivered' ELSE 'runner_lost' END,
			       finished_at = now()
			 WHERE workspace_id = $1 AND agent_id = ANY($2)
			   AND ((state = 'queued' AND expires_at <= now())
			     OR (state = 'claimed' AND lease_until <= now() AND attempt >= $3))
			RETURNING workspace_id, user_message_id, agent_message_id, error_code
		)
		UPDATE ext_agent_chat_messages m
		   SET status = 'failed', error_code = e.error_code
		  FROM ended e
		 WHERE m.workspace_id = e.workspace_id AND m.id IN (e.user_message_id, e.agent_message_id)`,
		p.WorkspaceID, agents, p.MaxAttempts); err != nil {
		return nil, err
	}

	var rows []chatClaimRow
	if qerr := tx.SelectContext(ctx, &rows, `
		WITH cand AS (
			SELECT id FROM ext_agent_chat_turns
			 WHERE workspace_id = $1 AND agent_id = ANY($2)
			   AND ((state = 'queued' AND expires_at > now())
			     OR (state = 'claimed' AND lease_until <= now() AND attempt < $3))
			 ORDER BY created_at, id
			 LIMIT $4
			 FOR UPDATE SKIP LOCKED
		)
		UPDATE ext_agent_chat_turns t
		   SET state = 'claimed',
		       runner_id = $5,
		       attempt = t.attempt + 1,
		       claim_token = 'ct_' || replace(gen_random_uuid()::text, '-', '') || replace(gen_random_uuid()::text, '-', ''),
		       lease_until = now() + make_interval(secs => $6),
		       first_claimed_at = COALESCE(t.first_claimed_at, now())
		  FROM cand
		 WHERE t.id = cand.id
		RETURNING t.id, t.thread_id, t.agent_id, t.user_message_id, t.attempt, t.claim_token,
		          t.lease_until, t.created_at, t.expires_at, COALESCE(t.execution_epoch, 0) AS execution_epoch`,
		p.WorkspaceID, agents, p.MaxAttempts, p.Max, p.RunnerID, p.LeaseSeconds); qerr != nil {
		return nil, qerr
	}

	out := make([]ChatClaimedTurn, 0, len(rows))
	for _, row := range rows {
		turn := ChatClaimedTurn{
			TurnID: row.ID, ThreadID: row.ThreadID, AgentID: row.AgentID, WorkspaceID: p.WorkspaceID,
			Attempt: row.Attempt, ClaimToken: row.ClaimToken, LeaseUntil: row.LeaseUntil.UTC(),
			CreatedAt: row.CreatedAt.UTC(), ExpiresAt: row.ExpiresAt.UTC(),
		}
		// The thread counter is the execution generation: +1 on every claim.
		var th struct {
			ProjectID uuid.UUID `db:"project_id"`
			UserID    uuid.UUID `db:"user_id"`
			Epoch     int64     `db:"execution_epoch"`
			Runtime   []byte    `db:"runtime"`
		}
		if qerr := tx.GetContext(ctx, &th, `
			UPDATE ext_agent_chat_threads SET execution_epoch = execution_epoch + 1
			 WHERE id = $1 AND workspace_id = $2
			RETURNING project_id, user_id, execution_epoch, runtime`, row.ThreadID, p.WorkspaceID); qerr != nil {
			return nil, qerr
		}
		if _, err = tx.ExecContext(ctx, `UPDATE ext_agent_chat_turns SET execution_epoch = $2 WHERE id = $1`,
			row.ID, th.Epoch); err != nil {
			return nil, err
		}
		turn.ProjectID, turn.RequestedBy, turn.ExecutionEpoch, turn.Runtime = th.ProjectID, th.UserID, th.Epoch, th.Runtime

		if qerr := tx.GetContext(ctx, &turn.DisplayName, `SELECT display_name FROM users WHERE id = $1`, th.UserID); qerr != nil && !errors.Is(qerr, sql.ErrNoRows) {
			return nil, qerr
		}
		if row.UserMessageID.Valid {
			turn.MessageID = row.UserMessageID.UUID
			if qerr := tx.GetContext(ctx, &turn.Body, `SELECT body FROM ext_agent_chat_messages WHERE id = $1 AND workspace_id = $2`,
				row.UserMessageID.UUID, p.WorkspaceID); qerr != nil && !errors.Is(qerr, sql.ErrNoRows) {
				return nil, qerr
			}
			if qerr := tx.SelectContext(ctx, &turn.Attachments, `
				SELECT id, file_name, mime, size, width, height FROM ext_agent_chat_attachments
				 WHERE message_id = $1 AND workspace_id = $2 AND uploaded_at IS NOT NULL
				 ORDER BY created_at, id`, row.UserMessageID.UUID, p.WorkspaceID); qerr != nil {
				return nil, qerr
			}
		}
		out = append(out, turn)
	}
	if qerr := tx.Commit(); qerr != nil {
		return nil, qerr
	}
	return out, nil
}

// ChatLease is what a successful lease renewal reports.
type ChatLease struct {
	LeaseUntil      time.Time
	State           string
	CancelRequested bool
	ExecutionEpoch  int64
}

// RenewLease extends the lease of a claimed or streaming turn (§5.2). Checks in
// protocol order: the turn belongs to the caller's agent (else 404, no existence
// leak) → the caller is the runner holding it (403) → the claim_token is the
// current one (409 chat_stale_claim, the fence) → the turn is still alive.
//
// A renewal after lease_until that the sweeper has not acted on yet succeeds
// (N4: a late but live runner is not punished) and bumps execution_epoch, so no
// session permission issued before the lapse is revived.
func (r *ChatRunnerRepo) RenewLease(ctx context.Context, workspaceID uuid.UUID, agentIDs []uuid.UUID, runnerID, turnID uuid.UUID,
	claimToken string, leaseSeconds int) (*ChatLease, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var t struct {
		ThreadID        uuid.UUID      `db:"thread_id"`
		AgentID         uuid.UUID      `db:"agent_id"`
		State           string         `db:"state"`
		RunnerID        uuid.NullUUID  `db:"runner_id"`
		ClaimToken      sql.NullString `db:"claim_token"`
		LeaseUntil      sql.NullTime   `db:"lease_until"`
		Expired         bool           `db:"expired"`
		CancelRequested bool           `db:"cancel_requested"`
		TerminalSeq     sql.NullInt64  `db:"terminal_seq"`
	}
	err = tx.GetContext(ctx, &t, `
		SELECT thread_id, agent_id, state, runner_id, claim_token, lease_until,
		       COALESCE(lease_until <= now(), false) AS expired, cancel_requested, terminal_seq
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
	if !t.ClaimToken.Valid || subtle.ConstantTimeCompare([]byte(t.ClaimToken.String), []byte(claimToken)) != 1 {
		return nil, chatProtocolError(409, ChatErrStaleClaim, "claim_token is not the current one for this turn", nil)
	}
	switch t.State {
	case "claimed", "streaming":
	case "failed":
		return nil, chatProtocolError(409, ChatErrLeaseExpired, "the lease of this turn has expired", map[string]any{"state": t.State})
	default:
		return nil, chatProtocolError(409, ChatErrTurnTerminal, "this turn is already finished", map[string]any{"state": t.State})
	}

	var out ChatLease
	out.State, out.CancelRequested = t.State, t.CancelRequested
	if qerr := tx.GetContext(ctx, &out.LeaseUntil, `
		UPDATE ext_agent_chat_turns SET lease_until = now() + make_interval(secs => $2)
		 WHERE id = $1 RETURNING lease_until`, turnID, leaseSeconds); qerr != nil {
		return nil, qerr
	}
	out.LeaseUntil = out.LeaseUntil.UTC()
	if t.Expired {
		if qerr := tx.GetContext(ctx, &out.ExecutionEpoch, `
			UPDATE ext_agent_chat_threads SET execution_epoch = execution_epoch + 1
			 WHERE id = $1 AND workspace_id = $2 RETURNING execution_epoch`, t.ThreadID, workspaceID); qerr != nil {
			return nil, qerr
		}
		if _, err = tx.ExecContext(ctx, `UPDATE ext_agent_chat_turns SET execution_epoch = $2 WHERE id = $1`,
			turnID, out.ExecutionEpoch); err != nil {
			return nil, err
		}
	} else if qerr := tx.GetContext(ctx, &out.ExecutionEpoch, `SELECT COALESCE(execution_epoch, 0) FROM ext_agent_chat_turns WHERE id = $1`, turnID); qerr != nil {
		return nil, qerr
	}
	if qerr := tx.Commit(); qerr != nil {
		return nil, qerr
	}
	return &out, nil
}

func errChatTurnNotFound() error {
	return chatProtocolError(404, ChatErrTurnNotFound, "turn not found", nil)
}
