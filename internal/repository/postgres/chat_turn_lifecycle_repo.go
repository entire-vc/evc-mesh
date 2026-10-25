package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

const (
	chatStopUserStop    = "user_stop"
	chatErrNotDelivered = "not_delivered"
)

// ChatTurnOutcome is a turn the lifecycle moved, with what the owner must hear.
type ChatTurnOutcome struct {
	ChatEventEffects
	State     string
	ErrorCode *string
	// Message is the user message whose status changed (expiry: not delivered;
	// stop of a queued turn: cancelled).
	UserMessage *ChatMessageRow
}

// chatTurnDeadline is when a queued turn gives up waiting for a runner: the
// workspace's current agent-chat offline_expiry_hours (integration config) counted from creation, so
// changing the setting takes effect on turns already waiting; without the
// setting, the expires_at stored at creation (the 24 h default).
const chatTurnDeadline = `COALESCE(
	t.created_at + make_interval(hours => NULLIF(GREATEST(NULLIF((SELECT ic.config->>'offline_expiry_hours' FROM integration_configs ic WHERE ic.workspace_id = t.workspace_id AND ic.provider = 'agent-chat'), '')::int, 0), 0)),
	t.expires_at)`

// OfflineTTL is how long a queued turn waits for a runner: the workspace
// agent-chat integration setting offline_expiry_hours, 24 h when unset or not positive.
func (r *ChatTurnEventsRepo) OfflineTTL(ctx context.Context, workspaceID uuid.UUID) (time.Duration, error) {
	var hours sql.NullInt64
	err := r.db.GetContext(ctx, &hours, `SELECT NULLIF(config->>'offline_expiry_hours', '')::int
		FROM integration_configs WHERE workspace_id = $1 AND provider = 'agent-chat'`, workspaceID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if !hours.Valid || hours.Int64 <= 0 {
		return ChatDefaultOfflineTTL, nil
	}
	return time.Duration(hours.Int64) * time.Hour, nil
}

// ExpireQueued is T13: a turn still queued past expires_at (no runner took it)
// becomes expired and its user message "not delivered". The single guarded
// UPDATE is what makes a claim racing the expiry safe: whoever flips the state
// first wins, and a just-claimed turn is no longer queued.
func (r *ChatTurnEventsRepo) ExpireQueued(ctx context.Context, limit int) ([]ChatTurnOutcome, error) {
	var ids []uuid.UUID
	if err := r.db.SelectContext(ctx, &ids, `SELECT t.id FROM ext_agent_chat_turns t
		WHERE t.state = 'queued' AND `+chatTurnDeadline+` < $1 ORDER BY `+chatTurnDeadline+` LIMIT $2`, r.now().UTC(), limit); err != nil {
		return nil, err
	}
	var out []ChatTurnOutcome
	for _, id := range ids {
		o, err := r.expireOne(ctx, id)
		if err != nil {
			return out, err
		}
		if o != nil {
			out = append(out, *o)
		}
	}
	return out, nil
}

func (r *ChatTurnEventsRepo) expireOne(ctx context.Context, id uuid.UUID) (*ChatTurnOutcome, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	now := r.now().UTC()
	var row struct {
		WorkspaceID   uuid.UUID  `db:"workspace_id"`
		ThreadID      uuid.UUID  `db:"thread_id"`
		UserMessageID *uuid.UUID `db:"user_message_id"`
	}
	err = tx.GetContext(ctx, &row, `UPDATE ext_agent_chat_turns t SET state = 'expired', finished_at = $2, error_code = $3
		WHERE t.id = $1 AND t.state = 'queued' AND `+chatTurnDeadline+` < $2
		RETURNING t.workspace_id, t.thread_id, t.user_message_id`, id, now, chatErrNotDelivered)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // claimed, cancelled or already expired meanwhile
	}
	if err != nil {
		return nil, err
	}
	o := &ChatTurnOutcome{State: ChatTurnExpired}
	code := chatErrNotDelivered
	o.ErrorCode = &code
	if err := r.fillThread(ctx, tx, &o.ChatEventEffects, row.WorkspaceID, row.ThreadID, id); err != nil {
		return nil, err
	}
	if row.UserMessageID != nil {
		m := &ChatMessageRow{}
		if err := tx.GetContext(ctx, m, `UPDATE ext_agent_chat_messages SET status = 'failed', error_code = $3
			WHERE id = $1 AND workspace_id = $2
			RETURNING id, thread_id, role, body, status, truncated, turn_id, error_code, created_at`,
			*row.UserMessageID, row.WorkspaceID, chatErrNotDelivered); err != nil {
			return nil, err
		}
		o.UserMessage = m
	}
	return o, tx.Commit()
}

type chatTxGetter interface {
	GetContext(ctx context.Context, dest any, query string, args ...any) error
}

func (r *ChatTurnEventsRepo) fillThread(ctx context.Context, tx chatTxGetter, fx *ChatEventEffects, ws, thread, turn uuid.UUID) error {
	var th struct {
		ProjectID uuid.UUID `db:"project_id"`
		OwnerID   uuid.UUID `db:"user_id"`
	}
	if err := tx.GetContext(ctx, &th, `SELECT project_id, user_id FROM ext_agent_chat_threads WHERE id = $1 AND workspace_id = $2`, thread, ws); err != nil {
		return err
	}
	*fx = ChatEventEffects{WorkspaceID: ws, ProjectID: th.ProjectID, ThreadID: thread, OwnerID: th.OwnerID, TurnID: turn}
	return nil
}

