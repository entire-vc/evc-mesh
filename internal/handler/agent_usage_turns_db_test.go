package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// POST /agents/me/sessions/report writes the usage journal (agent_usage_turns)
// and its hour (agent_usage_hourly) in the session addition's transaction.
// Real Postgres: the hour key, the NULL-not-0 contract and the turn_id
// dedupe live in SQL, a mock cannot prove them.

type usageTurnFixture struct {
	db        *sqlx.DB
	h         *AgentHandler
	wsID      uuid.UUID
	agentID   uuid.UUID
	projectID uuid.UUID
	taskID    uuid.UUID
}

func newUsageTurnFixture(t *testing.T) *usageTurnFixture {
	t.Helper()
	db := reportDB(t)
	ctx := context.Background()
	f := &usageTurnFixture{db: db, wsID: uuid.New(), agentID: uuid.New(), projectID: uuid.New(), taskID: uuid.New()}
	sfx := f.wsID.String()[:8]
	owner := uuid.New()
	_, err := db.ExecContext(ctx, `INSERT INTO workspaces (id, name, slug, owner_id) VALUES ($1,'usage',$2,$3)`, f.wsID, "usage-turn-"+sfx, owner)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix)
		VALUES ($1,$2,'Usage Agent',$3,'not-a-real-hash','test')`, f.agentID, f.wsID, "usage-turn-agent-"+sfx)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1,$2,'Usage',$3)`, f.projectID, f.wsID, "usage-"+sfx)
	require.NoError(t, err)
	statusID := uuid.New()
	_, err = db.ExecContext(ctx, `INSERT INTO task_statuses (id, project_id, name, slug, category)
		VALUES ($1,$2,'Todo','todo','todo')`, statusID, f.projectID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO tasks (id, project_id, status_id, title, task_number, created_by)
		VALUES ($1,$2,$3,'usage task',1,$4)`, f.taskID, f.projectID, statusID, f.agentID)
	require.NoError(t, err)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = db.ExecContext(bg, `DELETE FROM agent_usage_hourly WHERE workspace_id=$1`, f.wsID)
		_, _ = db.ExecContext(bg, `DELETE FROM agent_usage_turns WHERE workspace_id=$1`, f.wsID)
		_, _ = db.ExecContext(bg, `DELETE FROM agent_sessions WHERE agent_id=$1`, f.agentID)
		_, _ = db.ExecContext(bg, `DELETE FROM tasks WHERE id=$1`, f.taskID)
		_, _ = db.ExecContext(bg, `DELETE FROM task_statuses WHERE project_id=$1`, f.projectID)
		_, _ = db.ExecContext(bg, `DELETE FROM projects WHERE id=$1`, f.projectID)
		_, _ = db.ExecContext(bg, `DELETE FROM agents WHERE id=$1`, f.agentID)
		_, _ = db.ExecContext(bg, `DELETE FROM workspaces WHERE id=$1`, f.wsID)
	})
	agentSvc := service.NewAgentService(postgres.NewAgentRepo(db), nil, nil, nil)
	f.h = NewAgentHandlerWithEvents(agentSvc, nil, nil, nil, nil, postgres.NewSessionRepo(db))
	return f
}

func (f *usageTurnFixture) post(t *testing.T, body string) (status int, answer map[string]any) {
	t.Helper()
	rec := postReport(t, f.h, echo.New(), &f.agentID, body)
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &answer))
	}
	return rec.Code, answer
}

type usageTurnRow struct {
	ID           uuid.UUID       `db:"id"`
	TaskID       uuid.NullUUID   `db:"task_id"`
	ProjectID    uuid.NullUUID   `db:"project_id"`
	SessionID    uuid.NullUUID   `db:"session_id"`
	Source       string          `db:"source"`
	TurnRef      sql.NullString  `db:"turn_ref"`
	StartedAt    sql.NullTime    `db:"started_at"`
	EndedAt      sql.NullTime    `db:"ended_at"`
	OccurredAt   time.Time       `db:"occurred_at"`
	Model        string          `db:"model"`
	Provider     string          `db:"provider"`
	AccountRef   sql.NullString  `db:"account_ref"`
	BillingMode  sql.NullString  `db:"billing_mode"`
	TokensIn     sql.NullInt64   `db:"tokens_in"`
	TokensOut    sql.NullInt64   `db:"tokens_out"`
	CacheRead    sql.NullInt64   `db:"cache_read"`
	CacheWrite   sql.NullInt64   `db:"cache_write"`
	CacheWrite1h sql.NullInt64   `db:"cache_write_1h"`
	CostReported sql.NullFloat64 `db:"cost_reported"`
}

func (f *usageTurnFixture) turns(t *testing.T) []usageTurnRow {
	t.Helper()
	var rows []usageTurnRow
	require.NoError(t, f.db.Select(&rows, `SELECT id, task_id, project_id, session_id, source, turn_ref, started_at, ended_at,
		occurred_at, model, provider, account_ref, billing_mode, tokens_in, tokens_out, cache_read, cache_write,
		cache_write_1h, cost_reported::float8 AS cost_reported
		FROM agent_usage_turns WHERE agent_id=$1 ORDER BY created_at, id`, f.agentID))
	return rows
}

type usageHourRow struct {
	Hour                time.Time `db:"hour"`
	ProjectID           uuid.UUID `db:"project_id"`
	Model               string    `db:"model"`
	Provider            string    `db:"provider"`
	AccountRef          string    `db:"account_ref"`
	BillingMode         string    `db:"billing_mode"`
	Turns               int64     `db:"turns"`
	TurnsTokensKnown    int64     `db:"turns_tokens_known"`
	TurnsCacheKnown     int64     `db:"turns_cache_known"`
	TurnsDurationKnown  int64     `db:"turns_duration_known"`
	TurnsCostKnown      int64     `db:"turns_cost_reported_known"`
	TokensIn            int64     `db:"tokens_in"`
	TokensOut           int64     `db:"tokens_out"`
	CacheRead           int64     `db:"cache_read"`
	CacheWrite          int64     `db:"cache_write"`
	CacheWrite1h        int64     `db:"cache_write_1h"`
	CostReported        float64   `db:"cost_reported"`
	DurationMs          int64     `db:"duration_ms"`
	NosplitTokensIn     int64     `db:"nosplit_tokens_in"`
	NosplitTokensOut    int64     `db:"nosplit_tokens_out"`
	NosplitCostReported float64   `db:"nosplit_cost_reported"`
}

func (f *usageTurnFixture) hours(t *testing.T) []usageHourRow {
	t.Helper()
	var rows []usageHourRow
	require.NoError(t, f.db.Select(&rows, `SELECT hour, project_id, model, provider, account_ref, billing_mode, turns,
		turns_tokens_known, turns_cache_known, turns_duration_known, turns_cost_reported_known,
		tokens_in, tokens_out, cache_read, cache_write, cache_write_1h, cost_reported::float8 AS cost_reported,
		duration_ms, nosplit_tokens_in, nosplit_tokens_out, nosplit_cost_reported::float8 AS nosplit_cost_reported
		FROM agent_usage_hourly WHERE agent_id=$1 ORDER BY hour, model`, f.agentID))
	return rows
}

func (f *usageTurnFixture) sessionTokensIn(t *testing.T) int64 {
	t.Helper()
	var in int64
	require.NoError(t, f.db.Get(&in, `SELECT COALESCE(sum(tokens_in),0) FROM agent_sessions WHERE agent_id=$1`, f.agentID))
	return in
}

// A full report: one journal row with every field, its hour by ended_at in
// UTC, the project resolved from the task, the session added as before.
func TestReportSession_UsageTurn_JournalAndHour_RealPostgres(t *testing.T) {
	f := newUsageTurnFixture(t)
	body := `{"task_id":"` + f.taskID.String() + `","turn_id":"run-1:turn-1","model":"claude-opus-5-5",
		"tokens_in":1000,"tokens_out":200,"estimated_cost":0.25,
		"cache_read":600,"cache_write":300,"cache_write_1h":100,
		"started_at":"2026-11-18T10:58:30+03:00","ended_at":"2026-11-18T11:01:00+03:00",
		"provider":"Anthropic","account_ref":"claude/bob-team","billing_mode":"subscription"}`
	code, answer := f.post(t, body)
	require.Equal(t, http.StatusOK, code)

	turns := f.turns(t)
	require.Len(t, turns, 1)
	tr := turns[0]
	assert.Equal(t, answer["turn"].(map[string]any)["id"], tr.ID.String())
	assert.Equal(t, false, answer["turn"].(map[string]any)["repeat"])
	assert.Equal(t, "turn", tr.Source)
	assert.Equal(t, f.taskID, tr.TaskID.UUID)
	assert.Equal(t, f.projectID, tr.ProjectID.UUID, "project resolved from the task")
	assert.Equal(t, answer["session_id"], tr.SessionID.UUID.String())
	assert.Equal(t, "run-1:turn-1", tr.TurnRef.String)
	assert.Equal(t, "anthropic", tr.Provider)
	assert.Equal(t, "claude/bob-team", tr.AccountRef.String)
	assert.Equal(t, "subscription", tr.BillingMode.String)
	assert.EqualValues(t, 1000, tr.TokensIn.Int64)
	assert.EqualValues(t, 200, tr.TokensOut.Int64)
	assert.EqualValues(t, 600, tr.CacheRead.Int64)
	assert.EqualValues(t, 300, tr.CacheWrite.Int64)
	assert.EqualValues(t, 100, tr.CacheWrite1h.Int64)
	assert.InDelta(t, 0.25, tr.CostReported.Float64, 1e-9)
	assert.True(t, tr.OccurredAt.Equal(time.Date(2026, 11, 18, 8, 1, 0, 0, time.UTC)), "occurred_at = ended_at")

	hours := f.hours(t)
	require.Len(t, hours, 1)
	hr := hours[0]
	// ended_at 11:01 MSK = 08:01 UTC → hour 08:00 UTC (started_at's 07:58 does not pick it).
	assert.True(t, hr.Hour.Equal(time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)), "hour by ended_at, UTC: %s", hr.Hour)
	assert.Equal(t, f.projectID, hr.ProjectID)
	assert.Equal(t, "claude/bob-team", hr.AccountRef)
	assert.Equal(t, "subscription", hr.BillingMode)
	assert.EqualValues(t, 1, hr.Turns)
	assert.EqualValues(t, 1, hr.TurnsTokensKnown)
	assert.EqualValues(t, 1, hr.TurnsCacheKnown)
	assert.EqualValues(t, 1, hr.TurnsDurationKnown)
	assert.EqualValues(t, 1, hr.TurnsCostKnown)
	assert.EqualValues(t, 1000, hr.TokensIn)
	assert.EqualValues(t, 200, hr.TokensOut)
	assert.EqualValues(t, 600, hr.CacheRead)
	assert.EqualValues(t, 300, hr.CacheWrite)
	assert.EqualValues(t, 100, hr.CacheWrite1h)
	assert.InDelta(t, 0.25, hr.CostReported, 1e-9)
	assert.EqualValues(t, 150000, hr.DurationMs)
	assert.EqualValues(t, 0, hr.NosplitTokensIn, "a turn with a cache split is not nosplit")
	assert.InDelta(t, 0, hr.NosplitCostReported, 1e-9)

	// The session keeps accumulating as before.
	assert.EqualValues(t, 1000, f.sessionTokensIn(t))

	// A second turn in the same hour and key adds onto the same hour row.
	code, _ = f.post(t, `{"task_id":"`+f.taskID.String()+`","turn_id":"run-1:turn-2","model":"claude-opus-5-5",
		"tokens_in":10,"tokens_out":5,"estimated_cost":0.5,"cache_read":4,"cache_write":3,
		"ended_at":"2026-11-18T08:30:00Z","provider":"anthropic","account_ref":"claude/bob-team","billing_mode":"subscription"}`)
	require.Equal(t, http.StatusOK, code)
	hours = f.hours(t)
	require.Len(t, hours, 1, "same key and hour: one row, upserted")
	assert.EqualValues(t, 2, hours[0].Turns)
	assert.EqualValues(t, 1010, hours[0].TokensIn)
	assert.EqualValues(t, 604, hours[0].CacheRead)
	assert.EqualValues(t, 100, hours[0].CacheWrite1h, "the second turn sent no cache_write_1h: nothing added")
	assert.EqualValues(t, 1, hours[0].TurnsDurationKnown, "no started_at on the second turn: duration unknown")
	assert.InDelta(t, 0.75, hours[0].CostReported, 1e-9)
	assert.EqualValues(t, 1010, f.sessionTokensIn(t))
}

// The same turn_id twice adds nothing the second time: not to the journal,
// not to the hour, not to the session; the answer names the first turn.
func TestReportSession_UsageTurn_RepeatTurnIDAddsNothing_RealPostgres(t *testing.T) {
	f := newUsageTurnFixture(t)
	body := `{"task_id":"` + f.taskID.String() + `","turn_id":"run-7:turn-3","model":"gpt-6.1-sol",
		"tokens_in":70,"tokens_out":7,"tool_breakdown":{"get_task":2}}`
	code, first := f.post(t, body)
	require.Equal(t, http.StatusOK, code)
	code, second := f.post(t, body)
	require.Equal(t, http.StatusOK, code)

	turns := f.turns(t)
	require.Len(t, turns, 1)
	assert.Equal(t, first["turn"].(map[string]any)["id"], second["turn"].(map[string]any)["id"])
	assert.Equal(t, true, second["turn"].(map[string]any)["repeat"])
	assert.Equal(t, first["session_id"], second["session_id"])
	hours := f.hours(t)
	require.Len(t, hours, 1)
	assert.EqualValues(t, 1, hours[0].Turns)
	assert.EqualValues(t, 70, hours[0].TokensIn)
	assert.EqualValues(t, 70, f.sessionTokensIn(t), "the session is not added twice")
	var calls int64
	require.NoError(t, f.db.Get(&calls, `SELECT COALESCE(sum(tool_calls),0) FROM agent_sessions WHERE agent_id=$1`, f.agentID))
	assert.EqualValues(t, 2, calls, "tool counts of the repeat are not added")

	// Another turn id is another turn.
	code, _ = f.post(t, `{"task_id":"`+f.taskID.String()+`","turn_id":"run-7:turn-4","model":"gpt-6.1-sol","tokens_in":1,"tokens_out":1}`)
	require.Equal(t, http.StatusOK, code)
	assert.Len(t, f.turns(t), 2)
	assert.EqualValues(t, 71, f.sessionTokensIn(t))
}

// An old-format report (no new fields) still writes its journal row: provider
// derived from the model, every missing measure NULL (not 0), the hour by the
// report time. Reports without turn_id never collide.
func TestReportSession_UsageTurn_OldFormatAndNulls_RealPostgres(t *testing.T) {
	f := newUsageTurnFixture(t)
	before := time.Now().UTC().Truncate(time.Hour)
	code, _ := f.post(t, `{"tokens_in":120,"tokens_out":30,"model":"kimi","estimated_cost":0,"unknown":["estimated_cost"]}`)
	require.Equal(t, http.StatusOK, code)
	code, _ = f.post(t, `{"tokens_in":120,"tokens_out":30,"model":"kimi","estimated_cost":0,"unknown":["estimated_cost"]}`)
	require.Equal(t, http.StatusOK, code)
	code, _ = f.post(t, `{"model":"glm-5.3"}`)
	require.Equal(t, http.StatusOK, code)
	after := time.Now().UTC().Truncate(time.Hour)

	turns := f.turns(t)
	require.Len(t, turns, 3, "no turn_id: each report is its own turn")
	for _, tr := range turns[:2] {
		assert.Equal(t, "moonshot", tr.Provider, "provider derived from the model")
		assert.False(t, tr.TaskID.Valid)
		assert.False(t, tr.ProjectID.Valid)
		assert.False(t, tr.TurnRef.Valid)
		assert.EqualValues(t, 120, tr.TokensIn.Int64)
		assert.False(t, tr.CostReported.Valid, "estimated_cost declared unknown → NULL, not 0")
		assert.False(t, tr.CacheRead.Valid, "cache_read not sent → NULL")
		assert.False(t, tr.CacheWrite.Valid, "cache_write not sent → NULL")
		assert.False(t, tr.CacheWrite1h.Valid)
		assert.False(t, tr.StartedAt.Valid)
		assert.False(t, tr.EndedAt.Valid)
		assert.False(t, tr.AccountRef.Valid)
		assert.False(t, tr.BillingMode.Valid)
	}
	bare := turns[2]
	assert.Equal(t, "zai", bare.Provider)
	assert.False(t, bare.TokensIn.Valid, "tokens_in not sent → NULL in the journal")
	assert.False(t, bare.TokensOut.Valid)
	assert.False(t, bare.CostReported.Valid, "estimated_cost not sent → NULL")

	hours := f.hours(t)
	require.Len(t, hours, 2)
	byModel := map[string]usageHourRow{}
	for _, h := range hours {
		byModel[h.Model] = h
		assert.True(t, !h.Hour.Before(before) && !h.Hour.After(after), "hour by report time: %s", h.Hour)
		assert.Equal(t, uuid.Nil, h.ProjectID, "no project = nil uuid")
		assert.Equal(t, "", h.AccountRef)
		assert.Equal(t, "", h.BillingMode)
	}
	k := byModel["kimi"]
	assert.EqualValues(t, 2, k.Turns)
	assert.EqualValues(t, 2, k.TurnsTokensKnown)
	assert.EqualValues(t, 0, k.TurnsCacheKnown)
	assert.EqualValues(t, 0, k.TurnsCostKnown)
	assert.EqualValues(t, 240, k.TokensIn)
	assert.EqualValues(t, 240, k.NosplitTokensIn, "no cache split: counted as nosplit")
	assert.EqualValues(t, 60, k.NosplitTokensOut)
	g := byModel["glm-5.3"]
	assert.EqualValues(t, 1, g.Turns)
	assert.EqualValues(t, 0, g.TurnsTokensKnown, "a turn with no token figures is not covered")
	assert.EqualValues(t, 0, g.TokensIn)

	// The session still counts a missing number as a known 0 (unchanged contract).
	var known bool
	require.NoError(t, f.db.Get(&known, `SELECT tokens_in_known FROM agent_sessions WHERE agent_id=$1 AND model_used='glm-5.3'`, f.agentID))
	assert.True(t, known)
}

// Malformed turn fields are refused before anything is written.
func TestReportSession_UsageTurn_RefusalsWriteNothing_RealPostgres(t *testing.T) {
	f := newUsageTurnFixture(t)
	for name, body := range map[string]string{
		"cache_write_1h above cache_write":   `{"tokens_in":5,"cache_write":3,"cache_write_1h":4}`,
		"cache_write_1h without cache_write": `{"tokens_in":5,"cache_write_1h":4}`,
		"negative cache_read":                `{"tokens_in":5,"cache_read":-1}`,
		"cache_read unknown with a figure":   `{"tokens_in":5,"cache_read":2,"unknown":["cache_read"]}`,
		"billing_mode outside the set":       `{"tokens_in":5,"billing_mode":"free"}`,
		"account_ref is an email":            `{"tokens_in":5,"account_ref":"someone@example.com"}`,
		"ended before started":               `{"tokens_in":5,"started_at":"2026-11-18T10:00:00Z","ended_at":"2026-11-18T09:00:00Z"}`,
	} {
		t.Run(name, func(t *testing.T) {
			code, _ := f.post(t, body)
			assert.Equal(t, http.StatusBadRequest, code)
		})
	}
	assert.Empty(t, f.turns(t))
	assert.Empty(t, f.hours(t))
	assert.EqualValues(t, 0, f.sessionTokensIn(t))

	// cache_read / cache_write declared unknown with no figure: accepted, NULL.
	code, _ := f.post(t, `{"tokens_in":5,"tokens_out":1,"model":"codex","unknown":["cache_read","cache_write"]}`)
	require.Equal(t, http.StatusOK, code)
	turns := f.turns(t)
	require.Len(t, turns, 1)
	assert.Equal(t, "openai", turns[0].Provider)
	assert.False(t, turns[0].CacheRead.Valid)
	assert.False(t, turns[0].CacheWrite.Valid)
}

// The hour of a turn equals what a rebuild from the journal gives: the report
// path and the backfill/down rebuild share agent_usage_hourly_add.
func TestReportSession_UsageTurn_HourMatchesRebuild_RealPostgres(t *testing.T) {
	f := newUsageTurnFixture(t)
	for _, body := range []string{
		`{"turn_id":"a","model":"claude-sonnet-5-5","tokens_in":100,"tokens_out":10,"estimated_cost":0.1,"cache_read":50,"cache_write":20,"started_at":"2026-11-17T23:59:00Z","ended_at":"2026-11-18T00:00:30Z"}`,
		`{"turn_id":"b","model":"claude-sonnet-5-5","tokens_in":7,"tokens_out":1,"estimated_cost":0.01}`,
		`{"turn_id":"c","task_id":"` + f.taskID.String() + `","model":"gpt-6-luna","tokens_in":9,"tokens_out":2,"billing_mode":"api","account_ref":"codex/alex"}`,
	} {
		code, _ := f.post(t, body)
		require.Equal(t, http.StatusOK, code, body)
	}
	live := f.hours(t)
	require.Len(t, live, 3)

	tx, err := f.db.Beginx()
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`DELETE FROM agent_usage_hourly WHERE agent_id=$1`, f.agentID)
	require.NoError(t, err)
	_, err = tx.Exec(`SELECT agent_usage_hourly_add(ARRAY(SELECT id FROM agent_usage_turns WHERE agent_id=$1))`, f.agentID)
	require.NoError(t, err)
	var rebuilt []usageHourRow
	require.NoError(t, tx.Select(&rebuilt, `SELECT hour, project_id, model, provider, account_ref, billing_mode, turns,
		turns_tokens_known, turns_cache_known, turns_duration_known, turns_cost_reported_known,
		tokens_in, tokens_out, cache_read, cache_write, cache_write_1h, cost_reported::float8 AS cost_reported,
		duration_ms, nosplit_tokens_in, nosplit_tokens_out, nosplit_cost_reported::float8 AS nosplit_cost_reported
		FROM agent_usage_hourly WHERE agent_id=$1 ORDER BY hour, model`, f.agentID))
	assert.Equal(t, live, rebuilt)
}

// Mixed coverage must remain reconstructible from the hourly table alone.
// Missing token totals cannot be priced as a complete cache split; partial
// cache figures must not leak into the price of the fully covered turns.
func TestReportSession_UsageTurn_PartialCoverage_RealPostgres(t *testing.T) {
	f := newUsageTurnFixture(t)
	for _, body := range []string{
		`{"model":"claude-sonnet","tokens_in":100,"tokens_out":10,"cache_read":40,"cache_write":10,"cache_write_1h":5,"estimated_cost":0.1}`,
		`{"model":"claude-sonnet","tokens_in":50,"tokens_out":5,"cache_read":20,"estimated_cost":0}`,
		`{"model":"claude-sonnet","cache_read":7,"cache_write":3,"cache_write_1h":1,"estimated_cost":0.2}`,
		`{"model":"claude-sonnet","tokens_in":8,"tokens_out":2}`,
	} {
		code, _ := f.post(t, body)
		require.Equal(t, http.StatusOK, code)
	}
	hours := f.hours(t)
	require.Len(t, hours, 1)
	h := hours[0]
	assert.EqualValues(t, 4, h.Turns)
	assert.EqualValues(t, 3, h.TurnsTokensKnown)
	assert.EqualValues(t, 1, h.TurnsCacheKnown, "cache coverage requires known in, out, read and write")
	var got struct {
		CostKnown    int64   `db:"nosplit_turns_cost_known"`
		TokensIn     int64   `db:"nosplit_tokens_in"`
		TokensOut    int64   `db:"nosplit_tokens_out"`
		CacheRead    int64   `db:"nosplit_cache_read"`
		CacheWrite   int64   `db:"nosplit_cache_write"`
		CacheWrite1h int64   `db:"nosplit_cache_write_1h"`
		Cost         float64 `db:"nosplit_cost_reported"`
	}
	require.NoError(t, f.db.Get(&got, `SELECT nosplit_turns_cost_known, nosplit_tokens_in, nosplit_tokens_out,
  nosplit_cache_read, nosplit_cache_write, nosplit_cache_write_1h, nosplit_cost_reported::float8
  FROM agent_usage_hourly WHERE agent_id=$1`, f.agentID))
	assert.EqualValues(t, 2, got.CostKnown, "explicit zero is known; absent cost is not")
	assert.EqualValues(t, 58, got.TokensIn)
	assert.EqualValues(t, 7, got.TokensOut)
	assert.EqualValues(t, 27, got.CacheRead)
	assert.EqualValues(t, 3, got.CacheWrite)
	assert.EqualValues(t, 1, got.CacheWrite1h)
	assert.InDelta(t, 0.2, got.Cost, 1e-9)
	assert.EqualValues(t, 100, h.TokensIn-got.TokensIn)
	assert.EqualValues(t, 40, h.CacheRead-got.CacheRead)
	assert.EqualValues(t, 10, h.CacheWrite-got.CacheWrite)
	assert.EqualValues(t, 5, h.CacheWrite1h-got.CacheWrite1h)
}

// A failure after journal insertion must roll back both the session addition
// and the journal, including when the session already exists.
func TestReportSession_UsageTurn_AtomicRollback_RealPostgres(t *testing.T) {
	f := newUsageTurnFixture(t)
	code, _ := f.post(t, `{"model":"codex","tokens_in":1}`)
	require.Equal(t, http.StatusOK, code)
	// Force the hourly addition to overflow while leaving the journal valid.
	_, err := f.db.Exec(`UPDATE agent_usage_hourly SET tokens_in=9223372036854775807 WHERE agent_id=$1`, f.agentID)
	require.NoError(t, err)
	code, _ = f.post(t, `{"turn_id":"rollback","model":"codex","tokens_in":2}`)
	require.Equal(t, http.StatusInternalServerError, code)
	assert.Len(t, f.turns(t), 1, "failed hourly write rolls journal insertion back")
	assert.EqualValues(t, 1, f.sessionTokensIn(t), "failed hourly write rolls session addition back")
	_, err = f.db.Exec(`UPDATE agent_usage_hourly SET tokens_in=1 WHERE agent_id=$1`, f.agentID)
	require.NoError(t, err)
	code, _ = f.post(t, `{"turn_id":"rollback","model":"codex","tokens_in":2}`)
	require.Equal(t, http.StatusOK, code, "the failed turn is retryable")
	assert.Len(t, f.turns(t), 2)
	assert.EqualValues(t, 3, f.sessionTokensIn(t))
	assert.EqualValues(t, 3, f.hours(t)[0].TokensIn)
}

// Apply the actual two migration files in a private schema. Minimal source
// tables isolate the migration from other packages sharing the throwaway DB.
func TestUsageTurnMigrations_UpDownUpAndBackfill_RealPostgres(t *testing.T) {
	root := reportDB(t)
	schema := "usage_migration_" + uuid.New().String()[:8]
	_, err := root.Exec(`CREATE SCHEMA ` + schema)
	require.NoError(t, err)
	t.Cleanup(func() { _, e := root.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); require.NoError(t, e) })
	db := reportDB(t)
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`SELECT set_config('search_path', $1, false)`, schema)
	require.NoError(t, err)
	_, err = db.Exec(`
 CREATE TABLE workspaces (id uuid PRIMARY KEY);
 CREATE TABLE agents (id uuid PRIMARY KEY);
 CREATE TABLE tasks (id uuid PRIMARY KEY, project_id uuid);
 CREATE TABLE agent_sessions (id uuid PRIMARY KEY, workspace_id uuid, agent_id uuid, task_id uuid,
  started_at timestamptz, model_used text, tokens_in bigint, tokens_out bigint, estimated_cost numeric,
  tokens_in_known boolean, tokens_out_known boolean, cost_known boolean);
 INSERT INTO workspaces VALUES ('00000000-0000-0000-0000-000000000001');
 INSERT INTO agents VALUES ('00000000-0000-0000-0000-000000000002');
 INSERT INTO tasks VALUES ('00000000-0000-0000-0000-000000000003','00000000-0000-0000-0000-000000000004');
 INSERT INTO agent_sessions SELECT ('00000000-0000-0000-0000-'||lpad(i::text,12,'0'))::uuid,
  '00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000002',
  '00000000-0000-0000-0000-000000000003', now() - CASE WHEN i=3 THEN interval '91 days' ELSE interval '1 day' END,
  CASE WHEN i=4 THEN '' ELSE 'claude-sonnet' END, 100, 10, 0.5, i<>2, true, i<>2
 FROM generate_series(1,4) i;`)
	require.NoError(t, err)
	files := fstest.MapFS{}
	for _, name := range []string{"20261119200_agent_usage_turns.sql", "20261119300_agent_usage_session_backfill.sql"} {
		body, e := os.ReadFile("../../migrations/" + name)
		require.NoError(t, e)
		files[name] = &fstest.MapFile{Data: body}
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db.DB, files, goose.WithDisableGlobalRegistry(true))
	require.NoError(t, err)
	assertHistory := func() {
		t.Helper()
		var n, known int
		require.NoError(t, db.QueryRow(`SELECT count(*),count(tokens_in) FROM agent_usage_turns WHERE source='session_backfill'`).Scan(&n, &known))
		assert.Equal(t, 2, n, "only recent sessions with a model")
		assert.Equal(t, 1, known, "unknown session figure stays NULL")
		var tokens int64
		var cost float64
		require.NoError(t, db.QueryRow(`SELECT sum(tokens_in), sum(cost_reported)::float8 FROM agent_usage_hourly`).Scan(&tokens, &cost))
		assert.EqualValues(t, 100, tokens)
		assert.InDelta(t, 0.5, cost, 1e-9)
		var matched int
		require.NoError(t, db.Get(&matched, `SELECT count(*) FROM agent_usage_turns t JOIN agent_sessions s ON s.id=t.session_id
   WHERE t.provider='anthropic' AND t.occurred_at=s.started_at AND t.ended_at IS NULL
    AND t.cache_read IS NULL AND t.account_ref IS NULL AND t.project_id='00000000-0000-0000-0000-000000000004'`))
		assert.Equal(t, 2, matched)
	}
	_, err = provider.Up(context.Background())
	require.NoError(t, err)
	assertHistory()
	_, err = provider.Down(context.Background())
	require.NoError(t, err)
	var n int
	require.NoError(t, db.Get(&n, `SELECT count(*) FROM agent_usage_turns`))
	assert.Zero(t, n)
	require.NoError(t, db.Get(&n, `SELECT count(*) FROM agent_usage_hourly`))
	assert.Zero(t, n)
	_, err = provider.Up(context.Background())
	require.NoError(t, err)
	assertHistory()
	_, err = provider.Down(context.Background())
	require.NoError(t, err)
	_, err = provider.Down(context.Background())
	require.NoError(t, err)
	var gone bool
	require.NoError(t, db.Get(&gone, `SELECT to_regclass('agent_usage_turns') IS NULL AND to_regclass('agent_usage_hourly') IS NULL`))
	assert.True(t, gone, "schema down removes both tables")
	_, err = provider.Up(context.Background())
	require.NoError(t, err)
	assertHistory()
	require.NoError(t, db.Get(&n, `SELECT count(*) FROM agent_sessions WHERE tokens_in=100 AND tokens_out=10 AND estimated_cost=0.5`))
	assert.Equal(t, 4, n, "source sessions are unchanged")

	// A rollback after live reports keeps those reports. Reapplying history
	// excludes the live contribution already present in the session total.
	_, err = db.Exec(`INSERT INTO agent_usage_turns
  (id,workspace_id,agent_id,session_id,source,occurred_at,model,provider,tokens_in,tokens_out,cost_reported)
 VALUES ('00000000-0000-0000-0000-000000000005','00000000-0000-0000-0000-000000000001',
  '00000000-0000-0000-0000-000000000002','00000000-0000-0000-0000-000000000001',
  'turn',now(),'claude-sonnet','anthropic',25,2,0.1);
 UPDATE agent_sessions SET tokens_in=tokens_in+25,tokens_out=tokens_out+2,estimated_cost=estimated_cost+0.1
 WHERE id='00000000-0000-0000-0000-000000000001';
 SELECT agent_usage_hourly_add(ARRAY['00000000-0000-0000-0000-000000000005'::uuid]);`)
	require.NoError(t, err)
	_, err = provider.Down(context.Background())
	require.NoError(t, err)
	require.NoError(t, db.Get(&n, `SELECT count(*) FROM agent_usage_turns WHERE source='turn'`))
	assert.Equal(t, 1, n)
	var tokens int64
	require.NoError(t, db.Get(&tokens, `SELECT sum(tokens_in) FROM agent_usage_hourly`))
	assert.EqualValues(t, 25, tokens, "backfill rollback preserves the live hour")
	_, err = provider.Up(context.Background())
	require.NoError(t, err)
	require.NoError(t, db.Get(&tokens, `SELECT sum(tokens_in) FROM agent_usage_hourly`))
	assert.EqualValues(t, 125, tokens, "reapplying history does not count the live turn twice")
	require.NoError(t, db.Get(&tokens, `SELECT tokens_in FROM agent_usage_turns
  WHERE source='session_backfill' AND session_id='00000000-0000-0000-0000-000000000001'`))
	assert.EqualValues(t, 100, tokens)
	t.Log("usage migrations: up -> down -> up; 2/2 eligible sessions backfilled, NULL preserved, source sessions unchanged")
}

func TestUsageTurnProviderZaiMigration_RealPostgres(t *testing.T) {
	root := reportDB(t)
	schema := "usage_provider_" + uuid.New().String()[:8]
	_, err := root.Exec(`CREATE SCHEMA ` + schema)
	require.NoError(t, err)
	t.Cleanup(func() { _, e := root.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); require.NoError(t, e) })
	db := reportDB(t)
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`SELECT set_config('search_path', $1, false)`, schema)
	require.NoError(t, err)
	_, err = db.Exec(`
	 CREATE TABLE workspaces (id uuid PRIMARY KEY);
	 CREATE TABLE agents (id uuid PRIMARY KEY);
	 CREATE TABLE tasks (id uuid PRIMARY KEY, project_id uuid);
	 CREATE TABLE agent_sessions (id uuid PRIMARY KEY, workspace_id uuid, agent_id uuid, task_id uuid,
	  started_at timestamptz, model_used text, tokens_in bigint, tokens_out bigint, estimated_cost numeric,
	  tokens_in_known boolean, tokens_out_known boolean, cost_known boolean);
	 INSERT INTO workspaces VALUES ('00000000-0000-0000-0000-000000000001');
	 INSERT INTO agents VALUES ('00000000-0000-0000-0000-000000000002');`)
	require.NoError(t, err)
	files := fstest.MapFS{}
	for _, name := range []string{
		"20261119200_agent_usage_turns.sql",
		"20261119300_agent_usage_session_backfill.sql",
		"20261119700_agent_usage_provider_zai.sql",
	} {
		body, e := os.ReadFile("../../migrations/" + name)
		require.NoError(t, e)
		files[name] = &fstest.MapFile{Data: body}
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db.DB, files, goose.WithDisableGlobalRegistry(true))
	require.NoError(t, err)
	ctx := context.Background()
	_, err = provider.UpTo(ctx, 20261119300)
	require.NoError(t, err)
	_, err = db.Exec(`
	 INSERT INTO agent_usage_turns
	  (id,workspace_id,agent_id,source,occurred_at,model,provider,tokens_in,tokens_out,cache_read,cache_write)
	 VALUES
	  ('00000000-0000-0000-0000-000000000005','00000000-0000-0000-0000-000000000001',
	   '00000000-0000-0000-0000-000000000002','turn','2026-11-18T10:15:00Z','glm-5.3','zhipu',100,10,40,10),
	  ('00000000-0000-0000-0000-000000000006','00000000-0000-0000-0000-000000000001',
	   '00000000-0000-0000-0000-000000000002','turn','2026-11-18T10:45:00Z','glm-5.3','zai',20,2,NULL,NULL);
	 SELECT agent_usage_hourly_add(ARRAY[
	  '00000000-0000-0000-0000-000000000005'::uuid,
	  '00000000-0000-0000-0000-000000000006'::uuid]);`)
	require.NoError(t, err)
	var n int
	require.NoError(t, db.Get(&n, `SELECT count(*) FROM agent_usage_hourly`))
	require.Equal(t, 2, n, "fixture has distinct old and new provider hours")

	assertProvider := func(want string) {
		t.Helper()
		var got string
		require.NoError(t, db.Get(&got, `SELECT agent_usage_provider('glm-5.3')`))
		assert.Equal(t, want, got)
		require.NoError(t, db.Get(&n, `SELECT count(*) FROM agent_usage_turns WHERE provider<>$1`, want))
		assert.Zero(t, n)
		var hour struct {
			Provider      string `db:"provider"`
			Turns         int64  `db:"turns"`
			TokensKnown   int64  `db:"turns_tokens_known"`
			CacheKnown    int64  `db:"turns_cache_known"`
			TokensIn      int64  `db:"tokens_in"`
			TokensOut     int64  `db:"tokens_out"`
			CacheRead     int64  `db:"cache_read"`
			CacheWrite    int64  `db:"cache_write"`
			NosplitIn     int64  `db:"nosplit_tokens_in"`
			NosplitOut    int64  `db:"nosplit_tokens_out"`
			ProviderCount int    `db:"provider_count"`
		}
		require.NoError(t, db.Get(&hour, `SELECT provider,turns,turns_tokens_known,turns_cache_known,tokens_in,tokens_out,
		 cache_read,cache_write,nosplit_tokens_in,nosplit_tokens_out,count(*) OVER() AS provider_count
		 FROM agent_usage_hourly`))
		assert.Equal(t, want, hour.Provider)
		assert.EqualValues(t, 2, hour.Turns)
		assert.EqualValues(t, 2, hour.TokensKnown)
		assert.EqualValues(t, 1, hour.CacheKnown)
		assert.EqualValues(t, 120, hour.TokensIn)
		assert.EqualValues(t, 12, hour.TokensOut)
		assert.EqualValues(t, 40, hour.CacheRead)
		assert.EqualValues(t, 10, hour.CacheWrite)
		assert.EqualValues(t, 20, hour.NosplitIn)
		assert.EqualValues(t, 2, hour.NosplitOut)
		assert.Equal(t, 1, hour.ProviderCount, "same-key provider hours merge")
	}

	_, err = provider.UpTo(ctx, 20261119700)
	require.NoError(t, err)
	assertProvider("zai")
	_, err = provider.Down(ctx)
	require.NoError(t, err)
	assertProvider("zhipu")
	_, err = provider.Up(ctx)
	require.NoError(t, err)
	assertProvider("zai")
}
