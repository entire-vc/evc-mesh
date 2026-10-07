package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func (h *TaskHandler) RegisterParkedWait(c echo.Context) error {
	id, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskService)
	if err != nil {
		return handleError(c, err)
	}
	var input domain.ParkedWaitPlan
	if checkErr := c.Bind(&input); checkErr != nil {
		return handleError(c, apierror.BadRequest("invalid registration body"))
	}
	svc, ok := h.taskService.(service.ParkedWaitService)
	if !ok {
		return handleError(c, apierror.ServiceUnavailable("parked wait registration unavailable"))
	}
	result, err := svc.RegisterParkedWait(c.Request().Context(), id, input)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, result)
}

func (h *TaskHandler) ReleaseParkedWait(c echo.Context) error {
	id, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskService)
	if err != nil {
		return handleError(c, err)
	}
	var input domain.ReleaseParkedWait
	if checkErr := c.Bind(&input); checkErr != nil {
		return handleError(c, apierror.BadRequest("invalid release body"))
	}
	svc, ok := h.taskService.(service.ParkedWaitService)
	if !ok {
		return handleError(c, apierror.ServiceUnavailable("parked wait release unavailable"))
	}
	result, err := svc.ReleaseParkedWait(c.Request().Context(), id, input)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, result)
}
