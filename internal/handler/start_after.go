package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// CompareClearStartAfter handles POST /tasks/:task_id/start-after/compare-clear:
// the only way an agent removes start_after. 200 with the receipt (replayed for
// a repeated idempotency_key), 409 cas_conflict with the current values and no
// change, 422 for a request that does not name what it expects, 403 for an
// agent and a person's date.
func (h *TaskHandler) CompareClearStartAfter(c echo.Context) error {
	id, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskService)
	if err != nil {
		return handleError(c, err)
	}
	var input domain.StartAfterCompareClear
	if apiErr := bindStrictJSON(c, &input); apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}
	svc, ok := h.taskService.(service.StartAfterService)
	if !ok {
		return handleError(c, apierror.ServiceUnavailable("start_after compare-clear unavailable"))
	}
	result, err := svc.CompareClearStartAfter(c.Request().Context(), id, input)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, result)
}
