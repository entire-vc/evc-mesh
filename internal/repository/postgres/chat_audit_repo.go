package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Read-only audit of Chat with Agent for QA (E6 setup). Everything
// here is a SELECT; no row, token or lease is touched, and no message body,
// attachment key/name or tool label is returned: ids, states, deadlines and
// counts only.
//
// Access: a signed-in human who is owner or admin of the workspace in the
// path. A member, a viewer, an agent key, an OAuth connector or a chat-session
// token is refused with 403, and so is an admin of another workspace — the
// workspace in the path is the only tenant a query can reach, every statement
// carries it as an explicit predicate.

const (
	chatAuditTurnLimit    = 50
	chatAuditMessageLimit = 200
	chatAuditEventLimit   = 500
	// ChatAuditMemoryMaxLimit caps one page of memory-write rows.
	ChatAuditMemoryMaxLimit = 200
)

type ChatAuditRepo struct{ db *sqlx.DB }

func NewChatAuditRepo(db *sqlx.DB) *ChatAuditRepo { return &ChatAuditRepo{db: db} }

// authorize returns nil for a human owner/admin of ws.
func (r *ChatAuditRepo) authorize(ctx context.Context, actor UserRuntimeActor, ws uuid.UUID) error {
	if actor.Connector || actor.AgentID != uuid.Nil || actor.UserID == uuid.Nil {
		return apierror.ForbiddenWithDetails("chat audit is for signed-in workspace admins", "user_only")
	}
	// Same rule as the workspace middleware: the owner_id of the workspace counts
	// as owner even when no workspace_members row exists.
	var role string
	err := r.db.GetContext(ctx, &role, `SELECT CASE WHEN w.owner_id = $2 THEN 'owner' ELSE COALESCE(m.role::text, '') END
		FROM workspaces w
		LEFT JOIN workspace_members m ON m.workspace_id = w.id AND m.user_id = $2
		WHERE w.id = $1`, ws, actor.UserID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && role != "owner" && role != "admin") {
		return apierror.ForbiddenWithDetails("chat audit needs workspace admin", "admin_only")
	}
	return err
}

// ChatAuditTurn is one turn with the fields that explain where it is stuck.
// State is the turn lifecycle (turn.state), distinct from the message status
// and from the event kind.
type ChatAuditTurn struct {
	ID              uuid.UUID  `json:"id" db:"id"`
	State           string     `json:"state" db:"state"`
	RunnerID        *uuid.UUID `json:"runner_id" db:"runner_id"`
	Attempt         int        `json:"attempt" db:"attempt"`
	ExecutionEpoch  *int64     `json:"execution_epoch" db:"execution_epoch"`
	SessionID       *string    `json:"session_id" db:"session_id"`
	LeaseUntil      *time.Time `json:"lease_until" db:"lease_until"`
	LeaseLive       bool       `json:"lease_live" db:"lease_live"`
	ExpiresAt       time.Time  `json:"expires_at" db:"expires_at"`
	FirstClaimedAt  *time.Time `json:"first_claimed_at" db:"first_claimed_at"`
	StartedAt       *time.Time `json:"started_at" db:"started_at"`
	FinishedAt      *time.Time `json:"finished_at" db:"finished_at"`
	CancelRequested bool       `json:"cancel_requested" db:"cancel_requested"`
	CancelAt        *time.Time `json:"cancel_requested_at" db:"cancel_requested_at"`
	StopReason      *string    `json:"stop_reason" db:"stop_reason"`
	ErrorCode       *string    `json:"error_code" db:"error_code"`
	LastSeq         int        `json:"last_seq" db:"last_seq"`
	TerminalSeq     *int       `json:"terminal_seq" db:"terminal_seq"`
	Model           *string    `json:"model" db:"model"`
	CreatedAt       time.Time  `json:"created_at" db:"created_at"`
}

