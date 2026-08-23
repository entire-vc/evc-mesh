package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/presence"
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
	agentService    service.AgentService
	taskService     service.TaskService               // optional, used for GetMyTasks and PollTasks
	statusService   service.TaskStatusService         // optional, used for status_category filtering
	rdb             *redis.Client                     // optional, used for SSE and long-poll
	agentEventsRepo repository.AgentEventsRepository  // optional, enables Last-Event-ID SSE replay
	sessionRepo     repository.AgentSessionRepository // optional, enables POST /agents/me/sessions/report
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
func NewAgentHandlerFull(as service.AgentService, ts service.TaskService, ss service.TaskStatusService, rdb *redis.Client) *AgentHandler {
	return &AgentHandler{agentService: as, taskService: ts, statusService: ss, rdb: rdb}
}

// NewAgentHandlerWithEvents creates an AgentHandler with full SSE support plus
// durable event replay via Last-Event-ID cursor (backed by agentEventsRepo) and
// session cost tracking (backed by sessionRepo). sessionRepo may be nil; in that
// case POST /agents/me/sessions/report returns 501 Not Implemented.
func NewAgentHandlerWithEvents(as service.AgentService, ts service.TaskService, ss service.TaskStatusService, rdb *redis.Client, evRepo repository.AgentEventsRepository, sessionRepo repository.AgentSessionRepository) *AgentHandler {
	return &AgentHandler{agentService: as, taskService: ts, statusService: ss, rdb: rdb, agentEventsRepo: evRepo, sessionRepo: sessionRepo}
}

