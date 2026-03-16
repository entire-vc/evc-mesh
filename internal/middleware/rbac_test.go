package middleware

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// ---------------------------------------------------------------------------
// Minimal mock for WorkspaceMemberRepository — only GetRole is needed here.
// The full mock in auth_test.go is in the same package and would conflict on
// type name, so we use a distinct name: rbacMockMemberRepo.
// ---------------------------------------------------------------------------

type rbacMockMemberRepo struct {
	mu    sync.RWMutex
	items map[uuid.UUID]*domain.WorkspaceMember
}

func newRBACMockMemberRepo() *rbacMockMemberRepo {
	return &rbacMockMemberRepo{items: make(map[uuid.UUID]*domain.WorkspaceMember)}
}

func (r *rbacMockMemberRepo) Create(_ context.Context, m *domain.WorkspaceMember) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[m.ID] = m
	return nil
}

func (r *rbacMockMemberRepo) GetByWorkspaceAndUser(_ context.Context, wsID, userID uuid.UUID) (*domain.WorkspaceMember, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, m := range r.items {
		if m.WorkspaceID == wsID && m.UserID == userID {
			return m, nil
		}
	}
	return nil, nil
}

func (r *rbacMockMemberRepo) GetRole(_ context.Context, wsID, userID uuid.UUID) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, m := range r.items {
		if m.WorkspaceID == wsID && m.UserID == userID {
			return m.Role, nil
		}
	}
	return "", fmt.Errorf("user is not a member of this workspace")
}

func (r *rbacMockMemberRepo) List(_ context.Context, _ uuid.UUID) ([]domain.WorkspaceMemberWithUser, error) {
	return nil, nil
}

func (r *rbacMockMemberRepo) UpdateRole(_ context.Context, _, _ uuid.UUID, _ string) error {
	return nil
}

func (r *rbacMockMemberRepo) Delete(_ context.Context, _, _ uuid.UUID) error {
	return nil
}

func (r *rbacMockMemberRepo) CountOwners(_ context.Context, _ uuid.UUID) (int, error) {
	return 0, nil
}

func (r *rbacMockMemberRepo) ListWithProjects(_ context.Context, _ uuid.UUID) ([]repository.HumanWithProjects, error) {
	return nil, nil
}