// ChatAuditMessage is message metadata. Status is message.status; the body is
// never read, only its length.
type ChatAuditMessage struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	Role       string     `json:"role" db:"role"`
	Status     string     `json:"status" db:"status"`
	TurnID     *uuid.UUID `json:"turn_id" db:"turn_id"`
	ErrorCode  *string    `json:"error_code" db:"error_code"`
	Truncated  bool       `json:"truncated" db:"truncated"`
	BodyLength int        `json:"body_length" db:"body_length"`
	CreatedAt  time.Time  `json:"created_at" db:"created_at"`
}

// ChatAuditCommand is a stop_session / delete_transcript order and its ack.
type ChatAuditCommand struct {
	ID        uuid.UUID  `json:"id" db:"id"`
	Kind      string     `json:"kind" db:"kind"`
	RunnerID  *uuid.UUID `json:"runner_id" db:"runner_id"`
	SessionID *string    `json:"session_id" db:"session_id"`
	State     string     `json:"state" db:"state"`
	Attempts  int        `json:"attempts" db:"attempts"`
	CreatedAt time.Time  `json:"created_at" db:"created_at"`
	ExpiresAt time.Time  `json:"expires_at" db:"expires_at"`
}

// ChatAuditSessionToken is a chat-session token row, without the hash.
type ChatAuditSessionToken struct {
	ChatSessionID  uuid.UUID  `json:"chat_session_id" db:"chat_session_id"`
	RunnerID       string     `json:"runner_id" db:"runner_id"`
	ExecutionEpoch int64      `json:"execution_epoch" db:"execution_epoch"`
	IssuedAt       time.Time  `json:"issued_at" db:"issued_at"`
	ExpiresAt      time.Time  `json:"expires_at" db:"expires_at"`
	RevokedAt      *time.Time `json:"revoked_at" db:"revoked_at"`
	RevokedReason  *string    `json:"revoked_reason" db:"revoked_reason"`
	Live           bool       `json:"live" db:"live"`
}

// ChatAuditKindCount is how many tool events of one kind a thread has.
type ChatAuditKindCount struct {
	Kind  string `json:"kind" db:"kind"`
	Count int    `json:"count" db:"count"`
}

// ChatThreadAudit is GET /workspaces/{ws}/chat/audit/threads/{thread}.
type ChatThreadAudit struct {
	ThreadID       uuid.UUID               `json:"thread_id"`
	ProjectID      uuid.UUID               `json:"project_id"`
	AgentID        uuid.UUID               `json:"agent_id"`
	UserID         uuid.UUID               `json:"user_id"`
	ExecutionEpoch int64                   `json:"execution_epoch"`
	Deleted        bool                    `json:"deleted"`
	Runtime        map[string]any          `json:"runtime"`
	LiveSessions   int                     `json:"agent_live_sessions"`
	Turns          []ChatAuditTurn         `json:"turns"`
	Messages       []ChatAuditMessage      `json:"messages"`
	EventKinds     []ChatAuditKindCount    `json:"event_kinds"`
	Commands       []ChatAuditCommand      `json:"commands"`
	SessionTokens  []ChatAuditSessionToken `json:"session_tokens"`
	StoppedSession []uuid.UUID             `json:"stopped_sessions"`
}

// runtimeAuditKeys are the only keys of thread.runtime that leave the server:
// process addressing, nothing that carries content.
var runtimeAuditKeys = []string{"runner_id", "harness", "profile", "session_id"}

type chatThreadHead struct {
	ProjectID uuid.UUID       `db:"project_id"`
	AgentID   uuid.UUID       `db:"agent_id"`
	UserID    uuid.UUID       `db:"user_id"`
	Epoch     int64           `db:"execution_epoch"`
	Deleted   bool            `db:"deleted"`
	Runtime   json.RawMessage `db:"runtime"`
}

