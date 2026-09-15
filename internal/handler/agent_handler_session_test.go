package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// mockSessionRepo is an in-memory AgentSessionRepository for handler tests.
// It keeps at most one agent-wide active session per agent (byAgentWide —
// task_id IS NULL, mirroring GetActiveAgentWide's real WHERE clause) and one
// per agent+task pair (byAgentTask) so GetActiveAgentWide and
// GetActiveForTask can each be exercised as two genuinely separate rows, not
// two views onto the same map — that distinction is the whole point of task
// ea1b9fb6 (a task-scoped session must never be returned to an untagged
// caller just because it happens to be the agent's most recently touched
// one). All methods are guarded by a mutex so the concurrency test stays
// race-free under `go test -race`.
type mockSessionRepo struct {
	mu          sync.Mutex
	byAgentWide map[uuid.UUID]*domain.AgentSession
	byAgentTask map[string]*domain.AgentSession // key: agentID+":"+taskID
	createN     int
	updateN     int
	endStale    func(ctx context.Context, timeout time.Duration) (int, error)
}

func newMockSessionRepo() *mockSessionRepo {
	return &mockSessionRepo{
		byAgentWide: make(map[uuid.UUID]*domain.AgentSession),
		byAgentTask: make(map[string]*domain.AgentSession),
	}
}

func agentTaskKey(agentID, taskID uuid.UUID) string {
	return agentID.String() + ":" + taskID.String()
}

func (m *mockSessionRepo) Create(ctx context.Context, s *domain.AgentSession) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *s
	if s.TaskID != nil {
		m.byAgentTask[agentTaskKey(s.AgentID, *s.TaskID)] = &cp
	} else {
		m.byAgentWide[s.AgentID] = &cp
	}
	m.createN++
	return nil
}

func (m *mockSessionRepo) Update(ctx context.Context, s *domain.AgentSession) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *s
	if s.TaskID != nil {
		m.byAgentTask[agentTaskKey(s.AgentID, *s.TaskID)] = &cp
	} else {
		m.byAgentWide[s.AgentID] = &cp
	}
	m.updateN++
	return nil
}

func (m *mockSessionRepo) GetActiveAgentWide(ctx context.Context, agentID uuid.UUID) (*domain.AgentSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.byAgentWide[agentID]
	if !ok {
		return nil, nil
	}
	cp := *s
	return &cp, nil
}

func (m *mockSessionRepo) GetActiveForTask(ctx context.Context, agentID, taskID uuid.UUID) (*domain.AgentSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.byAgentTask[agentTaskKey(agentID, taskID)]
	if !ok {
		return nil, nil
	}
	cp := *s
	return &cp, nil
}

func (m *mockSessionRepo) EndStale(ctx context.Context, timeout time.Duration) (int, error) {
	if m.endStale != nil {
		return m.endStale(ctx, timeout)
	}
	return 0, nil
}

func (m *mockSessionRepo) GetPreviousStartedAt(ctx context.Context, agentID uuid.UUID) (*time.Time, error) {
	return nil, nil
}

func (m *mockSessionRepo) GetTaskCostSummary(ctx context.Context, taskID uuid.UUID) (*domain.TaskCostSummary, error) {
	return nil, nil
}

