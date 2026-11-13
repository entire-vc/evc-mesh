package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// SessionRepo implements persistent storage for agent sessions.
type SessionRepo struct {
	db *sqlx.DB
}

// NewSessionRepo creates a new SessionRepo.
func NewSessionRepo(db *sqlx.DB) *SessionRepo {
	return &SessionRepo{db: db}
}

// sessionRow is the DB row representation for agent_sessions.
// tasks_touched is stored as UUID[] — scanned via pq.GenericArray with a uuid.UUID slice target.
type sessionRow struct {
	ID               uuid.UUID                 `db:"id"`
	WorkspaceID      uuid.UUID                 `db:"workspace_id"`
	AgentID          uuid.UUID                 `db:"agent_id"`
	TaskID           *uuid.UUID                `db:"task_id"`
	StartedAt        time.Time                 `db:"started_at"`
	EndedAt          *time.Time                `db:"ended_at"`
	LastActivityAt   time.Time                 `db:"last_activity_at"`
	Status           domain.AgentSessionStatus `db:"status"`
	ToolCalls        int                       `db:"tool_calls"`
	ToolBreakdown    json.RawMessage           `db:"tool_breakdown"`
	TasksTouched     pq.StringArray            `db:"tasks_touched"` // scanned as strings, converted to []uuid.UUID
	EventsPublished  int                       `db:"events_published"`
	MemoriesCreated  int                       `db:"memories_created"`
	ModelUsed        string                    `db:"model_used"`
	TokensIn         int64                     `db:"tokens_in"`
	TokensOut        int64                     `db:"tokens_out"`
	EstimatedCost    float64                   `db:"estimated_cost"`
	ComplianceScore  float32                   `db:"compliance_score"`
	ComplianceDetail json.RawMessage           `db:"compliance_detail"`

	TokensInKnown    bool `db:"tokens_in_known"`
	TokensOutKnown   bool `db:"tokens_out_known"`
	CostKnown        bool `db:"cost_known"`
	TokensInUnknown  bool `db:"tokens_in_unknown"`
	TokensOutUnknown bool `db:"tokens_out_unknown"`
	CostUnknown      bool `db:"cost_unknown"`
}

func (r *sessionRow) toDomain() domain.AgentSession {
	tasks := make([]uuid.UUID, 0, len(r.TasksTouched))
	for _, s := range r.TasksTouched {
		if id, err := uuid.Parse(s); err == nil {
			tasks = append(tasks, id)
		}
	}

	toolBreakdown := r.ToolBreakdown
	if toolBreakdown == nil {
		toolBreakdown = json.RawMessage(`{}`)
	}
	complianceDetail := r.ComplianceDetail
	if complianceDetail == nil {
		complianceDetail = json.RawMessage(`{}`)
	}

	return domain.AgentSession{
		ID:               r.ID,
		WorkspaceID:      r.WorkspaceID,
		AgentID:          r.AgentID,
		TaskID:           r.TaskID,
		StartedAt:        r.StartedAt,
		EndedAt:          r.EndedAt,
		LastActivityAt:   r.LastActivityAt,
		Status:           r.Status,
		ToolCalls:        r.ToolCalls,
		ToolBreakdown:    toolBreakdown,
		TasksTouched:     tasks,
		EventsPublished:  r.EventsPublished,
		MemoriesCreated:  r.MemoriesCreated,
		ModelUsed:        r.ModelUsed,
		TokensIn:         r.TokensIn,
		TokensOut:        r.TokensOut,
		EstimatedCost:    r.EstimatedCost,
		ComplianceScore:  r.ComplianceScore,
		ComplianceDetail: complianceDetail,
		TokensInKnown:    r.TokensInKnown,
		TokensOutKnown:   r.TokensOutKnown,
		CostKnown:        r.CostKnown,
		TokensInUnknown:  r.TokensInUnknown,
		TokensOutUnknown: r.TokensOutUnknown,
		CostUnknown:      r.CostUnknown,
	}
}

const sessionColumns = `id, workspace_id, agent_id, task_id, started_at, ended_at, last_activity_at, status,
	tool_calls, tool_breakdown, tasks_touched, events_published, memories_created,
	model_used, tokens_in, tokens_out, estimated_cost, compliance_score, compliance_detail,
	tokens_in_known, tokens_out_known, cost_known, tokens_in_unknown, tokens_out_unknown, cost_unknown`

