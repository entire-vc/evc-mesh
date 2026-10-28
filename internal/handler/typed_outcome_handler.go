package handler

import (
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// TypedOutcomeHandler serves the runner's reads of typed turn outcomes:
// /tasks/:task_id/turn-outcomes and /tasks/:task_id/wait
// (docs/api/typed-outcomes.md §Reads).
type TypedOutcomeHandler struct {
	outcomes service.TypedOutcomeService
	tasks    taskIDResolver
}

// NewTypedOutcomeHandler creates the handler.
func NewTypedOutcomeHandler(outcomes service.TypedOutcomeService, tasks taskIDResolver) *TypedOutcomeHandler {
	return &TypedOutcomeHandler{outcomes: outcomes, tasks: tasks}
}

// ListTurnOutcomes handles GET /tasks/:task_id/turn-outcomes?session_id=&limit=
// — the task's outcomes newest first, one session's when session_id is given.
// An empty list is the explicit no_outcome state, never an error.
func (h *TypedOutcomeHandler) ListTurnOutcomes(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.tasks)
	if err != nil {
		return handleError(c, err)
	}
	var sessionID *string
	if s := c.QueryParam("session_id"); s != "" {
		sessionID = &s
	}
	limit := 0
	if l := c.QueryParam("limit"); l != "" {
		if limit, err = strconv.Atoi(l); err != nil || limit < 1 {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("limit must be a positive integer"))
		}
	}
	list, err := h.outcomes.ListTurnOutcomes(c.Request().Context(), taskID, sessionID, limit)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, list)
}

// GetActiveWait handles GET /tasks/:task_id/wait?agent_id= — the active wait of
// that agent, or the newest active wait on the task without agent_id. None is
// 404 with error_code no_active_wait, so a reader tells "no wait" from "no task".
func (h *TypedOutcomeHandler) GetActiveWait(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.tasks)
	if err != nil {
		return handleError(c, err)
	}
	var agentID *uuid.UUID
	if a := c.QueryParam("agent_id"); a != "" {
		id, perr := uuid.Parse(a)
		if perr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid agent_id"))
		}
		agentID = &id
	}
	w, err := h.outcomes.ActiveWait(c.Request().Context(), taskID, agentID)
	if err != nil {
		return handleError(c, err)
	}
	if w == nil {
		return c.JSON(http.StatusNotFound, &apierror.Error{
			Code:      http.StatusNotFound,
			ErrorCode: domain.CodeNoActiveWait,
			Message:   "no active wait on this task",
		})
	}
	return c.JSON(http.StatusOK, w)
}
