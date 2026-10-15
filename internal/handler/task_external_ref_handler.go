package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// TaskExternalRefHandler serves /tasks/:task_id/external-ref.
type TaskExternalRefHandler struct {
	refs  service.TaskExternalRefService
	tasks taskIDResolver
}

// NewTaskExternalRefHandler creates the handler.
func NewTaskExternalRefHandler(refs service.TaskExternalRefService, tasks taskIDResolver) *TaskExternalRefHandler {
	return &TaskExternalRefHandler{refs: refs, tasks: tasks}
}

type putExternalRefRequest struct {
	Ref  string `json:"external_ref"`
	Hash string `json:"external_hash"`
}

// Get handles GET /tasks/:task_id/external-ref (404 when the card is unbound).
func (h *TaskExternalRefHandler) Get(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.tasks)
	if err != nil {
		return handleError(c, err)
	}
	ref, err := h.refs.Get(c.Request().Context(), taskID)
	if err != nil {
		return handleError(c, err)
	}
	if ref == nil {
		return c.JSON(http.StatusNotFound, apierror.NotFound("ExternalRef"))
	}
	return c.JSON(http.StatusOK, ref)
}

// Put handles PUT /tasks/:task_id/external-ref. external_hash is write-once:
// the same value is idempotent, a different one is 409 and changes nothing.
func (h *TaskExternalRefHandler) Put(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.tasks)
	if err != nil {
		return handleError(c, err)
	}
	var req putExternalRefRequest
	if bindErr := c.Bind(&req); bindErr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	actor, uerr := mw.GetUserID(c)
	if uerr != nil {
		actor, uerr = mw.GetAgentID(c)
	}
	var by = &actor
	if uerr != nil {
		by = nil
	}
	ref, err := h.refs.Put(c.Request().Context(), taskID, req.Ref, req.Hash, by)
	switch {
	case errors.Is(err, service.ErrExternalRefInvalid):
		return c.JSON(http.StatusUnprocessableEntity, map[string]any{
			"error":      "Unprocessable Entity",
			"error_code": apierror.CodeValidationFailed,
			"message":    "external_ref and external_hash are required (ref ≤ 512, hash ≤ 128 chars)",
		})
	case errors.Is(err, service.ErrExternalHashConflict):
		return c.JSON(http.StatusConflict, apierror.Conflict("external_hash is already set for this task and cannot be changed"))
	case err != nil:
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, ref)
}