// Create inserts a new agent session row.
func (r *SessionRepo) Create(ctx context.Context, s *domain.AgentSession) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	if s.StartedAt.IsZero() {
		s.StartedAt = time.Now()
	}
	if s.LastActivityAt.IsZero() {
		s.LastActivityAt = s.StartedAt
	}
	if s.Status == "" {
		s.Status = domain.AgentSessionStatusActive
	}

	toolBreakdown := s.ToolBreakdown
	if toolBreakdown == nil {
		toolBreakdown = json.RawMessage(`{}`)
	}
	complianceDetail := s.ComplianceDetail
	if complianceDetail == nil {
		complianceDetail = json.RawMessage(`{}`)
	}

	taskStrs := make([]string, len(s.TasksTouched))
	for i, id := range s.TasksTouched {
		taskStrs[i] = id.String()
	}

	const q = `
		INSERT INTO agent_sessions (
			id, workspace_id, agent_id, task_id, started_at, ended_at, last_activity_at, status,
			tool_calls, tool_breakdown, tasks_touched, events_published, memories_created,
			model_used, tokens_in, tokens_out, estimated_cost, compliance_score, compliance_detail
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, $13,
			$14, $15, $16, $17, $18, $19
		)
	`
	_, err := r.db.ExecContext(ctx, q,
		s.ID, s.WorkspaceID, s.AgentID, s.TaskID, s.StartedAt, s.EndedAt, s.LastActivityAt, s.Status,
		s.ToolCalls, toolBreakdown, pq.Array(taskStrs), s.EventsPublished, s.MemoriesCreated,
		s.ModelUsed, s.TokensIn, s.TokensOut, s.EstimatedCost, s.ComplianceScore, complianceDetail,
	)
	return err
}

// Update persists all mutable fields of an agent session.
// Update persists the mutable fields of an agent session EXCEPT tool_calls and
// tool_breakdown. Those two columns are owned exclusively by
// IncrementToolBreakdown's atomic read-and-add UPDATE (below): a caller here
// always carries a plain copy of whatever ToolCalls/ToolBreakdown looked like
// at the time it fetched the session (GetActiveAgentWide/GetActiveForTask
// never zero them), and if this method wrote that copy back it would silently clobber
// any increment IncrementToolBreakdown applied to the row in between the
// fetch and this call — a classic read-modify-write lost update, and not a
// hypothetical one: ReportSession does exactly that fetch-then-Update() round
// trip on every /agents/me/sessions/report call, concurrently with the
// tool-breakdown flush loop (ToolBreakdownTracker) writing to the same row
// every ~15s. Leaving these two columns out of the SET list is what makes
// that concurrency safe.
//
// last_activity_at IS bumped here (via GREATEST, so a concurrent
// IncrementToolBreakdown's own newer stamp is never rolled back) — a
// session-report is exactly as much "activity" as a tool call, and a
// long-running session must not be closed by EndStale between reports.
func (r *SessionRepo) Update(ctx context.Context, s *domain.AgentSession) error {
	complianceDetail := s.ComplianceDetail
	if complianceDetail == nil {
		complianceDetail = json.RawMessage(`{}`)
	}

	taskStrs := make([]string, len(s.TasksTouched))
	for i, id := range s.TasksTouched {
		taskStrs[i] = id.String()
	}

	const q = `
		UPDATE agent_sessions
		SET ended_at          = $1,
		    status            = $2,
		    tasks_touched     = $3,
		    events_published  = $4,
		    memories_created  = $5,
		    model_used        = $6,
		    tokens_in         = $7,
		    tokens_out        = $8,
		    estimated_cost    = $9,
		    compliance_score  = $10,
		    compliance_detail = $11,
		    task_id           = COALESCE(task_id, $12),
		    last_activity_at  = GREATEST(last_activity_at, now())
		WHERE id = $13
	`
	_, err := r.db.ExecContext(ctx, q,
		s.EndedAt, s.Status,
		pq.Array(taskStrs), s.EventsPublished, s.MemoriesCreated,
		s.ModelUsed, s.TokensIn, s.TokensOut, s.EstimatedCost,
		s.ComplianceScore, complianceDetail,
		s.TaskID,
		s.ID,
	)
	return err
}