// RequestCancel is the user's Stop (protocol §7). A queued turn is cancelled at
// once (T10, the runner never sees it). A claimed or streaming one only gets
// cancel_requested, which the runner reads from its next events/lease answer and
// the session-token authorizer reads to refuse further tokens; the thread's
// execution epoch moves on so permissions issued before the Stop stop working.
// A thread with no unfinished turn is a no-op: the result's State is empty.
func (r *ChatTurnEventsRepo) RequestCancel(ctx context.Context, userID, project, thread uuid.UUID) (*ChatTurnOutcome, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// Lock order is turn, then thread: ApplyEvents locks the turn and later writes
	// the thread, so taking the thread first here could deadlock against it.
	var t struct {
		ID              uuid.UUID  `db:"id"`
		WorkspaceID     uuid.UUID  `db:"workspace_id"`
		State           string     `db:"state"`
		UserMessageID   *uuid.UUID `db:"user_message_id"`
		CancelRequested bool       `db:"cancel_requested"`
	}
	err = tx.GetContext(ctx, &t, `SELECT t.id, t.workspace_id, t.state, t.user_message_id, t.cancel_requested
		FROM ext_agent_chat_turns t JOIN ext_agent_chat_threads th ON th.id = t.thread_id AND th.workspace_id = t.workspace_id
		WHERE th.id = $1 AND th.project_id = $2 AND th.user_id = $3 AND th.deleted_at IS NULL
		  AND t.state IN ('queued', 'claimed', 'streaming') FOR UPDATE OF t`, thread, project, userID)
	if errors.Is(err, sql.ErrNoRows) {
		var exists bool
		if qerr := tx.GetContext(ctx, &exists, `SELECT EXISTS (SELECT 1 FROM ext_agent_chat_threads
			WHERE id = $1 AND project_id = $2 AND user_id = $3 AND deleted_at IS NULL)`, thread, project, userID); qerr != nil {
			return nil, qerr
		}
		if !exists {
			return nil, apierror.NotFoundWithDetails("thread", "thread_not_found")
		}
		return &ChatTurnOutcome{}, tx.Commit() // nothing running: Stop is a no-op
	}
	if err != nil {
		return nil, err
	}
	ws := t.WorkspaceID
	now := r.now().UTC()
	o := &ChatTurnOutcome{State: t.State}
	if err := r.fillThread(ctx, tx, &o.ChatEventEffects, ws, thread, t.ID); err != nil {
		return nil, err
	}
	if t.State == ChatTurnQueued {
		if _, err := tx.ExecContext(ctx, `UPDATE ext_agent_chat_turns SET state = 'cancelled', cancel_requested = true,
			cancel_requested_at = $2, stop_reason = $3, finished_at = $2 WHERE id = $1 AND state = 'queued'`, t.ID, now, chatStopUserStop); err != nil {
			return nil, err
		}
		o.State = ChatTurnCancelled
		if t.UserMessageID != nil {
			m := &ChatMessageRow{}
			if err := tx.GetContext(ctx, m, `UPDATE ext_agent_chat_messages SET status = 'cancelled'
				WHERE id = $1 AND workspace_id = $2
				RETURNING id, thread_id, role, body, status, truncated, turn_id, error_code, created_at`, *t.UserMessageID, ws); err != nil {
				return nil, err
			}
			o.UserMessage = m
		}
		return o, tx.Commit()
	}
	if !t.CancelRequested {
		if _, err := tx.ExecContext(ctx, `UPDATE ext_agent_chat_turns SET cancel_requested = true, cancel_requested_at = $2
			WHERE id = $1`, t.ID, now); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ext_agent_chat_threads SET execution_epoch = execution_epoch + 1 WHERE id = $1`, thread); err != nil {
			return nil, err
		}
	}
	// Stop is final for the thread's sessions: every chat-session token dies now
	// (the same tombstone ChatSessionService.RevokeSession writes), so a token the
	// runner already holds stops working instead of waiting for its expiry.
	if err := revokeThreadSessions(ctx, tx, thread, now); err != nil {
		return nil, err
	}
	return o, tx.Commit()
}

// revokeThreadSessions ends every chat session of a thread for good. A session
// is the turn (the session id of a token is its turn id), so the tombstone is
// written for the thread's live turns as well as for sessions that already have
// a token: a token requested after Stop but validated before it must find the
// tombstone. Each session id is locked with the same advisory key
// ChatSessionTokenRepo.Issue takes, so Stop and an issue in flight are
// serialised: either the token exists and is revoked here, or Issue sees the stop.
func revokeThreadSessions(ctx context.Context, tx *sqlx.Tx, thread uuid.UUID, now time.Time) error {
	var sessions []uuid.UUID
	if err := tx.SelectContext(ctx, &sessions, `
		SELECT id FROM ext_agent_chat_turns WHERE thread_id = $1 AND state IN ('claimed', 'streaming')
		UNION
		SELECT chat_session_id FROM chat_session_tokens WHERE chat_thread_id = $1
		ORDER BY 1`, thread); err != nil {
		return err
	}
	for _, id := range sessions {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1::text))`, "chat_session:"+id.String()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO chat_session_stops (chat_session_id, stopped_at, reason)
			VALUES ($1, $2::timestamptz, 'stop') ON CONFLICT (chat_session_id) DO NOTHING`, id, now); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE chat_session_tokens SET revoked_at = $2::timestamptz, revoked_reason = 'stop'
		WHERE chat_thread_id = $1 AND (revoked_at IS NULL OR revoked_at > $2::timestamptz)`, thread, now)
	return err
}
