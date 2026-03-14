package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

const (
	// agentNotifyChannelPrefix is the Redis pub/sub channel prefix for agent notifications.
	agentNotifyChannelPrefix = "agent-notify:"
	// agentSSEKeepaliveInterval is how often a keepalive comment is sent on SSE streams.
	agentSSEKeepaliveInterval = 30 * time.Second
	// agentPollDefaultTimeout is the default long-poll timeout in seconds.
	agentPollDefaultTimeout = 30
	// agentPollMaxTimeout is the maximum allowed long-poll timeout in seconds.
	agentPollMaxTimeout = 120
)

// AgentHandler handles HTTP requests for agent management.
type AgentHandler struct {
	agentService service.AgentService
	taskService  service.TaskService // optional, used for GetMyTasks and PollTasks
	rdb          *redis.Client       // optional, used for SSE and long-poll
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

// NewAgentHandlerFull creates an AgentHandler with full support for task queries,
// SSE streaming and long-polling via Redis pub/sub.
func NewAgentHandlerFull(as service.AgentService, ts service.TaskService, rdb *redis.Client) *AgentHandler {
	return &AgentHandler{agentService: as, taskService: ts, rdb: rdb}
}

// registerAgentRequest represents the JSON body for registering a new agent.
type registerAgentRequest struct {
	Name          string           `json:"name"`
	AgentType     domain.AgentType `json:"agent_type"`
	Capabilities  map[string]any   `json:"capabilities"`
	ParentAgentID *uuid.UUID       `json:"parent_agent_id,omitempty"`
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
		WorkspaceID:   wsID,
		Name:          req.Name,
		AgentType:     req.AgentType,
		Capabilities:  req.Capabilities,
		ParentAgentID: req.ParentAgentID,
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
	Name               *string           `json:"name"`
	AgentType          *domain.AgentType `json:"agent_type"`
	Capabilities       map[string]any    `json:"capabilities"`
	ProfileDescription *string           `json:"profile_description"`
	CallbackURL        *string           `json:"callback_url"`
	CurrentTaskID      *uuid.UUID        `json:"current_task_id"`
	ParentAgentID      *string           `json:"parent_agent_id"` // UUID string or "" to clear
	Role               *string           `json:"role"`
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
	if req.ProfileDescription != nil {
		agent.ProfileDescription = *req.ProfileDescription
	}
	if req.CallbackURL != nil {
		agent.CallbackURL = *req.CallbackURL
	}
	if req.CurrentTaskID != nil {
		agent.CurrentTaskID = req.CurrentTaskID
	}
	if req.Role != nil {
		agent.Role = *req.Role
	}
	if req.ParentAgentID != nil {
		if *req.ParentAgentID == "" {
			agent.ParentAgentID = nil
		} else {
			parentID, err := uuid.Parse(*req.ParentAgentID)
			if err != nil {
				return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid parent_agent_id"))
			}
			agent.ParentAgentID = &parentID
		}
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

	// Re-fetch agent so the response includes the updated record.
	agent, err := h.agentService.GetByID(c.Request().Context(), agentID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"agent":   agent,
		"api_key": newKey,
	})
}

// heartbeatRequest represents the optional JSON body for heartbeat.
type heartbeatRequest struct {
	Status        string         `json:"status"`
	Message       string         `json:"message"`
	Metadata      map[string]any `json:"metadata"`
	CurrentTaskID *string        `json:"current_task_id"`
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

	var req heartbeatRequest
	_ = c.Bind(&req) // optional body; empty body is fine

	var input *service.HeartbeatInput
	if req.Status != "" || req.Message != "" || req.Metadata != nil || req.CurrentTaskID != nil {
		input = &service.HeartbeatInput{
			Status:  req.Status,
			Message: req.Message,
		}
		if req.Metadata != nil {
			b, _ := json.Marshal(req.Metadata)
			input.Metadata = b
		}
		if req.CurrentTaskID != nil {
			if id, err := uuid.Parse(*req.CurrentTaskID); err == nil {
				input.CurrentTaskID = &id
			}
		}
	}

	if err := h.agentService.Heartbeat(c.Request().Context(), agentID, input); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// GetAgentHeartbeat handles GET /agents/:agent_id/heartbeat
func (h *AgentHandler) GetAgentHeartbeat(c echo.Context) error {
	agentID, err := uuid.Parse(c.Param("agent_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid agent_id"))
	}

	agent, err := h.agentService.GetByID(c.Request().Context(), agentID)
	if err != nil {
		return handleError(c, err)
	}
	if agent == nil {
		return c.JSON(http.StatusNotFound, apierror.NotFound("Agent"))
	}

	return c.JSON(http.StatusOK, map[string]any{
		"agent_id":                agent.ID,
		"status":                  agent.HeartbeatStatus,
		"message":                 agent.HeartbeatMessage,
		"metadata":                agent.HeartbeatMetadata,
		"last_heartbeat_at":       agent.LastHeartbeat,
		"seconds_since_heartbeat": agent.SecondsSinceHeartbeat(),
		"is_stale":                agent.IsHeartbeatStale(),
	})
}

// ListAgentActivity handles GET /agents/:agent_id/activity
func (h *AgentHandler) ListAgentActivity(c echo.Context) error {
	agentID, err := uuid.Parse(c.Param("agent_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid agent_id"))
	}

	var pg pagination.Params
	if err := c.Bind(&pg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination parameters"))
	}
	pg.Normalize()

	filter := repository.AgentActivityLogFilter{
		EventType: c.QueryParam("event_type"),
	}
	if since := c.QueryParam("since"); since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			filter.Since = &t
		}
	}
	if until := c.QueryParam("until"); until != "" {
		if t, err := time.Parse(time.RFC3339, until); err == nil {
			filter.Until = &t
		}
	}

	page, err := h.agentService.ListActivityLog(c.Request().Context(), agentID, filter, pg)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, page)
}

