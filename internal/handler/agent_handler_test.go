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
		HeartbeatFunc: func(ctx context.Context, id uuid.UUID) error {
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

func TestAgentHandler_Heartbeat_ServiceError(t *testing.T) {
	agentID := uuid.New()
	mockSvc := &MockAgentService{
		HeartbeatFunc: func(ctx context.Context, id uuid.UUID) error {
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
