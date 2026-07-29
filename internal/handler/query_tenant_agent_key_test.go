package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// The agent key is the credential that walked through this class three times.
//
// rbac() short-circuits to a static capability map when the caller presents an
// agent key and never looks at the workspace of the object being addressed, so
// "the route is protected by rbac" says nothing about tenancy for an agent. Every
// previous fix in this series had to land upstream of rbac, in WorkspaceRLS or
// RequireWorkspaceMemberScoped, for exactly that reason.
//
// On the query-string channel the equivalent guard is
// MemoryHandler.requireWorkspaceID, and memory_handler_security_test.go already
// pins it on the reads — Search, List, Export, FindRelated, GetByID. It does not
// pin the three POSTs that take ?workspace_id=, or RecallGraph. Those are the
// writes, which makes them the ones where a regression costs the most: a foreign
// import writes rows, a foreign reindex and backfill burn the victim's embedding
// budget and rewrite their vectors.
//
// Asserting the status code alone would not be enough. A handler that lost its
// guard and swallowed the resulting error would still answer 200, so each test
// below asserts the service was never called with the foreign workspace — which is
// the thing that would actually have happened.

// TestImportMemories_AgentCannotWriteIntoForeignWorkspace: POST /memories/import
// with ?workspace_id=<victim> must be refused before a single row is upserted.
func TestImportMemories_AgentCannotWriteIntoForeignWorkspace(t *testing.T) {
	ownWS, foreignWS := uuid.New(), uuid.New()

	imported := false
	ms := &MockMemoryService{
		ImportMemoriesFunc: func(_ context.Context, _ uuid.UUID, _ []byte) (int, error) {
			imported = true
			return 1, nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/memories/import?workspace_id="+foreignWS.String(),
		strings.NewReader("version: \"1\"\nmemories: []\n"))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ownWS)

	if err := h.ImportMemories(c); err != nil {
		t.Fatalf("ImportMemories returned error: %v", err)
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-tenant import: got status %d, want 403 (body: %s)", rec.Code, rec.Body.String())
	}
	if imported {
		t.Error("SECURITY: memories were imported into a foreign workspace")
	}
}

