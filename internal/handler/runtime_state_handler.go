package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// putRuntimeStateRequest is the body of PUT /tasks/:task_id/runtime-state.
// `kind: null` (or an absent kind alongside no other field) clears the state.
type putRuntimeStateRequest struct {
	Kind   *string    `json:"kind"`
	Since  *time.Time `json:"since"`
	Until  *time.Time `json:"until"`
	Ref    *string    `json:"ref"`
	Lane   *string    `json:"lane"`
	Reason *string    `json:"reason"`
	// ReasonCode is a code from the closed domain.RuntimeReasonCode list; the web
	// renders it in the viewer's language. ReasonParams fill its placeholders.
	ReasonCode   *string           `json:"reason_code"`
	ReasonParams map[string]string `json:"reason_params"`
}

// PutRuntimeState handles PUT /tasks/:task_id/runtime-state.
//
// Who may write is decided by the route (workspace access + update_task), the same
// gate as human-gate: fleet service agents report on behalf of lanes, so tying it to
// assignee/checkout holder would lock out exactly the writer this endpoint is for.
// 422 on a bad value, naming the field.
func (h *TaskHandler) PutRuntimeState(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskService)
	if err != nil {
		return handleError(c, err)
	}
	var req putRuntimeStateRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	// An omitted kind next to other fields is a malformed write, not a clear:
	// silently deleting the stored state would also drop what the caller sent.
	if req.Kind == nil && (req.Since != nil || req.Until != nil || req.Ref != nil || req.Lane != nil || req.Reason != nil || req.ReasonCode != nil || req.ReasonParams != nil) {
		return c.JSON(http.StatusUnprocessableEntity, map[string]any{
			"error":      "Unprocessable Entity",
			"error_code": apierror.CodeValidationFailed,
			"message":    "invalid runtime_state: kind is required unless the state is being cleared",
			"field":      "kind",
		})
	}

	var state *domain.RuntimeState
	if req.Kind != nil {
		state = &domain.RuntimeState{Kind: domain.RuntimeKind(*req.Kind), Until: req.Until, Ref: req.Ref, Lane: req.Lane, Reason: req.Reason, ReasonCode: req.ReasonCode, ReasonParams: req.ReasonParams}
		if req.Since != nil {
			state.Since = *req.Since
		}
	}
	return h.writeRuntimeState(c, taskID, state)
}

// DeleteRuntimeState handles DELETE /tasks/:task_id/runtime-state.
func (h *TaskHandler) DeleteRuntimeState(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskService)
	if err != nil {
		return handleError(c, err)
	}
	if _, err := h.taskService.SetRuntimeState(c.Request().Context(), taskID, nil); err != nil {
		return handleError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *TaskHandler) writeRuntimeState(c echo.Context, taskID uuid.UUID, state *domain.RuntimeState) error {
	eff, err := h.taskService.SetRuntimeState(c.Request().Context(), taskID, state)
	if err != nil {
		var vErr *domain.RuntimeStateValidationError
		if errors.As(err, &vErr) {
			return c.JSON(http.StatusUnprocessableEntity, map[string]any{
				"error":      "Unprocessable Entity",
				"error_code": apierror.CodeValidationFailed,
				"message":    "invalid runtime_state: " + vErr.Field + " " + vErr.Message,
				"field":      vErr.Field,
			})
		}
		return handleError(c, err)
	}
	if eff == nil {
		return c.JSON(http.StatusOK, map[string]any{"runtime_state": nil})
	}
	return c.JSON(http.StatusOK, eff)
}