// IncrementToolBreakdown mirrors the real repo's merge semantics (additive,
// task-scoped when taskID != nil else agent-wide, create-on-miss) closely
// enough to exercise ReportSession's client-tool_breakdown merge path in
// TestReportSession_MergesClientToolBreakdown below — it is not a stand-in
// for the real UPDATE/jsonb_set behavior, which is covered separately by
// internal/repository/postgres session_repo_tool_breakdown_sqlmock_test.go.
func (m *mockSessionRepo) IncrementToolBreakdown(ctx context.Context, agentID, workspaceID uuid.UUID, taskID *uuid.UUID, counts map[string]int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var target *domain.AgentSession
	if taskID != nil {
		target = m.byAgentTask[agentTaskKey(agentID, *taskID)]
	} else {
		target = m.byAgentWide[agentID]
	}

	merged := map[string]int64{}
	if target != nil && len(target.ToolBreakdown) > 0 {
		_ = json.Unmarshal(target.ToolBreakdown, &merged)
	}
	var total int64
	for k, v := range counts {
		if k == "" || v <= 0 {
			continue
		}
		merged[k] += v
		total += v
	}
	if total == 0 {
		return nil
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return err
	}

	if target == nil {
		target = &domain.AgentSession{
			ID:          uuid.New(),
			WorkspaceID: workspaceID,
			AgentID:     agentID,
			TaskID:      taskID,
			StartedAt:   time.Now(),
			Status:      domain.AgentSessionStatusActive,
		}
		m.createN++
	}
	target.ToolBreakdown = raw
	target.ToolCalls += int(total)
	cp := *target
	if taskID != nil {
		m.byAgentTask[agentTaskKey(agentID, *taskID)] = &cp
	} else {
		m.byAgentWide[agentID] = &cp
	}
	return nil
}

// setupSessionTest builds an AgentHandler wired with a session repo and an agent
// service that resolves agents to a fixed workspace.
func setupSessionTest(repo *mockSessionRepo, workspaceID uuid.UUID) (*AgentHandler, *echo.Echo) {
	e := echo.New()
	agentSvc := &MockAgentService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Agent, error) {
			return &domain.Agent{ID: id, WorkspaceID: workspaceID}, nil
		},
	}
	h := NewAgentHandlerWithEvents(agentSvc, nil, nil, nil, nil, repo)
	return h, e
}

func postReport(t *testing.T, h *AgentHandler, e *echo.Echo, agentID *uuid.UUID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/agents/me/sessions/report", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/agents/me/sessions/report")
	if agentID != nil {
		c.Set("agent_id", *agentID)
	}
	require.NoError(t, h.ReportSession(c))
	return rec
}

// No active session → a new row is created with the reported usage.
func TestReportSession_CreatesNewSession(t *testing.T) {
	repo := newMockSessionRepo()
	wsID := uuid.New()
	agentID := uuid.New()
	h, e := setupSessionTest(repo, wsID)

	rec := postReport(t, h, e, &agentID,
		`{"tokens_in":1000,"tokens_out":500,"model":"claude-opus-4-7","estimated_cost":0.05}`)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, repo.createN)
	assert.Equal(t, 0, repo.updateN)

	stored, _ := repo.GetActiveAgentWide(context.Background(), agentID)
	require.NotNil(t, stored)
	assert.Equal(t, wsID, stored.WorkspaceID)
	assert.Equal(t, domain.AgentSessionStatusActive, stored.Status)
	assert.Equal(t, int64(1000), stored.TokensIn)
	assert.Equal(t, int64(500), stored.TokensOut)
	assert.Equal(t, "claude-opus-4-7", stored.ModelUsed)
	assert.InDelta(t, 0.05, stored.EstimatedCost, 1e-9)

	var resp struct {
		SessionID uuid.UUID `json:"session_id"`
		Totals    struct {
			TokensIn      int64   `json:"tokens_in"`
			TokensOut     int64   `json:"tokens_out"`
			EstimatedCost float64 `json:"estimated_cost"`
			ModelUsed     string  `json:"model_used"`
		} `json:"totals"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, stored.ID, resp.SessionID)
	assert.Equal(t, int64(1000), resp.Totals.TokensIn)
}

// Existing active session → reported usage is accumulated additively.
func TestReportSession_AdditiveUpdate(t *testing.T) {
	repo := newMockSessionRepo()
	agentID := uuid.New()
	h, e := setupSessionTest(repo, uuid.New())

	rec1 := postReport(t, h, e, &agentID,
		`{"tokens_in":1000,"tokens_out":500,"model":"claude-opus-4-7","estimated_cost":0.05}`)
	assert.Equal(t, http.StatusOK, rec1.Code)

	rec2 := postReport(t, h, e, &agentID,
		`{"tokens_in":2000,"tokens_out":1500,"model":"claude-opus-4-7","estimated_cost":0.10}`)
	assert.Equal(t, http.StatusOK, rec2.Code)

	assert.Equal(t, 1, repo.createN, "should reuse the active session, not create a second")
	assert.Equal(t, 1, repo.updateN)

	stored, _ := repo.GetActiveAgentWide(context.Background(), agentID)
	require.NotNil(t, stored)
	assert.Equal(t, int64(3000), stored.TokensIn)
	assert.Equal(t, int64(2000), stored.TokensOut)
	assert.InDelta(t, 0.15, stored.EstimatedCost, 1e-9)
}

// No agent_id in context → 401 Unauthorized.
func TestReportSession_NoAuth(t *testing.T) {
	repo := newMockSessionRepo()
	h, e := setupSessionTest(repo, uuid.New())

	rec := postReport(t, h, e, nil, `{"tokens_in":1000}`)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, 0, repo.createN)
}

// Empty body (all-zero usage) → still 200 and an active session is created.
func TestReportSession_EmptyBodyCreatesSession(t *testing.T) {
	repo := newMockSessionRepo()
	agentID := uuid.New()
	h, e := setupSessionTest(repo, uuid.New())

	rec := postReport(t, h, e, &agentID, `{}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, repo.createN)

	stored, _ := repo.GetActiveAgentWide(context.Background(), agentID)
	require.NotNil(t, stored)
	assert.Equal(t, int64(0), stored.TokensIn)
	assert.Equal(t, int64(0), stored.TokensOut)
	assert.Equal(t, "", stored.ModelUsed)
}

