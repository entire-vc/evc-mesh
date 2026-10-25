package handler

import (
	"net/http"
	"strconv"

	"github.com/entire-vc/evc-mesh/internal/middleware"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// DependencyHandler handles HTTP requests for task dependency management.
type DependencyHandler struct {
	depService  service.TaskDependencyService
	taskService service.TaskService
	graphReader repository.DependencyGraphReader
}

// NewDependencyHandler creates a new DependencyHandler with the given services.
func NewDependencyHandler(ds service.TaskDependencyService, ts service.TaskService) *DependencyHandler {
	return &DependencyHandler{depService: ds, taskService: ts}
}

// WithGraphReader wires the bulk, read-only projection independently of write services.
func (h *DependencyHandler) WithGraphReader(reader repository.DependencyGraphReader) *DependencyHandler {
	h.graphReader = reader
	return h
}

// createDependencyRequest represents the JSON body for creating a dependency.
type createDependencyRequest struct {
	DependsOnTaskID uuid.UUID             `json:"depends_on_task_id"`
	DependencyType  domain.DependencyType `json:"dependency_type"`
	// Condition defaults to task_done; EvidenceURL with another condition marks
	// the edge satisfied at creation.
	Condition   domain.DependencyCondition `json:"condition"`
	EvidenceURL *string                    `json:"evidence_url"`
}

// updateDependencyRequest is the PATCH body; omitted fields are kept.
type updateDependencyRequest struct {
	Condition   *domain.DependencyCondition `json:"condition"`
	EvidenceURL *string                     `json:"evidence_url"`
}

// dependencyListResponse is returned by List. It replaces the old bare-array
// shape: a task's dependency graph has two sides, and returning only the
// outgoing one meant a parent task had no way to see its own subtasks or
// blockers here — it had to be the other end of the edge to see it at all.
type dependencyListResponse struct {
	Outgoing []domain.EnrichedTaskDependency `json:"outgoing"`
	Incoming []domain.EnrichedTaskDependency `json:"incoming"`
}

// List handles GET /tasks/:task_id/dependencies
func (h *DependencyHandler) List(c echo.Context) error {
	taskIDStr := c.Param("task_id")
	taskID, err := uuid.Parse(taskIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}

	outgoing, incoming, err := h.depService.ListByTaskBothDirections(c.Request().Context(), taskID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, dependencyListResponse{Outgoing: outgoing, Incoming: incoming})
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
		Condition:       req.Condition,
		EvidenceURL:     req.EvidenceURL,
	}

	if err := h.depService.Create(c.Request().Context(), dep); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, dep)
}

// Update handles PATCH /tasks/:task_id/dependencies/:dep_id: set the edge's
// condition and/or evidence_url. Evidence on a non-task_done edge marks it met
// now, so the dependent stops being held without the blocker reaching done.
func (h *DependencyHandler) Update(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskService)
	if err != nil {
		return handleError(c, err)
	}
	depID, err := uuid.Parse(c.Param("dep_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid dependency_id"))
	}
	var req updateDependencyRequest
	if bindErr := c.Bind(&req); bindErr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	if req.Condition == nil && req.EvidenceURL == nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("condition or evidence_url is required"))
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
	dep, err := h.depService.UpdateCondition(ctx, depID, service.UpdateDependencyConditionInput{Condition: req.Condition, EvidenceURL: req.EvidenceURL})
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, dep)
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

// DependencyGraph preserves the route and tasks/dependencies keys while making
// pagination and completeness explicit. No task-by-task fallback is permitted.
func (h *DependencyHandler) DependencyGraph(c echo.Context) error {
	projectID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return handleError(c, apierror.BadRequest("invalid proj_id"))
	}
	if h.graphReader == nil {
		return handleError(c, apierror.InternalError("dependency graph reader unavailable"))
	}
	opts := service.GraphOptions{}
	// Existing timeline clients get the complete compact graph. New overview
	// clients opt into pagination; a focus parameter selects the bounded view.
	switch c.QueryParam("mode") {
	case "", "overview":
		opts.Legacy = len(c.QueryParams()) == 0
	default:
		return handleError(c, apierror.BadRequest("invalid mode"))
	}
	if raw := c.QueryParam("focus"); raw != "" {
		opts.Focus, err = uuid.Parse(raw)
		if err != nil {
			return handleError(c, apierror.BadRequest("invalid focus"))
		}
	}
	for name, target := range map[string]*int{"page": &opts.Page, "page_size": &opts.PageSize, "before_page": &opts.BeforePage, "after_page": &opts.AfterPage, "relations_page": &opts.RelationsPage} {
		if raw := c.QueryParam(name); raw != "" {
			*target, err = strconv.Atoi(raw)
			if err != nil || *target < 1 || *target > 1000000 {
				return handleError(c, apierror.BadRequest("invalid "+name))
			}
		}
	}
	if raw := c.QueryParam("completed"); raw != "" {
		if raw != "true" && raw != "false" {
			return handleError(c, apierror.BadRequest("invalid completed"))
		}
		opts.Completed = raw == "true"
	}
	opts.Search = c.QueryParam("search")
	opts.Revision = c.QueryParam("revision")
	scope := domain.GraphScope{Agent: middleware.IsAgent(c)}
	scope.WorkspaceID, err = middleware.GetWorkspaceID(c)
	if err != nil {
		return handleError(c, apierror.Forbidden("workspace context required"))
	}
	if scope.Agent {
		scope.ActorID, err = middleware.GetAgentID(c)
	} else {
		scope.ActorID, err = middleware.GetUserID(c)
		role, _ := c.Get(middleware.ContextKeyWorkspaceRole).(string)
		scope.Admin = role == domain.RoleAdmin || role == domain.RoleOwner
	}
	if err != nil {
		return handleError(c, apierror.Forbidden("actor context required"))
	}
	snapshot, err := h.graphReader.ReadGraph(c.Request().Context(), projectID, opts.Focus, scope)
	if err != nil {
		return handleError(c, err)
	}
	result, err := service.ProjectGraph(snapshot, projectID, opts)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, result)
}
