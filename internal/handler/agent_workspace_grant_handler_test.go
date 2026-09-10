package handler

import (
	"context"
	"encoding/json"
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
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// MockAgentWorkspaceGrantService implements service.AgentWorkspaceGrantService.
type MockAgentWorkspaceGrantService struct {
	InviteAgentFunc         func(ctx context.Context, workspaceID, agentID uuid.UUID, role string, invitedBy uuid.UUID) (*service.InviteAgentResult, error)
	RevokeGrantFunc         func(ctx context.Context, workspaceID, grantID uuid.UUID) error
	ListWorkspaceAgentsFunc func(ctx context.Context, workspaceID uuid.UUID) ([]domain.AgentWorkspaceGrantWithAgent, error)
	ListAgentWorkspacesFunc func(ctx context.Context, agentID uuid.UUID) ([]domain.AgentWorkspaceGrantWithWorkspace, error)
}

func (m *MockAgentWorkspaceGrantService) InviteAgent(ctx context.Context, workspaceID, agentID uuid.UUID, role string, invitedBy uuid.UUID) (*service.InviteAgentResult, error) {
	if m.InviteAgentFunc != nil {
		return m.InviteAgentFunc(ctx, workspaceID, agentID, role, invitedBy)
	}
	return nil, nil
}

func (m *MockAgentWorkspaceGrantService) RevokeGrant(ctx context.Context, workspaceID, grantID uuid.UUID) error {
	if m.RevokeGrantFunc != nil {
		return m.RevokeGrantFunc(ctx, workspaceID, grantID)
	}
	return nil
}

func (m *MockAgentWorkspaceGrantService) ListWorkspaceAgents(ctx context.Context, workspaceID uuid.UUID) ([]domain.AgentWorkspaceGrantWithAgent, error) {
	if m.ListWorkspaceAgentsFunc != nil {
		return m.ListWorkspaceAgentsFunc(ctx, workspaceID)
	}
	return nil, nil
}

func (m *MockAgentWorkspaceGrantService) ListAgentWorkspaces(ctx context.Context, agentID uuid.UUID) ([]domain.AgentWorkspaceGrantWithWorkspace, error) {
	if m.ListAgentWorkspacesFunc != nil {
		return m.ListAgentWorkspacesFunc(ctx, agentID)
	}
	return nil, nil
}

// ---------------------------------------------------------------------------

func newGrantReq(t *testing.T, method, body string, wsID, grantID, callerID uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, "/", http.NoBody)
	} else {
		req = httptest.NewRequest(method, "/", strings.NewReader(body))
	}
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ws_id", "grant_id")
	c.SetParamValues(wsID.String(), grantID.String())
	if callerID != uuid.Nil {
		c.Set("user_id", callerID)
	}
	return c, rec
}

// ---------------------------------------------------------------------------
// Invite
// ---------------------------------------------------------------------------

func TestAgentWorkspaceGrantHandler_Invite_NewConnection_201WithKey(t *testing.T) {
	wsID, agentID := uuid.New(), uuid.New()
	grantID := uuid.New()
	inviter := uuid.New()
	var gotWS, gotAgent, gotInviter uuid.UUID
	var gotRole string

	svc := &MockAgentWorkspaceGrantService{
		InviteAgentFunc: func(_ context.Context, workspaceID, agentID uuid.UUID, role string, invitedBy uuid.UUID) (*service.InviteAgentResult, error) {
			gotWS, gotAgent, gotRole, gotInviter = workspaceID, agentID, role, invitedBy
			return &service.InviteAgentResult{
				Grant: &domain.AgentWorkspaceGrant{
					ID: grantID, AgentID: agentID, WorkspaceID: workspaceID, Role: role,
					APIKeyPrefix: "abcd1234",
				},
				APIKey:      "agk_target-ws_verysecretrandompart",
				Reactivated: false,
			}, nil
		},
	}
	h := NewAgentWorkspaceGrantHandler(svc)

	c, rec := newGrantReq(t, http.MethodPost,
		`{"agent_id":"`+agentID.String()+`","role":"admin"}`, wsID, uuid.Nil, inviter)

	require.NoError(t, h.Invite(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, wsID, gotWS)
	assert.Equal(t, agentID, gotAgent)
	assert.Equal(t, "admin", gotRole)
	assert.Equal(t, inviter, gotInviter)

	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, "agk_target-ws_verysecretrandompart", out["api_key"],
		"the raw key must be in the invite response — it is returned nowhere else")
	assert.Equal(t, "abcd1234", out["api_key_prefix"])
	assert.NotContains(t, rec.Body.String(), "api_key_hash", "the hash must never appear in any API response")
}