// First report omits model, second supplies it → model is recorded (latest wins).
func TestReportSession_ModelOverride(t *testing.T) {
	repo := newMockSessionRepo()
	agentID := uuid.New()
	h, e := setupSessionTest(repo, uuid.New())

	postReport(t, h, e, &agentID, `{"tokens_in":100}`)
	stored, _ := repo.GetActiveAgentWide(context.Background(), agentID)
	require.NotNil(t, stored)
	assert.Equal(t, "", stored.ModelUsed)

	postReport(t, h, e, &agentID, `{"tokens_in":200,"model":"claude-opus-4-7"}`)
	stored, _ = repo.GetActiveAgentWide(context.Background(), agentID)
	require.NotNil(t, stored)
	assert.Equal(t, "claude-opus-4-7", stored.ModelUsed)
	assert.Equal(t, int64(300), stored.TokensIn)
}

// Empty model on a later report must NOT clobber a previously recorded model.
func TestReportSession_ModelNotClobberedByEmpty(t *testing.T) {
	repo := newMockSessionRepo()
	agentID := uuid.New()
	h, e := setupSessionTest(repo, uuid.New())

	postReport(t, h, e, &agentID, `{"tokens_in":100,"model":"claude-opus-4-7"}`)
	postReport(t, h, e, &agentID, `{"tokens_in":200}`) // no model

	stored, _ := repo.GetActiveAgentWide(context.Background(), agentID)
	require.NotNil(t, stored)
	assert.Equal(t, "claude-opus-4-7", stored.ModelUsed, "empty model must not overwrite")
}

// nil sessionRepo → 501 Not Implemented (handler degrades gracefully).
func TestReportSession_NilRepoNotImplemented(t *testing.T) {
	e := echo.New()
	h := NewAgentHandlerWithEvents(&MockAgentService{}, nil, nil, nil, nil, nil)
	agentID := uuid.New()
	rec := postReport(t, h, e, &agentID, `{"tokens_in":100}`)
	assert.Equal(t, http.StatusNotImplemented, rec.Code)
}