// incrementToolBreakdownAgentWide and incrementToolBreakdownTaskScoped are
// the two — and only two — query shapes IncrementToolBreakdown ever runs.
// Both are fixed string constants: no fmt.Sprintf, no "+"-concatenation with
// a variable, nothing built at request time. The per-call variability
// (which tool names, how many, what their counts are) travels entirely
// inside $1, a single jsonb value built in Go and bound as one argument —
// the query text itself never changes shape no matter how many distinct
// tool names appear in a given batch.
//
// This is deliberately stricter than an earlier version of this method that
// assembled the query by concatenating one jsonb_set(...) call per tool name
// (with placeholder NUMBERS, never raw values, computed via strconv.Itoa).
// That version was still parameterized correctly — nothing but placeholders
// ever reached the query text — but it read as "$QUERY = $X + ..." to
// go.lang.security.audit.database.string-formatted-query, which flags any
// dynamically-assembled query string reaching ExecContext regardless of
// what actually produced it, precisely because it can't prove a given
// caller only ever concatenates safe fragments — that's the exact
// assumption a scanner exists to not have to trust. Two constants selected
// by a plain if/else, with every value bound as an argument, is what lets
// the scanner verify the query is safe by construction instead of a human
// asserting it.
//
// `tool_breakdown->>d.key` inside the merge reads the OLD value from the
// SAME row being updated (Postgres evaluates a SET expression against the
// row's pre-update values), so this is still one atomic read-and-add per
// call — Postgres's row lock during the UPDATE serializes concurrent
// increments to the same session instead of losing one to a read-then-write
// race, same guarantee the jsonb_set version had.
const incrementToolBreakdownAgentWide = `
	UPDATE agent_sessions
	SET tool_breakdown = COALESCE(tool_breakdown, '{}'::jsonb) || (
			SELECT jsonb_object_agg(
				d.key,
				COALESCE((tool_breakdown ->> d.key)::bigint, 0) + d.value::bigint
			)
			FROM jsonb_each_text($1::jsonb) AS d(key, value)
		),
	    tool_calls = tool_calls + $2,
	    last_activity_at = now()
	WHERE id = (
		SELECT id FROM agent_sessions
		WHERE agent_id = $3 AND status = 'active' AND task_id IS NULL
		ORDER BY started_at DESC
		LIMIT 1
	)
`

const incrementToolBreakdownTaskScoped = `
	UPDATE agent_sessions
	SET tool_breakdown = COALESCE(tool_breakdown, '{}'::jsonb) || (
			SELECT jsonb_object_agg(
				d.key,
				COALESCE((tool_breakdown ->> d.key)::bigint, 0) + d.value::bigint
			)
			FROM jsonb_each_text($1::jsonb) AS d(key, value)
		),
	    tool_calls = tool_calls + $2,
	    last_activity_at = now()
	WHERE id = (
		SELECT id FROM agent_sessions
		WHERE agent_id = $3 AND status = 'active' AND task_id = $4
		ORDER BY started_at DESC
		LIMIT 1
	)
`

// IncrementToolBreakdown atomically adds each entry in counts onto
// tool_breakdown[tool] and their sum onto tool_calls, for the agent's active
// session (task-scoped when taskID != nil, else agent-wide — see the
// interface doc comment for the precedence this mirrors, and the two
// constants above for the actual queries).
//
// When neither query matches a row (no active session yet for this
// agent/task), one is created seeded with these counts. This is deliberately
// allowed to be the very first thing that creates an agent_sessions row for a
// spawn — ahead of the dispatcher's session_report call at the end of the
// spawn — so a spawn that crashes or times out before ever reporting still
// leaves a populated tool_breakdown behind instead of no row existing at all.
func (r *SessionRepo) IncrementToolBreakdown(ctx context.Context, agentID, workspaceID uuid.UUID, taskID *uuid.UUID, counts map[string]int64) error {
	filtered := make(map[string]int64, len(counts))
	var total int64
	for k, v := range counts {
		if k == "" || v <= 0 {
			continue
		}
		filtered[k] = v
		total += v
	}
	if len(filtered) == 0 {
		return nil
	}
	delta, err := json.Marshal(filtered)
	if err != nil {
		return err
	}

	var (
		q    string
		args []interface{}
	)
	if taskID != nil {
		q = incrementToolBreakdownTaskScoped
		args = []interface{}{delta, total, agentID, *taskID}
	} else {
		q = incrementToolBreakdownAgentWide
		args = []interface{}{delta, total, agentID}
	}

	res, err := r.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}

	// No active session matched — seed a new one with this batch.
	return r.Create(ctx, &domain.AgentSession{
		ID:            uuid.New(),
		WorkspaceID:   workspaceID,
		AgentID:       agentID,
		TaskID:        taskID,
		StartedAt:     time.Now(),
		Status:        domain.AgentSessionStatusActive,
		ToolCalls:     int(total),
		ToolBreakdown: delta,
	})
}

