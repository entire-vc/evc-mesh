package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Runner-facing chat orders (runner protocol §8, §9). The runner is the agent
// that holds the key: every call is authorised against the caller's agent id, so
// a runner never sees or touches a command or thread of an agent it does not serve.
const (
	// ChatCommandMaxAttempts is how many failed acks a command survives before it is dead.
	ChatCommandMaxAttempts = 10
	// ChatHistoryDefaultLimit / ChatHistoryMaxLimit bound the recovery history page.
	ChatHistoryDefaultLimit = 20
	ChatHistoryMaxLimit     = 100
)

// ChatCommandAck results a runner may report.
const (
	ChatAckDone     = "done"
	ChatAckNotFound = "not_found"
	ChatAckFailed   = "failed"
)

// ChatRunnerRepo serves the orders and the session-recovery history to a runner.
type ChatRunnerRepo struct {
	db  *sqlx.DB
	now func() time.Time
}

func NewChatRunnerRepo(db *sqlx.DB) *ChatRunnerRepo {
	return &ChatRunnerRepo{db: db, now: time.Now}
}

// ChatCommand is one durable order for a runner.
type ChatCommand struct {
	ID        uuid.UUID  `db:"id" json:"command_id"`
	Kind      string     `db:"kind" json:"kind"`
	AgentID   uuid.UUID  `db:"agent_id" json:"agent_id"`
	RunnerID  *uuid.UUID `db:"runner_id" json:"runner_id"`
	ThreadID  uuid.UUID  `db:"thread_id" json:"thread_id"`
	SessionID *string    `db:"session_id" json:"session_id"`
	Harness   *string    `db:"harness" json:"harness"`
	CreatedAt time.Time  `db:"created_at" json:"created_at"`
}

// truncateUTF8 cuts s to at most limit bytes without splitting a multibyte character.
func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}

func errChatRunnerForbidden() error {
	return apierror.ForbiddenWithDetails("agent is not served by this runner", "chat_runner_forbidden")
}

