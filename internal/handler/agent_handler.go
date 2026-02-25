package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// AgentHandler handles HTTP requests for agent management.
type AgentHandler struct {
	agentService service.AgentService
	taskService  service.TaskService // optional, used for GetMyTasks
}

// NewAgentHandler creates a new AgentHandler with the given service.
func NewAgentHandler(as service.AgentService) *AgentHandler {
	return &AgentHandler{agentService: as}
}

// NewAgentHandlerWithTaskService creates an AgentHandler that also supports
// the GET /agents/me/tasks endpoint.
func NewAgentHandlerWithTaskService(as service.AgentService, ts service.TaskService) *AgentHandler {
	return &AgentHandler{agentService: as, taskService: ts}
}

// registerAgentRequest represents the JSON body for registering a new agent.
type registerAgentRequest struct {
	Name         string           `json:"name"`
	AgentType    domain.AgentType `json:"agent_type"`
	Capabilities map[string]any   `json:"capabilities"`
}

// listAgentsQuery represents query parameters for listing agents.
type listAgentsQuery struct {
	Status    string `query:"status"`
	AgentType string `query:"agent_type"`
	Search    string `query:"search"`
}

// List handles GET /workspaces/:ws_id/agents
func (h *AgentHandler) List(c echo.Context) error {
	wsIDStr := c.Param("ws_id")
	wsID, err := uuid.Parse(wsIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	var q listAgentsQuery
	if err := c.Bind(&q); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid query parameters"))
	}

	var pg pagination.Params
	if err := c.Bind(&pg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination parameters"))
	}
	pg.Normalize()

	filter := repository.AgentFilter{
		Search: q.Search,
	}

	if q.Status != "" {
		s := domain.AgentStatus(q.Status)
		filter.Status = &s
	}
	if q.AgentType != "" {
		at := domain.AgentType(q.AgentType)
		filter.AgentType = &at
	}

	page, err := h.agentService.List(c.Request().Context(), wsID, filter, pg)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, page)
}

// Register handles POST /workspaces/:ws_id/agents
func (h *AgentHandler) Register(c echo.Context) error {
	wsIDStr := c.Param("ws_id")
	wsID, err := uuid.Parse(wsIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	var req registerAgentRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"name": "name is required",
		}))
	}

	input := service.RegisterAgentInput{
		WorkspaceID:  wsID,
		Name:         req.Name,
		AgentType:    req.AgentType,
		Capabilities: req.Capabilities,
	}

	output, err := h.agentService.Register(c.Request().Context(), input)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, output)
}

// GetByID handles GET /agents/:agent_id
func (h *AgentHandler) GetByID(c echo.Context) error {
	agentIDStr := c.Param("agent_id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid agent_id"))
	}

	agent, err := h.agentService.GetByID(c.Request().Context(), agentID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, agent)
}

// updateAgentRequest represents the JSON body for updating an agent.
type updateAgentRequest struct {
	Name         *string          `json:"name"`
	AgentType    *domain.AgentType `json:"agent_type"`
	Capabilities map[string]any   `json:"capabilities"`
}

// Update handles PATCH /agents/:agent_id
func (h *AgentHandler) Update(c echo.Context) error {
	agentIDStr := c.Param("agent_id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid agent_id"))
	}

	var req updateAgentRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	// Fetch existing agent to apply partial updates.
	agent, err := h.agentService.GetByID(c.Request().Context(), agentID)
	if err != nil {
		return handleError(c, err)
	}

	if req.Name != nil {
		agent.Name = *req.Name
	}
	if req.AgentType != nil {
		agent.AgentType = *req.AgentType
	}
	if req.Capabilities != nil {
		capBytes, err := json.Marshal(req.Capabilities)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid capabilities"))
		}
		agent.Capabilities = capBytes
	}

	if err := h.agentService.Update(c.Request().Context(), agent); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, agent)
}

// Delete handles DELETE /agents/:agent_id
func (h *AgentHandler) Delete(c echo.Context) error {
	agentIDStr := c.Param("agent_id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid agent_id"))
	}

	if err := h.agentService.Delete(c.Request().Context(), agentID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// RegenerateKey handles POST /agents/:agent_id/regenerate-key
func (h *AgentHandler) RegenerateKey(c echo.Context) error {
	agentIDStr := c.Param("agent_id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid agent_id"))
	}

	newKey, err := h.agentService.RotateAPIKey(c.Request().Context(), agentID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]string{
		"api_key": newKey,
	})
}

// Heartbeat handles POST /agents/heartbeat
// The agent_id is expected to be set in the context by auth middleware.
func (h *AgentHandler) Heartbeat(c echo.Context) error {
	agentIDVal := c.Get("agent_id")
	if agentIDVal == nil {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("agent_id not found in context"))
	}

	agentID, ok := agentIDVal.(uuid.UUID)
	if !ok {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid agent_id in context"))
	}

	if err := h.agentService.Heartbeat(c.Request().Context(), agentID); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// Me handles GET /agents/me
// Returns the current agent's profile based on the API key used for auth.
func (h *AgentHandler) Me(c echo.Context) error {
	agentIDVal := c.Get("agent_id")
	if agentIDVal == nil {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("agent API key required"))
	}

	agentID, ok := agentIDVal.(uuid.UUID)
	if !ok {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid agent_id in context"))
	}

	agent, err := h.agentService.GetByID(c.Request().Context(), agentID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, agent)
}

// GetMyTasks handles GET /agents/me/tasks
// Returns tasks assigned to the current agent.
func (h *AgentHandler) GetMyTasks(c echo.Context) error {
	if h.taskService == nil {
		return c.JSON(http.StatusNotImplemented, apierror.InternalError("task service not configured"))
	}

	agentIDVal := c.Get("agent_id")
	if agentIDVal == nil {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("agent API key required"))
	}

	agentID, ok := agentIDVal.(uuid.UUID)
	if !ok {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid agent_id in context"))
	}

	tasks, err := h.taskService.GetMyTasks(c.Request().Context(), agentID, domain.AssigneeTypeAgent)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"tasks": tasks,
		"count": len(tasks),
	})
}
