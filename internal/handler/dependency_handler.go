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

// DependencyHandler handles HTTP requests for task dependency management.
type DependencyHandler struct {
	depService  service.TaskDependencyService
	taskService service.TaskService
}

// NewDependencyHandler creates a new DependencyHandler with the given services.
func NewDependencyHandler(ds service.TaskDependencyService, ts service.TaskService) *DependencyHandler {
	return &DependencyHandler{depService: ds, taskService: ts}
}

// createDependencyRequest represents the JSON body for creating a dependency.
type createDependencyRequest struct {
	DependsOnTaskID uuid.UUID             `json:"depends_on_task_id"`
	DependencyType  domain.DependencyType `json:"dependency_type"`
}

// List handles GET /tasks/:task_id/dependencies
func (h *DependencyHandler) List(c echo.Context) error {
	taskIDStr := c.Param("task_id")
	taskID, err := uuid.Parse(taskIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}

	deps, err := h.depService.ListByTask(c.Request().Context(), taskID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, deps)
}

// Create handles POST /tasks/:task_id/dependencies
func (h *DependencyHandler) Create(c echo.Context) error {
	taskIDStr := c.Param("task_id")
	taskID, err := uuid.Parse(taskIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}

	var req createDependencyRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.DependsOnTaskID == uuid.Nil {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"depends_on_task_id": "depends_on_task_id is required",
		}))
	}

	depType := req.DependencyType
	if depType == "" {
		depType = domain.DependencyTypeBlocks
	}

	dep := &domain.TaskDependency{
		ID:              uuid.New(),
		TaskID:          taskID,
		DependsOnTaskID: req.DependsOnTaskID,
		DependencyType:  depType,
	}

	if err := h.depService.Create(c.Request().Context(), dep); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, dep)
}

// Delete handles DELETE /tasks/:task_id/dependencies/:dep_id
//
// The edge has to hang off the task in the path. It did not have to before: only
// :dep_id was read, and the service deleted by id alone, so a member of any
// workspace could send one of their own task ids together with a stranger's
// :dep_id and cut an edge out of another tenant's dependency graph — 204, row
// gone, and the blocked task silently became startable.
//
// The guard refuses this request before it arrives now
// (middleware.workspaceParamResolvers); this is the same check written where the
// delete happens.
func (h *DependencyHandler) Delete(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskService)
	if err != nil {
		return handleError(c, err)
	}

	depIDStr := c.Param("dep_id")
	depID, err := uuid.Parse(depIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid dependency_id"))
	}

	ctx := c.Request().Context()
	deps, err := h.depService.ListByTask(ctx, taskID)
	if err != nil {
		return handleError(c, err)
	}
	belongs := false
	for i := range deps {
		if deps[i].ID == depID {
			belongs = true
			break
		}
	}
	if !belongs {
		return c.JSON(http.StatusNotFound, apierror.NotFound("TaskDependency"))
	}

	if err := h.depService.Delete(ctx, depID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// dependencyGraphResponse is returned by the DependencyGraph endpoint.
type dependencyGraphResponse struct {
	Tasks        []domain.Task           `json:"tasks"`
	Dependencies []domain.TaskDependency `json:"dependencies"`
}

// DependencyGraph handles GET /projects/:proj_id/dependency-graph
// It returns all tasks for the project together with all their dependencies in a single response,
// avoiding N+1 fetches on the client side.
func (h *DependencyHandler) DependencyGraph(c echo.Context) error {
	projIDStr := c.Param("proj_id")
	projID, err := uuid.Parse(projIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	ctx := c.Request().Context()

	// Fetch all tasks for the project (up to 1000).
	pg := pagination.Params{Page: 1, PageSize: 1000}
	pg.Normalize()
	taskPage, err := h.taskService.List(ctx, projID, repository.TaskFilter{}, pg)
	if err != nil {
		return handleError(c, err)
	}

	// Collect all dependencies for these tasks, deduplicating by ID.
	seen := make(map[uuid.UUID]bool)
	var allDeps []domain.TaskDependency
	for _, task := range taskPage.Items {
		deps, err := h.depService.ListByTask(ctx, task.ID)
		if err != nil {
			return handleError(c, err)
		}
		for _, d := range deps {
			if !seen[d.ID] {
				seen[d.ID] = true
				allDeps = append(allDeps, d)
			}
		}
	}

	if allDeps == nil {
		allDeps = []domain.TaskDependency{}
	}

	return c.JSON(http.StatusOK, dependencyGraphResponse{
		Tasks:        taskPage.Items,
		Dependencies: allDeps,
	})
}
