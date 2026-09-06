package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// minimalAgentSessionRepoStub implements every AgentSessionRepository method
// except IncrementToolBreakdown as an inert no-op, so test doubles in this
// package only need to override the one method they actually exercise.
type minimalAgentSessionRepoStub struct{}

func (minimalAgentSessionRepoStub) Create(ctx context.Context, session *domain.AgentSession) error {
	return nil
}
func (minimalAgentSessionRepoStub) Update(ctx context.Context, session *domain.AgentSession) error {
	return nil
}
func (minimalAgentSessionRepoStub) GetActive(ctx context.Context, agentID uuid.UUID) (*domain.AgentSession, error) {
	return nil, nil
}
func (minimalAgentSessionRepoStub) GetActiveForTask(ctx context.Context, agentID, taskID uuid.UUID) (*domain.AgentSession, error) {
	return nil, nil
}
func (minimalAgentSessionRepoStub) EndStale(ctx context.Context, timeout time.Duration) (int, error) {
	return 0, nil
}
func (minimalAgentSessionRepoStub) GetPreviousStartedAt(ctx context.Context, agentID uuid.UUID) (*time.Time, error) {
	return nil, nil
}
func (minimalAgentSessionRepoStub) GetTaskCostSummary(ctx context.Context, taskID uuid.UUID) (*domain.TaskCostSummary, error) {
	return nil, nil
}

// fakeToolBreakdownSessionRepo is a minimal AgentSessionRepository double
// that records every IncrementToolBreakdown call it receives, so tests can
// assert on exactly what a flush sent — without a real Postgres connection.
type fakeToolBreakdownSessionRepo struct {
	minimalAgentSessionRepoStub

	mu    sync.Mutex
	calls []incrementCall
}

type incrementCall struct {
	agentID     uuid.UUID
	workspaceID uuid.UUID
	taskID      *uuid.UUID
	counts      map[string]int64
}

func (f *fakeToolBreakdownSessionRepo) IncrementToolBreakdown(ctx context.Context, agentID, workspaceID uuid.UUID, taskID *uuid.UUID, counts map[string]int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make(map[string]int64, len(counts))
	for k, v := range counts {
		cp[k] = v
	}
	f.calls = append(f.calls, incrementCall{agentID: agentID, workspaceID: workspaceID, taskID: taskID, counts: cp})
	return nil
}

func newTrackerTestContext(t *testing.T, method, routePath, actualPath string, agentID, workspaceID uuid.UUID, isAgent bool) echo.Context {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(method, actualPath, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath(routePath)
	if isAgent {
		c.Set(ContextKeyAuthType, AuthTypeAgent)
		c.Set(ContextKeyAgentID, agentID)
		c.Set(ContextKeyWorkspaceID, workspaceID)
	}
	return c
}

// Green arm: a real recall + a real remember call through the middleware,
// followed by an explicit Flush, must produce exactly the counts made.
func TestToolBreakdownTracker_RecordsAndFlushesRealCalls(t *testing.T) {
	repo := &fakeToolBreakdownSessionRepo{}
	tr := NewToolBreakdownTracker(repo)

	agentID := uuid.New()
	workspaceID := uuid.New()
	next := func(c echo.Context) error { return c.NoContent(http.StatusOK) }
	mw := tr.Middleware()(next)

	// Two recall calls, one remember call — from the same agent, no task_id.
	require.NoError(t, mw(newTrackerTestContext(t, http.MethodGet, "/api/v1/memories/search", "/api/v1/memories/search?query=x", agentID, workspaceID, true)))
	require.NoError(t, mw(newTrackerTestContext(t, http.MethodGet, "/api/v1/memories/search", "/api/v1/memories/search?query=y", agentID, workspaceID, true)))
	require.NoError(t, mw(newTrackerTestContext(t, http.MethodPost, "/api/v1/memories", "/api/v1/memories", agentID, workspaceID, true)))

	tr.Flush(context.Background())

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.calls, 1, "one agent/workspace/no-task bucket should flush as exactly one IncrementToolBreakdown call")
	call := repo.calls[0]
	assert.Equal(t, agentID, call.agentID)
	assert.Equal(t, workspaceID, call.workspaceID)
	assert.Nil(t, call.taskID)
	assert.Equal(t, int64(2), call.counts["recall"], "two recall calls must be counted as 2, not overwritten to 1")
	assert.Equal(t, int64(1), call.counts["remember"])
}

// Red arm: a non-agent (JWT/user) request must NOT be counted at all — this
// middleware exists to track MCP tool usage, not every HTTP caller.
func TestToolBreakdownTracker_NonAgentRequestNotCounted(t *testing.T) {
	repo := &fakeToolBreakdownSessionRepo{}
	tr := NewToolBreakdownTracker(repo)
	next := func(c echo.Context) error { return c.NoContent(http.StatusOK) }
	mw := tr.Middleware()(next)

	require.NoError(t, mw(newTrackerTestContext(t, http.MethodGet, "/api/v1/memories/search", "/api/v1/memories/search", uuid.New(), uuid.New(), false)))

	tr.Flush(context.Background())

	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Empty(t, repo.calls, "a request with no agent auth context must not produce any tool_breakdown increment")
}

// Red arm, the shape the acceptance criterion actually cares about: if the
// middleware were never wired into a request's chain (or removed), the tool
// call must NOT silently appear in tool_breakdown. This is the same
// assertion Middleware() itself proves by construction (record only runs
// inside the returned middleware func), pinned as an explicit test so a
// future refactor that moves recording into `next` itself (— never invoked
// here) is caught.
func TestToolBreakdownTracker_NoCallsWithoutMiddlewareInChain(t *testing.T) {
	repo := &fakeToolBreakdownSessionRepo{}
	tr := NewToolBreakdownTracker(repo)

	// Deliberately do NOT wrap `next` with tr.Middleware() — simulates the
	// tracker existing but never being registered in the echo chain, which is
	// exactly the "session_report exists but nothing calls it" failure this
	// task's audit finding described for tool_breakdown before this change.
	next := func(c echo.Context) error { return c.NoContent(http.StatusOK) }
	c := newTrackerTestContext(t, http.MethodGet, "/api/v1/memories/search", "/api/v1/memories/search", uuid.New(), uuid.New(), true)
	require.NoError(t, next(c))

	tr.Flush(context.Background())

	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Empty(t, repo.calls, "with the middleware not in the chain, tool_breakdown must stay empty — proves the count is not an ambient side effect of something else")
}

// A task-scoped route (:task_id in the path) must flush against a distinct
// bucket from an agent-wide call, so a real IncrementToolBreakdown call gets
// the taskID it needs to resolve the same session_report already scopes by.
func TestToolBreakdownTracker_TaskScopedRouteCarriesTaskID(t *testing.T) {
	repo := &fakeToolBreakdownSessionRepo{}
	tr := NewToolBreakdownTracker(repo)
	agentID := uuid.New()
	workspaceID := uuid.New()
	taskID := uuid.New()
	next := func(c echo.Context) error { return c.NoContent(http.StatusOK) }
	mw := tr.Middleware()(next)

	c := newTrackerTestContext(t, http.MethodGet, "/api/v1/tasks/:task_id", "/api/v1/tasks/"+taskID.String(), agentID, workspaceID, true)
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())
	require.NoError(t, mw(c))

	tr.Flush(context.Background())

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.calls, 1)
	require.NotNil(t, repo.calls[0].taskID)
	assert.Equal(t, taskID, *repo.calls[0].taskID)
	assert.Equal(t, int64(1), repo.calls[0].counts["get_task"])
}
