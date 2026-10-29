package handler

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// TypedOutcomeCommandHandler serves ask_agent, answer_decision and finish_task
// (docs/api/typed-outcomes.md §Commands 3–5).
type TypedOutcomeCommandHandler struct {
	cmds  service.TypedOutcomeCommands
	tasks taskIDResolver
}

// NewTypedOutcomeCommandHandler creates the handler.
func NewTypedOutcomeCommandHandler(cmds service.TypedOutcomeCommands, tasks taskIDResolver) *TypedOutcomeCommandHandler {
	return &TypedOutcomeCommandHandler{cmds: cmds, tasks: tasks}
}

// commandError answers a typed-outcome refusal with 422 and its error_code;
// everything else keeps the API's usual mapping.
func commandError(c echo.Context, err error) error {
	var oe *domain.OutcomeError
	if errors.As(err, &oe) {
		return c.JSON(http.StatusUnprocessableEntity, &apierror.Error{
			Code:      http.StatusUnprocessableEntity,
			ErrorCode: oe.Code,
			Message:   oe.Message,
		})
	}
	return handleError(c, err)
}

// AskAgent handles POST /tasks/:task_id/decision-requests.
func (h *TypedOutcomeCommandHandler) AskAgent(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.tasks)
	if err != nil {
		return handleError(c, err)
	}
	var in service.AskAgentInput
	if bindErr := c.Bind(&in); bindErr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	res, err := h.cmds.AskAgent(c.Request().Context(), taskID, in)
	if err != nil {
		return commandError(c, err)
	}
	return c.JSON(http.StatusCreated, res)
}

// AnswerDecision handles POST /decision-requests/:decision_request_id/answer.
func (h *TypedOutcomeCommandHandler) AnswerDecision(c echo.Context) error {
	id, err := uuid.Parse(c.Param("decision_request_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid decision request id"))
	}
	var in service.AnswerDecisionInput
	if bindErr := c.Bind(&in); bindErr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	res, err := h.cmds.AnswerDecision(c.Request().Context(), id, in)
	if err != nil {
		return commandError(c, err)
	}
	return c.JSON(http.StatusOK, res)
}

// FinishTask handles POST /tasks/:task_id/finish.
func (h *TypedOutcomeCommandHandler) FinishTask(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.tasks)
	if err != nil {
		return handleError(c, err)
	}
	var in service.FinishTaskInput
	if bindErr := c.Bind(&in); bindErr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	res, err := h.cmds.FinishTask(c.Request().Context(), taskID, in)
	if err != nil {
		return commandError(c, err)
	}
	return c.JSON(http.StatusOK, res)
}