// registerAgentRequest represents the JSON body for registering a new agent.
type registerAgentRequest struct {
	Name               string           `json:"name"`
	AgentType          domain.AgentType `json:"agent_type"`
	Capabilities       map[string]any   `json:"capabilities"`
	ParentAgentID      *uuid.UUID       `json:"parent_agent_id,omitempty"`
	Role               *string          `json:"role,omitempty"`
	ResponsibilityZone *string          `json:"responsibility_zone,omitempty"`
	EscalationTo       *string          `json:"escalation_to,omitempty"`
	AcceptsFrom        json.RawMessage  `json:"accepts_from,omitempty"`
	MaxConcurrentTasks *int             `json:"max_concurrent_tasks,omitempty"`
	WorkingHours       *string          `json:"working_hours,omitempty"`
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
	if err = c.Bind(&q); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid query parameters"))
	}

	var pg pagination.Params
	if err = c.Bind(&pg); err != nil {
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
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"name": "name is required",
		}))
	}

	input := service.RegisterAgentInput{
		WorkspaceID:        wsID,
		Name:               req.Name,
		AgentType:          req.AgentType,
		Capabilities:       req.Capabilities,
		ParentAgentID:      req.ParentAgentID,
		Role:               req.Role,
		ResponsibilityZone: req.ResponsibilityZone,
		EscalationTo:       req.EscalationTo,
		AcceptsFrom:        req.AcceptsFrom,
		MaxConcurrentTasks: req.MaxConcurrentTasks,
		WorkingHours:       req.WorkingHours,
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
	ParentAgentID      *string           `json:"parent_agent_id"`    // UUID string or "" to clear
	SupervisorUserID   *string           `json:"supervisor_user_id"` // UUID string or "" to clear
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
	if err = c.Bind(&req); err != nil {
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
		var capBytes []byte
		capBytes, err = json.Marshal(req.Capabilities)
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
			var parentID uuid.UUID
			parentID, err = uuid.Parse(*req.ParentAgentID)
			if err != nil {
				return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid parent_agent_id"))
			}
			agent.ParentAgentID = &parentID
			agent.SupervisorUserID = nil // mutual exclusion
		}
	}
	if req.SupervisorUserID != nil {
		if *req.SupervisorUserID == "" {
			agent.SupervisorUserID = nil
		} else {
			var supervisorID uuid.UUID
			supervisorID, err = uuid.Parse(*req.SupervisorUserID)
			if err != nil {
				return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid supervisor_user_id"))
			}
			agent.SupervisorUserID = &supervisorID
			agent.ParentAgentID = nil // mutual exclusion
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

	if err = h.agentService.Delete(c.Request().Context(), agentID); err != nil {
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

// heartbeatStatusMaxLen mirrors the DB column width for agents.heartbeat_status
// (VARCHAR(20), migration 20260314039_agent_monitoring.sql). A status longer than
// this fails the INSERT/UPDATE at the DB layer with an unhelpful 500 unless
// rejected here first.
const heartbeatStatusMaxLen = 20

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

	if len(req.Status) > heartbeatStatusMaxLen {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest(fmt.Sprintf(
			"status must be <=%d chars; use message for free-form text (got %d chars)",
			heartbeatStatusMaxLen, len(req.Status),
		)))
	}

	// Auto-set status to "busy" when processing a task (unless explicitly set otherwise).
	if req.CurrentTaskID != nil && *req.CurrentTaskID != "" && req.Status == "" {
		req.Status = "busy"
	}

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
	if err = c.Bind(&pg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination parameters"))
	}
	pg.Normalize()

	filter := repository.AgentActivityLogFilter{
		EventType: c.QueryParam("event_type"),
	}
	if since := c.QueryParam("since"); since != "" {
		var t time.Time
		if t, err = time.Parse(time.RFC3339, since); err == nil {
			filter.Since = &t
		}
	}
	if until := c.QueryParam("until"); until != "" {
		var t time.Time
		if t, err = time.Parse(time.RFC3339, until); err == nil {
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
		CurrentTaskTitle      string     `json:"current_task_title,omitempty"`
	}

	// Resolve task titles for agents currently working on a task.
	taskTitles := map[uuid.UUID]string{}
	if h.taskService != nil {
		for _, a := range agents.Items {
			if a.CurrentTaskID != nil {
				if _, seen := taskTitles[*a.CurrentTaskID]; !seen {
					if t, terr := h.taskService.GetByID(c.Request().Context(), *a.CurrentTaskID); terr == nil && t != nil {
						taskTitles[*a.CurrentTaskID] = t.Title
					}
				}
			}
		}
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
		entry := agentStatus{
			ID:                    a.ID,
			Name:                  a.Name,
			Status:                string(a.Status),
			HeartbeatStatus:       a.HeartbeatStatus,
			LastHeartbeatAt:       a.LastHeartbeat,
			SecondsSinceHeartbeat: a.SecondsSinceHeartbeat(),
			IsStale:               isStale,
			HeartbeatMessage:      a.HeartbeatMessage,
			CurrentTaskID:         a.CurrentTaskID,
		}
		if a.CurrentTaskID != nil {
			entry.CurrentTaskTitle = taskTitles[*a.CurrentTaskID]
		}
		statuses = append(statuses, entry)
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

// reportSessionRequest is the JSON body for POST /agents/me/sessions/report.
// All fields are optional; semantics are additive — the reported usage is
// accumulated onto the agent's active session (one Mesh session may span
// several agent spawns reported separately by the dispatcher).
type reportSessionRequest struct {
	TokensIn      int64   `json:"tokens_in"`
	TokensOut     int64   `json:"tokens_out"`
	Model         string  `json:"model"`
	EstimatedCost float64 `json:"estimated_cost"`
	TaskID        *string `json:"task_id"`
}

// ReportSession handles POST /agents/me/sessions/report
// Accumulates token/cost usage onto the calling agent's active session, creating
// a new active session if none exists. Intended to be called by the dispatcher
// after reaping an agent spawn (with usage summed from the spawn's session log),
// using the agent's own API key for auth.
func (h *AgentHandler) ReportSession(c echo.Context) error {
	if h.sessionRepo == nil {
		return c.JSON(http.StatusNotImplemented, apierror.InternalError("session tracking not configured"))
	}

	agentIDVal := c.Get("agent_id")
	if agentIDVal == nil {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("agent API key required"))
	}
	agentID, ok := agentIDVal.(uuid.UUID)
	if !ok {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid agent_id in context"))
	}

	var req reportSessionRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	ctx := c.Request().Context()

	// Parse optional task_id FIRST so we can scope the session lookup per-task.
	var reqTaskID *uuid.UUID
	if req.TaskID != nil && *req.TaskID != "" {
		if parsed, perr := uuid.Parse(*req.TaskID); perr == nil {
			reqTaskID = &parsed
		}
	}

	// Look up an existing active session scoped to this task (when provided), or
	// fall back to the agent-wide active session for backward-compat with untagged reports.
	var active *domain.AgentSession
	var err error
	if reqTaskID != nil {
		active, err = h.sessionRepo.GetActiveForTask(ctx, agentID, *reqTaskID)
	} else {
		active, err = h.sessionRepo.GetActive(ctx, agentID)
	}
	if err != nil {
		return handleError(c, err)
	}

	if active == nil {
		// No active session — resolve the agent's workspace and create one,
		// seeded with this report's usage.
		agent, gerr := h.agentService.GetByID(ctx, agentID)
		if gerr != nil {
			return handleError(c, gerr)
		}
		if agent == nil {
			return c.JSON(http.StatusNotFound, apierror.NotFound("Agent"))
		}
		active = &domain.AgentSession{
			ID:            uuid.New(),
			AgentID:       agentID,
			WorkspaceID:   agent.WorkspaceID,
			TaskID:        reqTaskID,
			StartedAt:     time.Now(),
			Status:        domain.AgentSessionStatusActive,
			ModelUsed:     req.Model,
			TokensIn:      req.TokensIn,
			TokensOut:     req.TokensOut,
			EstimatedCost: req.EstimatedCost,
		}
		if err := h.sessionRepo.Create(ctx, active); err != nil {
			return handleError(c, err)
		}
	} else {
		// Additive: accumulate this spawn's totals onto the existing active session.
		active.TokensIn += req.TokensIn
		active.TokensOut += req.TokensOut
		active.EstimatedCost += req.EstimatedCost
		if req.Model != "" {
			active.ModelUsed = req.Model // latest model wins
		}
		// Only set task_id on the first report that carries one; never overwrite.
		if active.TaskID == nil && reqTaskID != nil {
			active.TaskID = reqTaskID
		}
		if err := h.sessionRepo.Update(ctx, active); err != nil {
			return handleError(c, err)
		}
	}

	return c.JSON(http.StatusOK, map[string]any{
		"session_id": active.ID,
		"totals": map[string]any{
			"tokens_in":      active.TokensIn,
			"tokens_out":     active.TokensOut,
			"estimated_cost": active.EstimatedCost,
			"model_used":     active.ModelUsed,
		},
	})
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

// agentFeedWorkspace resolves the workspace an agent's own task feed is confined
// to, from the AUTHENTICATED key rather than from anything in the request.
//
// /agents/me/tasks and its long-poll twin carry no path parameter, so the only
// workspace in play is the one the agent's API key resolved to — which is the
// property that makes the scoping meaningful. Taking it from a query parameter
// instead would hand the caller back the very choice this guard removes.
//
// Fails closed: an unresolvable workspace yields no feed rather than an
// unscoped one.
func agentFeedWorkspace(c echo.Context) (uuid.UUID, error) {
	wsID, err := mw.GetWorkspaceID(c)
	if err != nil || wsID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("workspace unresolved for agent key")
	}
	return wsID, nil
}

// validStatusCategories is the closed set accepted by the ?status_category
// filter. An unrecognised value is rejected rather than quietly matching
// nothing: an agent that mistypes the category and receives {"tasks":[]}
// concludes it has no work, which is indistinguishable from being idle.
var validStatusCategories = map[domain.StatusCategory]struct{}{
	domain.StatusCategoryBacklog:    {},
	domain.StatusCategoryTodo:       {},
	domain.StatusCategoryInProgress: {},
	domain.StatusCategoryReview:     {},
	domain.StatusCategoryDone:       {},
	domain.StatusCategoryCancelled:  {},
	domain.StatusCategoryTriage:     {},
}

// parseAgentFeedFilter reads the feed's query parameters into a repository
// filter applied in SQL.
//
// limit is accepted under both names: the MCP client has always sent page_size
// (evc-mesh-mcp, internal/mcp/tools.go, get_my_tasks), and "limit" is what a hand-written
// caller reaches for. Neither was read before this change, so an agent asking
// for 50 tasks was served all of them.
func parseAgentFeedFilter(c echo.Context) (repository.AssigneeTaskFilter, error) {
	var filter repository.AssigneeTaskFilter

	if raw := c.QueryParam("status_category"); raw != "" {
		cat := domain.StatusCategory(raw)
		if _, ok := validStatusCategories[cat]; !ok {
			return filter, fmt.Errorf("unknown status_category %q", raw)
		}
		filter.StatusCategory = &cat
	}

	if raw := c.QueryParam("project_id"); raw != "" {
		projID, err := uuid.Parse(raw)
		if err != nil {
			return filter, fmt.Errorf("project_id must be a UUID")
		}
		filter.ProjectID = &projID
	}

	rawLimit := c.QueryParam("limit")
	if rawLimit == "" {
		rawLimit = c.QueryParam("page_size")
	}
	if rawLimit != "" {
		n, err := strconv.Atoi(rawLimit)
		if err != nil || n < 1 {
			return filter, fmt.Errorf("limit must be a positive integer")
		}
		filter.Limit = n
	}

	return filter, nil
}

// GetMyTasks handles GET /agents/me/tasks
// Returns tasks assigned to the current agent.
//
// Optional query params, all applied in SQL:
//   - status_category — backlog|todo|in_progress|review|done|cancelled|triage
//   - project_id      — restrict to one project
//   - limit/page_size — cap the number of rows; omit for the whole feed
//
// When a limit truncates the result the response carries total_count and
// has_more, so a partial feed is never mistaken for an empty one.
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

	workspaceID, wsErr := agentFeedWorkspace(c)
	if wsErr != nil {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
	}

	filter, filterErr := parseAgentFeedFilter(c)
	if filterErr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest(filterErr.Error()))
	}

	tasks, total, err := h.taskService.GetMyTasks(
		c.Request().Context(), workspaceID, agentID, domain.AssigneeTypeAgent, filter)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"tasks":       tasks,
		"count":       len(tasks),
		"total_count": total,
		"has_more":    len(tasks) < total,
	})
}

