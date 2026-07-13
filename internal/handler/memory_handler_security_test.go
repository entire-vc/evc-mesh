package handler

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// Cross-tenant memory reads/writes: an agent key from workspace A must never be
// able to reach workspace B's memories by passing ?workspace_id=B (or a
// workspace_id body field). The memories table has no RLS policy, so the handler
// is the only tenant boundary — these tests pin it.

// mockWorkspaceMemberRepo implements repository.WorkspaceMemberRepository.
// members maps "<wsID>/<userID>" -> role.
type mockWorkspaceMemberRepo struct {
	members map[string]string
}

func (m *mockWorkspaceMemberRepo) GetRole(_ context.Context, wsID, userID uuid.UUID) (string, error) {
	if role, ok := m.members[fmt.Sprintf("%s/%s", wsID, userID)]; ok {
		return role, nil
	}
	return "", sql.ErrNoRows
}

func (m *mockWorkspaceMemberRepo) Create(context.Context, *domain.WorkspaceMember) error { return nil }
func (m *mockWorkspaceMemberRepo) GetByWorkspaceAndUser(context.Context, uuid.UUID, uuid.UUID) (*domain.WorkspaceMember, error) {
	return nil, nil
}
func (m *mockWorkspaceMemberRepo) List(context.Context, uuid.UUID) ([]domain.WorkspaceMemberWithUser, error) {
	return nil, nil
}
func (m *mockWorkspaceMemberRepo) ListWithProjects(context.Context, uuid.UUID) ([]repository.HumanWithProjects, error) {
	return nil, nil
}
func (m *mockWorkspaceMemberRepo) UpdateRole(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}
func (m *mockWorkspaceMemberRepo) Delete(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (m *mockWorkspaceMemberRepo) CountOwners(context.Context, uuid.UUID) (int, error) {
	return 0, nil
}

// asAgent authenticates the request context as an agent belonging to ownWS.
func asAgent(c echo.Context, agentID, ownWS uuid.UUID) {
	c.Set("auth_type", "agent")
	c.Set("agent_id", agentID)
	c.Set("workspace_id", ownWS)
}

// asUser authenticates the request context as a user (no workspace on the token).
func asUser(c echo.Context, userID uuid.UUID) {
	c.Set("auth_type", "user")
	c.Set("user_id", userID)
}

// TestSearch_AgentCannotReadForeignWorkspace is the direct regression test for the
// live cross-tenant read: agent key of workspace A + ?workspace_id=B.
func TestSearch_AgentCannotReadForeignWorkspace(t *testing.T) {
	ownWS := uuid.New()
	foreignWS := uuid.New()

	recallCalled := false
	ms := &MockMemoryService{
		RecallFunc: func(_ context.Context, opts domain.RecallOpts) ([]domain.ScoredMemory, domain.SearchMode, error) {
			recallCalled = true
			return nil, domain.SearchModeHybrid, nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/memories/search?q=session&workspace_id="+foreignWS.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ownWS)

	if err := h.Search(c); err != nil {
		t.Fatalf("Search returned error: %v", err)
	}

	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-tenant search: got status %d, want 403 (body: %s)", rec.Code, rec.Body.String())
	}
	if recallCalled {
		t.Error("SECURITY: the service was queried for a foreign workspace — the request must be rejected before it reaches the data layer")
	}
}

// TestSearch_AgentOwnWorkspaceStillWorks guards against the fix over-rejecting:
// passing your OWN workspace_id explicitly must still be honoured.
func TestSearch_AgentOwnWorkspaceStillWorks(t *testing.T) {
	ownWS := uuid.New()

	var gotWS uuid.UUID
	ms := &MockMemoryService{
		RecallFunc: func(_ context.Context, opts domain.RecallOpts) ([]domain.ScoredMemory, domain.SearchMode, error) {
			gotWS = opts.WorkspaceID
			return []domain.ScoredMemory{}, domain.SearchModeHybrid, nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/memories/search?q=session&workspace_id="+ownWS.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ownWS)

	if err := h.Search(c); err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("own-workspace search: got status %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if gotWS != ownWS {
		t.Errorf("service queried workspace %s, want own workspace %s", gotWS, ownWS)
	}
}

// TestSearch_AgentNoParamDefaultsToOwnWorkspace: omitting workspace_id must still
// fall back to the key's workspace (the pre-existing, correct behaviour).
func TestSearch_AgentNoParamDefaultsToOwnWorkspace(t *testing.T) {
	ownWS := uuid.New()

	var gotWS uuid.UUID
	ms := &MockMemoryService{
		RecallFunc: func(_ context.Context, opts domain.RecallOpts) ([]domain.ScoredMemory, domain.SearchMode, error) {
			gotWS = opts.WorkspaceID
			return []domain.ScoredMemory{}, domain.SearchModeHybrid, nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/memories/search?q=session", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ownWS)

	if err := h.Search(c); err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("default-workspace search: got status %d, want 200", rec.Code)
	}
	if gotWS != ownWS {
		t.Errorf("service queried workspace %s, want own workspace %s", gotWS, ownWS)
	}
}

// TestList_AgentCannotReadForeignWorkspace covers GET /memories.
func TestList_AgentCannotReadForeignWorkspace(t *testing.T) {
	ownWS := uuid.New()
	foreignWS := uuid.New()

	listCalled := false
	ms := &MockMemoryService{
		ListMemoriesFunc: func(_ context.Context, f domain.MemoryListFilter) (*service.RecallResult, error) {
			listCalled = true
			return &service.RecallResult{Items: []domain.ScoredMemory{}}, nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/memories?workspace_id="+foreignWS.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ownWS)

	if err := h.List(c); err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-tenant list: got status %d, want 403", rec.Code)
	}
	if listCalled {
		t.Error("SECURITY: service queried for a foreign workspace")
	}
}

// TestRemember_AgentCannotWriteIntoForeignWorkspace covers the WRITE half of the
// hole: workspace_id supplied in the POST body.
func TestRemember_AgentCannotWriteIntoForeignWorkspace(t *testing.T) {
	ownWS := uuid.New()
	foreignWS := uuid.New()

	wrote := false
	ms := &MockMemoryService{
		RememberFunc: func(_ context.Context, mem *domain.Memory) (service.RememberResult, error) {
			wrote = true
			return service.RememberResult{Outcome: "created"}, nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	body := fmt.Sprintf(`{"workspace_id":%q,"key":"pwn","content":"injected","scope":"workspace"}`, foreignWS)
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memories", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ownWS)

	if err := h.Remember(c); err != nil {
		t.Fatalf("Remember returned error: %v", err)
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-tenant write: got status %d, want 403", rec.Code)
	}
	if wrote {
		t.Error("SECURITY: a memory was written into a foreign workspace")
	}
}

// TestRemember_AgentWritesIntoOwnWorkspace guards the happy path.
func TestRemember_AgentWritesIntoOwnWorkspace(t *testing.T) {
	ownWS := uuid.New()

	var gotWS uuid.UUID
	ms := &MockMemoryService{
		RememberFunc: func(_ context.Context, mem *domain.Memory) (service.RememberResult, error) {
			gotWS = mem.WorkspaceID
			return service.RememberResult{Outcome: "created"}, nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memories",
		strings.NewReader(`{"key":"k","content":"c","scope":"workspace"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ownWS)

	if err := h.Remember(c); err != nil {
		t.Fatalf("Remember returned error: %v", err)
	}
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Errorf("own-workspace write: got status %d, want 2xx (body: %s)", rec.Code, rec.Body.String())
	}
	if gotWS != ownWS {
		t.Errorf("memory written to workspace %s, want %s", gotWS, ownWS)
	}
}

// TestGetByID_AgentCannotReadForeignMemory: /memories/:id carries no workspace
// param at all and the repo query is WHERE id = $1, so the handler must reject.
// 404 (not 403) so the endpoint cannot be used to probe which IDs exist.
func TestGetByID_AgentCannotReadForeignMemory(t *testing.T) {
	ownWS := uuid.New()
	foreignWS := uuid.New()
	memID := uuid.New()

	ms := &MockMemoryService{
		GetByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
			return &domain.Memory{ID: id, WorkspaceID: foreignWS, Key: "secret", Content: "tenant B data"}, nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/memories/"+memID.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(memID.String())
	asAgent(c, uuid.New(), ownWS)

	if err := h.GetByID(c); err != nil {
		t.Fatalf("GetByID returned error: %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant get-by-id: got status %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "tenant B data") {
		t.Error("SECURITY: foreign workspace memory content leaked in the response body")
	}
}

// TestDelete_AgentCannotDeleteForeignMemory: Forget() is reached by primary key,
// so a foreign memory must be refused before the service is asked to delete it.
func TestDelete_AgentCannotDeleteForeignMemory(t *testing.T) {
	ownWS := uuid.New()
	foreignWS := uuid.New()
	memID := uuid.New()

	deleted := false
	ms := &MockMemoryService{
		GetByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
			return &domain.Memory{ID: id, WorkspaceID: foreignWS}, nil
		},
		ForgetFunc: func(_ context.Context, id uuid.UUID, _ *uuid.UUID, _ bool) error {
			deleted = true
			return nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/memories/"+memID.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(memID.String())
	asAgent(c, uuid.New(), ownWS)

	if err := h.Delete(c); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant delete: got status %d, want 404", rec.Code)
	}
	if deleted {
		t.Error("SECURITY: a foreign workspace's memory was deleted")
	}
}

// TestDelete_UserCannotDeleteForeignMemory: a user JWT is treated as admin for
// memory deletion, which made this destructive across tenants until the
// membership check gated it.
func TestDelete_UserCannotDeleteForeignMemory(t *testing.T) {
	foreignWS := uuid.New()
	userID := uuid.New()
	memID := uuid.New()

	deleted := false
	ms := &MockMemoryService{
		GetByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
			return &domain.Memory{ID: id, WorkspaceID: foreignWS}, nil
		},
		ForgetFunc: func(_ context.Context, id uuid.UUID, _ *uuid.UUID, isAdmin bool) error {
			deleted = true
			return nil
		},
	}
	// The user is a member of nothing.
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{members: map[string]string{}})

	e := echo.New()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/memories/"+memID.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(memID.String())
	asUser(c, userID)

	if err := h.Delete(c); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("non-member user delete: got status %d, want 404", rec.Code)
	}
	if deleted {
		t.Error("SECURITY: a non-member user deleted another workspace's memory")
	}
}

// TestSearch_UserMustBeWorkspaceMember: user tokens carry no workspace, so
// workspace_id is load-bearing for them — but only for workspaces they belong to.
func TestSearch_UserMustBeWorkspaceMember(t *testing.T) {
	memberWS := uuid.New()
	foreignWS := uuid.New()
	userID := uuid.New()

	members := &mockWorkspaceMemberRepo{
		members: map[string]string{
			fmt.Sprintf("%s/%s", memberWS, userID): "admin",
		},
	}

	t.Run("member workspace allowed", func(t *testing.T) {
		var gotWS uuid.UUID
		ms := &MockMemoryService{
			RecallFunc: func(_ context.Context, opts domain.RecallOpts) ([]domain.ScoredMemory, domain.SearchMode, error) {
				gotWS = opts.WorkspaceID
				return []domain.ScoredMemory{}, domain.SearchModeHybrid, nil
			},
		}
		h := NewMemoryHandler(ms, members)

		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/memories/search?q=x&workspace_id="+memberWS.String(), nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		asUser(c, userID)

		if err := h.Search(c); err != nil {
			t.Fatalf("Search returned error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Errorf("member search: got status %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		if gotWS != memberWS {
			t.Errorf("queried workspace %s, want %s", gotWS, memberWS)
		}
	})

	t.Run("non-member workspace denied", func(t *testing.T) {
		called := false
		ms := &MockMemoryService{
			RecallFunc: func(_ context.Context, opts domain.RecallOpts) ([]domain.ScoredMemory, domain.SearchMode, error) {
				called = true
				return nil, domain.SearchModeHybrid, nil
			},
		}
		h := NewMemoryHandler(ms, members)

		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/memories/search?q=x&workspace_id="+foreignWS.String(), nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		asUser(c, userID)

		if err := h.Search(c); err != nil {
			t.Fatalf("Search returned error: %v", err)
		}
		if rec.Code != http.StatusForbidden {
			t.Errorf("non-member search: got status %d, want 403", rec.Code)
		}
		if called {
			t.Error("SECURITY: non-member user reached another workspace's memories")
		}
	})
}

// TestExportMemories_AgentCannotDumpForeignWorkspace — export is the highest-value
// read of the family: one call returns an entire workspace's memories.
func TestExportMemories_AgentCannotDumpForeignWorkspace(t *testing.T) {
	ownWS := uuid.New()
	foreignWS := uuid.New()

	h := NewMemoryHandler(&MockMemoryService{}, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/memories/export?workspace_id="+foreignWS.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ownWS)

	if err := h.ExportMemories(c); err != nil {
		t.Fatalf("ExportMemories returned error: %v", err)
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-tenant export: got status %d, want 403", rec.Code)
	}
}

// TestUnauthenticatedCallerRejected: with no auth context at all, a bare
// workspace_id must not be honoured.
func TestSearch_UnauthenticatedCallerRejected(t *testing.T) {
	someWS := uuid.New()

	called := false
	ms := &MockMemoryService{
		RecallFunc: func(_ context.Context, opts domain.RecallOpts) ([]domain.ScoredMemory, domain.SearchMode, error) {
			called = true
			return nil, domain.SearchModeHybrid, nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/memories/search?q=x&workspace_id="+someWS.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	// no auth context set

	if err := h.Search(c); err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if rec.Code == http.StatusOK {
		t.Errorf("unauthenticated search returned 200; want a denial (body: %s)", rec.Body.String())
	}
	if called {
		t.Error("SECURITY: unauthenticated caller reached the memory service")
	}
}