// ThreadAudit returns the process/session/stop/expiry picture of one thread.
func (r *ChatAuditRepo) ThreadAudit(ctx context.Context, actor UserRuntimeActor, ws, thread uuid.UUID) (*ChatThreadAudit, error) {
	if err := r.authorize(ctx, actor, ws); err != nil {
		return nil, err
	}
	var head chatThreadHead
	err := r.db.GetContext(ctx, &head, `SELECT project_id, agent_id, user_id, execution_epoch,
			(deleted_at IS NOT NULL) AS deleted, runtime
		FROM ext_agent_chat_threads WHERE id = $1 AND workspace_id = $2`, thread, ws)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apierror.NotFound("chat thread")
	}
	if err != nil {
		return nil, err
	}
	out := &ChatThreadAudit{ThreadID: thread, ProjectID: head.ProjectID, AgentID: head.AgentID, UserID: head.UserID,
		ExecutionEpoch: head.Epoch, Deleted: head.Deleted, Runtime: pickRuntime(head.Runtime),
		Turns: []ChatAuditTurn{}, Messages: []ChatAuditMessage{}, EventKinds: []ChatAuditKindCount{},
		Commands: []ChatAuditCommand{}, SessionTokens: []ChatAuditSessionToken{}, StoppedSession: []uuid.UUID{}}

	if err := r.db.SelectContext(ctx, &out.Turns, `SELECT id, state, runner_id, attempt, execution_epoch, session_id,
			lease_until, COALESCE(lease_until > now(), false) AS lease_live, expires_at, first_claimed_at,
			started_at, finished_at, cancel_requested, cancel_requested_at, stop_reason, error_code,
			last_seq, terminal_seq, model, created_at
		FROM ext_agent_chat_turns WHERE thread_id = $1 AND workspace_id = $2
		ORDER BY created_at DESC, id DESC LIMIT $3`, thread, ws, chatAuditTurnLimit); err != nil {
		return nil, err
	}
	if err := r.db.SelectContext(ctx, &out.Messages, `SELECT id, role, status, turn_id, error_code, truncated,
			char_length(body) AS body_length, created_at
		FROM ext_agent_chat_messages WHERE thread_id = $1 AND workspace_id = $2
		ORDER BY created_at DESC, id DESC LIMIT $3`, thread, ws, chatAuditMessageLimit); err != nil {
		return nil, err
	}
	if err := r.db.SelectContext(ctx, &out.EventKinds, `SELECT te.kind, count(*)::int AS count
		FROM ext_agent_chat_tool_events te
		JOIN ext_agent_chat_turns tr ON tr.id = te.turn_id AND tr.workspace_id = te.workspace_id
		WHERE tr.thread_id = $1 AND tr.workspace_id = $2 GROUP BY te.kind ORDER BY te.kind`, thread, ws); err != nil {
		return nil, err
	}
	if err := r.db.SelectContext(ctx, &out.Commands, `SELECT id, kind, runner_id, session_id, state, attempts, created_at, expires_at
		FROM ext_agent_chat_commands WHERE thread_id = $1 AND workspace_id = $2
		ORDER BY created_at DESC, id DESC LIMIT 50`, thread, ws); err != nil {
		return nil, err
	}
	if err := r.db.SelectContext(ctx, &out.SessionTokens, `SELECT chat_session_id, runner_id, execution_epoch, issued_at, expires_at,
			revoked_at, revoked_reason, (revoked_at IS NULL AND expires_at > now()) AS live
		FROM chat_session_tokens WHERE chat_thread_id = $1 AND workspace_id = $2
		ORDER BY issued_at DESC LIMIT 50`, thread, ws); err != nil {
		return nil, err
	}
	if err := r.db.SelectContext(ctx, &out.StoppedSession, `SELECT st.chat_session_id FROM chat_session_stops st
		WHERE st.chat_session_id IN (SELECT chat_session_id FROM chat_session_tokens
				WHERE chat_thread_id = $1 AND workspace_id = $2)
			-- the runner path uses the turn id as chat_session_id, and a Stop that
			-- arrives before the first token leaves a tombstone with no token row
			OR st.chat_session_id IN (SELECT id FROM ext_agent_chat_turns
				WHERE thread_id = $1 AND workspace_id = $2)
		ORDER BY st.stopped_at DESC`, thread, ws); err != nil {
		return nil, err
	}
	if err := r.db.GetContext(ctx, &out.LiveSessions, `SELECT count(*)::int FROM ext_agent_chat_turns
		WHERE agent_id = $1 AND workspace_id = $2 AND state IN ('claimed', 'streaming')`, head.AgentID, ws); err != nil {
		return nil, err
	}
	return out, nil
}

