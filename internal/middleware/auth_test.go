package middleware

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// ---------------------------------------------------------------------------
// In-memory mock repositories for auth.Service
// ---------------------------------------------------------------------------

type mockUserRepo struct {
	mu    sync.RWMutex
	users map[uuid.UUID]*domain.User
}

func newMockUserRepo() *mockUserRepo {
	return &mockUserRepo{users: make(map[uuid.UUID]*domain.User)}
}

func (r *mockUserRepo) Create(_ context.Context, user *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users[user.ID] = user
	return nil
}

func (r *mockUserRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	u, ok := r.users[id]
	if !ok {
		return nil, nil
	}
	return u, nil
}

func (r *mockUserRepo) GetByEmail(_ context.Context, email string) (*domain.User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, u := range r.users {
		if u.Email == email {
			return u, nil
		}
	}
	return nil, nil
}

func (r *mockUserRepo) Update(_ context.Context, user *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users[user.ID] = user
	return nil
}

func (r *mockUserRepo) SearchUsers(_ context.Context, _ string, _ int) ([]domain.User, error) {
	return nil, nil
}

type mockRefreshTokenRepo struct {
	mu     sync.RWMutex
	tokens map[string]*repository.RefreshToken
}

func newMockRefreshTokenRepo() *mockRefreshTokenRepo {
	return &mockRefreshTokenRepo{tokens: make(map[string]*repository.RefreshToken)}
}

func (r *mockRefreshTokenRepo) Create(_ context.Context, userID uuid.UUID, tokenHash string, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens[tokenHash] = &repository.RefreshToken{
		ID:        uuid.New(),
		UserID:    userID,
		TokenHash: tokenHash,
		ExpiresAt: expiresAt,
		CreatedAt: time.Now(),
	}
	return nil
}

func (r *mockRefreshTokenRepo) GetByHash(_ context.Context, tokenHash string) (*repository.RefreshToken, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tokens[tokenHash]
	if !ok {
		return nil, nil
	}
	return t, nil
}

func (r *mockRefreshTokenRepo) RevokeByUserID(_ context.Context, userID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for _, t := range r.tokens {
		if t.UserID == userID {
			t.RevokedAt = &now
		}
	}
	return nil
}

func (r *mockRefreshTokenRepo) RevokeByHash(_ context.Context, tokenHash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tokens[tokenHash]; ok {
		now := time.Now()
		t.RevokedAt = &now
	}
	return nil
}

func (r *mockRefreshTokenRepo) DeleteExpired(_ context.Context) error { return nil }

type mockWorkspaceRepo struct {
	mu    sync.RWMutex
	items map[uuid.UUID]*domain.Workspace
}

func newMockWorkspaceRepo() *mockWorkspaceRepo {
	return &mockWorkspaceRepo{items: make(map[uuid.UUID]*domain.Workspace)}
}

func (r *mockWorkspaceRepo) Create(_ context.Context, ws *domain.Workspace) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[ws.ID] = ws
	return nil
}

func (r *mockWorkspaceRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Workspace, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ws, ok := r.items[id]
	if !ok {
		return nil, nil
	}
	return ws, nil
}

func (r *mockWorkspaceRepo) GetBySlug(_ context.Context, slug string) (*domain.Workspace, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, ws := range r.items {
		if ws.Slug == slug {
			return ws, nil
		}
	}
	return nil, nil
}

func (r *mockWorkspaceRepo) Update(_ context.Context, ws *domain.Workspace) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[ws.ID] = ws
	return nil
}

func (r *mockWorkspaceRepo) Delete(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.items, id)
	return nil
}

func (r *mockWorkspaceRepo) ListByOwner(_ context.Context, ownerID uuid.UUID) ([]domain.Workspace, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []domain.Workspace
	for _, ws := range r.items {
		if ws.OwnerID == ownerID {
			result = append(result, *ws)
		}
	}
	return result, nil
}

type mockWorkspaceMemberRepo struct {
	mu    sync.RWMutex
	items map[uuid.UUID]*domain.WorkspaceMember
}

func newMockWorkspaceMemberRepo() *mockWorkspaceMemberRepo {
	return &mockWorkspaceMemberRepo{items: make(map[uuid.UUID]*domain.WorkspaceMember)}
}