// Arguments: agent-wide $1..$5 = tokens in, tokens out, cost, model, agent; $6..$8 = the three
// unknown flags. Task-scoped: $6 = task, $7..$9 = the unknown flags.
// addUsageAgentWide and addUsageTaskScoped are the two fixed query shapes of
// AddUsage: one UPDATE that adds the report onto the active session row
// inside the database (`tokens_in = tokens_in + $1`), so the sum is taken
// against the row's current values under its row lock, never against a copy
// read earlier. Every value is a bound argument.
const addUsageAgentWide = `
	UPDATE agent_sessions
	SET tokens_in          = tokens_in + $1,
	    tokens_out         = tokens_out + $2,
	    estimated_cost     = estimated_cost + $3,
	    tokens_in_known    = tokens_in_known OR NOT $6::boolean,
	    tokens_out_known   = tokens_out_known OR NOT $7::boolean,
	    cost_known         = cost_known OR NOT $8::boolean,
	    tokens_in_unknown  = tokens_in_unknown OR $6::boolean,
	    tokens_out_unknown = tokens_out_unknown OR $7::boolean,
	    cost_unknown       = cost_unknown OR $8::boolean,
	    model_used       = COALESCE(NULLIF($4, ''), model_used),
	    last_activity_at = GREATEST(last_activity_at, now())
	WHERE status = 'active' AND id = (
		SELECT id FROM agent_sessions
		WHERE agent_id = $5 AND status = 'active' AND task_id IS NULL
		ORDER BY started_at DESC
		LIMIT 1
	)
	RETURNING ` + sessionColumns

const addUsageTaskScoped = `
	UPDATE agent_sessions
	SET tokens_in          = tokens_in + $1,
	    tokens_out         = tokens_out + $2,
	    estimated_cost     = estimated_cost + $3,
	    tokens_in_known    = tokens_in_known OR NOT $7::boolean,
	    tokens_out_known   = tokens_out_known OR NOT $8::boolean,
	    cost_known         = cost_known OR NOT $9::boolean,
	    tokens_in_unknown  = tokens_in_unknown OR $7::boolean,
	    tokens_out_unknown = tokens_out_unknown OR $8::boolean,
	    cost_unknown       = cost_unknown OR $9::boolean,
	    model_used       = COALESCE(NULLIF($4, ''), model_used),
	    last_activity_at = GREATEST(last_activity_at, now())
	WHERE status = 'active' AND id = (
		SELECT id FROM agent_sessions
		WHERE agent_id = $5 AND status = 'active' AND task_id = $6
		ORDER BY started_at DESC
		LIMIT 1
	)
	RETURNING ` + sessionColumns

// AddUsage adds one usage report onto the agent's active session (task-scoped
// when taskID != nil, else agent-wide) and returns the session as it is after
// the addition. The addition is a single UPDATE (see the constants above), so
// concurrent reports sum exactly. When there is no active session the call
// creates one seeded with the report; a transaction-scoped advisory lock keyed
// on agent + task makes that "update, else insert" step run one at a time, so
// two first reports for the same agent and task end up on one session, not two.
func (r *SessionRepo) AddUsage(ctx context.Context, agentID, workspaceID uuid.UUID, taskID *uuid.UUID, tokensIn, tokensOut int64, cost float64, model string) (*domain.AgentSession, error) {
	return r.ReportUsage(ctx, agentID, workspaceID, taskID, domain.SessionUsage{TokensIn: tokensIn, TokensOut: tokensOut, Cost: cost}, model)
}