func pickRuntime(raw json.RawMessage) map[string]any {
	out := map[string]any{}
	var all map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &all) != nil {
		return out
	}
	for _, k := range runtimeAuditKeys {
		if v, ok := all[k]; ok {
			out[k] = v
		}
	}
	return out
}

// ChatAgentSessions is GET /workspaces/{ws}/chat/audit/agents/{agent}/sessions.
type ChatAgentSessions struct {
	AgentID        uuid.UUID `json:"agent_id"`
	MaxConcurrent  int       `json:"max_concurrent_tasks"`
	ClaimedTurns   int       `json:"claimed_turns"`
	StreamingTurns int       `json:"streaming_turns"`
	QueuedTurns    int       `json:"queued_turns"`
	// LeaseLapsed counts claimed|streaming turns whose lease has already run
	// out: sessions the server still counts as live but nobody renews.
	LeaseLapsed    int                `json:"lease_lapsed_turns"`
	LiveTokens     int                `json:"live_session_tokens"`
	Runners        []ChatAuditRunner  `json:"runners"`
	ActiveTurns    []ChatAuditTurnRef `json:"active_turns"`
	NextExpiryAt   *time.Time         `json:"next_queued_expiry_at"`
	NextLeaseEndAt *time.Time         `json:"next_lease_end_at"`
}

// ChatAuditRunner is a registered runner credential of the agent (no hash).
type ChatAuditRunner struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	Name       string     `json:"name" db:"name"`
	CreatedAt  time.Time  `json:"created_at" db:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at" db:"last_seen_at"`
	RevokedAt  *time.Time `json:"revoked_at" db:"revoked_at"`
}

// ChatAuditTurnRef points at a live turn without repeating its detail.
type ChatAuditTurnRef struct {
	TurnID     uuid.UUID  `json:"turn_id" db:"turn_id"`
	ThreadID   uuid.UUID  `json:"thread_id" db:"thread_id"`
	State      string     `json:"state" db:"state"`
	RunnerID   *uuid.UUID `json:"runner_id" db:"runner_id"`
	LeaseUntil *time.Time `json:"lease_until" db:"lease_until"`
}