func (r *mockWorkspaceMemberRepo) Create(_ context.Context, m *domain.WorkspaceMember) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[m.ID] = m
	return nil
}

func (r *mockWorkspaceMemberRepo) GetByWorkspaceAndUser(_ context.Context, wsID, userID uuid.UUID) (*domain.WorkspaceMember, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, m := range r.items {
		if m.WorkspaceID == wsID && m.UserID == userID {
			return m, nil
		}
	}
	return nil, nil
}

func (r *mockWorkspaceMemberRepo) GetRole(_ context.Context, wsID, userID uuid.UUID) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, m := range r.items {
		if m.WorkspaceID == wsID && m.UserID == userID {
			return m.Role, nil
		}
	}
	return "", fmt.Errorf("user is not a member of this workspace")
}

func (r *mockWorkspaceMemberRepo) List(_ context.Context, _ uuid.UUID) ([]domain.WorkspaceMemberWithUser, error) {
	return nil, nil
}

func (r *mockWorkspaceMemberRepo) UpdateRole(_ context.Context, _, _ uuid.UUID, _ string) error {
	return nil
}

func (r *mockWorkspaceMemberRepo) Delete(_ context.Context, _, _ uuid.UUID) error {
	return nil
}

func (r *mockWorkspaceMemberRepo) CountOwners(_ context.Context, _ uuid.UUID) (int, error) {
	return 0, nil
}