// CreateAgentActivity handles POST /agents/:agent_id/activity
func (h *AgentHandler) CreateAgentActivity(c echo.Context) error {
	agentID, err := uuid.Parse(c.Param("agent_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid agent_id"))
	}

	agent, err := h.agentService.GetByID(c.Request().Context(), agentID)
	if err != nil {
		return handleError(c, err)
	}
	if agent == nil {
		return c.JSON(http.StatusNotFound, apierror.NotFound("Agent"))
	}

	var req struct {
		EventType string         `json:"event_type"`
		TaskID    *string        `json:"task_id"`
		Message   string         `json:"message"`
		Metadata  map[string]any `json:"metadata"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	if req.EventType == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"event_type": "event_type is required",
		}))
	}

	entry := &domain.AgentActivityLog{
		AgentID:     agentID,
		WorkspaceID: agent.WorkspaceID,
		EventType:   req.EventType,
		Message:     req.Message,
	}
	if req.TaskID != nil {
		if tid, err := uuid.Parse(*req.TaskID); err == nil {
			entry.TaskID = &tid
		}
	}
	if req.Metadata != nil {
		b, _ := json.Marshal(req.Metadata)
		entry.Metadata = b
	}

	if err := h.agentService.CreateActivityLog(c.Request().Context(), entry); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, entry)
}

// GetAgentsStatus handles GET /workspaces/:ws_id/agents/status
func (h *AgentHandler) GetAgentsStatus(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	pg := pagination.Params{Page: 1, PageSize: 200}
	agents, err := h.agentService.List(c.Request().Context(), wsID, repository.AgentFilter{}, pg)
	if err != nil {
		return handleError(c, err)
	}

	type agentStatus struct {
		ID                    uuid.UUID  `json:"id"`
		Name                  string     `json:"name"`
		Status                string     `json:"status"`
		HeartbeatStatus       string     `json:"heartbeat_status"`
		LastHeartbeatAt       *time.Time `json:"last_heartbeat_at"`
		SecondsSinceHeartbeat *int       `json:"seconds_since_heartbeat"`
		IsStale               bool       `json:"is_stale"`
		HeartbeatMessage      string     `json:"heartbeat_message,omitempty"`
		CurrentTaskID         *uuid.UUID `json:"current_task_id,omitempty"`
	}

	var working, stale int
	statuses := make([]agentStatus, 0, len(agents.Items))
	for _, a := range agents.Items {
		isStale := a.IsHeartbeatStale()
		if isStale {
			stale++
		}
		if a.Status == domain.AgentStatusOnline || a.Status == domain.AgentStatusBusy {
			working++
		}
		statuses = append(statuses, agentStatus{
			ID:                    a.ID,
			Name:                  a.Name,
			Status:                string(a.Status),
			HeartbeatStatus:       a.HeartbeatStatus,
			LastHeartbeatAt:       a.LastHeartbeat,
			SecondsSinceHeartbeat: a.SecondsSinceHeartbeat(),
			IsStale:               isStale,
			HeartbeatMessage:      a.HeartbeatMessage,
			CurrentTaskID:         a.CurrentTaskID,
		})
	}

	return c.JSON(http.StatusOK, map[string]any{
		"agents":        statuses,
		"stale_count":   stale,
		"working_count": working,
		"total_count":   len(statuses),
	})
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

// updateMeRequest represents the JSON body for self-service agent profile updates.
// Only safe fields — no name/type/capabilities changes (those require admin).
type updateMeRequest struct {
	ProfileDescription *string `json:"profile_description"`
	CallbackURL        *string `json:"callback_url"`
}

// UpdateMe handles PATCH /agents/me
// Allows an agent to update its own profile (callback_url, profile_description)
// without requiring admin permissions.
func (h *AgentHandler) UpdateMe(c echo.Context) error {
	agentIDVal := c.Get("agent_id")
	if agentIDVal == nil {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("agent API key required"))
	}
	agentID, ok := agentIDVal.(uuid.UUID)
	if !ok {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid agent_id in context"))
	}

	var req updateMeRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	agent, err := h.agentService.GetByID(c.Request().Context(), agentID)
	if err != nil {
		return handleError(c, err)
	}

	if req.ProfileDescription != nil {
		agent.ProfileDescription = *req.ProfileDescription
	}
	if req.CallbackURL != nil {
		agent.CallbackURL = *req.CallbackURL
	}

	if err := h.agentService.Update(c.Request().Context(), agent); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, agent)
}

// ListSubAgents handles GET /agents/:agent_id/sub-agents
// Returns child agents of the specified agent.
// Query parameter ?recursive=true returns all descendants up to 10 levels deep.
func (h *AgentHandler) ListSubAgents(c echo.Context) error {
	agentIDStr := c.Param("agent_id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid agent_id"))
	}

	recursive := c.QueryParam("recursive") == "true"

	agents, err := h.agentService.ListSubAgents(c.Request().Context(), agentID, recursive)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"agents": agents,
		"count":  len(agents),
	})
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

// EventStream handles GET /agents/me/events/stream
// Server-Sent Events endpoint for agents to receive real-time notifications.
// The connection is kept open and events are pushed as they arrive on the
// Redis pub/sub channel "agent-notify:{agent_id}".
func (h *AgentHandler) EventStream(c echo.Context) error {
	if h.rdb == nil {
		return c.JSON(http.StatusNotImplemented, apierror.InternalError("event streaming not configured"))
	}

	agentIDVal := c.Get("agent_id")
	if agentIDVal == nil {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("agent API key required"))
	}
	agentID, ok := agentIDVal.(uuid.UUID)
	if !ok {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid agent_id in context"))
	}

	// Set SSE response headers.
	c.Response().Header().Set("Content-Type", "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().Header().Set("Connection", "keep-alive")
	c.Response().Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering.
	c.Response().WriteHeader(http.StatusOK)
	c.Response().Flush()

	channel := fmt.Sprintf("%s%s", agentNotifyChannelPrefix, agentID.String())

	// Subscribe to the agent's Redis pub/sub channel.
	sub := h.rdb.Subscribe(c.Request().Context(), channel)
	defer sub.Close()

	subCh := sub.Channel()
	keepalive := time.NewTicker(agentSSEKeepaliveInterval)
	defer keepalive.Stop()

	reqCtx := c.Request().Context()

	for {
		select {
		case <-reqCtx.Done():
			// Client disconnected.
			return nil

		case msg, ok := <-subCh:
			if !ok {
				// Subscription closed.
				return nil
			}
			// Write SSE event.
			// Parse to extract event_type for the SSE event field.
			var notif map[string]any
			eventType := "message"
			if err := json.Unmarshal([]byte(msg.Payload), &notif); err == nil {
				if et, ok := notif["event_type"].(string); ok && et != "" {
					eventType = et
				}
			}
			if _, err := fmt.Fprintf(c.Response(), "event: %s\ndata: %s\n\n", eventType, msg.Payload); err != nil {
				return nil
			}
			c.Response().Flush()

		case <-keepalive.C:
			// Send a keepalive comment to prevent connection timeout.
			if _, err := fmt.Fprintf(c.Response(), ": ping\n\n"); err != nil {
				return nil
			}
			c.Response().Flush()
		}
	}
}

// PollTasks handles GET /agents/me/tasks/poll?timeout=30
// Long-polling endpoint: blocks until a new task notification arrives or timeout.
// Returns the current list of tasks assigned to this agent plus a changed flag.
func (h *AgentHandler) PollTasks(c echo.Context) error {
	if h.rdb == nil || h.taskService == nil {
		return c.JSON(http.StatusNotImplemented, apierror.InternalError("long-polling not configured"))
	}

	agentIDVal := c.Get("agent_id")
	if agentIDVal == nil {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("agent API key required"))
	}
	agentID, ok := agentIDVal.(uuid.UUID)
	if !ok {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid agent_id in context"))
	}

	// Parse timeout query parameter (default 30s, max 120s).
	timeoutSecs := agentPollDefaultTimeout
	if raw := c.QueryParam("timeout"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			timeoutSecs = parsed
		}
	}
	if timeoutSecs < 1 {
		timeoutSecs = 1
	}
	if timeoutSecs > agentPollMaxTimeout {
		timeoutSecs = agentPollMaxTimeout
	}

	channel := fmt.Sprintf("%s%s", agentNotifyChannelPrefix, agentID.String())

	// Subscribe before entering the wait loop to avoid a race where a notification
	// arrives between the subscription setup and the blocking select.
	sub := h.rdb.Subscribe(c.Request().Context(), channel)
	defer sub.Close()

	subCh := sub.Channel()
	timer := time.NewTimer(time.Duration(timeoutSecs) * time.Second)
	defer timer.Stop()

	reqCtx := c.Request().Context()
	changed := false

	select {
	case <-reqCtx.Done():
		// Client disconnected before timeout.
		return nil

	case _, ok := <-subCh:
		if ok {
			changed = true
		}

	case <-timer.C:
		// Timeout reached — return current tasks with changed=false.
	}

	// Fetch current tasks for this agent.
	ctx := context.Background()
	tasks, err := h.taskService.GetMyTasks(ctx, agentID, domain.AssigneeTypeAgent)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"tasks":   tasks,
		"count":   len(tasks),
		"changed": changed,
	})
}