// AgentSessions counts the live chat sessions of one agent and lists them.
func (r *ChatAuditRepo) AgentSessions(ctx context.Context, actor UserRuntimeActor, ws, agent uuid.UUID) (*ChatAgentSessions, error) {
	if err := r.authorize(ctx, actor, ws); err != nil {
		return nil, err
	}
	out := &ChatAgentSessions{AgentID: agent, Runners: []ChatAuditRunner{}, ActiveTurns: []ChatAuditTurnRef{}}
	err := r.db.GetContext(ctx, &out.MaxConcurrent,
		`SELECT max_concurrent_tasks FROM agents WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, agent, ws)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apierror.NotFound("agent")
	}
	if err != nil {
		return nil, err
	}
	err = r.db.QueryRowxContext(ctx, `SELECT
			count(*) FILTER (WHERE state = 'claimed')::int,
			count(*) FILTER (WHERE state = 'streaming')::int,
			count(*) FILTER (WHERE state = 'queued')::int,
			count(*) FILTER (WHERE state IN ('claimed', 'streaming') AND lease_until <= now())::int,
			min(expires_at) FILTER (WHERE state = 'queued'),
			min(lease_until) FILTER (WHERE state IN ('claimed', 'streaming'))
		FROM ext_agent_chat_turns WHERE agent_id = $1 AND workspace_id = $2
			AND state IN ('queued', 'claimed', 'streaming')`, agent, ws).
		Scan(&out.ClaimedTurns, &out.StreamingTurns, &out.QueuedTurns, &out.LeaseLapsed, &out.NextExpiryAt, &out.NextLeaseEndAt)
	if err != nil {
		return nil, err
	}
	if err := r.db.GetContext(ctx, &out.LiveTokens, `SELECT count(*)::int FROM chat_session_tokens
		WHERE agent_id = $1 AND workspace_id = $2 AND revoked_at IS NULL AND expires_at > now()`, agent, ws); err != nil {
		return nil, err
	}
	if err := r.db.SelectContext(ctx, &out.Runners, `SELECT id, name, created_at, last_seen_at, revoked_at
		FROM ext_agent_chat_runners WHERE agent_id = $1 AND workspace_id = $2 ORDER BY created_at, id`, agent, ws); err != nil {
		return nil, err
	}
	if err := r.db.SelectContext(ctx, &out.ActiveTurns, `SELECT id AS turn_id, thread_id, state, runner_id, lease_until
		FROM ext_agent_chat_turns WHERE agent_id = $1 AND workspace_id = $2 AND state IN ('claimed', 'streaming')
		ORDER BY created_at, id LIMIT 100`, agent, ws); err != nil {
		return nil, err
	}
	return out, nil
}

// ChatMemoryWrite is one memory revision written by an agent inside a chat
// session. Attribution is DERIVED, not recorded: memory_revisions carries only
// the acting agent, so thread/user/session come from the chat-session token of
// that agent that was live at the write time, and the turn from the thread's
// turn that was running then. "attribution" says so on every row; a write the
// same agent made outside chat during an open session window is
// indistinguishable and is listed too.
type ChatMemoryWrite struct {
	RevisionID    uuid.UUID  `json:"revision_id" db:"revision_id"`
	MemoryID      uuid.UUID  `json:"memory_id" db:"memory_id"`
	Version       int        `json:"version" db:"version"`
	Action        string     `json:"action" db:"action"`
	Scope         *string    `json:"scope" db:"scope"`
	ProjectID     *uuid.UUID `json:"project_id" db:"project_id"`
	HasReason     bool       `json:"has_reason" db:"has_reason"`
	ActorAgentID  uuid.UUID  `json:"actor_agent_id" db:"actor_agent_id"`
	WrittenAt     time.Time  `json:"written_at" db:"written_at"`
	ChatThreadID  uuid.UUID  `json:"chat_thread_id" db:"chat_thread_id"`
	ChatSessionID uuid.UUID  `json:"chat_session_id" db:"chat_session_id"`
	UserID        uuid.UUID  `json:"initiated_by_user_id" db:"initiated_by_user_id"`
	TurnID        *uuid.UUID `json:"turn_id" db:"turn_id"`
	Attribution   string     `json:"attribution" db:"-"`
}

// ChatAttributionDerived marks attribution inferred from the session window.
const ChatAttributionDerived = "derived_from_session_window"

// MemoryWrites lists chat-session memory writes, newest first, optionally of one
// thread. Neither memory content nor key is returned: scope, version and ids only.
func (r *ChatAuditRepo) MemoryWrites(ctx context.Context, actor UserRuntimeActor, ws uuid.UUID, thread *uuid.UUID, limit int) ([]ChatMemoryWrite, error) {
	if err := r.authorize(ctx, actor, ws); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > ChatAuditMemoryMaxLimit {
		limit = ChatAuditMemoryMaxLimit
	}
	if thread != nil {
		var exists bool
		if err := r.db.GetContext(ctx, &exists, `SELECT EXISTS (SELECT 1 FROM ext_agent_chat_threads
			WHERE id = $1 AND workspace_id = $2)`, *thread, ws); err != nil {
			return nil, err
		}
		if !exists {
			return nil, apierror.NotFound("chat thread")
		}
	}
	rows := []ChatMemoryWrite{}
	err := r.db.SelectContext(ctx, &rows, `SELECT mr.id AS revision_id, mr.memory_id, mr.version, mr.action,
			m.scope, m.project_id, (mr.reason IS NOT NULL) AS has_reason,
			mr.actor_agent_id, mr.created_at AS written_at,
			s.chat_thread_id, s.chat_session_id, s.user_id AS initiated_by_user_id, t.id AS turn_id
		FROM memory_revisions mr
		LEFT JOIN memories m ON m.id = mr.memory_id AND m.workspace_id = $1
		JOIN LATERAL (SELECT st.chat_thread_id, st.chat_session_id, st.user_id
			FROM chat_session_tokens st
			WHERE st.workspace_id = $1 AND st.agent_id = mr.actor_agent_id
				AND st.issued_at <= mr.created_at
				AND mr.created_at <= LEAST(st.revoked_at, st.expires_at)
			ORDER BY st.issued_at DESC LIMIT 1) s ON true
		LEFT JOIN LATERAL (SELECT tr.id FROM ext_agent_chat_turns tr
			WHERE tr.thread_id = s.chat_thread_id AND tr.workspace_id = $1
				AND tr.started_at <= mr.created_at
				AND (tr.finished_at IS NULL OR mr.created_at <= tr.finished_at)
			ORDER BY tr.started_at DESC LIMIT 1) t ON true
		WHERE mr.workspace_id = $1 AND ($2::uuid IS NULL OR s.chat_thread_id = $2)
		ORDER BY mr.created_at DESC, mr.id DESC LIMIT $3`, ws, thread, limit)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].Attribution = ChatAttributionDerived
	}
	return rows, nil
}

// ChatAuditEvent is one "agent is working" event, without its label text.
type ChatAuditEvent struct {
	TurnID      uuid.UUID `json:"turn_id" db:"turn_id"`
	Seq         int       `json:"seq" db:"seq"`
	Kind        string    `json:"kind" db:"kind"`
	LabelLength int       `json:"label_length" db:"label_length"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
}

// ChatAuditAttachment is attachment metadata, without key or file name.
type ChatAuditAttachment struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	MessageID  *uuid.UUID `json:"message_id" db:"message_id"`
	Mime       string     `json:"mime" db:"mime"`
	Size       int64      `json:"size" db:"size"`
	Uploaded   bool       `json:"uploaded" db:"uploaded"`
	CreatedAt  time.Time  `json:"created_at" db:"created_at"`
	UploadedAt *time.Time `json:"uploaded_at" db:"uploaded_at"`
}