// Two different task_ids from the same agent within one EndStale window must
// each get their own session row — costs must not pile onto the first task.
func TestReportSession_TwoTasksSeparateSessions(t *testing.T) {
	repo := newMockSessionRepo()
	agentID := uuid.New()
	taskA := uuid.New()
	taskB := uuid.New()
	h, e := setupSessionTest(repo, uuid.New())

	// Report for taskA: creates a new session.
	postReport(t, h, e, &agentID, `{"task_id":"`+taskA.String()+`","tokens_in":100,"tokens_out":50,"estimated_cost":0.01}`)

	// Report for taskB: must NOT accumulate onto taskA's session.
	postReport(t, h, e, &agentID, `{"task_id":"`+taskB.String()+`","tokens_in":200,"tokens_out":80,"estimated_cost":0.02}`)

	// A second report for taskA accumulates on its own session (not taskB's).
	postReport(t, h, e, &agentID, `{"task_id":"`+taskA.String()+`","tokens_in":10}`)

	sessA, err := repo.GetActiveForTask(context.Background(), agentID, taskA)
	require.NoError(t, err)
	require.NotNil(t, sessA, "taskA session must exist")
	assert.Equal(t, taskA, *sessA.TaskID)
	assert.Equal(t, int64(110), sessA.TokensIn, "taskA should have 100+10, not taskB's 200")

	sessB, err := repo.GetActiveForTask(context.Background(), agentID, taskB)
	require.NoError(t, err)
	require.NotNil(t, sessB, "taskB session must exist")
	assert.Equal(t, taskB, *sessB.TaskID)
	assert.Equal(t, int64(200), sessB.TokensIn, "taskB should only have its own 200")

	// Two distinct sessions created.
	assert.Equal(t, 2, repo.createN)
}

// Regression for task ea1b9fb6: an untagged (task_id=nil) report must get
// its OWN agent-wide session — it must NOT accumulate onto whichever
// task-scoped session happens to be active, however recently that one
// started. Before the fix, ReportSession's agent-wide branch called plain
// GetActive(agentID), which had no task_id IS NULL filter and simply
// returned "the agent's latest active session, any task" — so a periodic
// agent-wide flush (e.g. fiddler reporting idle-time activity between tasks)
// silently misattributed its cost onto the currently-fed task instead of
// getting a distinct task_id-NULL row.
func TestReportSession_AgentWideReportDoesNotPileOntoTaskSession(t *testing.T) {
	repo := newMockSessionRepo()
	agentID := uuid.New()
	taskA := uuid.New()
	h, e := setupSessionTest(repo, uuid.New())

	// Task-scoped session exists and is the most recently touched one.
	postReport(t, h, e, &agentID, `{"task_id":"`+taskA.String()+`","tokens_in":500,"estimated_cost":0.05}`)

	// An untagged report arrives (e.g. agent-wide idle-time cost) — must NOT
	// land on taskA's session.
	postReport(t, h, e, &agentID, `{"tokens_in":30,"estimated_cost":0.003}`)

	sessA, err := repo.GetActiveForTask(context.Background(), agentID, taskA)
	require.NoError(t, err)
	require.NotNil(t, sessA)
	assert.Equal(t, int64(500), sessA.TokensIn, "taskA's session must be untouched by the agent-wide report")

	wide, err := repo.GetActiveAgentWide(context.Background(), agentID)
	require.NoError(t, err)
	require.NotNil(t, wide, "the untagged report must create its own agent-wide (task_id=nil) session")
	assert.Nil(t, wide.TaskID)
	assert.Equal(t, int64(30), wide.TokensIn)

	// Two distinct sessions: one per task-scoped, one agent-wide.
	assert.Equal(t, 2, repo.createN)

	// A second untagged report must accumulate onto that SAME agent-wide
	// session, not spawn a third one and not touch taskA's.
	postReport(t, h, e, &agentID, `{"tokens_in":5}`)

	wide, err = repo.GetActiveAgentWide(context.Background(), agentID)
	require.NoError(t, err)
	require.NotNil(t, wide)
	assert.Equal(t, int64(35), wide.TokensIn, "second agent-wide report accumulates onto the same task_id=nil row")
	assert.Equal(t, 2, repo.createN, "no third session created")

	sessA, err = repo.GetActiveForTask(context.Background(), agentID, taskA)
	require.NoError(t, err)
	require.NotNil(t, sessA)
	assert.Equal(t, int64(500), sessA.TokensIn, "taskA's session must still be untouched")
}

