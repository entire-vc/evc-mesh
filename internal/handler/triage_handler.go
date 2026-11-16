package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// TriageHandler handles HTTP requests for the triage inbox and the dashboard
// task queries built on the same cross-project read.
type TriageHandler struct {
	triageService service.TriageService
}

// NewTriageHandler creates a new TriageHandler.
func NewTriageHandler(svc service.TriageService) *TriageHandler {
	return &TriageHandler{triageService: svc}
}

// List handles GET /workspaces/:ws_id/triage
// Returns the tasks needing a human across the workspace projects the caller may
// read: a "triage" status category or a live human gate.
//
// Query: project_id (one project), hard_gates_only=true (live hard gates only),
// page / page_size (limit). total_count is the number of matches.
func (h *TriageHandler) List(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	var pg pagination.Params
	if err = c.Bind(&pg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination parameters"))
	}
	pg.Normalize()

	q, err := taskQueryBase(c, wsID)
	if err != nil {
		return err
	}
	if q.ProjectID, err = optionalUUIDParam(c, "project_id"); err != nil {
		return err
	}
	if q.HardGatesOnly, err = optionalBoolParam(c, "hard_gates_only"); err != nil {
		return err
	}

	page, err := h.triageService.ListTriageTasks(c.Request().Context(), q, pg)
	if err != nil {
		return handleError(c, err)
	}
	// The inbox row shows the description, so it travels in full here.
	for i := range page.Items {
		decorateTask(c, &page.Items[i])
	}
	return c.JSON(http.StatusOK, page)
}

// taskQueryMaxLimit caps one dashboard task query: a widget shows at most 50 rows.
const taskQueryMaxLimit = 50

// Query handles GET /workspaces/:ws_id/tasks/query, the server side of the SDK
// TaskQuery read by dashboard task widgets. Every filter runs in SQL over the
// projects the caller may read, so total_count is the real number of matches.
//
// Query:
//   - scope        — mine | project | workspace (required)
//   - project_id   — required for scope=project, optional otherwise
//   - status_category — comma list; default every category except done, cancelled
//   - priority     — comma list of urgent|high|medium|low|none
//   - due_before   — RFC 3339 time or YYYY-MM-DD (start of that day, UTC)
//   - gated        — true: only the triage queue (triage category or a live human gate)
//   - hard_gates_only — true: only live hard human gates
//   - sort         — updated (default) | priority | due_date | triage
//   - limit / page_size (1-50, default 50), page
func (h *TriageHandler) Query(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}
	q, err := taskQueryBase(c, wsID)
	if err != nil {
		return err
	}
	pg, err := taskQueryPage(c)
	if err != nil {
		return err
	}
	if q.ProjectID, err = optionalUUIDParam(c, "project_id"); err != nil {
		return err
	}

	switch scope := c.QueryParam("scope"); scope {
	case "mine":
		if mw.IsAgent(c) || mw.IsExtension(c) {
			return apierror.ValidationError(map[string]string{"scope": "mine is for a signed-in user"})
		}
		me := q.Viewer.ActorID
		q.AssigneeUserID = &me
	case "project":
		if q.ProjectID == nil {
			return apierror.ValidationError(map[string]string{"project_id": "required for scope=project"})
		}
	case "workspace":
	default:
		return apierror.ValidationError(map[string]string{"scope": "must be mine, project or workspace"})
	}

	if raw := c.QueryParam("status_category"); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			cat := domain.StatusCategory(strings.TrimSpace(part))
			if _, ok := validStatusCategories[cat]; !ok {
				return apierror.ValidationError(map[string]string{"status_category": fmt.Sprintf("unknown status_category %q", part)})
			}
			q.StatusCategories = append(q.StatusCategories, cat)
		}
	}
	if raw := c.QueryParam("priority"); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			p := domain.Priority(strings.TrimSpace(part))
			if _, ok := validTaskQueryPriorities[p]; !ok {
				return apierror.ValidationError(map[string]string{"priority": fmt.Sprintf("unknown priority %q", part)})
			}
			q.Priorities = append(q.Priorities, p)
		}
	}
	if raw := c.QueryParam("due_before"); raw != "" {
		due, perr := time.Parse(time.RFC3339, raw)
		if perr != nil {
			due, perr = time.Parse(time.DateOnly, raw)
		}
		if perr != nil {
			return apierror.ValidationError(map[string]string{"due_before": "RFC 3339 time or YYYY-MM-DD"})
		}
		q.DueBefore = &due
	}
	if q.Gated, err = optionalBoolParam(c, "gated"); err != nil {
		return err
	}
	if q.HardGatesOnly, err = optionalBoolParam(c, "hard_gates_only"); err != nil {
		return err
	}
	switch s := repository.TaskQuerySort(c.QueryParam("sort")); s {
	case "":
		q.Sort = repository.TaskQuerySortUpdated
	case repository.TaskQuerySortUpdated, repository.TaskQuerySortPriority, repository.TaskQuerySortDueDate, repository.TaskQuerySortTriage:
		q.Sort = s
	default:
		return apierror.ValidationError(map[string]string{"sort": "must be updated, priority, due_date or triage"})
	}

	page, err := h.triageService.QueryTasks(c.Request().Context(), q, pg)
	if err != nil {
		return handleError(c, err)
	}
	decorateTaskList(c, page.Items)
	return c.JSON(http.StatusOK, page)
}

