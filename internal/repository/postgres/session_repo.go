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
	}
}

const sessionColumns = `id, workspace_id, agent_id, task_id, started_at, ended_at, status,
	tool_calls, tool_breakdown, tasks_touched, events_published, memories_created,
	model_used, tokens_in, tokens_out, estimated_cost, compliance_score, compliance_detail`

// Create inserts a new agent session row.
func (r *SessionRepo) Create(ctx context.Context, s *domain.AgentSession) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	if s.StartedAt.IsZero() {
		s.StartedAt = time.Now()
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
			id, workspace_id, agent_id, task_id, started_at, ended_at, status,
			tool_calls, tool_breakdown, tasks_touched, events_published, memories_created,
			model_used, tokens_in, tokens_out, estimated_cost, compliance_score, compliance_detail
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12,
			$13, $14, $15, $16, $17, $18
		)
	`
	_, err := r.db.ExecContext(ctx, q,
		s.ID, s.WorkspaceID, s.AgentID, s.TaskID, s.StartedAt, s.EndedAt, s.Status,
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
// at the time it fetched the session (GetActive/GetActiveForTask never zero
// them), and if this method wrote that copy back it would silently clobber
// any increment IncrementToolBreakdown applied to the row in between the
// fetch and this call — a classic read-modify-write lost update, and not a
// hypothetical one: ReportSession does exactly that fetch-then-Update() round
// trip on every /agents/me/sessions/report call, concurrently with the
// tool-breakdown flush loop (ToolBreakdownTracker) writing to the same row
// every ~15s. Leaving these two columns out of the SET list is what makes
// that concurrency safe.
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
		    task_id           = COALESCE(task_id, $12)
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
	    tool_calls = tool_calls + $2
	WHERE id = (
		SELECT id FROM agent_sessions
		WHERE agent_id = $3 AND status = 'active'
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
	    tool_calls = tool_calls + $2
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

// GetActive returns the active session for an agent, or nil if none exists.
func (r *SessionRepo) GetActive(ctx context.Context, agentID uuid.UUID) (*domain.AgentSession, error) {
	var row sessionRow
	err := r.db.GetContext(ctx, &row,
		`SELECT `+sessionColumns+`
		 FROM agent_sessions
		 WHERE agent_id = $1 AND status = 'active'
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
	var total float64
	var tokIn, tokOut int64
	var count int

	err := r.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(estimated_cost), 0),
		       COALESCE(SUM(tokens_in), 0),
		       COALESCE(SUM(tokens_out), 0),
		       COUNT(*)
		FROM agent_sessions
		WHERE task_id = $1
	`, taskID).Scan(&total, &tokIn, &tokOut, &count)
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

	return &domain.TaskCostSummary{
		TotalCost:    total,
		TokensIn:     tokIn,
		TokensOut:    tokOut,
		SessionCount: count,
		ReworkCount:  rework,
		QualityFlag:  flag,
	}, nil
}

// EndStale marks all active sessions that have been running longer than timeout as ended.
// Returns the number of sessions that were terminated.
func (r *SessionRepo) EndStale(ctx context.Context, timeout time.Duration) (int, error) {
	cutoff := time.Now().Add(-timeout)
	res, err := r.db.ExecContext(ctx,
		`UPDATE agent_sessions
		 SET status   = 'ended',
		     ended_at = NOW()
		 WHERE status = 'active'
		   AND started_at < $1`,
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