// Concurrent reports from the same agent must not race (run with -race).
// Note: the in-memory mock serializes via mutex; this asserts the handler
// itself holds no unsynchronized shared state across goroutines.
func TestReportSession_ConcurrentReports(t *testing.T) {
	repo := newMockSessionRepo()
	agentID := uuid.New()
	h, e := setupSessionTest(repo, uuid.New())

	// Seed an active session so all goroutines take the update path.
	postReport(t, h, e, &agentID, `{"tokens_in":1}`)

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/agents/me/sessions/report",
				strings.NewReader(`{"tokens_in":10}`))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.Set("agent_id", agentID)
			_ = h.ReportSession(c)
		}()
	}
	wg.Wait()

	stored, _ := repo.GetActiveAgentWide(context.Background(), agentID)
	require.NotNil(t, stored)
	// No lost-update guarantee from the read-modify-write mock under concurrency,
	// but the session must remain a single active row with a positive total.
	assert.GreaterOrEqual(t, stored.TokensIn, int64(11))
}

// session_report accepts a client-computed tool_breakdown and merges it
// additively into the session, same as tokens_in/tokens_out/estimated_cost.
// This is the "session_report принимает и мержит breakdown от клиента" half
// of task ce1bc187 — the per-request middleware (ToolBreakdownTracker) is
// the primary write path, but the dispatcher/fiddler side can also report a
// running tally it kept itself, e.g. for a spawn whose HTTP calls it proxied.
func TestReportSession_MergesClientToolBreakdown(t *testing.T) {
	repo := newMockSessionRepo()
	agentID := uuid.New()
	h, e := setupSessionTest(repo, uuid.New())

	rec := postReport(t, h, e, &agentID,
		`{"tokens_in":100,"tool_breakdown":{"recall":3,"remember":1}}`)
	assert.Equal(t, http.StatusOK, rec.Code)

	stored, _ := repo.GetActiveAgentWide(context.Background(), agentID)
	require.NotNil(t, stored)
	var breakdown map[string]int64
	require.NoError(t, json.Unmarshal(stored.ToolBreakdown, &breakdown))
	assert.Equal(t, int64(3), breakdown["recall"])
	assert.Equal(t, int64(1), breakdown["remember"])
	assert.Equal(t, 4, stored.ToolCalls)
}

// A second report's tool_breakdown accumulates onto the first, same additive
// semantics as every other numeric field on this endpoint — including the
// consequence that a naive retry of an identical report double-counts, which
// is the existing, accepted behavior for tokens_in/tokens_out/estimated_cost
// on this same endpoint (there is no idempotency key on any of them). This
// test documents that this is a deliberate, pre-existing tradeoff extended
// to tool_breakdown for consistency, not a new one introduced by it.
func TestReportSession_ToolBreakdownAccumulatesAcrossReports(t *testing.T) {
	repo := newMockSessionRepo()
	agentID := uuid.New()
	h, e := setupSessionTest(repo, uuid.New())

	postReport(t, h, e, &agentID, `{"tool_breakdown":{"recall":2}}`)
	postReport(t, h, e, &agentID, `{"tool_breakdown":{"recall":2}}`) // e.g. a retried report

	stored, _ := repo.GetActiveAgentWide(context.Background(), agentID)
	require.NotNil(t, stored)
	var breakdown map[string]int64
	require.NoError(t, json.Unmarshal(stored.ToolBreakdown, &breakdown))
	assert.Equal(t, int64(4), breakdown["recall"], "additive merge — a retried report is expected to double-count, same as tokens_in already does")
}

// No tool_breakdown field at all must not touch the session's existing
// breakdown or manufacture a session with an empty one where none was asked
// for — session_report's other fields (tokens/cost/model) still work exactly
// as before this change for a caller that never sends tool_breakdown.
func TestReportSession_NoToolBreakdownFieldLeavesNothingToMerge(t *testing.T) {
	repo := newMockSessionRepo()
	agentID := uuid.New()
	h, e := setupSessionTest(repo, uuid.New())

	rec := postReport(t, h, e, &agentID, `{"tokens_in":50}`)
	assert.Equal(t, http.StatusOK, rec.Code)

	stored, _ := repo.GetActiveAgentWide(context.Background(), agentID)
	require.NotNil(t, stored)
	assert.Equal(t, int64(50), stored.TokensIn)
	assert.Equal(t, 0, stored.ToolCalls)
}
