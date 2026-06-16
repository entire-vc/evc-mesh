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
// It keeps at most one active session per agent (byAgent) and one per
// agent+task pair (byAgentTask) so both GetActive and GetActiveForTask can be
// exercised. All methods are guarded by a mutex so the concurrency test stays
// race-free under `go test -race`.
type mockSessionRepo struct {
	mu          sync.Mutex
	byAgent     map[uuid.UUID]*domain.AgentSession
	byAgentTask map[string]*domain.AgentSession // key: agentID+":"+taskID
	createN     int
	updateN     int
	endStale    func(ctx context.Context, timeout time.Duration) (int, error)
}

func newMockSessionRepo() *mockSessionRepo {
	return &mockSessionRepo{
		byAgent:     make(map[uuid.UUID]*domain.AgentSession),
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
	m.byAgent[s.AgentID] = &cp
	if s.TaskID != nil {
		m.byAgentTask[agentTaskKey(s.AgentID, *s.TaskID)] = &cp
	}
	m.createN++
	return nil
}

func (m *mockSessionRepo) Update(ctx context.Context, s *domain.AgentSession) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *s
	m.byAgent[s.AgentID] = &cp
	if s.TaskID != nil {
		m.byAgentTask[agentTaskKey(s.AgentID, *s.TaskID)] = &cp
	}
	m.updateN++
	return nil
}

func (m *mockSessionRepo) GetActive(ctx context.Context, agentID uuid.UUID) (*domain.AgentSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.byAgent[agentID]
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

	stored, _ := repo.GetActive(context.Background(), agentID)
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

	stored, _ := repo.GetActive(context.Background(), agentID)
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

	stored, _ := repo.GetActive(context.Background(), agentID)
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
	stored, _ := repo.GetActive(context.Background(), agentID)
	require.NotNil(t, stored)
	assert.Equal(t, "", stored.ModelUsed)

	postReport(t, h, e, &agentID, `{"tokens_in":200,"model":"claude-opus-4-7"}`)
	stored, _ = repo.GetActive(context.Background(), agentID)
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

	stored, _ := repo.GetActive(context.Background(), agentID)
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

	stored, _ := repo.GetActive(context.Background(), agentID)
	require.NotNil(t, stored)
	// No lost-update guarantee from the read-modify-write mock under concurrency,
	// but the session must remain a single active row with a positive total.
	assert.GreaterOrEqual(t, stored.TokensIn, int64(11))
}
