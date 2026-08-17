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
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// This file covers the handler half of the composite-route fix: the check that
// the child id in the path belongs to the parent id in the path.
//
// The middleware refuses these requests before they reach a handler. These tests
// exist because that is one layer, configured in one place, and the rows these
// handlers write are gone for good if it is ever mis-wired — every one of these
// endpoints previously deleted or rewrote by child id alone and answered 2xx.

// --- PATCH /projects/:proj_id/statuses/:status_id ---------------------------

// TestTaskStatusHandler_Update_ForeignStatusIsRefused: the reported repro. A
// member of any workspace sent their OWN :proj_id with a stranger's :status_id
// and the service merged the update into the stranger's row — 200, written.
func TestTaskStatusHandler_Update_ForeignStatusIsRefused(t *testing.T) {
	ownProject := uuid.New()
	foreignStatus := uuid.New()

	updated := false
	mockSvc := &MockTaskStatusService{
		ListByProjectFunc: func(_ context.Context, projID uuid.UUID) ([]domain.TaskStatus, error) {
			assert.Equal(t, ownProject, projID, "the handler must scope the lookup to the path's project")
			// The caller's own project has statuses, just not this one.
			return []domain.TaskStatus{{ID: uuid.New(), ProjectID: ownProject}}, nil
		},
		UpdateFunc: func(context.Context, *domain.TaskStatus) error {
			updated = true
			return nil
		},
	}

	h, e := setupTaskStatusTest(mockSvc)
	rec := statusUpdateRequest(t, e, h, ownProject.String(), foreignStatus.String(), `{"name":"hijacked","category":"done"}`)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.False(t, updated, "a status outside the path's project was rewritten anyway")
}