// PendingCommands lists the orders the runner has not acked yet, oldest first.
// Delivery is at least once: a command is returned until it is acked. An order
// addressed to a runner is returned only to that runner: a caller that does not
// name itself sees just the orders addressed to the agent as a whole.
//
// Trust model: runner is the identity of the runner credential the caller
// presented (AuthenticateRunner), never a value taken from the request. An
// order addressed to a runner reaches only that runner; a caller with no runner
// credential sees just the orders addressed to the agent as a whole. Another
// agent gets a 403.
func (r *ChatRunnerRepo) PendingCommands(ctx context.Context, caller uuid.UUID, agents []uuid.UUID, runnerID *uuid.UUID, limit int) ([]ChatCommand, error) {
	if caller == uuid.Nil {
		return nil, errChatRunnerForbidden()
	}
	if len(agents) == 0 {
		agents = []uuid.UUID{caller}
	}
	ids := make([]string, 0, len(agents))
	for _, a := range agents {
		if a != caller {
			return nil, errChatRunnerForbidden()
		}
		ids = append(ids, a.String())
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var rid any
	if runnerID != nil {
		rid = *runnerID
	}
	out := []ChatCommand{}
	err := r.db.SelectContext(ctx, &out, `SELECT id, kind, agent_id, runner_id, thread_id, session_id, harness, created_at
		FROM ext_agent_chat_commands
		WHERE agent_id = ANY($1::uuid[]) AND state = 'pending' AND expires_at > $2
		  AND (runner_id IS NULL OR runner_id = $3::uuid)
		ORDER BY created_at, id LIMIT $4`, pq.Array(ids), r.now().UTC(), rid, limit)
	return out, err
}

// AckCommand closes a command. done and not_found close it; failed keeps it and
// counts an attempt, and the tenth failure makes it dead. A repeated ack of a
// closed command is a no-op that answers its state. A command of another agent
// and an unknown or invisible one (another workspace under RLS) are both a 403.
//
// An order addressed to a runner is acked only by that runner (the verified
// identity of its credential, nil when the caller presented none): the same 403
// as a foreign command, so a sibling runner cannot even tell it exists.
func (r *ChatRunnerRepo) AckCommand(ctx context.Context, caller, command uuid.UUID, runner *uuid.UUID, result, detail string) (string, error) {
	switch result {
	case ChatAckDone, ChatAckNotFound, ChatAckFailed:
	default:
		return "", apierror.BadRequest("result must be done, not_found or failed")
	}
	detail = truncateUTF8(strings.ToValidUTF8(detail, ""), 500)
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	var cmd struct {
		AgentID  uuid.UUID  `db:"agent_id"`
		RunnerID *uuid.UUID `db:"runner_id"`
		State    string     `db:"state"`
		Attempts int        `db:"attempts"`
	}
	err = tx.GetContext(ctx, &cmd, `SELECT agent_id, runner_id, state, attempts FROM ext_agent_chat_commands WHERE id = $1 FOR UPDATE`, command)
	if errors.Is(err, sql.ErrNoRows) {
		// RLS hides a command of another workspace, so "unknown" and "foreign" look
		// the same here; both are refused alike and the id leaks nothing.
		return "", errChatRunnerForbidden()
	}
	if err != nil {
		return "", err
	}
	if caller == uuid.Nil || cmd.AgentID != caller {
		return "", errChatRunnerForbidden()
	}
	if cmd.RunnerID != nil && (runner == nil || *runner != *cmd.RunnerID) {
		return "", errChatRunnerForbidden()
	}
	if cmd.State != "pending" {
		return cmd.State, tx.Commit()
	}
	state, attempts := "done", cmd.Attempts
	if result == ChatAckFailed {
		attempts++
		state = "pending"
		if attempts >= ChatCommandMaxAttempts {
			state = "dead"
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ext_agent_chat_commands SET state = $2, attempts = $3, detail = NULLIF($4, '') WHERE id = $1`,
		command, state, attempts, detail); err != nil {
		return "", err
	}
	return state, tx.Commit()
}

// ChatHistoryAttachment is attachment metadata only: history carries no download URL.
type ChatHistoryAttachment struct {
	FileName string `json:"file_name"`
	Mime     string `json:"mime"`
	Size     int64  `json:"size"`
}

// ChatHistoryMessage is one past message handed to a recovering session.
type ChatHistoryMessage struct {
	MessageID   uuid.UUID               `json:"message_id"`
	Role        string                  `json:"role"`
	Body        string                  `json:"body"`
	CreatedAt   time.Time               `json:"created_at"`
	Status      string                  `json:"status"`
	Truncated   bool                    `json:"truncated"`
	Attachments []ChatHistoryAttachment `json:"attachments"`
}

// ChatHistory is a page of the last messages of a thread, oldest first.
type ChatHistory struct {
	ThreadID uuid.UUID            `json:"thread_id"`
	Messages []ChatHistoryMessage `json:"messages"`
	HasMore  bool                 `json:"has_more"`
}

// History returns the last limit messages of a thread, oldest to newest, before
// the optional cursor message. Only the runner of the thread's agent that
// currently holds a claimed or streaming turn of the thread with a live lease may read it (the
// history is a user's private chat). The current turn's user message is left
// out (the claim already carries it), and so are messages still being written
// and failed/cancelled ones with no body.
func (r *ChatRunnerRepo) History(ctx context.Context, caller, thread uuid.UUID, limit int, before *uuid.UUID) (*ChatHistory, error) {
	if limit <= 0 {
		limit = ChatHistoryDefaultLimit
	}
	if limit > ChatHistoryMaxLimit {
		limit = ChatHistoryMaxLimit
	}
	forbidden := apierror.ForbiddenWithDetails("history is only available to the runner of an active turn", "chat_history_forbidden")
	if caller == uuid.Nil {
		return nil, forbidden
	}
	// One query answers both "is this the caller's thread" and "does it hold an
	// active turn", so a foreign or idle thread is indistinguishable.
	var cur struct {
		WorkspaceID   uuid.UUID     `db:"workspace_id"`
		UserMessageID uuid.NullUUID `db:"user_message_id"`
	}
	err := r.db.GetContext(ctx, &cur, `SELECT t.workspace_id, u.user_message_id
		FROM ext_agent_chat_threads t
		JOIN ext_agent_chat_turns u ON u.thread_id = t.id AND u.workspace_id = t.workspace_id
		WHERE t.id = $1 AND t.agent_id = $2 AND t.deleted_at IS NULL
		  AND u.agent_id = $2 AND u.state IN ('claimed', 'streaming') AND u.lease_until > $3
		LIMIT 1`, thread, caller, r.now().UTC())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, forbidden
	}
	if err != nil {
		return nil, err
	}

	var cursor any
	if before != nil {
		cursor = *before
	}
	type row struct {
		ID        uuid.UUID `db:"id"`
		Role      string    `db:"role"`
		Body      string    `db:"body"`
		Status    string    `db:"status"`
		Truncated bool      `db:"truncated"`
		CreatedAt time.Time `db:"created_at"`
	}
	var rows []row
	if err := r.db.SelectContext(ctx, &rows, `SELECT m.id, m.role, m.body, m.status, m.truncated, m.created_at
		FROM ext_agent_chat_messages m
		WHERE m.thread_id = $1 AND m.workspace_id = $2
		  AND m.role IN ('user', 'agent')
		  AND m.status IN ('done', 'failed', 'cancelled')
		  AND ($3::uuid IS NULL OR m.id <> $3::uuid)
		  AND NOT (m.status IN ('failed', 'cancelled') AND m.body = '')
		  AND ($4::uuid IS NULL OR (m.created_at, m.id) <
		       (SELECT b.created_at, b.id FROM ext_agent_chat_messages b WHERE b.id = $4::uuid AND b.thread_id = $1))
		ORDER BY m.created_at DESC, m.id DESC LIMIT $5`,
		thread, cur.WorkspaceID, cur.UserMessageID, cursor, limit+1); err != nil {
		return nil, err
	}
	out := &ChatHistory{ThreadID: thread, Messages: []ChatHistoryMessage{}}
	if len(rows) > limit {
		out.HasMore = true
		rows = rows[:limit]
	}
	ids := make([]string, 0, len(rows))
	for _, m := range rows {
		ids = append(ids, m.ID.String())
	}
	atts := map[uuid.UUID][]ChatHistoryAttachment{}
	if len(ids) > 0 {
		var arows []struct {
			MessageID uuid.UUID `db:"message_id"`
			FileName  string    `db:"file_name"`
			Mime      string    `db:"mime"`
			Size      int64     `db:"size"`
		}
		if err := r.db.SelectContext(ctx, &arows, `SELECT message_id, file_name, mime, size FROM ext_agent_chat_attachments
			WHERE message_id = ANY($1::uuid[]) AND uploaded_at IS NOT NULL ORDER BY created_at, id`, pq.Array(ids)); err != nil {
			return nil, err
		}
		for _, a := range arows {
			atts[a.MessageID] = append(atts[a.MessageID], ChatHistoryAttachment{FileName: a.FileName, Mime: a.Mime, Size: a.Size})
		}
	}
	// rows are newest first; the contract is oldest first.
	for i := len(rows) - 1; i >= 0; i-- {
		m := rows[i]
		a := atts[m.ID]
		if a == nil {
			a = []ChatHistoryAttachment{}
		}
		out.Messages = append(out.Messages, ChatHistoryMessage{
			MessageID: m.ID, Role: m.Role, Body: m.Body, CreatedAt: m.CreatedAt.UTC(),
			Status: m.Status, Truncated: m.Truncated, Attachments: a,
		})
	}
	return out, nil
}
