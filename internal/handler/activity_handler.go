package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// ActivityHandler handles HTTP requests for activity log.
type ActivityHandler struct {
	activityService service.ActivityLogService
}

// NewActivityHandler creates a new ActivityHandler with the given service.
func NewActivityHandler(as service.ActivityLogService) *ActivityHandler {
	return &ActivityHandler{activityService: as}
}

// listActivityQuery represents query parameters for listing activity.
type listActivityQuery struct {
	EntityType string `query:"entity_type"`
	EntityID   string `query:"entity_id"`
	ActorID    string `query:"actor_id"`
	ActorType  string `query:"actor_type"`
	Action     string `query:"action"`
}

// ListByWorkspace handles GET /workspaces/:ws_id/activity
func (h *ActivityHandler) ListByWorkspace(c echo.Context) error {
	wsIDStr := c.Param("ws_id")
	wsID, err := uuid.Parse(wsIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	var q listActivityQuery
	if err := c.Bind(&q); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid query parameters"))
	}

	var pg pagination.Params
	if err := c.Bind(&pg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination parameters"))
	}
	pg.Normalize()

	filter := repository.ActivityLogFilter{}

	if q.EntityType != "" {
		filter.EntityType = &q.EntityType
	}
	if q.EntityID != "" {
		entityID, err := uuid.Parse(q.EntityID)
		if err == nil {
			filter.EntityID = &entityID
		}
	}
	if q.ActorID != "" {
		actorID, err := uuid.Parse(q.ActorID)
		if err == nil {
			filter.ActorID = &actorID
		}
	}
	if q.ActorType != "" {
		at := domain.ActorType(q.ActorType)
		filter.ActorType = &at
	}
	if q.Action != "" {
		filter.Action = &q.Action
	}

	page, err := h.activityService.List(c.Request().Context(), wsID, filter, pg)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, page)
}

// ListByTask handles GET /tasks/:task_id/activity
func (h *ActivityHandler) ListByTask(c echo.Context) error {
	taskIDStr := c.Param("task_id")
	taskID, err := uuid.Parse(taskIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}

	var pg pagination.Params
	if err := c.Bind(&pg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination parameters"))
	}
	pg.Normalize()

	page, err := h.activityService.ListByTask(c.Request().Context(), taskID, pg)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, page)
}