// sseReplayLimit is the max number of events fetched per replay query.
const sseReplayLimit = 500

// EventStream handles GET /agents/me/events/stream
// Server-Sent Events endpoint for agents to receive real-time notifications.
//
// When the client sends a Last-Event-ID header and agentEventsRepo is wired,
// the handler replays all missed events before switching to live mode.
// If the cursor is expired or unknown, returns 410 Gone so the client can
// perform a full state recovery (get_my_tasks across all categories).
//
// Without Last-Event-ID (or without agentEventsRepo), behaves exactly as before:
// subscribe and stream live events only.
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

	// Parse Last-Event-ID cursor before subscribing so we can return 410 before SSE headers.
	lastEventIDStr := c.Request().Header.Get("Last-Event-ID")
	var lastEventID uuid.UUID
	wantReplay := false
	if lastEventIDStr != "" && h.agentEventsRepo != nil {
		parsed, parseErr := uuid.Parse(lastEventIDStr)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid Last-Event-ID: must be a UUID"))
		}
		// Validate cursor exists and hasn't expired.
		existing, lookupErr := h.agentEventsRepo.Lookup(c.Request().Context(), parsed)
		if lookupErr != nil {
			return c.JSON(http.StatusInternalServerError, apierror.InternalError("cursor lookup failed"))
		}
		if existing == nil {
			// Cursor unknown or expired — client must do full recovery.
			return c.JSON(http.StatusGone, map[string]string{
				"error":   "cursor_expired",
				"message": "Last-Event-ID cursor expired or unknown; perform full state recovery via get_my_tasks",
			})
		}
		lastEventID = parsed
		wantReplay = true
	}

	channel := fmt.Sprintf("%s%s", agentNotifyChannelPrefix, agentID.String())

	// Subscribe BEFORE querying the DB to avoid the race where an event is published
	// between the DB query end and subscription start.
	sub := h.rdb.Subscribe(c.Request().Context(), channel)
	defer func() { _ = sub.Close() }()
	subCh := sub.Channel()

	// Track SSE connection in presence registry.
	presence.Register(agentID)
	defer presence.Unregister(agentID)
	_ = h.agentService.TouchLastSeen(c.Request().Context(), agentID)

	// Write SSE response headers now that we've validated the cursor (if any).
	c.Response().Header().Set("Content-Type", "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().Header().Set("Connection", "keep-alive")
	c.Response().Header().Set("X-Accel-Buffering", "no")
	c.Response().WriteHeader(http.StatusOK)
	c.Response().Flush()

	// maxReplayed tracks the highest event_id sent during DB replay so we can
	// deduplicate events that arrived in the Redis buffer during that window.
	var maxReplayed uuid.UUID

	if wantReplay {
		replayed, err := h.agentEventsRepo.ListAfter(c.Request().Context(), agentID, lastEventID, sseReplayLimit)
		if err != nil {
			// Non-fatal: stream is open, just switch to live mode.
			log.Printf("[sse-replay] ListAfter failed for agent %s: %v", agentID, err)
		} else {
			for _, ev := range replayed {
				eventType := ev.EventType
				if eventType == "" {
					eventType = "message"
				}
				if _, writeErr := fmt.Fprintf(c.Response(), "id: %s\nevent: %s\ndata: %s\n\n",
					ev.EventID.String(), eventType, string(ev.Payload)); writeErr != nil {
					return nil
				}
				maxReplayed = ev.EventID
			}
			if len(replayed) > 0 {
				c.Response().Flush()
			}
		}
	}

	keepalive := time.NewTicker(agentSSEKeepaliveInterval)
	defer keepalive.Stop()
	reqCtx := c.Request().Context()

	for {
		select {
		case <-reqCtx.Done():
			return nil

		case msg, ok := <-subCh:
			if !ok {
				return nil
			}
			var notif map[string]any
			eventType := "message"
			eventIDStr := ""
			if err := json.Unmarshal([]byte(msg.Payload), &notif); err == nil {
				if et, ok := notif["event_type"].(string); ok && et != "" {
					eventType = et
				}
				if eid, ok := notif["event_id"].(string); ok && eid != "" {
					eventIDStr = eid
				}
			}
			// Deduplicate: skip events already sent during DB replay.
			if wantReplay && eventIDStr != "" {
				if incomingID, parseErr := uuid.Parse(eventIDStr); parseErr == nil {
					// UUID v7 bytes are time-ordered so byte comparison is correct.
					if incomingID.String() <= maxReplayed.String() {
						continue
					}
				}
			}
			idLine := ""
			if eventIDStr != "" {
				idLine = fmt.Sprintf("id: %s\n", eventIDStr)
			}
			if _, err := fmt.Fprintf(c.Response(), "%sevent: %s\ndata: %s\n\n", idLine, eventType, msg.Payload); err != nil {
				return nil
			}
			c.Response().Flush()

		case <-keepalive.C:
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

	// Resolved up front, before the caller is parked on a long poll: refusing
	// after a 30-second wait would report an authorization outcome as a timeout.
	workspaceID, wsErr := agentFeedWorkspace(c)
	if wsErr != nil {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
	}

	// Parsed before parking the caller for up to two minutes: a malformed filter
	// is a client bug and must be reported immediately, not after the timeout.
	filter, filterErr := parseAgentFeedFilter(c)
	if filterErr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest(filterErr.Error()))
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
	defer func() { _ = sub.Close() }()

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

	// Fetch current tasks for this agent. The same filters as the non-polling
	// twin apply, so a long-poller can ask for just its todo queue instead of
	// re-downloading the whole feed on every wake-up.
	ctx := context.Background()
	tasks, total, err := h.taskService.GetMyTasks(ctx, workspaceID, agentID, domain.AssigneeTypeAgent, filter)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"tasks":       tasks,
		"count":       len(tasks),
		"total_count": total,
		"has_more":    len(tasks) < total,
		"changed":     changed,
	})
}
