package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func setupAgentTest(mockSvc *MockAgentService) (*AgentHandler, *echo.Echo) {
	e := echo.New()
	h := NewAgentHandler(mockSvc)
	return h, e
}

// --- TestAgentHandler_Register ---

func TestAgentHandler_Register_Success(t *testing.T) {
	wsID := uuid.New()
	agentID := uuid.New()
	now := time.Now()

	mockSvc := &MockAgentService{
		RegisterFunc: func(ctx context.Context, input service.RegisterAgentInput) (*service.RegisterAgentOutput, error) {
			assert.Equal(t, wsID, input.WorkspaceID)
			assert.Equal(t, "my-agent", input.Name)
			assert.Equal(t, domain.AgentTypeClaudeCode, input.AgentType)
			assert.Equal(t, "code", input.Capabilities["language"])
			return &service.RegisterAgentOutput{
				Agent: &domain.Agent{
					ID:          agentID,
					WorkspaceID: wsID,
					Name:        "my-agent",
					AgentType:   domain.AgentTypeClaudeCode,
					Status:      domain.AgentStatusOnline,
					CreatedAt:   now,
					UpdatedAt:   now,
				},
				APIKey: "evc_abcdef123456",
			}, nil
		},
	}

	h, e := setupAgentTest(mockSvc)

	body := `{"name":"my-agent","agent_type":"claude_code","capabilities":{"language":"code"}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id/agents")
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())

	err := h.Register(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, rec.Code)

	var result service.RegisterAgentOutput
	err = json.Unmarshal(rec.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.Equal(t, "evc_abcdef123456", result.APIKey)
	assert.Equal(t, "my-agent", result.Agent.Name)
	assert.Equal(t, agentID, result.Agent.ID)
}

func TestAgentHandler_Register_MissingName(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockAgentService{}
	h, e := setupAgentTest(mockSvc)

	body := `{"agent_type":"claude_code"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id/agents")
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())

	err := h.Register(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var apiErr apierror.Error
	err = json.Unmarshal(rec.Body.Bytes(), &apiErr)
	require.NoError(t, err)
	assert.Equal(t, "Validation failed", apiErr.Message)
	assert.Equal(t, "name is required", apiErr.Validation["name"])
}

func TestAgentHandler_Register_InvalidWorkspaceID(t *testing.T) {
	mockSvc := &MockAgentService{}
	h, e := setupAgentTest(mockSvc)

	body := `{"name":"agent","agent_type":"claude_code"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id/agents")
	c.SetParamNames("ws_id")
	c.SetParamValues("bad-uuid")

	err := h.Register(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAgentHandler_Register_InvalidJSON(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockAgentService{}
	h, e := setupAgentTest(mockSvc)

	body := `{broken json`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id/agents")
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())

	err := h.Register(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAgentHandler_Register_ServiceError(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockAgentService{
		RegisterFunc: func(ctx context.Context, input service.RegisterAgentInput) (*service.RegisterAgentOutput, error) {
			return nil, apierror.Conflict("agent name already exists")
		},
	}

	h, e := setupAgentTest(mockSvc)

	body := `{"name":"duplicate","agent_type":"claude_code"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id/agents")
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())

	err := h.Register(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestAgentHandler_Register_WithCapabilities(t *testing.T) {
	wsID := uuid.New()

	mockSvc := &MockAgentService{
		RegisterFunc: func(ctx context.Context, input service.RegisterAgentInput) (*service.RegisterAgentOutput, error) {
			assert.NotNil(t, input.Capabilities)
			assert.Equal(t, "go", input.Capabilities["language"])
			assert.Equal(t, true, input.Capabilities["can_deploy"])
			return &service.RegisterAgentOutput{
				Agent: &domain.Agent{
					ID:          uuid.New(),
					WorkspaceID: wsID,
					Name:        "capable-agent",
					AgentType:   domain.AgentTypeCustom,
					Status:      domain.AgentStatusOnline,
					CreatedAt:   time.Now(),
					UpdatedAt:   time.Now(),
				},
				APIKey: "evc_cap_key",
			}, nil
		},
	}

	h, e := setupAgentTest(mockSvc)

	body := `{"name":"capable-agent","agent_type":"custom","capabilities":{"language":"go","can_deploy":true}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id/agents")
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())

	err := h.Register(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, rec.Code)
}

// --- TestAgentHandler_GetByID ---

func TestAgentHandler_GetByID_Found(t *testing.T) {
	agentID := uuid.New()
	now := time.Now()
	expectedAgent := &domain.Agent{
		ID:          agentID,
		WorkspaceID: uuid.New(),
		Name:        "test-agent",
		AgentType:   domain.AgentTypeClaudeCode,
		Status:      domain.AgentStatusOnline,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	mockSvc := &MockAgentService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Agent, error) {
			assert.Equal(t, agentID, id)
			return expectedAgent, nil
		},
	}

	h, e := setupAgentTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/agents/:agent_id")
	c.SetParamNames("agent_id")
	c.SetParamValues(agentID.String())

	err := h.GetByID(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var result domain.Agent
	err = json.Unmarshal(rec.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.Equal(t, agentID, result.ID)
	assert.Equal(t, "test-agent", result.Name)
	assert.Equal(t, domain.AgentStatusOnline, result.Status)
}

func TestAgentHandler_GetByID_NotFound(t *testing.T) {
	agentID := uuid.New()
	mockSvc := &MockAgentService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Agent, error) {
			return nil, apierror.NotFound("Agent")
		},
	}

	h, e := setupAgentTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/agents/:agent_id")
	c.SetParamNames("agent_id")
	c.SetParamValues(agentID.String())

	err := h.GetByID(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	var apiErr apierror.Error
	err = json.Unmarshal(rec.Body.Bytes(), &apiErr)
	require.NoError(t, err)
	assert.Equal(t, "Agent not found", apiErr.Message)
}

func TestAgentHandler_GetByID_InvalidUUID(t *testing.T) {
	mockSvc := &MockAgentService{}
	h, e := setupAgentTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/agents/:agent_id")
	c.SetParamNames("agent_id")
	c.SetParamValues("not-valid")

	err := h.GetByID(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAgentHandler_GetByID_InternalError(t *testing.T) {
	agentID := uuid.New()
	mockSvc := &MockAgentService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Agent, error) {
			return nil, assert.AnError
		},
	}

	h, e := setupAgentTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/agents/:agent_id")
	c.SetParamNames("agent_id")
	c.SetParamValues(agentID.String())

	err := h.GetByID(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- TestAgentHandler_Heartbeat ---

func TestAgentHandler_Heartbeat_Success(t *testing.T) {
	agentID := uuid.New()
	mockSvc := &MockAgentService{
		HeartbeatFunc: func(ctx context.Context, id uuid.UUID, input *service.HeartbeatInput) error {
			assert.Equal(t, agentID, id)
			return nil
		},
	}

	h, e := setupAgentTest(mockSvc)

	req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/agents/heartbeat")
	c.Set("agent_id", agentID)

	err := h.Heartbeat(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var result map[string]string
	err = json.Unmarshal(rec.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.Equal(t, "ok", result["status"])
}

func TestAgentHandler_Heartbeat_NoAgentInContext(t *testing.T) {
	mockSvc := &MockAgentService{}
	h, e := setupAgentTest(mockSvc)

	req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/agents/heartbeat")
	// Do NOT set agent_id in context

	err := h.Heartbeat(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAgentHandler_Heartbeat_InvalidAgentIDType(t *testing.T) {
	mockSvc := &MockAgentService{}
	h, e := setupAgentTest(mockSvc)

	req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/agents/heartbeat")
	c.Set("agent_id", "string-not-uuid") // wrong type

	err := h.Heartbeat(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestAgentHandler_Heartbeat_StatusExactly20Chars_OK verifies the boundary: a
// status of exactly heartbeatStatusMaxLen (20) chars must succeed — this is the
// DB column width (agents.heartbeat_status VARCHAR(20)), so 20 is the largest
// value that must still work.
func TestAgentHandler_Heartbeat_StatusExactly20Chars_OK(t *testing.T) {
	agentID := uuid.New()
	status20 := strings.Repeat("a", 20)
	var gotStatus string
	mockSvc := &MockAgentService{
		HeartbeatFunc: func(ctx context.Context, id uuid.UUID, input *service.HeartbeatInput) error {
			gotStatus = input.Status
			return nil
		},
	}

	h, e := setupAgentTest(mockSvc)

	body := `{"status":"` + status20 + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/agents/heartbeat")
	c.Set("agent_id", agentID)

	err := h.Heartbeat(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code, "20-char status must be accepted (boundary)")
	assert.Equal(t, status20, gotStatus)
}

// TestAgentHandler_Heartbeat_Status21Chars_BadRequest verifies the boundary: a
// status of 21 chars (one over the VARCHAR(20) column width) must be rejected
// with 400 BEFORE it reaches the DB layer, not a 500 from a truncation/length
// constraint violation. The error message must name both the limit and the
// `message` field as the place for free-form text.
func TestAgentHandler_Heartbeat_Status21Chars_BadRequest(t *testing.T) {
	agentID := uuid.New()
	status21 := strings.Repeat("a", 21)
	serviceCalled := false
	mockSvc := &MockAgentService{
		HeartbeatFunc: func(ctx context.Context, id uuid.UUID, input *service.HeartbeatInput) error {
			serviceCalled = true
			return nil
		},
	}

	h, e := setupAgentTest(mockSvc)

	body := `{"status":"` + status21 + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/agents/heartbeat")
	c.Set("agent_id", agentID)

	err := h.Heartbeat(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "21-char status must be rejected as 400, not reach the DB as a 500")
	assert.False(t, serviceCalled, "service must not be called once validation rejects the request")

	var result apierror.Error
	err = json.Unmarshal(rec.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.Contains(t, result.Message, "20", "error must name the length limit")
	assert.Contains(t, result.Message, "message", "error must point to the `message` field for free-form text")
}

// TestAgentHandler_Heartbeat_EmptyStatus_OK verifies the fix doesn't break the
// working path: an empty/omitted status must still succeed.
func TestAgentHandler_Heartbeat_EmptyStatus_OK(t *testing.T) {
	agentID := uuid.New()
	mockSvc := &MockAgentService{
		HeartbeatFunc: func(ctx context.Context, id uuid.UUID, input *service.HeartbeatInput) error {
			return nil
		},
	}

	h, e := setupAgentTest(mockSvc)

	body := `{"status":""}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/agents/heartbeat")
	c.Set("agent_id", agentID)

	err := h.Heartbeat(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAgentHandler_Heartbeat_ServiceError(t *testing.T) {
	agentID := uuid.New()
	mockSvc := &MockAgentService{
		HeartbeatFunc: func(ctx context.Context, id uuid.UUID, input *service.HeartbeatInput) error {
			return apierror.NotFound("Agent")
		},
	}

	h, e := setupAgentTest(mockSvc)

	req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/agents/heartbeat")
	c.Set("agent_id", agentID)

	err := h.Heartbeat(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- TestHandleError ---

func TestHandleError_APIError(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	apiErr := apierror.BadRequest("test error")
	err := handleError(c, apiErr)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var result apierror.Error
	err = json.Unmarshal(rec.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.Equal(t, "test error", result.Message)
}

func TestHandleError_GenericError(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handleError(c, assert.AnError)
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	var result apierror.Error
	err = json.Unmarshal(rec.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.Equal(t, "Internal server error", result.Message)
}

// --- TestAgentHandler_Me — #606c215e ---
//
// meRespView mirrors meResponse's anonymous struct shape for unmarshalling in
// tests (domain.Agent's own fields flattened, plus home_workspace_id).
type meRespView struct {
	domain.Agent
	HomeWorkspaceID uuid.UUID `json:"home_workspace_id"`
}

// meRequest builds a GET /agents/me (or PATCH with body) request carrying the
// agent_id + agent-auth-workspace context a real DualAuth pass would set —
// see notification_prefs_tenancy_test.go / workspace_member_test.go for the
// same pattern used elsewhere. authWsID mirrors ContextKeyAgentAuthWorkspaceID
// (agent.WorkspaceID as agentService.Authenticate resolved it); pass
// uuid.Nil to simulate the invariant-violation case (never set).
func meRequest(t *testing.T, h *AgentHandler, method string, agentID, authWsID uuid.UUID, body string, handle func(echo.Context) error) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, "/", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	} else {
		req = httptest.NewRequest(method, "/", http.NoBody)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("agent_id", agentID)
	if authWsID != uuid.Nil {
		c.Set(mw.ContextKeyAgentAuthWorkspaceID, authWsID)
	}

	err := handle(c)
	require.NoError(t, err)
	return rec
}

func TestAgentHandler_Me_ReturnsAuthWorkspace_NotHome(t *testing.T) {
	agentID := uuid.New()
	homeWS := uuid.New()
	grantWS := uuid.New() // the workspace the guest key actually authenticated into
	require.NotEqual(t, homeWS, grantWS)

	mockSvc := &MockAgentService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Agent, error) {
			assert.Equal(t, agentID, id)
			return &domain.Agent{ID: agentID, WorkspaceID: homeWS, Name: "guest-agent"}, nil
		},
	}
	h, _ := setupAgentTest(mockSvc)

	rec := meRequest(t, h, http.MethodGet, agentID, grantWS, "", h.Me)
	require.Equal(t, http.StatusOK, rec.Code)

	var result meRespView
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.Equal(t, grantWS, result.WorkspaceID, "workspace_id must be the workspace the presented key authenticated into, not the home row")
	assert.Equal(t, homeWS, result.HomeWorkspaceID, "home_workspace_id must still carry the agent's home workspace (additive, not lost)")
}

func TestAgentHandler_Me_HomeKey_WorkspaceIDUnchanged(t *testing.T) {
	agentID := uuid.New()
	homeWS := uuid.New()

	mockSvc := &MockAgentService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Agent, error) {
			return &domain.Agent{ID: agentID, WorkspaceID: homeWS, Name: "home-agent"}, nil
		},
	}
	h, _ := setupAgentTest(mockSvc)

	// A home key's auth workspace equals its home workspace — the existing
	// fleet's behavior must not change (#606c215e explicitly measures this).
	rec := meRequest(t, h, http.MethodGet, agentID, homeWS, "", h.Me)
	require.Equal(t, http.StatusOK, rec.Code)

	var result meRespView
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.Equal(t, homeWS, result.WorkspaceID)
	assert.Equal(t, homeWS, result.HomeWorkspaceID)
}

func TestAgentHandler_Me_NoAuthWorkspaceInContext_FailsClosed(t *testing.T) {
	agentID := uuid.New()
	homeWS := uuid.New()

	mockSvc := &MockAgentService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Agent, error) {
			return &domain.Agent{ID: agentID, WorkspaceID: homeWS}, nil
		},
	}
	h, _ := setupAgentTest(mockSvc)

	// ContextKeyAgentAuthWorkspaceID deliberately not set — this route is
	// only reachable via DualAuth's agent branch, which always sets it
	// alongside agent_id; if it's missing, that invariant broke and the
	// handler must refuse rather than silently fall back to the home
	// workspace (the exact bug being fixed).
	rec := meRequest(t, h, http.MethodGet, agentID, uuid.Nil, "", h.Me)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestAgentHandler_UpdateMe_ReturnsAuthWorkspace_NotHome(t *testing.T) {
	agentID := uuid.New()
	homeWS := uuid.New()
	grantWS := uuid.New()
	require.NotEqual(t, homeWS, grantWS)

	mockSvc := &MockAgentService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Agent, error) {
			return &domain.Agent{ID: agentID, WorkspaceID: homeWS, ProfileDescription: "old"}, nil
		},
		UpdateFunc: func(ctx context.Context, agent *domain.Agent) error {
			// The persisted row must carry the HOME workspace, never the
			// auth workspace — the swap in the handler happens strictly
			// after this call returns (see UpdateMe's own comment).
			assert.Equal(t, homeWS, agent.WorkspaceID, "must not persist the auth workspace onto the agent row")
			assert.Equal(t, "new", agent.ProfileDescription)
			return nil
		},
	}
	h, _ := setupAgentTest(mockSvc)

	rec := meRequest(t, h, http.MethodPatch, agentID, grantWS, `{"profile_description":"new"}`, h.UpdateMe)
	require.Equal(t, http.StatusOK, rec.Code)

	var result meRespView
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.Equal(t, grantWS, result.WorkspaceID)
	assert.Equal(t, homeWS, result.HomeWorkspaceID)
	assert.Equal(t, "new", result.ProfileDescription)
}
