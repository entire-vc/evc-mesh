package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// These tests pin the parent-child and body-tenant checks at the handler layer,
// where the failure is a wrong SQL statement rather than a middleware decision.
//
// The middleware guard stops a caller from another TENANT. It cannot stop a
// caller inside the same tenant from naming a parent in project A and a child in
// project B — the two resolve to one workspace and agree, so the guard passes and
// the handler is the only thing left. Every case below is that one: the caller is
// legitimately entitled to the parent, and the child belongs somewhere else.

// --- statuses ---------------------------------------------------------------

// TestTaskStatusHandler_Update_RefusesStatusOfAnotherProject pins the check that
// PATCH /projects/:proj_id/statuses/:status_id acquired. The handler used to parse
// only :status_id and update by it, so the project in the path was decorative.
func TestTaskStatusHandler_Update_RefusesStatusOfAnotherProject(t *testing.T) {
	projID := uuid.New()
	foreignStatusID := uuid.New()
	updated := false

	mockSvc := &MockTaskStatusService{
		ListByProjectFunc: func(_ context.Context, pid uuid.UUID) ([]domain.TaskStatus, error) {
			assert.Equal(t, projID, pid, "the handler scoped the lookup to the wrong project")
			// This project has a status, just not the one being addressed.
			return []domain.TaskStatus{{ID: uuid.New(), ProjectID: projID, Name: "Todo"}}, nil
		},
		UpdateFunc: func(_ context.Context, _ *domain.TaskStatus) error {
			updated = true
			return nil
		},
	}

	h, e := setupTaskStatusTest(mockSvc)
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{"name":"PWNED"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/statuses/:status_id")
	c.SetParamNames("proj_id", "status_id")
	c.SetParamValues(projID.String(), foreignStatusID.String())

	require.NoError(t, h.Update(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.False(t, updated, "the handler updated a status belonging to another project")
}

// TestTaskStatusHandler_Update_AllowsStatusOfTheSameProject is the other half: the
// scoping must not refuse the status that really is in the project.
func TestTaskStatusHandler_Update_AllowsStatusOfTheSameProject(t *testing.T) {
	projID := uuid.New()
	statusID := uuid.New()
	updated := false

	mockSvc := &MockTaskStatusService{
		ListByProjectFunc: func(_ context.Context, _ uuid.UUID) ([]domain.TaskStatus, error) {
			return []domain.TaskStatus{{ID: statusID, ProjectID: projID, Name: "Backlog"}}, nil
		},
		UpdateFunc: func(_ context.Context, s *domain.TaskStatus) error {
			updated = true
			assert.Equal(t, statusID, s.ID)
			assert.Equal(t, "Renamed", s.Name)
			return nil
		},
	}

	h, e := setupTaskStatusTest(mockSvc)
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{"name":"Renamed"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/statuses/:status_id")
	c.SetParamNames("proj_id", "status_id")
	c.SetParamValues(projID.String(), statusID.String())

	require.NoError(t, h.Update(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, updated, "the owner's update did not reach the service")
}

func TestTaskStatusHandler_Update_InvalidProjID(t *testing.T) {
	h, e := setupTaskStatusTest(&MockTaskStatusService{})
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/statuses/:status_id")
	c.SetParamNames("proj_id", "status_id")
	c.SetParamValues("nope", uuid.NewString())

	require.NoError(t, h.Update(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- auto-transition rules --------------------------------------------------

// MockAutoTransitionService is a hand-written double for the auto-transition
// service; only the rule CRUD used by the handler is exercised.
type MockAutoTransitionService struct {
	ListRulesFunc  func(ctx context.Context, projectID uuid.UUID) ([]domain.AutoTransitionRule, error)
	DeleteRuleFunc func(ctx context.Context, ruleID uuid.UUID) error
}

func (m *MockAutoTransitionService) EvaluateOnTaskMove(context.Context, uuid.UUID, domain.StatusCategory) error {
	return nil
}
func (m *MockAutoTransitionService) CheckSubtaskCompletion(context.Context, uuid.UUID) error {
	return nil
}
func (m *MockAutoTransitionService) CheckDependencyResolution(context.Context, uuid.UUID) error {
	return nil
}
func (m *MockAutoTransitionService) ListRules(ctx context.Context, projectID uuid.UUID) ([]domain.AutoTransitionRule, error) {
	if m.ListRulesFunc != nil {
		return m.ListRulesFunc(ctx, projectID)
	}
	return nil, nil
}
func (m *MockAutoTransitionService) CreateRule(context.Context, *domain.AutoTransitionRule) error {
	return nil
}
func (m *MockAutoTransitionService) UpdateRule(context.Context, *domain.AutoTransitionRule) error {
	return nil
}
func (m *MockAutoTransitionService) DeleteRule(ctx context.Context, ruleID uuid.UUID) error {
	if m.DeleteRuleFunc != nil {
		return m.DeleteRuleFunc(ctx, ruleID)
	}
	return nil
}

// TestAutoTransitionHandler_Delete_RefusesRuleOfAnotherProject pins the check the
// handler was missing outright: it parsed :proj_id into the blank identifier and
// deleted by :auto_rule_id alone.
func TestAutoTransitionHandler_Delete_RefusesRuleOfAnotherProject(t *testing.T) {
	projID := uuid.New()
	foreignRuleID := uuid.New()
	deleted := false

	svc := &MockAutoTransitionService{
		ListRulesFunc: func(_ context.Context, pid uuid.UUID) ([]domain.AutoTransitionRule, error) {
			assert.Equal(t, projID, pid)
			return []domain.AutoTransitionRule{{ID: uuid.New(), ProjectID: projID}}, nil
		},
		DeleteRuleFunc: func(context.Context, uuid.UUID) error {
			deleted = true
			return nil
		},
	}

	h := NewAutoTransitionHandler(svc)
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodDelete, "/", http.NoBody), rec)
	c.SetPath("/projects/:proj_id/auto-transition-rules/:auto_rule_id")
	c.SetParamNames("proj_id", "auto_rule_id")
	c.SetParamValues(projID.String(), foreignRuleID.String())

	require.NoError(t, h.Delete(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.False(t, deleted, "the handler deleted a rule belonging to another project")
}

// TestAutoTransitionHandler_Delete_AllowsRuleOfTheSameProject is also the
// regression test for the parameter rename: the handler reads :auto_rule_id now,
// and if it still read :rule_id this would delete nothing and 400.
func TestAutoTransitionHandler_Delete_AllowsRuleOfTheSameProject(t *testing.T) {
	projID := uuid.New()
	ruleID := uuid.New()
	deleted := false

	svc := &MockAutoTransitionService{
		ListRulesFunc: func(context.Context, uuid.UUID) ([]domain.AutoTransitionRule, error) {
			return []domain.AutoTransitionRule{{ID: ruleID, ProjectID: projID}}, nil
		},
		DeleteRuleFunc: func(_ context.Context, id uuid.UUID) error {
			deleted = true
			assert.Equal(t, ruleID, id)
			return nil
		},
	}

	h := NewAutoTransitionHandler(svc)
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodDelete, "/", http.NoBody), rec)
	c.SetPath("/projects/:proj_id/auto-transition-rules/:auto_rule_id")
	c.SetParamNames("proj_id", "auto_rule_id")
	c.SetParamValues(projID.String(), ruleID.String())

	require.NoError(t, h.Delete(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.True(t, deleted, "the owner's delete did not reach the service")
}

func TestAutoTransitionHandler_Delete_InvalidIDs(t *testing.T) {
	h := NewAutoTransitionHandler(&MockAutoTransitionService{})
	e := echo.New()

	for _, tc := range []struct{ name, proj, rule string }{
		{"bad project", "nope", uuid.NewString()},
		{"bad rule", uuid.NewString(), "nope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c := e.NewContext(httptest.NewRequest(http.MethodDelete, "/", http.NoBody), rec)
			c.SetPath("/projects/:proj_id/auto-transition-rules/:auto_rule_id")
			c.SetParamNames("proj_id", "auto_rule_id")
			c.SetParamValues(tc.proj, tc.rule)

			require.NoError(t, h.Delete(c))
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

// --- invites ----------------------------------------------------------------

// MockWorkspaceInviteService records the workspace the handler passed down, which
// is the whole point: it used to pass none.
type MockWorkspaceInviteService struct {
	gotWorkspaceID uuid.UUID
	gotInviteID    uuid.UUID
	called         bool
}

func (m *MockWorkspaceInviteService) CreateInvite(context.Context, service.CreateInviteInput) (*domain.WorkspaceInvite, error) {
	return nil, nil
}
func (m *MockWorkspaceInviteService) ListInvites(context.Context, uuid.UUID) ([]domain.WorkspaceInvite, error) {
	return nil, nil
}
func (m *MockWorkspaceInviteService) ResendInvite(_ context.Context, wsID, inviteID uuid.UUID) error {
	m.called, m.gotWorkspaceID, m.gotInviteID = true, wsID, inviteID
	return nil
}
func (m *MockWorkspaceInviteService) RevokeInvite(_ context.Context, wsID, inviteID uuid.UUID) error {
	m.called, m.gotWorkspaceID, m.gotInviteID = true, wsID, inviteID
	return nil
}
func (m *MockWorkspaceInviteService) GetByToken(context.Context, string) (*domain.WorkspaceInvite, error) {
	return nil, nil
}
func (m *MockWorkspaceInviteService) AcceptInvite(context.Context, service.AcceptInviteInput) (accessToken, refreshToken string, err error) {
	return "", "", nil
}

// TestInviteHandler_PassesWorkspaceDown pins that the workspace in the path
// actually reaches the service. Both handlers parsed it and dropped it, so the
// invite was resent or revoked by its own id and the workspace was decoration —
// the service had nothing to scope on and could not have refused a foreign invite
// even in principle.
func TestInviteHandler_PassesWorkspaceDown(t *testing.T) {
	wsID := uuid.New()
	inviteID := uuid.New()

	for _, tc := range []struct {
		name   string
		invoke func(*InviteHandler, echo.Context) error
		method string
		want   int
	}{
		{"Revoke", (*InviteHandler).Revoke, http.MethodDelete, http.StatusNoContent},
		{"Resend", (*InviteHandler).Resend, http.MethodPost, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &MockWorkspaceInviteService{}
			h := NewInviteHandler(svc)
			e := echo.New()
			rec := httptest.NewRecorder()
			c := e.NewContext(httptest.NewRequest(tc.method, "/", http.NoBody), rec)
			c.SetPath("/workspaces/:ws_id/invites/:invite_id")
			c.SetParamNames("ws_id", "invite_id")
			c.SetParamValues(wsID.String(), inviteID.String())

			require.NoError(t, tc.invoke(h, c))
			assert.Equal(t, tc.want, rec.Code)
			require.True(t, svc.called, "the service was not called")
			assert.Equal(t, wsID, svc.gotWorkspaceID,
				"the workspace in the path did not reach the service — the invite is unscoped again")
			assert.Equal(t, inviteID, svc.gotInviteID)
		})
	}
}

func TestInviteHandler_RefusesInvalidWorkspace(t *testing.T) {
	svc := &MockWorkspaceInviteService{}
	h := NewInviteHandler(svc)
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodDelete, "/", http.NoBody), rec)
	c.SetPath("/workspaces/:ws_id/invites/:invite_id")
	c.SetParamNames("ws_id", "invite_id")
	c.SetParamValues("nope", uuid.NewString())

	require.NoError(t, h.Revoke(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.False(t, svc.called, "the service was reached with an unparseable workspace")
}

// --- body-supplied workspace ------------------------------------------------

// MockRuleService only implements what EvaluateRules touches.
type MockRuleService struct {
	evaluated   bool
	gotEvaluate service.EvaluateInput
}

func (m *MockRuleService) Create(context.Context, service.CreateRuleInput) (*domain.Rule, error) {
	return nil, nil
}
func (m *MockRuleService) GetByID(context.Context, uuid.UUID) (*domain.Rule, error) { return nil, nil }
func (m *MockRuleService) Update(context.Context, uuid.UUID, service.UpdateRuleInput) (*domain.Rule, error) {
	return nil, nil
}
func (m *MockRuleService) Delete(context.Context, uuid.UUID) error { return nil }
func (m *MockRuleService) ListByWorkspace(context.Context, uuid.UUID, bool) ([]domain.Rule, error) {
	return nil, nil
}
func (m *MockRuleService) ListByProject(context.Context, uuid.UUID, bool) ([]domain.Rule, error) {
	return nil, nil
}
func (m *MockRuleService) ListByAgent(context.Context, uuid.UUID, bool) ([]domain.Rule, error) {
	return nil, nil
}
func (m *MockRuleService) GetEffective(context.Context, service.RuleContext) ([]domain.Rule, error) {
	return nil, nil
}
func (m *MockRuleService) Evaluate(_ context.Context, input service.EvaluateInput) ([]domain.RuleViolation, error) {
	m.evaluated, m.gotEvaluate = true, input
	return nil, nil
}

// TestEvaluateRules_RefusesForeignWorkspaceFromBody is the direct regression test
// for the body-supplied tenant. POST /rules/evaluate has no path parameter, so the
// middleware guard has nothing to resolve and never fires; the workspace arrived
// in the JSON and was believed.
func TestEvaluateRules_RefusesForeignWorkspaceFromBody(t *testing.T) {
	ownWS := uuid.New()
	foreignWS := uuid.New()

	svc := &MockRuleService{}
	h := NewRuleHandler(svc, &mockWorkspaceMemberRepo{})

	e := echo.New()
	body := `{"action":"move_task","workspace_id":"` + foreignWS.String() + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ownWS)

	require.NoError(t, h.EvaluateRules(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, svc.evaluated, "another tenant's rules were evaluated")
}

// TestEvaluateRules_RefusesNonMemberUser covers the user-token half. A user token
// carries no workspace, so the only thing that can authorize the body value is a
// membership row — and this caller has none.
func TestEvaluateRules_RefusesNonMemberUser(t *testing.T) {
	svc := &MockRuleService{}
	h := NewRuleHandler(svc, &mockWorkspaceMemberRepo{members: map[string]string{}})

	e := echo.New()
	body := `{"action":"move_task","workspace_id":"` + uuid.NewString() + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asUser(c, uuid.New())

	require.NoError(t, h.EvaluateRules(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, svc.evaluated, "a non-member evaluated a workspace's rules")
}

// TestEvaluateRules_AllowsOwnWorkspace proves the guard did not simply close the
// endpoint: an agent key naming its own workspace still evaluates, and so does one
// naming no workspace at all — the fallback to the credential's own tenant.
func TestEvaluateRules_AllowsOwnWorkspace(t *testing.T) {
	ownWS := uuid.New()

	for _, tc := range []struct{ name, body string }{
		{"explicit own workspace", `{"action":"move_task","workspace_id":"` + ownWS.String() + `"}`},
		{"no workspace supplied", `{"action":"move_task"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &MockRuleService{}
			h := NewRuleHandler(svc, &mockWorkspaceMemberRepo{})

			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			asAgent(c, uuid.New(), ownWS)

			require.NoError(t, h.EvaluateRules(c))
			assert.Equal(t, http.StatusOK, rec.Code)
			require.True(t, svc.evaluated, "the agent's own workspace was refused")
			assert.Equal(t, ownWS, svc.gotEvaluate.WorkspaceID,
				"the evaluated workspace is not the authorized one")
		})
	}
}

// TestEvaluateRules_AllowsMemberUser covers the user path through a real
// membership row.
func TestEvaluateRules_AllowsMemberUser(t *testing.T) {
	wsID := uuid.New()
	userID := uuid.New()

	svc := &MockRuleService{}
	h := NewRuleHandler(svc, &mockWorkspaceMemberRepo{
		members: map[string]string{wsID.String() + "/" + userID.String(): domain.RoleMember},
	})

	e := echo.New()
	body := `{"action":"move_task","workspace_id":"` + wsID.String() + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asUser(c, userID)

	require.NoError(t, h.EvaluateRules(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, svc.evaluated, "a member was refused their own workspace")
}

// MockNotificationService only implements what UpdatePreferences touches.
type MockNotificationService struct {
	upserted bool
}

func (m *MockNotificationService) Notify(context.Context, domain.NotificationEvent) {}
func (m *MockNotificationService) GetPreferences(context.Context, uuid.UUID) ([]domain.NotificationPreference, error) {
	return nil, nil
}
func (m *MockNotificationService) UpsertPreferences(_ context.Context, pref *domain.NotificationPreference) (*domain.NotificationPreference, error) {
	m.upserted = true
	return pref, nil
}
func (m *MockNotificationService) ListUnread(context.Context, uuid.UUID) ([]domain.Notification, error) {
	return nil, nil
}
func (m *MockNotificationService) CountUnread(context.Context, uuid.UUID) (int, error) {
	return 0, nil
}
func (m *MockNotificationService) MarkRead(context.Context, uuid.UUID, []uuid.UUID) error { return nil }
func (m *MockNotificationService) MarkAllRead(context.Context, uuid.UUID) error           { return nil }

// TestUpdatePreferences_RefusesForeignWorkspaceFromBody pins the other
// body-supplied tenant. This one is a write: the upsert created a row in a
// workspace the caller had no relationship with.
func TestUpdatePreferences_RefusesForeignWorkspaceFromBody(t *testing.T) {
	svc := &MockNotificationService{}
	h := NewNotificationHandler(svc, &mockWorkspaceMemberRepo{members: map[string]string{}})

	e := echo.New()
	body := `{"workspace_id":"` + uuid.NewString() + `","channel":"web_push"}`
	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asUser(c, uuid.New())

	require.NoError(t, h.UpdatePreferences(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, svc.upserted, "a preferences row was written into another tenant's workspace")
}

// TestUpdatePreferences_AllowsOwnWorkspace keeps the endpoint working for the
// people it belongs to.
func TestUpdatePreferences_AllowsOwnWorkspace(t *testing.T) {
	wsID := uuid.New()
	userID := uuid.New()

	svc := &MockNotificationService{}
	h := NewNotificationHandler(svc, &mockWorkspaceMemberRepo{
		members: map[string]string{wsID.String() + "/" + userID.String(): domain.RoleOwner},
	})

	e := echo.New()
	body := `{"workspace_id":"` + wsID.String() + `","channel":"web_push"}`
	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asUser(c, userID)

	require.NoError(t, h.UpdatePreferences(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, svc.upserted, "a member was refused their own preferences")
}

// --- the shared guard itself ------------------------------------------------

// TestWorkspaceAccess_Allows exercises workspaceAccess directly, since it is now
// the one implementation standing in front of every body-supplied tenant.
func TestWorkspaceAccess_Allows(t *testing.T) {
	wsID := uuid.New()
	userID := uuid.New()
	members := &mockWorkspaceMemberRepo{
		members: map[string]string{wsID.String() + "/" + userID.String(): domain.RoleMember},
	}

	newCtx := func() echo.Context {
		e := echo.New()
		return e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), httptest.NewRecorder())
	}

	t.Run("agent in its own workspace", func(t *testing.T) {
		c := newCtx()
		asAgent(c, uuid.New(), wsID)
		assert.True(t, workspaceAccess{members: members}.allows(c, wsID))
	})

	t.Run("agent in another workspace", func(t *testing.T) {
		c := newCtx()
		asAgent(c, uuid.New(), uuid.New())
		assert.False(t, workspaceAccess{members: members}.allows(c, wsID))
	})

	t.Run("member user", func(t *testing.T) {
		c := newCtx()
		asUser(c, userID)
		assert.True(t, workspaceAccess{members: members}.allows(c, wsID))
	})

	t.Run("non-member user", func(t *testing.T) {
		c := newCtx()
		asUser(c, uuid.New())
		assert.False(t, workspaceAccess{members: members}.allows(c, wsID))
	})

	t.Run("unauthenticated", func(t *testing.T) {
		assert.False(t, workspaceAccess{members: members}.allows(newCtx(), wsID))
	})

	t.Run("nil workspace is never allowed", func(t *testing.T) {
		c := newCtx()
		asAgent(c, uuid.New(), uuid.Nil)
		assert.False(t, workspaceAccess{members: members}.allows(c, uuid.Nil),
			"the zero workspace must not authorize anything")
	})

	t.Run("no members repo refuses rather than opening", func(t *testing.T) {
		c := newCtx()
		asUser(c, userID)
		assert.False(t, workspaceAccess{}.allows(c, wsID),
			"a handler wired without a members repo must fail closed")
	})
}

// TestWorkspaceAccess_Require covers the resolution half, including the fallback
// that keeps agent keys working when they name no workspace at all.
func TestWorkspaceAccess_Require(t *testing.T) {
	wsID := uuid.New()
	members := &mockWorkspaceMemberRepo{}

	newCtx := func() echo.Context {
		e := echo.New()
		return e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), httptest.NewRecorder())
	}

	t.Run("falls back to the credential's own workspace", func(t *testing.T) {
		c := newCtx()
		asAgent(c, uuid.New(), wsID)
		got, err := workspaceAccess{members: members}.require(c, uuid.Nil)
		require.NoError(t, err)
		assert.Equal(t, wsID, got)
	})

	t.Run("a user with no workspace anywhere is a bad request", func(t *testing.T) {
		c := newCtx()
		asUser(c, uuid.New())
		_, err := workspaceAccess{members: members}.require(c, uuid.Nil)
		require.Error(t, err)
	})

	t.Run("a workspace the caller cannot reach is refused", func(t *testing.T) {
		c := newCtx()
		asAgent(c, uuid.New(), wsID)
		_, err := workspaceAccess{members: members}.require(c, uuid.New())
		require.Error(t, err)
	})
}