// TestTaskStatusHandler_Update_OwnStatusStillWorks is the other half: the check
// must not refuse the project the status actually belongs to.
func TestTaskStatusHandler_Update_OwnStatusStillWorks(t *testing.T) {
	projID := uuid.New()
	statusID := uuid.New()

	var got *domain.TaskStatus
	mockSvc := &MockTaskStatusService{
		ListByProjectFunc: func(context.Context, uuid.UUID) ([]domain.TaskStatus, error) {
			return []domain.TaskStatus{{ID: statusID, ProjectID: projID}}, nil
		},
		UpdateFunc: func(_ context.Context, s *domain.TaskStatus) error {
			got = s
			return nil
		},
	}

	h, e := setupTaskStatusTest(mockSvc)
	rec := statusUpdateRequest(t, e, h, projID.String(), statusID.String(), `{"name":"In Review","slug":"in-review","color":"#ff9900"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got, "the owner's own update did not reach the service")
	assert.Equal(t, statusID, got.ID)
	assert.Equal(t, "In Review", got.Name)
	assert.Equal(t, "in-review", got.Slug)
	assert.Equal(t, "#ff9900", got.Color)
}

func TestTaskStatusHandler_Update_InvalidIDs(t *testing.T) {
	h, e := setupTaskStatusTest(&MockTaskStatusService{})

	t.Run("bad project id", func(t *testing.T) {
		rec := statusUpdateRequest(t, e, h, "nope", uuid.New().String(), `{}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("bad status id", func(t *testing.T) {
		rec := statusUpdateRequest(t, e, h, uuid.New().String(), "nope", `{}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func statusUpdateRequest(t *testing.T, e *echo.Echo, h *TaskStatusHandler, projID, statusID, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/statuses/:status_id")
	c.SetParamNames("proj_id", "status_id")
	c.SetParamValues(projID, statusID)

	require.NoError(t, h.Update(c))
	return rec
}

// --- DELETE /projects/:proj_id/auto-transition-rules/:atr_id ----------------

// mockAutoTransitionService implements service.AutoTransitionService. Only the
// rule CRUD half is exercised here; the evaluation half has its own tests in the
// service package.
type mockAutoTransitionService struct {
	listRulesFunc  func(ctx context.Context, projectID uuid.UUID) ([]domain.AutoTransitionRule, error)
	deleteRuleFunc func(ctx context.Context, ruleID uuid.UUID) error
}

func (m *mockAutoTransitionService) EvaluateOnTaskMove(context.Context, uuid.UUID, domain.StatusCategory) error {
	return nil
}
func (m *mockAutoTransitionService) CheckSubtaskCompletion(context.Context, uuid.UUID) error {
	return nil
}
func (m *mockAutoTransitionService) CheckDependencyResolution(context.Context, uuid.UUID) error {
	return nil
}

func (m *mockAutoTransitionService) ListRules(ctx context.Context, projectID uuid.UUID) ([]domain.AutoTransitionRule, error) {
	if m.listRulesFunc != nil {
		return m.listRulesFunc(ctx, projectID)
	}
	return nil, nil
}
func (m *mockAutoTransitionService) CreateRule(context.Context, *domain.AutoTransitionRule) error {
	return nil
}
func (m *mockAutoTransitionService) UpdateRule(context.Context, *domain.AutoTransitionRule) error {
	return nil
}

func (m *mockAutoTransitionService) DeleteRule(ctx context.Context, ruleID uuid.UUID) error {
	if m.deleteRuleFunc != nil {
		return m.deleteRuleFunc(ctx, ruleID)
	}
	return nil
}

// TestAutoTransitionHandler_Delete_ForeignRuleIsRefused: :proj_id was parsed and
// thrown away, so DeleteRule went straight at the id. Update, four lines up in
// the same file, had always cross-checked — which is what makes this the clearest
// case that "the handler remembered" is not a property you can rely on.
func TestAutoTransitionHandler_Delete_ForeignRuleIsRefused(t *testing.T) {
	ownProject := uuid.New()
	foreignRule := uuid.New()

	deleted := false
	svc := &mockAutoTransitionService{
		listRulesFunc: func(_ context.Context, projID uuid.UUID) ([]domain.AutoTransitionRule, error) {
			assert.Equal(t, ownProject, projID)
			return []domain.AutoTransitionRule{{ID: uuid.New(), ProjectID: ownProject}}, nil
		},
		deleteRuleFunc: func(context.Context, uuid.UUID) error {
			deleted = true
			return nil
		},
	}

	rec := autoTransitionDeleteRequest(t, svc, ownProject.String(), foreignRule.String())
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.False(t, deleted, "a rule outside the path's project was deleted anyway")
}

func TestAutoTransitionHandler_Delete_OwnRuleStillWorks(t *testing.T) {
	projID := uuid.New()
	ruleID := uuid.New()

	var deletedID uuid.UUID
	svc := &mockAutoTransitionService{
		listRulesFunc: func(context.Context, uuid.UUID) ([]domain.AutoTransitionRule, error) {
			return []domain.AutoTransitionRule{{ID: ruleID, ProjectID: projID}}, nil
		},
		deleteRuleFunc: func(_ context.Context, id uuid.UUID) error {
			deletedID = id
			return nil
		},
	}

	rec := autoTransitionDeleteRequest(t, svc, projID.String(), ruleID.String())
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, ruleID, deletedID)
}

func TestAutoTransitionHandler_Delete_InvalidIDs(t *testing.T) {
	svc := &mockAutoTransitionService{}

	t.Run("bad project id", func(t *testing.T) {
		rec := autoTransitionDeleteRequest(t, svc, "nope", uuid.New().String())
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("bad rule id", func(t *testing.T) {
		rec := autoTransitionDeleteRequest(t, svc, uuid.New().String(), "nope")
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func autoTransitionDeleteRequest(t *testing.T, svc service.AutoTransitionService, projID, ruleID string) *httptest.ResponseRecorder {
	t.Helper()

	e := echo.New()
	h := NewAutoTransitionHandler(svc)

	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodDelete, "/", http.NoBody), rec)
	c.SetPath("/projects/:proj_id/auto-transition-rules/:atr_id")
	c.SetParamNames("proj_id", "atr_id")
	c.SetParamValues(projID, ruleID)

	require.NoError(t, h.Delete(c))
	return rec
}

// --- /workspaces/:ws_id/invites/:invite_id ----------------------------------

// mockInviteService implements the two calls the guarded routes make.
type mockInviteService struct {
	service.WorkspaceInviteService
	resend func(ctx context.Context, workspaceID, inviteID uuid.UUID) error
	revoke func(ctx context.Context, workspaceID, inviteID uuid.UUID) error
}

func (m *mockInviteService) ResendInvite(ctx context.Context, workspaceID, inviteID uuid.UUID) (service.InviteDelivery, error) {
	if m.resend != nil {
		if err := m.resend(ctx, workspaceID, inviteID); err != nil {
			return service.InviteDelivery{}, err
		}
	}
	return service.InviteDelivery{Status: service.InviteDeliverySent}, nil
}

func (m *mockInviteService) RevokeInvite(ctx context.Context, workspaceID, inviteID uuid.UUID) error {
	if m.revoke != nil {
		return m.revoke(ctx, workspaceID, inviteID)
	}
	return nil
}

// TestInviteHandler_ResendAndRevoke_PassTheWorkspaceThrough pins the fix at its
// narrowest point: :ws_id used to be ignored entirely, so the service could not
// have checked the tenant even if it had wanted to. An admin of any workspace
// revoked another tenant's pending invite (204, row gone) or re-sent it, which
// mails a stranger's invitee on demand.
func TestInviteHandler_ResendAndRevoke_PassTheWorkspaceThrough(t *testing.T) {
	wsID := uuid.New()
	inviteID := uuid.New()

	t.Run("resend", func(t *testing.T) {
		var gotWS, gotInvite uuid.UUID
		svc := &mockInviteService{resend: func(_ context.Context, w, i uuid.UUID) error {
			gotWS, gotInvite = w, i
			return nil
		}}

		rec := inviteRequest(t, svc, http.MethodPost, "resend", wsID.String(), inviteID.String())
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, wsID, gotWS, "the workspace from the path never reached the service")
		assert.Equal(t, inviteID, gotInvite)
	})

	t.Run("revoke", func(t *testing.T) {
		var gotWS, gotInvite uuid.UUID
		svc := &mockInviteService{revoke: func(_ context.Context, w, i uuid.UUID) error {
			gotWS, gotInvite = w, i
			return nil
		}}

		rec := inviteRequest(t, svc, http.MethodDelete, "revoke", wsID.String(), inviteID.String())
		assert.Equal(t, http.StatusNoContent, rec.Code)
		assert.Equal(t, wsID, gotWS, "the workspace from the path never reached the service")
		assert.Equal(t, inviteID, gotInvite)
	})
}

func TestInviteHandler_ResendAndRevoke_InvalidIDs(t *testing.T) {
	svc := &mockInviteService{}
	for _, tc := range []struct{ name, op, method, ws, invite string }{
		{"resend bad workspace", "resend", http.MethodPost, "nope", uuid.New().String()},
		{"resend bad invite", "resend", http.MethodPost, uuid.New().String(), "nope"},
		{"revoke bad workspace", "revoke", http.MethodDelete, "nope", uuid.New().String()},
		{"revoke bad invite", "revoke", http.MethodDelete, uuid.New().String(), "nope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := inviteRequest(t, svc, tc.method, tc.op, tc.ws, tc.invite)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

func inviteRequest(t *testing.T, svc service.WorkspaceInviteService, method, op, wsID, inviteID string) *httptest.ResponseRecorder {
	t.Helper()

	e := echo.New()
	// nil authService is safe here — this helper only ever drives
	// Resend/Revoke, neither of which touches it (only Accept does).
	h := NewInviteHandler(svc, nil)

	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(method, "/", http.NoBody), rec)
	c.SetPath("/workspaces/:ws_id/invites/:invite_id")
	c.SetParamNames("ws_id", "invite_id")
	c.SetParamValues(wsID, inviteID)

	if op == "resend" {
		require.NoError(t, h.Resend(c))
	} else {
		require.NoError(t, h.Revoke(c))
	}
	return rec
}

// --- POST /rules/evaluate ---------------------------------------------------

// mockRuleService implements service.RuleService for the evaluate path.
type mockRuleService struct {
	service.RuleService
	evaluate func(ctx context.Context, input service.EvaluateInput) ([]domain.RuleViolation, error)
}

func (m *mockRuleService) Evaluate(ctx context.Context, input service.EvaluateInput) ([]domain.RuleViolation, error) {
	if m.evaluate != nil {
		return m.evaluate(ctx, input)
	}
	return nil, nil
}

// TestRuleHandler_EvaluateRules_WorkspaceSource covers the route whose tenant
// lives in the request body.
//
// The guard (middleware.RequireBodyWorkspace) has already refused any workspace
// the caller cannot act in by the time this runs, and leaves the one it accepted
// in the request context. The handler reads it back from there when the body
// omitted it — anything else evaluates against the zero uuid — and refuses when
// there is neither.
func TestRuleHandler_EvaluateRules_WorkspaceSource(t *testing.T) {
	t.Run("body workspace is used", func(t *testing.T) {
		bodyWS := uuid.New()
		var got service.EvaluateInput
		h := NewRuleHandler(&mockRuleService{evaluate: func(_ context.Context, in service.EvaluateInput) ([]domain.RuleViolation, error) {
			got = in
			return []domain.RuleViolation{{Enforcement: domain.RuleEnforcementBlock}}, nil
		}})

		rec := evaluateRequest(t, h, `{"action":"task.move","workspace_id":"`+bodyWS.String()+`"}`, uuid.Nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, bodyWS, got.WorkspaceID)
		assert.Contains(t, rec.Body.String(), `"blocked":true`)
	})

	t.Run("omitted workspace falls back to the one the guard accepted", func(t *testing.T) {
		ctxWS := uuid.New()
		var got service.EvaluateInput
		h := NewRuleHandler(&mockRuleService{evaluate: func(_ context.Context, in service.EvaluateInput) ([]domain.RuleViolation, error) {
			got = in
			return nil, nil
		}})

		rec := evaluateRequest(t, h, `{"action":"task.move"}`, ctxWS)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, ctxWS, got.WorkspaceID,
			"an omitted workspace_id evaluated against the zero uuid instead of the caller's own workspace")
		assert.Contains(t, rec.Body.String(), `"violations":[]`)
	})

	t.Run("no workspace anywhere is refused", func(t *testing.T) {
		evaluated := false
		h := NewRuleHandler(&mockRuleService{evaluate: func(context.Context, service.EvaluateInput) ([]domain.RuleViolation, error) {
			evaluated = true
			return nil, nil
		}})

		rec := evaluateRequest(t, h, `{"action":"task.move"}`, uuid.Nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.False(t, evaluated, "rules were evaluated against no workspace at all")
	})

	t.Run("malformed body is refused", func(t *testing.T) {
		h := NewRuleHandler(&mockRuleService{})
		rec := evaluateRequest(t, h, `{"action":`, uuid.Nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func evaluateRequest(t *testing.T, h *RuleHandler, body string, ctxWS uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/rules/evaluate")
	if ctxWS != uuid.Nil {
		c.Set(mw.ContextKeyWorkspaceID, ctxWS)
	}

	require.NoError(t, h.EvaluateRules(c))
	return rec
}
