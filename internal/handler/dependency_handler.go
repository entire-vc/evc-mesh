package handler

import (
	"context"
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
	// vcsLinks feeds blocker review evidence (head_sha/ship_sha of the blocker's
	// PR links) into the next-action projection; nil disables the feed.
	proposals BindingProposalStore
	resolver  service.DependencyResolver
	vcsLinks  func(ctx context.Context, taskID uuid.UUID) ([]domain.VCSLink, error)
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

// WithVCSLinks wires the blocker's PR links so the projection can refuse a
// released edge whose recorded SHIP is not for the blocker's current head.
func (h *DependencyHandler) WithVCSLinks(f func(ctx context.Context, taskID uuid.UUID) ([]domain.VCSLink, error)) *DependencyHandler {
	h.vcsLinks = f
	return h
}

// blockerEvidence returns the current head and the independent verdict of the
// blocker's live (not closed) pr links. Only a recorded SHIP is positive: a link
// with a head but no ship_sha means no SHIP was given (a DO-NOT-SHIP is not
// stored anywhere, so it looks the same), and that is reported as a non-SHIP
// verdict so the projection never treats such a done blocker as released.
// Multiple links: any link whose verdict is not SHIP for its own head wins.
func (h *DependencyHandler) blockerEvidence(c echo.Context, blocker uuid.UUID) (string, *domain.ReviewVerdict) {
	if h.vcsLinks == nil || !middleware.MayAccessTask(c, blocker) {
		return "", nil
	}
	links, err := h.vcsLinks(c.Request().Context(), blocker)
	if err != nil {
		return "", nil
	}
	var head string
	var v *domain.ReviewVerdict
	for _, l := range links {
		if l.LinkType != domain.VCSLinkTypePR || l.Status == domain.VCSLinkStatusClosed || l.HeadSHA() == "" {
			continue
		}
		cand := &domain.ReviewVerdict{Verdict: "SHIP", SHA: l.ShipSHA()}
		if l.ShipSHA() == "" {
			cand = &domain.ReviewVerdict{Verdict: "NO-SHIP-RECORDED"}
		}
		negative := cand.Verdict != "SHIP" || cand.SHA != l.HeadSHA()
		if v == nil || (negative && (v.Verdict == "SHIP" && v.SHA == head)) {
			head, v = l.HeadSHA(), cand
		}
	}
	return head, v
}

// createDependencyRequest represents the JSON body for creating a dependency.
type createDependencyRequest struct {
	DependsOnTaskID uuid.UUID             `json:"depends_on_task_id"`
	DependencyType  domain.DependencyType `json:"dependency_type"`
	// Condition defaults to task_done; EvidenceURL with another condition marks
	// the edge satisfied at creation.
	Condition   domain.DependencyCondition `json:"condition"`
	Criterion   string                     `json:"criterion"`
	EvidenceURL *string                    `json:"evidence_url"`
}

// updateDependencyRequest is the PATCH body; omitted fields are kept.
type updateDependencyRequest struct {
	Condition   *domain.DependencyCondition `json:"condition"`
	Criterion   *string                     `json:"criterion"`
	EvidenceURL *string                     `json:"evidence_url"`
	// Claim bound to the criterion: compared with the blocker's own records.
	EvidenceCriterion string `json:"evidence_criterion"`
	HeadSHA           string `json:"head_sha"`
	Environment       string `json:"environment"`
	Attest            bool   `json:"attest"`
	// ExpectedGeneration is the edge generation the caller read (CAS).
	ExpectedGeneration *int64 `json:"expected_generation"`
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
	// Edges may cross projects. The edge itself belongs to this task, but what
	// it says about the far task is only for a caller who may open that task.
	for i := range outgoing {
		hideUnreachableRelatedTask(c, &outgoing[i], outgoing[i].DependsOnTaskID)
	}
	for i := range incoming {
		hideUnreachableRelatedTask(c, &incoming[i], incoming[i].TaskID)
	}

	return c.JSON(http.StatusOK, dependencyListResponse{Outgoing: outgoing, Incoming: incoming})
}

// NextAction handles GET /tasks/:task_id/next-action: the versioned projection
// of what may proceed now (prep) versus what still waits for a real prerequisite
// (final acceptance). Derived from existing edges on read; nothing is stored.
func (h *DependencyHandler) NextAction(c echo.Context) error {
	taskID, err := uuid.Parse(c.Param("task_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}
	p, err := h.projection(c, taskID)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, p)
}

// projection builds the next-action projection of taskID for this caller.
func (h *DependencyHandler) projection(c echo.Context, taskID uuid.UUID) (domain.NextActionProjection, error) {
	// ListByTaskBothDirections: "outgoing" are the edges where this task is the
	// dependent (task_id = taskID), i.e. what it REQUIRES; "incoming" are edges
	// where it is the blocker (depends_on_task_id = taskID), i.e. what it PRODUCES.
	outgoing, incoming, err := h.depService.ListByTaskBothDirections(c.Request().Context(), taskID)
	if err != nil {
		return domain.NextActionProjection{}, err
	}
	var in []domain.NextActionEdge
	for _, d := range outgoing {
		head, verdict := h.blockerEvidence(c, d.DependsOnTaskID)
		in = append(in, domain.NextActionEdge{Edge: d.TaskDependency, BlockerHead: head, Verdict: verdict})
	}
	var out []domain.TaskDependency
	for _, d := range incoming {
		if middleware.MayAccessTask(c, d.TaskID) {
			out = append(out, d.TaskDependency)
		}
	}
	return domain.BuildNextAction(taskID, in, out), nil
}

func hideUnreachableRelatedTask(c echo.Context, dep *domain.EnrichedTaskDependency, related uuid.UUID) {
	if !middleware.MayAccessTask(c, related) {
		dep.RelatedTaskTitle, dep.RelatedTaskStatusID = nil, nil
	}
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

	// The far end is named in the body, out of the route guard's sight. Edges
	// may cross projects, but only to a task the caller may open; any other
	// answers like a task that does not exist.
	if !middleware.MayAccessTask(c, req.DependsOnTaskID) {
		return c.JSON(http.StatusNotFound, apierror.NotFound("Task"))
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
		Criterion:       req.Criterion,
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
	if req.Condition == nil && req.EvidenceURL == nil && req.Criterion == nil {
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
	// Authority: attesting a criterion or recording a receipt needs access to the
	// blocker, the card the evidence is about; the writer is stored on the edge.
	var actor *uuid.UUID
	if id, aerr := middleware.GetAgentID(c); aerr == nil {
		actor = &id
	} else if id, uerr := middleware.GetUserID(c); uerr == nil {
		actor = &id
	}
	authorized := false
	for i := range deps {
		if deps[i].ID == depID {
			authorized = actor != nil && middleware.MayAccessTask(c, deps[i].DependsOnTaskID)
		}
	}
	dep, err := h.depService.UpdateCondition(ctx, depID, service.UpdateDependencyConditionInput{
		Condition: req.Condition, Criterion: req.Criterion, EvidenceURL: req.EvidenceURL,
		EvidenceCriterion: req.EvidenceCriterion, HeadSHA: req.HeadSHA, Environment: req.Environment, Attest: req.Attest,
		ActorID: actor, ActorAuthorized: authorized, ExpectedGeneration: req.ExpectedGeneration,
	})
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

	// expected_generation makes the delete a compare-and-delete. Absent: the
	// legacy unconditional delete. Present it must be one positive integer.
	var expectedGen *int64
	if vals, ok := c.QueryParams()["expected_generation"]; ok {
		if len(vals) != 1 {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("expected_generation must be given once"))
		}
		g, perr := strconv.ParseInt(vals[0], 10, 64)
		if perr != nil || g <= 0 {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("expected_generation must be a positive integer"))
		}
		expectedGen = &g
	}
	// expected_owner_wait_id adds the owner to the compare-and-delete: only the
	// typed wait that owns the edge removes it. It needs expected_generation.
	var expectedOwner *uuid.UUID
	if vals, ok := c.QueryParams()["expected_owner_wait_id"]; ok {
		if len(vals) != 1 {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("expected_owner_wait_id must be given once"))
		}
		w, perr := uuid.Parse(vals[0])
		if perr != nil || w == uuid.Nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("expected_owner_wait_id must be a wait id (uuid)"))
		}
		if expectedGen == nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("expected_owner_wait_id needs expected_generation"))
		}
		expectedOwner = &w
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

	if expectedOwner != nil {
		// Generation and owner are both predicates of the storage DELETE.
		if err := h.depService.DeleteIfOwned(ctx, taskID, depID, *expectedGen, *expectedOwner); err != nil {
			return handleError(c, err)
		}
		return c.NoContent(http.StatusNoContent)
	}

	if expectedGen != nil {
		// The generation is checked by the storage DELETE itself; the membership
		// read above only decides 404 early.
		if err := h.depService.DeleteIfGeneration(ctx, taskID, depID, *expectedGen); err != nil {
			return handleError(c, err)
		}
		return c.NoContent(http.StatusNoContent)
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
