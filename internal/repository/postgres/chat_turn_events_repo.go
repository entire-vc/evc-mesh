package postgres

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Runner protocol §6 / §11 limits and defaults.
const (
	ChatMaxBatchEvents    = 200
	ChatMaxBatchBytes     = 256 << 10
	ChatMessageBodyCap    = 256 << 10
	ChatToolLabelRunes    = 120
	ChatDefaultOfflineTTL = 24 * time.Hour
)

// Turn states (protocol §3).
const (
	ChatTurnQueued    = "queued"
	ChatTurnClaimed   = "claimed"
	ChatTurnStreaming = "streaming"
	ChatTurnDone      = "done"
	ChatTurnFailed    = "failed"
	ChatTurnCancelled = "cancelled"
	ChatTurnExpired   = "expired"
)

// Event types a runner sends.
const (
	ChatEvSession    = "session"
	ChatEvTextDelta  = "text_delta"
	ChatEvToolStatus = "tool_status"
	ChatEvUsage      = "usage"
	ChatEvFinal      = "final"
	ChatEvError      = "error"
)

// ChatTurnEvent is one entry of a runner batch. Fields are shared across types.
type ChatTurnEvent struct {
	Seq  int    `json:"seq"`
	Type string `json:"type"`

	// session
	SessionID string `json:"session_id"`
	Harness   string `json:"harness"`
	Profile   string `json:"profile"`

	// text_delta, final (full reply)
	Text *string `json:"text"`

	// tool_status
	ToolCallID string `json:"tool_call_id"`
	Label      string `json:"label"`
	Status     string `json:"status"`

	// usage, final
	TokensIn  *int           `json:"tokens_in"`
	TokensOut *int           `json:"tokens_out"`
	CostUSD   *float64       `json:"cost_usd"`
	Model     string         `json:"model"`
	Usage     *ChatTurnUsage `json:"usage"`

	// final
	StopReason string `json:"stop_reason"`

	// error
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// ChatTurnUsage is the usage object a final event may carry.
type ChatTurnUsage struct {
	TokensIn  *int     `json:"tokens_in"`
	TokensOut *int     `json:"tokens_out"`
	CostUSD   *float64 `json:"cost_usd"`
	Model     string   `json:"model"`
}

// ChatEventsInput is one authenticated runner call.
type ChatEventsInput struct {
	TurnID      uuid.UUID
	RunnerID    uuid.UUID
	ServesAgent func(agentID uuid.UUID) bool
	ClaimToken  string
	Events      []ChatTurnEvent
}

// ChatIgnoredEvent is an event that was acknowledged but not applied.
type ChatIgnoredEvent struct {
	Seq    int    `json:"seq"`
	Reason string `json:"reason"`
}

// ChatEventsResult is the 200 body of the events endpoint.
type ChatEventsResult struct {
	AppliedSeq      int                `json:"applied_seq"`
	State           string             `json:"state"`
	CancelRequested bool               `json:"cancel_requested"`
	LeaseUntil      *time.Time         `json:"lease_until"`
	ExecutionEpoch  int64              `json:"execution_epoch"`
	Ignored         []ChatIgnoredEvent `json:"ignored"`
}

// ChatMessageRow is the agent message as the WS payload needs it.
type ChatMessageRow struct {
	ID        uuid.UUID  `db:"id"`
	ThreadID  uuid.UUID  `db:"thread_id"`
	Role      string     `db:"role"`
	Body      string     `db:"body"`
	Status    string     `db:"status"`
	Truncated bool       `db:"truncated"`
	TurnID    *uuid.UUID `db:"turn_id"`
	ErrorCode *string    `db:"error_code"`
	CreatedAt time.Time  `db:"created_at"`
}

// ChatToolEffect is one stored tool line to push to the owner.
type ChatToolEffect struct{ Kind, Label string }

// ChatStateEffect is one turn state the owner must hear about.
type ChatStateEffect struct {
	State     string
	ErrorCode *string
}

// ChatEventEffects is what a committed batch obliges the service to publish.
// The repository never touches WS: it only says what became true.
type ChatEventEffects struct {
	WorkspaceID uuid.UUID
	ProjectID   uuid.UUID
	ThreadID    uuid.UUID
	OwnerID     uuid.UUID
	TurnID      uuid.UUID

	// Created is set when this batch inserted the agent message.
	Created *ChatMessageRow
	// Deltas are the stored text fragments, in order, and DeltaOffset the code
	// points the body held before the first of them.
	Deltas      []string
	DeltaOffset int
	// Tools are the stored tool lines, in order.
	Tools []ChatToolEffect
	// Terminal is set when the batch ended the turn; Message is the message row
	// as it stands after the batch (nil when nothing about it changed).
	Terminal *ChatStateEffect
	Message  *ChatMessageRow
}

// ChatTurnEventsRepo applies runner event batches and ends overdue turns.
type ChatTurnEventsRepo struct {
	db  *sqlx.DB
	now func() time.Time
}

func NewChatTurnEventsRepo(db *sqlx.DB) *ChatTurnEventsRepo {
	return &ChatTurnEventsRepo{db: db, now: time.Now}
}

func chatRunnerErr(status int, code, msg string, ctx map[string]any) error {
	return apierror.ChatRunner(status, code, msg, ctx)
}

func errRunnerEventsTurnNotFound() error {
	return chatRunnerErr(http.StatusNotFound, "chat_turn_not_found", "chat turn not found", nil)
}

type runnerTurnRow struct {
	ID              uuid.UUID      `db:"id"`
	WorkspaceID     uuid.UUID      `db:"workspace_id"`
	ThreadID        uuid.UUID      `db:"thread_id"`
	AgentID         uuid.UUID      `db:"agent_id"`
	AgentMessageID  *uuid.UUID     `db:"agent_message_id"`
	State           string         `db:"state"`
	RunnerID        *uuid.UUID     `db:"runner_id"`
	ClaimToken      sql.NullString `db:"claim_token"`
	ExecutionEpoch  sql.NullInt64  `db:"execution_epoch"`
	LeaseUntil      *time.Time     `db:"lease_until"`
	LastSeq         int            `db:"last_seq"`
	TerminalSeq     *int           `db:"terminal_seq"`
	CancelRequested bool           `db:"cancel_requested"`
	StartedAt       *time.Time     `db:"started_at"`
	FirstClaimedAt  *time.Time     `db:"first_claimed_at"`
	OwnerID         uuid.UUID      `db:"owner_id"`
	ProjectID       uuid.UUID      `db:"project_id"`
}

func isTerminalChatState(s string) bool {
	switch s {
	case ChatTurnDone, ChatTurnFailed, ChatTurnCancelled, ChatTurnExpired:
		return true
	}
	return false
}

// ApplyEvents is POST /runner/chat/turns/:id/events. The order of checks is the
// protocol's: turn visible to this runner -> claim holder -> claim token ->
// idempotent replay -> turn state -> payload (gap).
func (r *ChatTurnEventsRepo) ApplyEvents(ctx context.Context, in ChatEventsInput) (*ChatEventsResult, *ChatEventEffects, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var t runnerTurnRow
	err = tx.GetContext(ctx, &t, `SELECT t.id, t.workspace_id, t.thread_id, t.agent_id, t.agent_message_id,
			t.state, t.runner_id, t.claim_token, t.execution_epoch, t.lease_until, t.last_seq, t.terminal_seq,
			t.cancel_requested, t.started_at, t.first_claimed_at, th.user_id AS owner_id, th.project_id
		FROM ext_agent_chat_turns t
		JOIN ext_agent_chat_threads th ON th.id = t.thread_id AND th.workspace_id = t.workspace_id
		WHERE t.id = $1 FOR UPDATE OF t`, in.TurnID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, errRunnerEventsTurnNotFound()
	}
	if err != nil {
		return nil, nil, err
	}
	// Foreign and unknown turns answer the same (N14): no existence leak.
	if in.ServesAgent != nil && !in.ServesAgent(t.AgentID) {
		return nil, nil, errRunnerEventsTurnNotFound()
	}
	if t.RunnerID != nil && *t.RunnerID != in.RunnerID {
		return nil, nil, chatRunnerErr(http.StatusForbidden, "chat_not_claim_holder", "the turn is held by another runner", nil)
	}
	if !t.ClaimToken.Valid || t.ClaimToken.String == "" || in.ClaimToken == "" ||
		subtle.ConstantTimeCompare([]byte(in.ClaimToken), []byte(t.ClaimToken.String)) != 1 {
		return nil, nil, chatRunnerErr(http.StatusConflict, "chat_stale_claim", "the claim token is not the turn's current one", nil)
	}

	now := r.now().UTC()
	res := &ChatEventsResult{State: t.State, CancelRequested: t.CancelRequested, AppliedSeq: t.LastSeq,
		LeaseUntil: t.LeaseUntil, ExecutionEpoch: t.ExecutionEpoch.Int64, Ignored: []ChatIgnoredEvent{}}

	// Idempotent replay beats the terminal check (N10).
	var fresh []ChatTurnEvent
	for _, e := range in.Events {
		if e.Seq > t.LastSeq {
			fresh = append(fresh, e)
		}
	}
	if len(fresh) == 0 {
		return res, nil, nil
	}

	if isTerminalChatState(t.State) {
		ctx := map[string]any{"state": t.State}
		if t.TerminalSeq != nil {
			ctx["terminal_seq"] = *t.TerminalSeq
		}
		return nil, nil, chatRunnerErr(http.StatusConflict, "chat_turn_terminal", "the turn is already finished", ctx)
	}
	if t.State == ChatTurnQueued {
		// A queued turn holds no valid claim: the token matched a previous attempt.
		return nil, nil, chatRunnerErr(http.StatusConflict, "chat_stale_claim", "the turn is back in the queue", nil)
	}
	if fresh[0].Seq != t.LastSeq+1 {
		return nil, nil, chatRunnerErr(http.StatusConflict, "chat_seq_gap", "events are missing before this batch",
			map[string]any{"expected_seq": t.LastSeq + 1})
	}
	if t.State == ChatTurnClaimed && !claimedBatchAllowed(fresh) {
		return nil, nil, chatRunnerErr(http.StatusConflict, "chat_not_started", "the turn has not been started", nil)
	}

	a := &batchApplier{r: r, tx: tx, t: &t, now: now, res: res,
		fx: &ChatEventEffects{WorkspaceID: t.WorkspaceID, ProjectID: t.ProjectID, ThreadID: t.ThreadID,
			OwnerID: t.OwnerID, TurnID: t.ID}}
	if err := a.run(ctx, fresh); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return res, a.fx, nil
}

// claimedBatchAllowed: on a claimed turn only a lone error event, or a lone
// final(cancelled) at seq 1, is accepted (protocol §6, §7).
func claimedBatchAllowed(ev []ChatTurnEvent) bool {
	if len(ev) != 1 {
		return false
	}
	e := ev[0]
	if e.Type == ChatEvError {
		return true
	}
	return e.Type == ChatEvFinal && e.StopReason == "cancelled" && e.Seq == 1
}

type batchApplier struct {
	r   *ChatTurnEventsRepo
	tx  *sqlx.Tx
	t   *runnerTurnRow
	now time.Time
	res *ChatEventsResult
	fx  *ChatEventEffects

	msg       *ChatMessageRow
	body      string
	bodyDirty bool

	usage struct {
		set     bool
		in, out *int
		cost    *float64
		model   string
	}
	runtime map[string]string
}

func (a *batchApplier) run(ctx context.Context, fresh []ChatTurnEvent) error {
	// N4: a late but live runner is not punished, but the permission it held
	// before the lease ran out must stay dead.
	if a.t.LeaseUntil != nil && a.t.LeaseUntil.Before(a.now) {
		var epoch int64
		if err := a.tx.GetContext(ctx, &epoch, `UPDATE ext_agent_chat_threads SET execution_epoch = execution_epoch + 1
			WHERE id = $1 RETURNING execution_epoch`, a.t.ThreadID); err != nil {
			return err
		}
		a.t.ExecutionEpoch = sql.NullInt64{Int64: epoch, Valid: true}
		a.res.ExecutionEpoch = epoch
		if _, err := a.tx.ExecContext(ctx, `UPDATE ext_agent_chat_turns SET execution_epoch = $2 WHERE id = $1`, a.t.ID, epoch); err != nil {
			return err
		}
	}

	last := a.t.LastSeq
	terminal := false
	for _, e := range fresh {
		if terminal {
			a.res.Ignored = append(a.res.Ignored, ChatIgnoredEvent{Seq: e.Seq, Reason: "after_terminal"})
			continue
		}
		// Seqs inside a batch must keep counting by one; the first was checked.
		if e.Seq != last+1 {
			return chatRunnerErr(http.StatusConflict, "chat_seq_gap", "events are missing inside the batch",
				map[string]any{"expected_seq": last + 1})
		}
		var err error
		terminal, err = a.apply(ctx, e)
		if err != nil {
			return err
		}
		last = e.Seq
	}
	a.res.AppliedSeq = last
	return a.flush(ctx, last, terminal)
}

func (a *batchApplier) message(ctx context.Context) (*ChatMessageRow, error) {
	if a.msg != nil {
		return a.msg, nil
	}
	m := &ChatMessageRow{}
	if a.t.AgentMessageID != nil {
		if err := a.tx.GetContext(ctx, m, `SELECT id, thread_id, role, body, status, truncated, turn_id, error_code, created_at
			FROM ext_agent_chat_messages WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, *a.t.AgentMessageID, a.t.WorkspaceID); err != nil {
			return nil, err
		}
	} else {
		turn := a.t.ID
		if err := a.tx.GetContext(ctx, m, `INSERT INTO ext_agent_chat_messages (workspace_id, thread_id, role, status, turn_id)
			VALUES ($1, $2, 'agent', 'streaming', $3)
			RETURNING id, thread_id, role, body, status, truncated, turn_id, error_code, created_at`,
			a.t.WorkspaceID, a.t.ThreadID, turn); err != nil {
			return nil, err
		}
		if _, err := a.tx.ExecContext(ctx, `UPDATE ext_agent_chat_turns SET agent_message_id = $2 WHERE id = $1`, a.t.ID, m.ID); err != nil {
			return nil, err
		}
		a.t.AgentMessageID = &m.ID
		created := *m
		a.fx.Created = &created
	}
	a.msg, a.body = m, m.Body
	return m, nil
}

// apply handles one event and reports whether it ended the turn.
func (a *batchApplier) apply(ctx context.Context, e ChatTurnEvent) (bool, error) {
	switch e.Type {
	case ChatEvSession:
		if a.runtime == nil {
			a.runtime = map[string]string{}
		}
		a.runtime["session_id"], a.runtime["harness"], a.runtime["profile"] = e.SessionID, e.Harness, e.Profile
	case ChatEvTextDelta:
		if e.Text == nil || *e.Text == "" {
			return false, nil
		}
		m, err := a.message(ctx)
		if err != nil {
			return false, err
		}
		if len(a.fx.Deltas) == 0 {
			a.fx.DeltaOffset = utf8.RuneCountInString(a.body)
		}
		if stored := a.appendBody(m, *e.Text); stored != "" {
			a.fx.Deltas = append(a.fx.Deltas, stored)
		}
	case ChatEvToolStatus:
		switch e.Status {
		case "started", "finished", "failed":
		default:
			a.res.Ignored = append(a.res.Ignored, ChatIgnoredEvent{Seq: e.Seq, Reason: "invalid_tool_status"})
			return false, nil
		}
		label := e.Label
		if utf8.RuneCountInString(label) > ChatToolLabelRunes {
			label = string([]rune(label)[:ChatToolLabelRunes])
		}
		if _, err := a.tx.ExecContext(ctx, `INSERT INTO ext_agent_chat_tool_events (turn_id, seq, workspace_id, kind, label)
			VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`, a.t.ID, e.Seq, a.t.WorkspaceID, e.Status, label); err != nil {
			return false, err
		}
		a.fx.Tools = append(a.fx.Tools, ChatToolEffect{Kind: e.Status, Label: label})
	case ChatEvUsage:
		a.setUsage(e.TokensIn, e.TokensOut, e.CostUSD, e.Model)
	case ChatEvFinal:
		return true, a.final(ctx, e)
	case ChatEvError:
		return true, a.fail(ctx, e)
	default:
		a.res.Ignored = append(a.res.Ignored, ChatIgnoredEvent{Seq: e.Seq, Reason: "unknown_type"})
	}
	return false, nil
}

func (a *batchApplier) setUsage(in, out *int, cost *float64, model string) {
	// Only accepted (non-negative) values count; the rest keep what earlier batches stored.
	if in != nil && *in >= 0 {
		a.usage.in, a.usage.set = in, true
	}
	if out != nil && *out >= 0 {
		a.usage.out, a.usage.set = out, true
	}
	if cost != nil && *cost >= 0 {
		a.usage.cost, a.usage.set = cost, true
	}
	if model != "" {
		a.usage.model = model
	}
}

// appendBody adds text under the body cap (256 KiB, cut on a rune boundary) and
// returns what was actually kept, so the owner's view matches the stored body.
func (a *batchApplier) appendBody(m *ChatMessageRow, text string) string {
	if m.Truncated {
		return ""
	}
	room := ChatMessageBodyCap - len(a.body)
	if len(text) > room {
		text = cutUTF8(text, room)
		m.Truncated = true
	}
	a.body += text
	a.bodyDirty = true
	return text
}

func cutUTF8(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}

func (a *batchApplier) final(ctx context.Context, e ChatTurnEvent) error {
	switch e.StopReason {
	case "end_turn", "cancelled", "max_tokens":
	default:
		return chatRunnerErr(http.StatusBadRequest, "bad_request", "unknown stop_reason", nil)
	}
	m, err := a.message(ctx)
	if err != nil {
		return err
	}
	if e.Text != nil {
		// The full reply replaces what accumulated: it repairs a lost delta.
		m.Truncated = false
		a.body = ""
		a.appendBody(m, *e.Text)
		a.bodyDirty = true
	}
	// Usage rides either in the nested object or in the event's own fields.
	if u := e.Usage; u != nil {
		a.setUsage(u.TokensIn, u.TokensOut, u.CostUSD, u.Model)
	}
	a.setUsage(e.TokensIn, e.TokensOut, e.CostUSD, e.Model)
	state, msgState := ChatTurnDone, "done"
	if e.StopReason == "cancelled" {
		state, msgState = ChatTurnCancelled, "cancelled"
	}
	m.Status = msgState
	return a.terminate(ctx, e, state, e.StopReason, nil)
}

func (a *batchApplier) fail(ctx context.Context, e ChatTurnEvent) error {
	m, err := a.message(ctx)
	if err != nil {
		return err
	}
	code := strings.TrimSpace(e.Code)
	if code == "" {
		code = "runner_error"
	}
	m.Status, m.ErrorCode = "failed", &code
	return a.terminate(ctx, e, ChatTurnFailed, "", &code)
}

// terminate writes the guarded terminal transition. The guard is the state read
// under FOR UPDATE; a concurrent Mesh-side transition lost the row lock race and
// the loser sees a terminal turn on its own read (409 chat_turn_terminal).
func (a *batchApplier) terminate(ctx context.Context, e ChatTurnEvent, state, stopReason string, errCode *string) error {
	var stop *string
	if stopReason != "" {
		stop = &stopReason
	}
	if _, err := a.tx.ExecContext(ctx, `UPDATE ext_agent_chat_turns
		SET state = $2, terminal_seq = $3, finished_at = $4, lease_until = NULL, stop_reason = $5, error_code = $6
		WHERE id = $1 AND state IN ('claimed', 'streaming')`, a.t.ID, state, e.Seq, a.now, stop, errCode); err != nil {
		return err
	}
	if state == ChatTurnDone {
		// The agent answered, so the user message was delivered: it leaves queued.
		if _, err := a.tx.ExecContext(ctx, `UPDATE ext_agent_chat_messages SET status = 'done'
			WHERE workspace_id = $2 AND status = 'queued'
			  AND id = (SELECT user_message_id FROM ext_agent_chat_turns WHERE id = $1)`, a.t.ID, a.t.WorkspaceID); err != nil {
			return err
		}
	}
	a.res.State, a.res.LeaseUntil = state, nil
	a.fx.Terminal = &ChatStateEffect{State: state, ErrorCode: errCode}
	return nil
}

// flush writes the batch's accumulated state in one pass.
func (a *batchApplier) flush(ctx context.Context, last int, terminal bool) error {
	if a.msg != nil && (a.bodyDirty || terminal) {
		if _, err := a.tx.ExecContext(ctx, `UPDATE ext_agent_chat_messages SET body = $2, truncated = $3, status = $4, error_code = $5
			WHERE id = $1 AND workspace_id = $6`, a.msg.ID, a.body, a.msg.Truncated, a.msg.Status, a.msg.ErrorCode, a.t.WorkspaceID); err != nil {
			return err
		}
		a.msg.Body = a.body
		final := *a.msg
		a.fx.Message = &final
	}
	if a.usage.set {
		if _, err := a.tx.ExecContext(ctx, `UPDATE ext_agent_chat_turns SET tokens_in = COALESCE($2, tokens_in), tokens_out = COALESCE($3, tokens_out),
			cost_usd = COALESCE($4, cost_usd), model = COALESCE(NULLIF($5, ''), model) WHERE id = $1`,
			a.t.ID, a.usage.in, a.usage.out, a.usage.cost, a.usage.model); err != nil {
			return err
		}
	}
	if len(a.runtime) > 0 {
		if _, err := a.tx.ExecContext(ctx, `UPDATE ext_agent_chat_threads
			SET runtime = runtime || jsonb_build_object('runner_id', $2::text, 'session_id', $3::text, 'harness', $4::text, 'profile', $5::text)
			WHERE id = $1`, a.t.ThreadID, a.t.RunnerID, a.runtime["session_id"], a.runtime["harness"], a.runtime["profile"]); err != nil {
			return err
		}
		if _, err := a.tx.ExecContext(ctx, `UPDATE ext_agent_chat_turns SET session_id = NULLIF($2, '') WHERE id = $1`,
			a.t.ID, a.runtime["session_id"]); err != nil {
			return err
		}
	}
	if terminal {
		if _, err := a.tx.ExecContext(ctx, `UPDATE ext_agent_chat_threads SET last_message_at = $2 WHERE id = $1`, a.t.ThreadID, a.now); err != nil {
			return err
		}
		if err := a.sessionReport(ctx); err != nil {
			return err
		}
	} else {
		lease := a.now.Add(ChatLeaseSeconds * time.Second)
		a.res.LeaseUntil = &lease
	}
	var lease *time.Time
	if !terminal {
		lease = a.res.LeaseUntil
	}
	_, err := a.tx.ExecContext(ctx, `UPDATE ext_agent_chat_turns SET last_seq = $2, lease_until = CASE WHEN $4::boolean THEN NULL ELSE $3::timestamptz END
		WHERE id = $1`, a.t.ID, last, lease, terminal)
	return err
}

// sessionReport records the turn's cost and tokens as an agent session tied to
// the chat thread, so chat spend shows up next to every other agent's. It reads
// the totals back from the turn row, where usage of earlier batches already is,
// so a final that does not repeat usage still reports it.
func (a *batchApplier) sessionReport(ctx context.Context) error {
	_, err := a.tx.ExecContext(ctx, `INSERT INTO agent_sessions
		(workspace_id, agent_id, started_at, ended_at, last_activity_at, status, model_used, tokens_in, tokens_out, estimated_cost, chat_thread_id)
		SELECT workspace_id, agent_id, COALESCE(started_at, first_claimed_at, $2), $2, $2, 'ended',
		       COALESCE(model, ''), COALESCE(tokens_in, 0), COALESCE(tokens_out, 0), COALESCE(cost_usd, 0), thread_id
		FROM ext_agent_chat_turns
		WHERE id = $1 AND (tokens_in IS NOT NULL OR tokens_out IS NOT NULL OR cost_usd IS NOT NULL)`, a.t.ID, a.now)
	return err
}

// LockTurn serialises everything that publishes for one turn across API
// replicas: the advisory lock is held by the caller from before the batch is
// applied until its events are published, so a slow replica cannot be overtaken.
func (r *ChatTurnEventsRepo) LockTurn(ctx context.Context, turn uuid.UUID) (unlock func(), err error) {
	conn, err := r.db.Connx(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, "chat-turn:"+turn.String()); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return func() {
		// Background: the request context may already be gone, and the lock must not outlive the call.
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, "chat-turn:"+turn.String())
		_ = conn.Close()
	}, nil
}

// HoldsClaim reports whether runner and claim token match the turn's current
// claim, without taking any lock. The service uses it to charge the per-turn rate
// budget only to the holder, so a caller without the claim cannot spend it.
func (r *ChatTurnEventsRepo) HoldsClaim(ctx context.Context, turn uuid.UUID, runner, token string) bool {
	var held sql.NullString
	var holder *string
	if err := r.db.QueryRowContext(ctx, `SELECT claim_token, runner_id::text FROM ext_agent_chat_turns WHERE id = $1`, turn).Scan(&held, &holder); err != nil {
		return false
	}
	if holder != nil && *holder != runner {
		return false
	}
	return held.Valid && held.String != "" && token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(held.String)) == 1
}