func (r *mockWorkspaceMemberRepo) ListWithProjects(_ context.Context, _ uuid.UUID) ([]repository.HumanWithProjects, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Mock AgentService for AgentKeyAuth tests
// ---------------------------------------------------------------------------

type mockAgentService struct {
	AuthenticateFunc func(ctx context.Context, workspaceSlug, apiKey string) (*domain.Agent, error)
}

func (m *mockAgentService) Register(_ context.Context, _ service.RegisterAgentInput) (*service.RegisterAgentOutput, error) {
	return nil, nil
}
func (m *mockAgentService) GetByID(_ context.Context, _ uuid.UUID) (*domain.Agent, error) {
	return nil, nil
}
func (m *mockAgentService) Update(_ context.Context, _ *domain.Agent) error { return nil }
func (m *mockAgentService) Delete(_ context.Context, _ uuid.UUID) error     { return nil }
func (m *mockAgentService) List(_ context.Context, _ uuid.UUID, _ repository.AgentFilter, _ pagination.Params) (*pagination.Page[domain.Agent], error) {
	return nil, nil
}
func (m *mockAgentService) Heartbeat(_ context.Context, _ uuid.UUID) error { return nil }
func (m *mockAgentService) RotateAPIKey(_ context.Context, _ uuid.UUID) (string, error) {
	return "", nil
}
func (m *mockAgentService) ListSubAgents(_ context.Context, _ uuid.UUID, _ bool) ([]domain.Agent, error) {
	return nil, nil
}

func (m *mockAgentService) Authenticate(ctx context.Context, workspaceSlug, apiKey string) (*domain.Agent, error) {
	if m.AuthenticateFunc != nil {
		return m.AuthenticateFunc(ctx, workspaceSlug, apiKey)
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const testJWTSecret = "test-secret-key-for-jwt-signing-32b"

func newTestAuthService() *auth.Service {
	return auth.NewService(
		newMockUserRepo(),
		newMockRefreshTokenRepo(),
		newMockWorkspaceRepo(),
		newMockWorkspaceMemberRepo(),
		testJWTSecret,
	)
}

// registerAndGetToken registers a user and returns a valid access token.
func registerAndGetToken(t *testing.T, svc *auth.Service) string {
	t.Helper()
	_, tokens, err := svc.Register(context.Background(), "mw-test@example.com", "StrongP4ss", "MW User")
	require.NoError(t, err)
	return tokens.AccessToken
}

// newEchoContext creates a minimal Echo context from a request and recorder.
func newEchoContext(req *http.Request, rec *httptest.ResponseRecorder) echo.Context {
	e := echo.New()
	return e.NewContext(req, rec)
}

// ---------------------------------------------------------------------------
// Tests: JWTAuth
// ---------------------------------------------------------------------------

func TestJWTAuth_ValidToken(t *testing.T) {
	svc := newTestAuthService()
	token := registerAndGetToken(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	c := newEchoContext(req, rec)

	var handlerCalled bool
	handler := JWTAuth(svc)(func(c echo.Context) error {
		handlerCalled = true
		return c.NoContent(http.StatusOK)
	})

	err := handler(c)
	require.NoError(t, err)
	assert.True(t, handlerCalled)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Verify context values.
	userID, err := GetUserID(c)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, userID)
	assert.Equal(t, AuthTypeUser, c.Get(ContextKeyAuthType))
}

func TestJWTAuth_NoToken(t *testing.T) {
	svc := newTestAuthService()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	rec := httptest.NewRecorder()
	c := newEchoContext(req, rec)

	handler := JWTAuth(svc)(func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	err := handler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestJWTAuth_ExpiredToken(t *testing.T) {
	// Create a service and register a user with expired token.
	svc := auth.NewService(
		newMockUserRepo(),
		newMockRefreshTokenRepo(),
		newMockWorkspaceRepo(),
		newMockWorkspaceMemberRepo(),
		testJWTSecret,
	)

	// We cannot easily create an expired token without exposing internal clock.
	// Instead, use a token signed with a wrong secret (simulates invalid).
	wrongSvc := auth.NewService(
		newMockUserRepo(),
		newMockRefreshTokenRepo(),
		newMockWorkspaceRepo(),
		newMockWorkspaceMemberRepo(),
		"wrong-secret-key-for-testing-only",
	)
	_, tokens, err := wrongSvc.Register(context.Background(), "expired@example.com", "StrongP4ss", "User")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	rec := httptest.NewRecorder()
	c := newEchoContext(req, rec)

	handler := JWTAuth(svc)(func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	err = handler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestJWTAuth_InvalidBearerFormat(t *testing.T) {
	svc := newTestAuthService()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Basic dGVzdDp0ZXN0") // not Bearer
	rec := httptest.NewRecorder()
	c := newEchoContext(req, rec)

	handler := JWTAuth(svc)(func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	err := handler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// ---------------------------------------------------------------------------
// Tests: AgentKeyAuth
// ---------------------------------------------------------------------------

func TestAgentKeyAuth_ValidKey(t *testing.T) {
	agentID := uuid.New()
	wsID := uuid.New()

	agentSvc := &mockAgentService{
		AuthenticateFunc: func(_ context.Context, _, _ string) (*domain.Agent, error) {
			return &domain.Agent{ID: agentID, WorkspaceID: wsID}, nil
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	req.Header.Set("X-Agent-Key", "agk_my-workspace_abcdefghijklmnopqrstuvwxyz123456")
	rec := httptest.NewRecorder()
	c := newEchoContext(req, rec)

	var handlerCalled bool
	handler := AgentKeyAuth(agentSvc)(func(c echo.Context) error {
		handlerCalled = true
		return c.NoContent(http.StatusOK)
	})

	err := handler(c)
	require.NoError(t, err)
	assert.True(t, handlerCalled)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Verify context values.
	gotAgentID, err := GetAgentID(c)
	require.NoError(t, err)
	assert.Equal(t, agentID, gotAgentID)

	gotWsID, err := GetWorkspaceID(c)
	require.NoError(t, err)
	assert.Equal(t, wsID, gotWsID)

	assert.True(t, IsAgent(c))
}

func TestAgentKeyAuth_InvalidKey(t *testing.T) {
	agentSvc := &mockAgentService{
		AuthenticateFunc: func(_ context.Context, _, _ string) (*domain.Agent, error) {
			return nil, assert.AnError
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	req.Header.Set("X-Agent-Key", "agk_my-workspace_invalidkey1234567890abcdef")
	rec := httptest.NewRecorder()
	c := newEchoContext(req, rec)

	handler := AgentKeyAuth(agentSvc)(func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	err := handler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAgentKeyAuth_MissingHeader(t *testing.T) {
	agentSvc := &mockAgentService{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	rec := httptest.NewRecorder()
	c := newEchoContext(req, rec)

	handler := AgentKeyAuth(agentSvc)(func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	err := handler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAgentKeyAuth_BadKeyFormat(t *testing.T) {
	agentSvc := &mockAgentService{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	req.Header.Set("X-Agent-Key", "not_an_agent_key")
	rec := httptest.NewRecorder()
	c := newEchoContext(req, rec)

	handler := AgentKeyAuth(agentSvc)(func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	err := handler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// ---------------------------------------------------------------------------
// Tests: Context helpers
// ---------------------------------------------------------------------------

func TestGetUserID_Present(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	id := uuid.New()
	c.Set(ContextKeyUserID, id)

	got, err := GetUserID(c)
	require.NoError(t, err)
	assert.Equal(t, id, got)
}

func TestGetUserID_Missing(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	_, err := GetUserID(c)
	require.Error(t, err)
}

func TestGetWorkspaceID_Present(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	id := uuid.New()
	c.Set(ContextKeyWorkspaceID, id)

	got, err := GetWorkspaceID(c)
	require.NoError(t, err)
	assert.Equal(t, id, got)
}

func TestGetWorkspaceID_Missing(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	_, err := GetWorkspaceID(c)
	require.Error(t, err)
}

func TestGetAgentID_Present(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	id := uuid.New()
	c.Set(ContextKeyAgentID, id)

	got, err := GetAgentID(c)
	require.NoError(t, err)
	assert.Equal(t, id, got)
}

func TestGetAgentID_Missing(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	_, err := GetAgentID(c)
	require.Error(t, err)
}

func TestIsAgent_True(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(ContextKeyAuthType, AuthTypeAgent)

	assert.True(t, IsAgent(c))
}

func TestIsAgent_False_User(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(ContextKeyAuthType, AuthTypeUser)

	assert.False(t, IsAgent(c))
}

func TestIsAgent_False_NotSet(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	assert.False(t, IsAgent(c))
}

// ---------------------------------------------------------------------------
// Tests: parseWorkspaceSlugFromKey
// ---------------------------------------------------------------------------

func TestParseWorkspaceSlugFromKey(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		want    string
		wantErr bool
	}{
		{"valid simple", "agk_myworkspace_abcdef1234567890", "myworkspace", false},
		{"valid with hyphens", "agk_my-workspace_abcdef1234567890", "my-workspace", false},
		{"no agk prefix", "xyz_myworkspace_abcdef", "", true},
		{"missing random part", "agk_myworkspace", "", true},
		{"empty slug", "agk__abcdef", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseWorkspaceSlugFromKey(tt.key)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: OptionalAuth
// ---------------------------------------------------------------------------

func TestOptionalAuth_NoAuth(t *testing.T) {
	svc := newTestAuthService()
	agentSvc := &mockAgentService{}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := newEchoContext(req, rec)

	var handlerCalled bool
	handler := OptionalAuth(svc, agentSvc)(func(c echo.Context) error {
		handlerCalled = true
		return c.NoContent(http.StatusOK)
	})

	err := handler(c)
	require.NoError(t, err)
	assert.True(t, handlerCalled)
	assert.Equal(t, http.StatusOK, rec.Code)

	// No auth type should be set.
	assert.Nil(t, c.Get(ContextKeyAuthType))
}

func TestOptionalAuth_WithJWT(t *testing.T) {
	svc := newTestAuthService()
	token := registerAndGetToken(t, svc)
	agentSvc := &mockAgentService{}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	c := newEchoContext(req, rec)

	var handlerCalled bool
	handler := OptionalAuth(svc, agentSvc)(func(c echo.Context) error {
		handlerCalled = true
		return c.NoContent(http.StatusOK)
	})

	err := handler(c)
	require.NoError(t, err)
	assert.True(t, handlerCalled)
	assert.Equal(t, AuthTypeUser, c.Get(ContextKeyAuthType))
}

func TestOptionalAuth_WithAgentKey(t *testing.T) {
	svc := newTestAuthService()
	agentID := uuid.New()
	wsID := uuid.New()
	agentSvc := &mockAgentService{
		AuthenticateFunc: func(_ context.Context, _, _ string) (*domain.Agent, error) {
			return &domain.Agent{ID: agentID, WorkspaceID: wsID}, nil
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Agent-Key", "agk_test-ws_abcdefghijklmnopqrstuvwxyz123456")
	rec := httptest.NewRecorder()
	c := newEchoContext(req, rec)

	var handlerCalled bool
	handler := OptionalAuth(svc, agentSvc)(func(c echo.Context) error {
		handlerCalled = true
		return c.NoContent(http.StatusOK)
	})

	err := handler(c)
	require.NoError(t, err)
	assert.True(t, handlerCalled)
	assert.Equal(t, AuthTypeAgent, c.Get(ContextKeyAuthType))
}