// TestImportMemories_AgentOwnWorkspaceStillWorks guards the other direction: the
// refusal above must not be "refuse everything", which would pass the test above
// and break the endpoint.
func TestImportMemories_AgentOwnWorkspaceStillWorks(t *testing.T) {
	ownWS := uuid.New()

	var gotWS uuid.UUID
	ms := &MockMemoryService{
		ImportMemoriesFunc: func(_ context.Context, wsID uuid.UUID, _ []byte) (int, error) {
			gotWS = wsID
			return 1, nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/memories/import?workspace_id="+ownWS.String(),
		strings.NewReader("version: \"1\"\nmemories: []\n"))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ownWS)

	if err := h.ImportMemories(c); err != nil {
		t.Fatalf("ImportMemories returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("own-workspace import: got status %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if gotWS != ownWS {
		t.Errorf("service imported into workspace %s, want own workspace %s", gotWS, ownWS)
	}
}

// TestReindex_AgentCannotTouchForeignWorkspace: POST /memories/reindex re-embeds
// every memory it selects. Pointed at another tenant it rewrites their vectors and
// spends their embedding budget, and returns only a count — so the caller learns
// how many memories the victim has as well.
func TestReindex_AgentCannotTouchForeignWorkspace(t *testing.T) {
	ownWS, foreignWS := uuid.New(), uuid.New()

	embedded := false
	ms := &MockMemoryService{
		BatchEmbedFunc: func(_ context.Context, _ uuid.UUID) (int, error) {
			embedded = true
			return 7, nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/memories/reindex?workspace_id="+foreignWS.String(), http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ownWS)

	if err := h.Reindex(c); err != nil {
		t.Fatalf("Reindex returned error: %v", err)
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-tenant reindex: got status %d, want 403 (body: %s)", rec.Code, rec.Body.String())
	}
	if embedded {
		t.Error("SECURITY: a foreign workspace's memories were re-embedded")
	}
}

// TestBackfillChunks_AgentCannotTouchForeignWorkspace: same shape as Reindex, over
// the chunked embed path.
func TestBackfillChunks_AgentCannotTouchForeignWorkspace(t *testing.T) {
	ownWS, foreignWS := uuid.New(), uuid.New()

	chunked := false
	ms := &MockMemoryService{
		BackfillChunksFunc: func(_ context.Context, _ uuid.UUID, _ int) (int, error) {
			chunked = true
			return 3, nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/memories/backfill-chunks?workspace_id="+foreignWS.String(), http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ownWS)

	if err := h.BackfillChunks(c); err != nil {
		t.Fatalf("BackfillChunks returned error: %v", err)
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-tenant backfill: got status %d, want 403 (body: %s)", rec.Code, rec.Body.String())
	}
	if chunked {
		t.Error("SECURITY: a foreign workspace's memories were chunked and re-embedded")
	}
}

// TestRecallGraph_AgentCannotReadForeignWorkspace: GET /memories/recall_graph
// returns memory content, and it also memoises what it computes in a package-level
// cache. Both halves depend on the same check.
func TestRecallGraph_AgentCannotReadForeignWorkspace(t *testing.T) {
	ownWS, foreignWS := uuid.New(), uuid.New()

	traversed := false
	ms := &MockMemoryService{
		RecallGraphFunc: func(_ context.Context, _ domain.RecallGraphOpts) ([]domain.RecallGraphResult, error) {
			traversed = true
			return nil, nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/memories/recall_graph?query=deploy&workspace_id="+foreignWS.String(), http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ownWS)

	if err := h.RecallGraph(c); err != nil {
		t.Fatalf("RecallGraph returned error: %v", err)
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-tenant recall_graph: got status %d, want 403 (body: %s)", rec.Code, rec.Body.String())
	}
	if traversed {
		t.Error("SECURITY: the graph was traversed for a foreign workspace")
	}
}

// TestRecallGraph_AgentOwnWorkspaceIsWhatReachesTheCacheKey pins the reasoning that
// makes recallGraphQuery's task_id and project_id safe.
//
// The handler writes into a process-wide cache, so "it is a GET" is not what makes
// it sound — the workspace inside the cache key is. What must reach the service is
// the workspace the guard resolved, never the raw parameter, because that value is
// what separates one tenant's cache bucket from another's.
func TestRecallGraph_AgentOwnWorkspaceIsWhatReachesTheCacheKey(t *testing.T) {
	ownWS := uuid.New()
	foreignTask := uuid.New()

	var gotOpts domain.RecallGraphOpts
	ms := &MockMemoryService{
		RecallGraphFunc: func(_ context.Context, opts domain.RecallGraphOpts) ([]domain.RecallGraphResult, error) {
			gotOpts = opts
			return nil, nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/memories/recall_graph?query=deploy&task_id="+foreignTask.String(), http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ownWS)

	if err := h.RecallGraph(c); err != nil {
		t.Fatalf("RecallGraph returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("own-workspace recall_graph: got status %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if gotOpts.WorkspaceID != ownWS {
		t.Errorf("the cache key would be built from workspace %s, want the agent's own %s",
			gotOpts.WorkspaceID, ownWS)
	}
}

// TestGetCurrentUserTasks_AgentKeyIsRefused covers the one site whose workspace_id
// nothing authorizes.
//
// GET /me/tasks passes ?workspace_id= straight into the SQL. That is sound only
// because the pin is the assignee, taken from the auth context — a caller gets
// their own tasks in the named workspace, which is nothing unless they are in it.
// The reasoning depends entirely on the actor being the human the tasks are
// assigned to, so an agent key must not reach the query at all: assignee_type is
// 'user' in that WHERE clause and an agent id would match no row, but relying on
// that would be relying on a coincidence of the schema rather than on the check.
func TestGetCurrentUserTasks_AgentKeyIsRefused(t *testing.T) {
	listed := false
	ts := &MockTaskService{
		GetUserActiveTasksFunc: func(context.Context, uuid.UUID, uuid.UUID, pagination.Params) (*pagination.Page[domain.Task], error) {
			listed = true
			return nil, nil
		},
	}
	h := NewTaskHandler(ts)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/me/tasks?workspace_id="+uuid.New().String(), http.NoBody)
	agentID := uuid.New()
	req = req.WithContext(actorctx.WithActor(req.Context(), agentID, domain.ActorTypeAgent))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.GetCurrentUserTasks(c)
	if err == nil && rec.Code != http.StatusUnauthorized {
		t.Errorf("agent key on /me/tasks: got status %d and no error, want a refusal", rec.Code)
	}
	if listed {
		t.Error("SECURITY: an agent key reached the /me/tasks query, whose only tenant pin is the assignee")
	}
}
