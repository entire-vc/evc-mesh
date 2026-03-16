package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// EventHandler handles HTTP requests for event bus management.
type EventHandler struct {
	eventService service.EventBusService
}

// NewEventHandler creates a new EventHandler with the given service.
func NewEventHandler(es service.EventBusService) *EventHandler {
	return &EventHandler{eventService: es}
}

// createEventRequest represents the JSON body for creating an event.
type createEventRequest struct {
	EventType  domain.EventType   `json:"event_type"`
	Subject    string             `json:"subject"`
	Payload    map[string]any     `json:"payload"`
	TaskID     *uuid.UUID         `json:"task_id"`
	Tags       []string           `json:"tags"`
	TTLSeconds int                `json:"ttl_seconds"`
	MemoryHint *domain.MemoryHint `json:"memory_hint"`
}

// listEventsQuery represents query parameters for listing events.
type listEventsQuery struct {
	EventType string `query:"event_type"`
	AgentID   string `query:"agent_id"`
	TaskID    string `query:"task_id"`
	Tags      string `query:"tags"`
}

// List handles GET /projects/:proj_id/events
func (h *EventHandler) List(c echo.Context) error {
	projIDStr := c.Param("proj_id")
	projID, err := uuid.Parse(projIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	var q listEventsQuery
	if err = c.Bind(&q); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid query parameters"))
	}

	var pg pagination.Params
	if err = c.Bind(&pg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination parameters"))
	}
	pg.Normalize()

	filter := repository.EventBusMessageFilter{}

	if q.EventType != "" {
		et := domain.EventType(q.EventType)
		filter.EventType = &et
	}
	if q.AgentID != "" {
		var agentID uuid.UUID
		agentID, err = uuid.Parse(q.AgentID)
		if err == nil {
			filter.AgentID = &agentID
		}
	}
	if q.TaskID != "" {
		var taskID uuid.UUID
		taskID, err = uuid.Parse(q.TaskID)
		if err == nil {
			filter.TaskID = &taskID
		}
	}
	if q.Tags != "" {
		filter.Tags = []string{q.Tags}
	}

	page, err := h.eventService.List(c.Request().Context(), projID, filter, pg)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, page)
}

// Create handles POST /projects/:proj_id/events
func (h *EventHandler) Create(c echo.Context) error {
	projIDStr := c.Param("proj_id")
	projID, err := uuid.Parse(projIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	var req createEventRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.Subject == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"subject": "subject is required",
		}))
	}

	// Determine the agent ID from context if available.
	var agentID *uuid.UUID
	if agentIDVal := c.Get("agent_id"); agentIDVal != nil {
		if aid, ok := agentIDVal.(uuid.UUID); ok {
			agentID = &aid
		}
	}

	// Ensure payload is not nil.
	var payloadMap map[string]any
	if req.Payload != nil {
		payloadMap = req.Payload
	} else {
		payloadMap = map[string]any{}
	}
	// Validate payload is serializable.
	if _, err = json.Marshal(payloadMap); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid payload"))
	}

	// Resolve workspace_id from auth context (agent key sets it)
	// or fall back to looking up the project.
	wsID, _ := mw.GetWorkspaceID(c)

	input := service.PublishEventInput{
		WorkspaceID: wsID,
		ProjectID:   projID,
		TaskID:      req.TaskID,
		AgentID:     agentID,
		EventType:   req.EventType,
		Subject:     req.Subject,
		Payload:     payloadMap,
		Tags:        req.Tags,
		TTLSeconds:  req.TTLSeconds,
		MemoryHint:  req.MemoryHint,
	}

	event, err := h.eventService.Publish(c.Request().Context(), input)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, event)
}

// GetByID handles GET /events/:event_id
func (h *EventHandler) GetByID(c echo.Context) error {
	eventIDStr := c.Param("event_id")
	eventID, err := uuid.Parse(eventIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid event_id"))
	}

	event, err := h.eventService.GetByID(c.Request().Context(), eventID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, event)
}