// ReportUsage is AddUsage for a report that may say a measure is unknown. An unknown measure
// adds nothing and sets the session's *_unknown flag, which no later report clears; a known
// measure (a real 0 included) adds its figure and sets *_known.
func (r *SessionRepo) ReportUsage(ctx context.Context, agentID, workspaceID uuid.UUID, taskID *uuid.UUID, u domain.SessionUsage, model string) (*domain.AgentSession, error) {
	if u.TokensInUnknown {
		u.TokensIn = 0
	}
	if u.TokensOutUnknown {
		u.TokensOut = 0
	}
	if u.CostUnknown {
		u.Cost = 0
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	lockKey := "agent_session_usage:" + agentID.String() + ":"
	if taskID != nil {
		lockKey += taskID.String()
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return nil, err
	}

	var row sessionRow
	if taskID != nil {
		err = tx.GetContext(ctx, &row, addUsageTaskScoped, u.TokensIn, u.TokensOut, u.Cost, model, agentID, *taskID,
			u.TokensInUnknown, u.TokensOutUnknown, u.CostUnknown)
	} else {
		err = tx.GetContext(ctx, &row, addUsageAgentWide, u.TokensIn, u.TokensOut, u.Cost, model, agentID,
			u.TokensInUnknown, u.TokensOutUnknown, u.CostUnknown)
	}
	if errors.Is(err, sql.ErrNoRows) {
		now := time.Now()
		sess := &domain.AgentSession{
			ID: uuid.New(), AgentID: agentID, WorkspaceID: workspaceID, TaskID: taskID,
			StartedAt: now, LastActivityAt: now, Status: domain.AgentSessionStatusActive,
			ModelUsed: model, TokensIn: u.TokensIn, TokensOut: u.TokensOut, EstimatedCost: u.Cost,
			TokensInKnown: !u.TokensInUnknown, TokensOutKnown: !u.TokensOutUnknown, CostKnown: !u.CostUnknown,
			TokensInUnknown: u.TokensInUnknown, TokensOutUnknown: u.TokensOutUnknown, CostUnknown: u.CostUnknown,
			ToolBreakdown: json.RawMessage(`{}`), ComplianceDetail: json.RawMessage(`{}`),
			TasksTouched: []uuid.UUID{},
		}
		const ins = `
			INSERT INTO agent_sessions (id, workspace_id, agent_id, task_id, started_at, last_activity_at, status,
				model_used, tokens_in, tokens_out, estimated_cost,
				tokens_in_known, tokens_out_known, cost_known, tokens_in_unknown, tokens_out_unknown, cost_unknown)
			VALUES ($1, $2, $3, $4, $5, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`
		if _, err = tx.ExecContext(ctx, ins, sess.ID, workspaceID, agentID, taskID, now,
			sess.Status, model, u.TokensIn, u.TokensOut, u.Cost,
			sess.TokensInKnown, sess.TokensOutKnown, sess.CostKnown,
			sess.TokensInUnknown, sess.TokensOutUnknown, sess.CostUnknown); err != nil {
			return nil, err
		}
		if cerr := tx.Commit(); cerr != nil {
			return nil, cerr
		}
		return sess, nil
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	sess := row.toDomain()
	return &sess, nil
}

// GetActiveAgentWide returns the agent's active session that is NOT scoped to
// any task (task_id IS NULL), or nil if none exists. Deliberately filters on
// task_id IS NULL rather than just "the agent's latest active session,
// whatever task it belongs to" — the latter is what this method used to do
// (as plain GetActive), which meant an untagged session_report silently
// accumulated cost onto whichever task's session happened to be most recently
// active instead of getting its own agent-wide row (task ea1b9fb6).
func (r *SessionRepo) GetActiveAgentWide(ctx context.Context, agentID uuid.UUID) (*domain.AgentSession, error) {
	var row sessionRow
	err := r.db.GetContext(ctx, &row,
		`SELECT `+sessionColumns+`
		 FROM agent_sessions
		 WHERE agent_id = $1 AND status = 'active' AND task_id IS NULL
		 ORDER BY started_at DESC
		 LIMIT 1`,
		agentID,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	sess := row.toDomain()
	return &sess, nil
}

// GetActiveForTask returns the agent's active session for a specific task, or nil.
func (r *SessionRepo) GetActiveForTask(ctx context.Context, agentID, taskID uuid.UUID) (*domain.AgentSession, error) {
	var row sessionRow
	err := r.db.GetContext(ctx, &row,
		`SELECT `+sessionColumns+`
		 FROM agent_sessions
		 WHERE agent_id = $1 AND task_id = $2 AND status = 'active'
		 ORDER BY started_at DESC
		 LIMIT 1`,
		agentID, taskID,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	sess := row.toDomain()
	return &sess, nil
}

// GetPreviousStartedAt returns the started_at of the most recent non-active session for
// the agent. Returns nil (no error) when no such session exists.
func (r *SessionRepo) GetPreviousStartedAt(ctx context.Context, agentID uuid.UUID) (*time.Time, error) {
	var t time.Time
	err := r.db.GetContext(ctx, &t,
		`SELECT started_at FROM agent_sessions
		 WHERE agent_id = $1 AND status != 'active'
		 ORDER BY started_at DESC LIMIT 1`,
		agentID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// GetTaskCostSummary aggregates session cost/token metrics for a task and computes
// rework count from activity_log backward-move transitions.
func (r *SessionRepo) GetTaskCostSummary(ctx context.Context, taskID uuid.UUID) (*domain.TaskCostSummary, error) {
	var total sql.NullFloat64
	var tokIn, tokOut sql.NullInt64
	var costInc, inInc, outInc bool
	var count int

	// Known figures only; NULL when no session has one. Incomplete when any session's figure is
	// unknown or was never given. No sessions: SUM is NULL and no flag, answered as 0 below.
	err := r.db.QueryRowContext(ctx, `
		SELECT SUM(estimated_cost) FILTER (WHERE cost_known),
		       SUM(tokens_in)      FILTER (WHERE tokens_in_known),
		       SUM(tokens_out)     FILTER (WHERE tokens_out_known),
		       COALESCE(bool_or(cost_unknown       OR NOT cost_known), false),
		       COALESCE(bool_or(tokens_in_unknown  OR NOT tokens_in_known), false),
		       COALESCE(bool_or(tokens_out_unknown OR NOT tokens_out_known), false),
		       COUNT(*)
		FROM agent_sessions
		WHERE task_id = $1
	`, taskID).Scan(&total, &tokIn, &tokOut, &costInc, &inInc, &outInc, &count)
	if err != nil {
		return nil, err
	}

	var rework int
	err = r.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM activity_log al
		JOIN tasks t ON t.id = al.entity_id
		JOIN task_statuses new_ts ON new_ts.project_id = t.project_id
		    AND new_ts.name = al.changes->'status'->>'new'
		JOIN task_statuses old_ts ON old_ts.project_id = t.project_id
		    AND old_ts.name = al.changes->'status'->>'old'
		WHERE al.entity_id = $1
		    AND al.action = 'task.moved'
		    AND al.changes ? 'status'
		    AND new_ts.category IN ('in_progress', 'triage', 'todo')
		    AND old_ts.category IN ('review', 'done')
	`, taskID).Scan(&rework)
	if err != nil {
		return nil, err
	}

	flag := "unknown"
	switch {
	case rework > 0:
		flag = "rework"
	case count == 1:
		flag = "golden"
	case count > 1:
		flag = "multi-turn"
	}

	sum := &domain.TaskCostSummary{
		CostIncomplete:      costInc,
		TokensInIncomplete:  inInc,
		TokensOutIncomplete: outInc,
		SessionCount:        count,
		ReworkCount:         rework,
		QualityFlag:         flag,
	}
	if count == 0 {
		// No sessions: nothing is unknown, the answer stays a plain 0 as before.
		zeroCost, zeroIn, zeroOut := 0.0, int64(0), int64(0)
		sum.TotalCost, sum.TokensIn, sum.TokensOut = &zeroCost, &zeroIn, &zeroOut
		return sum, nil
	}
	if total.Valid {
		sum.TotalCost = &total.Float64
	}
	if tokIn.Valid {
		sum.TokensIn = &tokIn.Int64
	}
	if tokOut.Valid {
		sum.TokensOut = &tokOut.Int64
	}
	return sum, nil
}

// EndStale marks all active sessions that have had no activity for longer
// than timeout as ended. Returns the number of sessions that were
// terminated.
//
// Filters on last_activity_at, NOT started_at (fixed 2026-09-15, task
// this change): a fiddler lane's session can legitimately stay active for many
// hours across a single long task, and closing it purely for being OLD —
// while tool calls keep landing on it — force-ends still-live work and
// starts an uncosted successor session on the very next tool call (see
// migration 20260915001's comment). Genuinely abandoned sessions (no tool
// call, no session-report) still age out exactly the same as before, just
// measured from their last real activity instead of their birth.
func (r *SessionRepo) EndStale(ctx context.Context, timeout time.Duration) (int, error) {
	cutoff := time.Now().Add(-timeout)
	res, err := r.db.ExecContext(ctx,
		`UPDATE agent_sessions
		 SET status   = 'ended',
		     ended_at = NOW()
		 WHERE status = 'active'
		   AND last_activity_at < $1`,
		cutoff,
	)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}