var validTaskQueryPriorities = map[domain.Priority]struct{}{
	domain.PriorityUrgent: {}, domain.PriorityHigh: {}, domain.PriorityMedium: {}, domain.PriorityLow: {}, domain.PriorityNone: {},
}

// taskQueryBase starts a query in the caller's rights: an extension key and a
// workspace owner or admin read every project, anyone else only the projects
// they are a member of (the rule of middleware.UserMayAccessProject).
func taskQueryBase(c echo.Context, wsID uuid.UUID) (repository.TaskQuery, error) {
	q := repository.TaskQuery{WorkspaceID: wsID}
	switch {
	case mw.IsExtension(c):
		q.Viewer.AllProjects = true
	case mw.IsAgent(c):
		id, err := mw.GetAgentID(c)
		if err != nil {
			return q, apierror.Unauthorized("agent context required")
		}
		q.Viewer = repository.TaskViewer{ActorID: id, Agent: true}
	default:
		id, err := mw.GetUserID(c)
		if err != nil {
			return q, apierror.Unauthorized("authentication required")
		}
		role, _ := c.Get(mw.ContextKeyWorkspaceRole).(string)
		// the owner admitted by the guard's fallback (no workspace_members row) is still the owner
		ownerFallback, _ := c.Get(mw.ContextKeyWorkspaceOwnerWithoutMembership).(bool)
		q.Viewer = repository.TaskViewer{ActorID: id, AllProjects: role == domain.RoleOwner || role == domain.RoleAdmin || ownerFallback}
	}
	return q, nil
}

func taskQueryPage(c echo.Context) (pagination.Params, error) {
	pg := pagination.Params{Page: 1, PageSize: taskQueryMaxLimit}
	if raw := c.QueryParam("page"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return pg, apierror.ValidationError(map[string]string{"page": "must be a positive integer"})
		}
		pg.Page = n
	}
	for _, name := range []string{"limit", "page_size"} {
		if raw := c.QueryParam(name); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > taskQueryMaxLimit {
				return pg, apierror.ValidationError(map[string]string{name: fmt.Sprintf("must be 1-%d", taskQueryMaxLimit)})
			}
			pg.PageSize = n
		}
	}
	return pg, nil
}

func optionalUUIDParam(c echo.Context, name string) (*uuid.UUID, error) {
	raw := c.QueryParam(name)
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, apierror.ValidationError(map[string]string{name: "must be a UUID"})
	}
	return &id, nil
}

func optionalBoolParam(c echo.Context, name string) (bool, error) {
	raw := c.QueryParam(name)
	if raw == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, apierror.ValidationError(map[string]string{name: "must be true or false"})
	}
	return v, nil
}
