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
	"github.com/entire-vc/evc-mesh/internal/repository"
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

// An OAuth connector is a third-party client a member consented to; it must not
// be able to point the server's callback delivery at an address of its choosing.
// Everything else PATCH /agents/me does stays available to it, and an
// X-Agent-Key agent is unchanged.
func TestAgentHandler_UpdateMe_CallbackURL_ConnectorRefused(t *testing.T) {
	agentID, wsID, userID := uuid.New(), uuid.New(), uuid.New()

	run := func(t *testing.T, connector bool, body string) (code int, updated bool, stored string) {
		t.Helper()
		mockSvc := &MockAgentService{
			GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Agent, error) {
				return &domain.Agent{ID: agentID, WorkspaceID: wsID, CallbackURL: "https://old.example/cb"}, nil
			},
			UpdateFunc: func(ctx context.Context, agent *domain.Agent) error {
				updated, stored = true, agent.CallbackURL
				return nil
			},
		}
		h, _ := setupAgentTest(mockSvc)
		e := echo.New()
		req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.Set("agent_id", agentID)
		c.Set(mw.ContextKeyAgentAuthWorkspaceID, wsID)
		if connector {
			c.Set(mw.ContextKeyOAuthConnectorUserID, userID)
		}
		require.NoError(t, h.UpdateMe(c))
		return rec.Code, updated, stored
	}

	t.Run("connector setting a callback URL is refused and nothing is written", func(t *testing.T) {
		code, updated, _ := run(t, true, `{"callback_url":"http://169.254.169.254/latest/meta-data"}`)
		assert.Equal(t, http.StatusForbidden, code)
		assert.False(t, updated, "a refused request must not reach the service")
	})
	t.Run("connector may still update its description", func(t *testing.T) {
		code, updated, _ := run(t, true, `{"profile_description":"a description"}`)
		assert.Equal(t, http.StatusOK, code)
		assert.True(t, updated)
	})
	t.Run("connector may clear a callback URL", func(t *testing.T) {
		code, updated, stored := run(t, true, `{"callback_url":""}`)
		assert.Equal(t, http.StatusOK, code)
		assert.True(t, updated)
		assert.Equal(t, "", stored)
	})
	t.Run("X-Agent-Key agent may set a callback URL, unchanged", func(t *testing.T) {
		code, updated, stored := run(t, false, `{"callback_url":"https://hooks.example/cb"}`)
		assert.Equal(t, http.StatusOK, code)
		assert.True(t, updated)
		assert.Equal(t, "https://hooks.example/cb", stored)
	})
}

// --- harness (agent_type) + model self-report (#5548367d) ---

func TestAgentHandler_UpdateMe_HarnessAndModel(t *testing.T) {
	agentID, wsID := uuid.New(), uuid.New()
	var saved *domain.Agent
	mockSvc := &MockAgentService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Agent, error) {
			return &domain.Agent{ID: agentID, WorkspaceID: wsID, AgentType: domain.AgentTypeClaudeCode}, nil
		},
		UpdateFunc: func(ctx context.Context, a *domain.Agent) error { saved = a; return nil },
	}
	h, _ := setupAgentTest(mockSvc)

	rec := meRequest(t, h, http.MethodPatch, agentID, wsID, `{"agent_type":"codex","model":"  gpt-6.1-sol "}`, h.UpdateMe)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, saved)
	assert.Equal(t, domain.AgentTypeCodex, saved.AgentType)
	require.NotNil(t, saved.Model)
	assert.Equal(t, "gpt-6.1-sol", *saved.Model, "model is trimmed")

	var result meRespView
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.Equal(t, domain.AgentTypeCodex, result.AgentType)
	require.NotNil(t, result.Model)
	assert.Equal(t, "gpt-6.1-sol", *result.Model)
}

func TestAgentHandler_UpdateMe_EmptyModelClears(t *testing.T) {
	agentID, wsID := uuid.New(), uuid.New()
	old := "old-model"
	var saved *domain.Agent
	mockSvc := &MockAgentService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Agent, error) {
			return &domain.Agent{ID: agentID, WorkspaceID: wsID, Model: &old}, nil
		},
		UpdateFunc: func(ctx context.Context, a *domain.Agent) error { saved = a; return nil },
	}
	h, _ := setupAgentTest(mockSvc)
	rec := meRequest(t, h, http.MethodPatch, agentID, wsID, `{"model":"   "}`, h.UpdateMe)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Nil(t, saved.Model, "blank model clears to NULL")
}

func TestAgentHandler_UpdateMe_RejectsBadHarnessAndLongModel(t *testing.T) {
	agentID, wsID := uuid.New(), uuid.New()
	updates := 0
	mockSvc := &MockAgentService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Agent, error) {
			return &domain.Agent{ID: agentID, WorkspaceID: wsID}, nil
		},
		UpdateFunc: func(ctx context.Context, a *domain.Agent) error { updates++; return nil },
	}
	h, _ := setupAgentTest(mockSvc)

	rec := meRequest(t, h, http.MethodPatch, agentID, wsID, `{"agent_type":"skynet"}`, h.UpdateMe)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = meRequest(t, h, http.MethodPatch, agentID, wsID, `{"model":"`+strings.Repeat("m", 129)+`"}`, h.UpdateMe)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, 0, updates, "nothing persisted on a rejected request")
}