// ChatThreadEvents is GET /workspaces/{ws}/chat/audit/threads/{thread}/events.
type ChatThreadEvents struct {
	ThreadID    uuid.UUID             `json:"thread_id"`
	Events      []ChatAuditEvent      `json:"events"`
	Attachments []ChatAuditAttachment `json:"attachments"`
}

// ThreadEvents lists extension-event metadata of a thread: tool events and
// attachments, no body, label, key or file name.
func (r *ChatAuditRepo) ThreadEvents(ctx context.Context, actor UserRuntimeActor, ws, thread uuid.UUID) (*ChatThreadEvents, error) {
	if err := r.authorize(ctx, actor, ws); err != nil {
		return nil, err
	}
	var exists bool
	if err := r.db.GetContext(ctx, &exists, `SELECT EXISTS (SELECT 1 FROM ext_agent_chat_threads
		WHERE id = $1 AND workspace_id = $2)`, thread, ws); err != nil {
		return nil, err
	}
	if !exists {
		return nil, apierror.NotFound("chat thread")
	}
	out := &ChatThreadEvents{ThreadID: thread, Events: []ChatAuditEvent{}, Attachments: []ChatAuditAttachment{}}
	if err := r.db.SelectContext(ctx, &out.Events, `SELECT te.turn_id, te.seq, te.kind,
			char_length(te.label) AS label_length, te.created_at
		FROM ext_agent_chat_tool_events te
		JOIN ext_agent_chat_turns tr ON tr.id = te.turn_id AND tr.workspace_id = te.workspace_id
		WHERE tr.thread_id = $1 AND tr.workspace_id = $2
		ORDER BY te.created_at, te.turn_id, te.seq LIMIT $3`, thread, ws, chatAuditEventLimit); err != nil {
		return nil, err
	}
	if err := r.db.SelectContext(ctx, &out.Attachments, `SELECT id, message_id, mime, size,
			(uploaded_at IS NOT NULL) AS uploaded, created_at, uploaded_at
		FROM ext_agent_chat_attachments WHERE thread_id = $1 AND workspace_id = $2
		ORDER BY created_at, id LIMIT $3`, thread, ws, chatAuditEventLimit); err != nil {
		return nil, err
	}
	return out, nil
}