// addMember is a helper to add a workspace member to the mock repo.
func (r *rbacMockMemberRepo) addMember(wsID, userID uuid.UUID, role string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[uuid.New()] = &domain.WorkspaceMember{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		UserID:      userID,
		Role:        role,
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newRBACEchoContext creates an Echo context pre-wired with a user ID and
// workspace ID (simulating what DualAuth + WorkspaceRLS would set).
func newRBACEchoContext(userID, wsID uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(ContextKeyAuthType, AuthTypeUser)
	c.Set(ContextKeyUserID, userID)
	c.Set(ContextKeyWorkspaceID, wsID)
	return c, rec
}

// newRBACAgentContext creates an Echo context pre-wired as an agent.
func newRBACAgentContext(agentID, wsID uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(ContextKeyAuthType, AuthTypeAgent)
	c.Set(ContextKeyAgentID, agentID)
	c.Set(ContextKeyWorkspaceID, wsID)
	return c, rec
}

// okHandler is a trivial handler that returns 200 OK.
func okHandler(c echo.Context) error {
	return c.NoContent(http.StatusOK)
}

// ---------------------------------------------------------------------------
// Tests: owner can do everything
// ---------------------------------------------------------------------------

func TestRBAC_Owner_CanDeleteWorkspace(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID := uuid.New()
	userID := uuid.New()
	repo.addMember(wsID, userID, domain.RoleOwner)

	c, rec := newRBACEchoContext(userID, wsID)

	h := RequirePermission(PermDeleteWorkspace, repo)(okHandler)
	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRBAC_Owner_CanRegisterAgent(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID := uuid.New()
	userID := uuid.New()
	repo.addMember(wsID, userID, domain.RoleOwner)

	c, rec := newRBACEchoContext(userID, wsID)

	h := RequirePermission(PermRegisterAgent, repo)(okHandler)
	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRBAC_Owner_CanManageCF(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID := uuid.New()
	userID := uuid.New()
	repo.addMember(wsID, userID, domain.RoleOwner)

	c, rec := newRBACEchoContext(userID, wsID)

	h := RequirePermission(PermManageCF, repo)(okHandler)
	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRBAC_Owner_CanExportAuditLog(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID := uuid.New()
	userID := uuid.New()
	repo.addMember(wsID, userID, domain.RoleOwner)

	c, rec := newRBACEchoContext(userID, wsID)

	h := RequirePermission(PermExportAuditLog, repo)(okHandler)
	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// ---------------------------------------------------------------------------
// Tests: member can create tasks but not delete workspace
// ---------------------------------------------------------------------------

func TestRBAC_Member_CanCreateTask(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID := uuid.New()
	userID := uuid.New()
	repo.addMember(wsID, userID, domain.RoleMember)

	c, rec := newRBACEchoContext(userID, wsID)

	h := RequirePermission(PermCreateTask, repo)(okHandler)
	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRBAC_Member_CanCreateProject(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID := uuid.New()
	userID := uuid.New()
	repo.addMember(wsID, userID, domain.RoleMember)

	c, rec := newRBACEchoContext(userID, wsID)

	h := RequirePermission(PermCreateProject, repo)(okHandler)
	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRBAC_Member_CannotDeleteWorkspace(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID := uuid.New()
	userID := uuid.New()
	repo.addMember(wsID, userID, domain.RoleMember)

	c, rec := newRBACEchoContext(userID, wsID)

	h := RequirePermission(PermDeleteWorkspace, repo)(okHandler)
	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRBAC_Member_CannotDeleteProject(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID := uuid.New()
	userID := uuid.New()
	repo.addMember(wsID, userID, domain.RoleMember)

	c, rec := newRBACEchoContext(userID, wsID)

	h := RequirePermission(PermDeleteProject, repo)(okHandler)
	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRBAC_Member_CannotRegisterAgent(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID := uuid.New()
	userID := uuid.New()
	repo.addMember(wsID, userID, domain.RoleMember)

	c, rec := newRBACEchoContext(userID, wsID)

	h := RequirePermission(PermRegisterAgent, repo)(okHandler)
	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRBAC_Member_CannotExportAuditLog(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID := uuid.New()
	userID := uuid.New()
	repo.addMember(wsID, userID, domain.RoleMember)

	c, rec := newRBACEchoContext(userID, wsID)

	h := RequirePermission(PermExportAuditLog, repo)(okHandler)
	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// ---------------------------------------------------------------------------
// Tests: agent can create tasks but not create projects
// ---------------------------------------------------------------------------

func TestRBAC_Agent_CanCreateTask(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID := uuid.New()
	agentID := uuid.New()

	c, rec := newRBACAgentContext(agentID, wsID)

	h := RequirePermission(PermCreateTask, repo)(okHandler)
	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRBAC_Agent_CanAddComment(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID := uuid.New()
	agentID := uuid.New()

	c, rec := newRBACAgentContext(agentID, wsID)

	h := RequirePermission(PermAddComment, repo)(okHandler)
	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRBAC_Agent_CanPublishEvent(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID := uuid.New()
	agentID := uuid.New()

	c, rec := newRBACAgentContext(agentID, wsID)

	h := RequirePermission(PermPublishEvent, repo)(okHandler)
	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRBAC_Agent_CannotCreateProject(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID := uuid.New()
	agentID := uuid.New()

	c, rec := newRBACAgentContext(agentID, wsID)

	h := RequirePermission(PermCreateProject, repo)(okHandler)
	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRBAC_Agent_CannotDeleteWorkspace(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID := uuid.New()
	agentID := uuid.New()

	c, rec := newRBACAgentContext(agentID, wsID)

	h := RequirePermission(PermDeleteWorkspace, repo)(okHandler)
	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRBAC_Agent_CannotManageCF(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID := uuid.New()
	agentID := uuid.New()

	c, rec := newRBACAgentContext(agentID, wsID)

	h := RequirePermission(PermManageCF, repo)(okHandler)
	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// ---------------------------------------------------------------------------
// Tests: unauthenticated / missing context returns 403
// ---------------------------------------------------------------------------

func TestRBAC_NoAuth_ReturnsForbidden(t *testing.T) {
	repo := newRBACMockMemberRepo()

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	// No auth type set — simulates unauthenticated request that somehow reached RBAC.

	h := RequirePermission(PermCreateTask, repo)(okHandler)
	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRBAC_UserWithNoWorkspaceContext_ReturnsForbidden(t *testing.T) {
	repo := newRBACMockMemberRepo()

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	// User auth but no workspace_id in context.
	c.Set(ContextKeyAuthType, AuthTypeUser)
	c.Set(ContextKeyUserID, uuid.New())

	h := RequirePermission(PermCreateTask, repo)(okHandler)
	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRBAC_UserNotMember_ReturnsForbidden(t *testing.T) {
	repo := newRBACMockMemberRepo()
	// Do not add the user to the workspace.

	wsID := uuid.New()
	userID := uuid.New()

	c, rec := newRBACEchoContext(userID, wsID)

	h := RequirePermission(PermCreateTask, repo)(okHandler)
	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// ---------------------------------------------------------------------------
// Tests: viewer has no write permissions
// ---------------------------------------------------------------------------

func TestRBAC_Viewer_CannotCreateTask(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID := uuid.New()
	userID := uuid.New()
	repo.addMember(wsID, userID, domain.RoleViewer)

	c, rec := newRBACEchoContext(userID, wsID)

	h := RequirePermission(PermCreateTask, repo)(okHandler)
	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// ---------------------------------------------------------------------------
// Tests: hasPermission helper coverage
// ---------------------------------------------------------------------------

func TestHasPermission_UnknownRole(t *testing.T) {
	assert.False(t, hasPermission("unknown_role", PermCreateTask))
}

func TestHasPermission_OwnerHasAll(t *testing.T) {
	allPerms := []Permission{
		PermDeleteWorkspace, PermManageMembers, PermCreateProject, PermDeleteProject,
		PermRegisterAgent, PermDeleteAgent, PermCreateTask, PermUpdateTask,
		PermDeleteTask, PermAddComment, PermUploadArtifact, PermPublishEvent,
		PermManageCF, PermExportAuditLog, PermManageWebhooks,
	}
	for _, p := range allPerms {
		assert.True(t, hasPermission(domain.RoleOwner, p), "owner should have permission: %s", p)
	}
}

func TestHasPermission_AgentLimitedPerms(t *testing.T) {
	allowed := []Permission{PermCreateTask, PermUpdateTask, PermDeleteTask, PermAddComment, PermUploadArtifact, PermPublishEvent}
	denied := []Permission{PermDeleteWorkspace, PermManageMembers, PermCreateProject, PermDeleteProject, PermRegisterAgent, PermDeleteAgent, PermManageCF, PermExportAuditLog, PermManageWebhooks}

	for _, p := range allowed {
		assert.True(t, agentPerms[p], "agent should have permission: %s", p)
	}
	for _, p := range denied {
		assert.False(t, agentPerms[p], "agent should NOT have permission: %s", p)
	}
}