func TestAgentHandler_Heartbeat_HarnessModelAndStatusLen(t *testing.T) {
	agentID := uuid.New()
	var got *service.HeartbeatInput
	mockSvc := &MockAgentService{
		HeartbeatFunc: func(ctx context.Context, id uuid.UUID, in *service.HeartbeatInput) error { got = in; return nil },
	}
	h, _ := setupAgentTest(mockSvc)

	post := func(body string) int {
		e := echo.New()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		c := e.NewContext(req, httptest.NewRecorder())
		c.Set("agent_id", agentID)
		rec := httptest.NewRecorder()
		c = e.NewContext(req, rec)
		c.Set("agent_id", agentID)
		require.NoError(t, h.Heartbeat(c))
		return rec.Code
	}

	require.Equal(t, http.StatusOK, post(`{"agent_type":"codex","model":"gpt-6.1-sol"}`))
	require.NotNil(t, got)
	assert.Equal(t, domain.AgentTypeCodex, got.AgentType)
	require.NotNil(t, got.Model)
	assert.Equal(t, "gpt-6.1-sol", *got.Model)

	got = nil
	assert.Equal(t, http.StatusBadRequest, post(`{"agent_type":"skynet"}`))
	assert.Equal(t, http.StatusBadRequest, post(`{"status":"`+strings.Repeat("s", 21)+`"}`))
	assert.Nil(t, got, "rejected heartbeat must not reach the service")
}

func TestAgentHandler_Update_AdminSetsModel(t *testing.T) {
	agentID := uuid.New()
	var saved *domain.Agent
	mockSvc := &MockAgentService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Agent, error) {
			return &domain.Agent{ID: agentID, AgentType: domain.AgentTypeClaudeCode}, nil
		},
		UpdateFunc: func(ctx context.Context, a *domain.Agent) error { saved = a; return nil },
	}
	h, _ := setupAgentTest(mockSvc)
	e := echo.New()
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{"model":"gpt-6.1-sol"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("agent_id")
	c.SetParamValues(agentID.String())
	require.NoError(t, h.Update(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, saved.Model)
	assert.Equal(t, "gpt-6.1-sol", *saved.Model)
	assert.Equal(t, domain.AgentTypeClaudeCode, saved.AgentType, "agent_type behaviour unchanged")
}

// --- has_description on the agent task feed (#fc032545) ---
//
// The feed's task DTOs serialize has_description (the field has no omitempty),
// so leaving it at the Go zero value made every feed item claim
// has_description:false even when the description was right there in the same
// JSON object. The flag must mean TrimSpace(description) != "" here too.
func TestGetMyTasks_HasDescriptionConsistentWithBody(t *testing.T) {
	agentID, wsID := uuid.New(), uuid.New()
	taskSvc := &MockTaskService{
		GetMyTasksFunc: func(_ context.Context, _, _ uuid.UUID, _ domain.AssigneeType, _ repository.AssigneeTaskFilter) ([]domain.Task, int, error) {
			return []domain.Task{
				{ID: uuid.New(), Title: "With body", Description: "has one"},
				{ID: uuid.New(), Title: "Whitespace", Description: " \t "},
				{ID: uuid.New(), Title: "Empty", Description: ""},
			}, 3, nil
		},
	}
	h := NewAgentHandlerWithTaskService(nil, taskSvc)

	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
	c.Set(mw.ContextKeyAgentID, agentID)
	c.Set(mw.ContextKeyWorkspaceID, wsID)

	require.NoError(t, h.GetMyTasks(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Tasks []domain.Task `json:"tasks"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Tasks, 3)
	assert.True(t, resp.Tasks[0].HasDescription)
	assert.False(t, resp.Tasks[1].HasDescription)
	assert.False(t, resp.Tasks[2].HasDescription)
	// The feed has no include_description contract — text stays untouched.
	assert.Equal(t, "has one", resp.Tasks[0].Description)
}

// The long-poll twin answers the same feed after its wait; its task DTOs carry
// the same computed flag (caught uncovered by independent review, MR !1061).
func TestAgentHandler_PollTasks_HasDescriptionConsistentWithBody(t *testing.T) {
	agentID, wsID := uuid.New(), uuid.New()
	taskSvc := &MockTaskService{
		GetMyTasksFunc: func(_ context.Context, _, _ uuid.UUID, _ domain.AssigneeType, _ repository.AssigneeTaskFilter) ([]domain.Task, int, error) {
			return []domain.Task{
				{ID: uuid.New(), Title: "With body", Description: "has one"},
				{ID: uuid.New(), Title: "Whitespace", Description: " \t "},
				{ID: uuid.New(), Title: "Empty", Description: ""},
			}, 3, nil
		},
	}
	_, rdb := newSSEMiniredis(t)
	h := NewAgentHandlerFull(nil, taskSvc, nil, rdb)

	e := echo.New()
	// timeout=1 keeps the park short: no pub/sub message arrives on the fresh
	// miniredis, so the timer branch fires and the response is the plain feed.
	req := httptest.NewRequest(http.MethodGet, "/?timeout=1", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(mw.ContextKeyAgentID, agentID)
	c.Set(mw.ContextKeyWorkspaceID, wsID)

	require.NoError(t, h.PollTasks(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Tasks []domain.Task `json:"tasks"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Tasks, 3)
	assert.True(t, resp.Tasks[0].HasDescription)
	assert.False(t, resp.Tasks[1].HasDescription)
	assert.False(t, resp.Tasks[2].HasDescription)
	assert.Equal(t, "has one", resp.Tasks[0].Description)
}
