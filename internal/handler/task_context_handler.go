package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// TaskContextHandler handles the GET /tasks/:task_id/context endpoint.
// It aggregates task details, comments, artifacts, dependencies, and events.
type TaskContextHandler struct {
	taskService           service.TaskService
	commentService        service.CommentService
	artifactService       service.ArtifactService
	taskDependencyService service.TaskDependencyService
	eventBusService       service.EventBusService
	cache                 *service.ContextCacheService // optional; nil = no caching
}

// NewTaskContextHandler creates a new TaskContextHandler without caching.
func NewTaskContextHandler(
	taskService service.TaskService,
	commentService service.CommentService,
	artifactService service.ArtifactService,
	taskDependencyService service.TaskDependencyService,
	eventBusService service.EventBusService,
) *TaskContextHandler {
	return &TaskContextHandler{
		taskService:           taskService,
		commentService:        commentService,
		artifactService:       artifactService,
		taskDependencyService: taskDependencyService,
		eventBusService:       eventBusService,
	}
}

// NewTaskContextHandlerWithCache creates a new TaskContextHandler with Redis caching.
// When cache is nil the handler behaves identically to NewTaskContextHandler.
func NewTaskContextHandlerWithCache(
	taskService service.TaskService,
	commentService service.CommentService,
	artifactService service.ArtifactService,
	taskDependencyService service.TaskDependencyService,
	eventBusService service.EventBusService,
	cache *service.ContextCacheService,
) *TaskContextHandler {
	return &TaskContextHandler{
		taskService:           taskService,
		commentService:        commentService,
		artifactService:       artifactService,
		taskDependencyService: taskDependencyService,
		eventBusService:       eventBusService,
		cache:                 cache,
	}
}

// GetTaskContext handles GET /tasks/:task_id/context.
// Returns a comprehensive view of the task including all related data.
//
// Cache strategy:
//   - On hit: return cached JSON directly (no upstream calls).
//   - On miss: build the response, marshal it once, store in cache, return.
//   - If Redis is unavailable the cache is skipped silently.
func (h *TaskContextHandler) GetTaskContext(c echo.Context) error {
	taskIDStr := c.Param("task_id")
	taskID, err := uuid.Parse(taskIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}

	// Cache lookup — skip on miss or unavailability.
	if cached, ok := h.cache.Get(c.Request().Context(), taskID); ok {
		c.Response().Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSONCharsetUTF8)
		return c.JSONBlob(http.StatusOK, cached)
	}

	task, err := h.taskService.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return handleError(c, err)
	}
	if task == nil {
		return c.JSON(http.StatusNotFound, apierror.NotFound("Task"))
	}

	resp := map[string]any{
		"task": task,
	}

	// Comments — best effort.
	commentFilter := repository.CommentFilter{IncludeInternal: true}
	commentPg := pagination.Params{Page: 1, PageSize: 100, SortBy: "created_at", SortDir: "asc"}
	commentPg.Normalize()
	if commentPage, err := h.commentService.ListByTask(c.Request().Context(), taskID, commentFilter, commentPg); err == nil {
		resp["comments"] = commentPage.Items
	} else {
		resp["comments"] = []any{}
	}

	// Artifacts — best effort.
	artifactPg := pagination.Params{Page: 1, PageSize: 100, SortBy: "created_at", SortDir: "desc"}
	artifactPg.Normalize()
	if artifactPage, err := h.artifactService.ListByTask(c.Request().Context(), taskID, artifactPg); err == nil {
		resp["artifacts"] = artifactPage.Items
	} else {
		resp["artifacts"] = []any{}
	}

	// Dependencies — best effort.
	if deps, err := h.taskDependencyService.ListByTask(c.Request().Context(), taskID); err == nil {
		resp["dependencies"] = deps
	} else {
		resp["dependencies"] = []any{}
	}

	// Events — best effort.
	eventOpts := service.GetContextOptions{
		TaskID: &taskID,
		Limit:  50,
	}
	if events, err := h.eventBusService.GetContext(c.Request().Context(), task.ProjectID, eventOpts); err == nil {
		resp["events"] = events
	} else {
		resp["events"] = []any{}
	}

	// Marshal once and cache the result.
	data, marshalErr := json.Marshal(resp)
	if marshalErr == nil {
		h.cache.Set(c.Request().Context(), taskID, data)
		c.Response().Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSONCharsetUTF8)
		return c.JSONBlob(http.StatusOK, data)
	}

	// Fallback: let Echo marshal normally if json.Marshal unexpectedly fails.
	return c.JSON(http.StatusOK, resp)
}