func TestAgentWorkspaceGrantHandler_Invite_Reactivated_200(t *testing.T) {
	wsID, agentID := uuid.New(), uuid.New()

	svc := &MockAgentWorkspaceGrantService{
		InviteAgentFunc: func(_ context.Context, workspaceID, agentID uuid.UUID, role string, _ uuid.UUID) (*service.InviteAgentResult, error) {
			return &service.InviteAgentResult{
				Grant:       &domain.AgentWorkspaceGrant{ID: uuid.New(), AgentID: agentID, WorkspaceID: workspaceID, Role: role},
				APIKey:      "agk_target-ws_freshkey",
				Reactivated: true,
			}, nil
		},
	}
	h := NewAgentWorkspaceGrantHandler(svc)

	c, rec := newGrantReq(t, http.MethodPost, `{"agent_id":"`+agentID.String()+`","role":"member"}`, wsID, uuid.Nil, uuid.New())

	require.NoError(t, h.Invite(c))
	assert.Equal(t, http.StatusOK, rec.Code, "a reactivated connection must respond 200, not 201")
}

func TestAgentWorkspaceGrantHandler_Invite_RejectsBadWorkspaceID(t *testing.T) {
	h := NewAgentWorkspaceGrantHandler(&MockAgentWorkspaceGrantService{})
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"agent_id":"`+uuid.New().String()+`"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ws_id")
	c.SetParamValues("not-a-uuid")

	require.NoError(t, h.Invite(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAgentWorkspaceGrantHandler_Invite_MissingAgentID_ValidationError(t *testing.T) {
	h := NewAgentWorkspaceGrantHandler(&MockAgentWorkspaceGrantService{})
	c, rec := newGrantReq(t, http.MethodPost, `{"role":"member"}`, uuid.New(), uuid.Nil, uuid.New())

	require.NoError(t, h.Invite(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// AC5: insufficient rights — the service layer or an rbac middleware refuses,
// and this proves the handler propagates that refusal as-is rather than
// swallowing it into a generic error.
func TestAgentWorkspaceGrantHandler_Invite_ServiceConflict_PropagatesAs409(t *testing.T) {
	svc := &MockAgentWorkspaceGrantService{
		InviteAgentFunc: func(context.Context, uuid.UUID, uuid.UUID, string, uuid.UUID) (*service.InviteAgentResult, error) {
			return nil, apierror.Conflict("agent already has an active connection to this workspace — revoke it first to reissue a key")
		},
	}
	h := NewAgentWorkspaceGrantHandler(svc)
	c, rec := newGrantReq(t, http.MethodPost, `{"agent_id":"`+uuid.New().String()+`"}`, uuid.New(), uuid.Nil, uuid.New())

	require.NoError(t, h.Invite(c))
	assert.Equal(t, http.StatusConflict, rec.Code)
}

// ---------------------------------------------------------------------------
// Revoke
// ---------------------------------------------------------------------------

func TestAgentWorkspaceGrantHandler_Revoke_204(t *testing.T) {
	wsID, grantID := uuid.New(), uuid.New()
	var gotWS, gotGrant uuid.UUID

	svc := &MockAgentWorkspaceGrantService{
		RevokeGrantFunc: func(_ context.Context, workspaceID, grantIDArg uuid.UUID) error {
			gotWS, gotGrant = workspaceID, grantIDArg
			return nil
		},
	}
	h := NewAgentWorkspaceGrantHandler(svc)
	c, rec := newGrantReq(t, http.MethodDelete, "", wsID, grantID, uuid.New())

	require.NoError(t, h.Revoke(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, wsID, gotWS)
	assert.Equal(t, grantID, gotGrant)
}

func TestAgentWorkspaceGrantHandler_Revoke_NotFound(t *testing.T) {
	svc := &MockAgentWorkspaceGrantService{
		RevokeGrantFunc: func(context.Context, uuid.UUID, uuid.UUID) error {
			return apierror.NotFound("AgentWorkspaceGrant")
		},
	}
	h := NewAgentWorkspaceGrantHandler(svc)
	c, rec := newGrantReq(t, http.MethodDelete, "", uuid.New(), uuid.New(), uuid.New())

	require.NoError(t, h.Revoke(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAgentWorkspaceGrantHandler_Revoke_RejectsBadGrantID(t *testing.T) {
	h := NewAgentWorkspaceGrantHandler(&MockAgentWorkspaceGrantService{})
	e := echo.New()
	req := httptest.NewRequest(http.MethodDelete, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ws_id", "grant_id")
	c.SetParamValues(uuid.New().String(), "not-a-uuid")

	require.NoError(t, h.Revoke(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// ---------------------------------------------------------------------------
// List (GET /workspaces/:ws_id/agent-grants)
// ---------------------------------------------------------------------------

func TestAgentWorkspaceGrantHandler_List_NeverLeaksKeyMaterial(t *testing.T) {
	wsID := uuid.New()
	svc := &MockAgentWorkspaceGrantService{
		ListWorkspaceAgentsFunc: func(_ context.Context, _ uuid.UUID) ([]domain.AgentWorkspaceGrantWithAgent, error) {
			return []domain.AgentWorkspaceGrantWithAgent{
				{
					AgentWorkspaceGrant: domain.AgentWorkspaceGrant{
						ID: uuid.New(), AgentID: uuid.New(), WorkspaceID: wsID, Role: "member",
						APIKeyPrefix: "listpfx1", APIKeyHash: "$2a$12$should-never-serialize",
					},
					Agent: domain.AgentBrief{ID: uuid.New(), Name: "Roamer", Slug: "roamer"},
				},
			}, nil
		},
	}
	h := NewAgentWorkspaceGrantHandler(svc)
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())

	require.NoError(t, h.List(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.NotContains(t, body, "api_key_hash", "AC4: the key material must never appear in a listing response")
	assert.NotContains(t, body, "should-never-serialize")

	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, float64(1), out["count"])
}

func TestAgentWorkspaceGrantHandler_List_EmptyIsArrayNotNull(t *testing.T) {
	h := NewAgentWorkspaceGrantHandler(&MockAgentWorkspaceGrantService{})
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ws_id")
	c.SetParamValues(uuid.New().String())

	require.NoError(t, h.List(c))
	assert.JSONEq(t, `{"agent_grants":[],"count":0}`, rec.Body.String())
}

// ---------------------------------------------------------------------------
// ListAgentWorkspaces (GET /agents/:agent_id/workspaces)
// ---------------------------------------------------------------------------

func TestAgentWorkspaceGrantHandler_ListAgentWorkspaces_NeverLeaksKeyMaterial(t *testing.T) {
	agentID := uuid.New()
	svc := &MockAgentWorkspaceGrantService{
		ListAgentWorkspacesFunc: func(_ context.Context, _ uuid.UUID) ([]domain.AgentWorkspaceGrantWithWorkspace, error) {
			return []domain.AgentWorkspaceGrantWithWorkspace{
				{
					AgentWorkspaceGrant: domain.AgentWorkspaceGrant{
						ID: uuid.New(), AgentID: agentID, WorkspaceID: uuid.New(), Role: "member",
						APIKeyPrefix: "lawpfx1", APIKeyHash: "$2a$12$should-never-serialize-either",
					},
					Workspace: domain.WorkspaceBrief{ID: uuid.New(), Name: "Guest Co", Slug: "guest-co"},
				},
			}, nil
		},
	}
	h := NewAgentWorkspaceGrantHandler(svc)
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("agent_id")
	c.SetParamValues(agentID.String())

	require.NoError(t, h.ListAgentWorkspaces(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "should-never-serialize-either")

	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, float64(1), out["count"])
}

func TestAgentWorkspaceGrantHandler_ListAgentWorkspaces_RejectsBadAgentID(t *testing.T) {
	h := NewAgentWorkspaceGrantHandler(&MockAgentWorkspaceGrantService{})
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("agent_id")
	c.SetParamValues("not-a-uuid")

	require.NoError(t, h.ListAgentWorkspaces(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
