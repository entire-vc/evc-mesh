package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/mdoc"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// computeTaskURL builds a canonical short URL for the given task ID.
// It respects X-Forwarded-Proto and X-Forwarded-Host headers set by reverse proxies (Caddy).
func computeTaskURL(r *http.Request, taskID uuid.UUID) string {
	scheme := "https"
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	} else if r.TLS == nil {
		scheme = "http"
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	return fmt.Sprintf("%s://%s/t/%s", scheme, host, taskID.String())
}

// validSlugRe allows only safe identifiers for custom field slugs (prevents SQL injection).
var validSlugRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// TaskHandler handles HTTP requests for task management.
type TaskHandler struct {
	taskService    service.TaskService
	sessionRepo    repository.AgentSessionRepository
	commentService service.CommentService
}

// NewTaskHandler creates a new TaskHandler with the given service.
func NewTaskHandler(ts service.TaskService) *TaskHandler {
	return &TaskHandler{taskService: ts}
}

// NewTaskHandlerWithSessions creates a TaskHandler that can serve the cost-summary endpoint.
func NewTaskHandlerWithSessions(ts service.TaskService, sr repository.AgentSessionRepository) *TaskHandler {
	return &TaskHandler{taskService: ts, sessionRepo: sr}
}

// WithCommentService attaches a comment service used to write audit comments on gate changes.
func (h *TaskHandler) WithCommentService(cs service.CommentService) *TaskHandler {
	h.commentService = cs
	return h
}

// createTaskRequest represents the JSON body for creating a task.
type createTaskRequest struct {
	Title           string                 `json:"title"`
	Description     string                 `json:"description"`
	Priority        domain.Priority        `json:"priority"`
	StatusID        string                 `json:"status_id"`
	ParentTaskID    *uuid.UUID             `json:"parent_task_id"`
	AssigneeID      *uuid.UUID             `json:"assignee_id"`
	AssigneeType    domain.AssigneeType    `json:"assignee_type"`
	ReviewerID      *uuid.UUID             `json:"reviewer_id"`
	ReviewerType    *domain.AssigneeType   `json:"reviewer_type"`
	DueDate         *time.Time             `json:"due_date"`
	EstimatedHours  *float64               `json:"estimated_hours"`
	Labels          []string               `json:"labels"`
	CustomFields    json.RawMessage        `json:"custom_fields"`
	DelegationLevel domain.DelegationLevel `json:"delegation_level"`
	ThreadID        *string                `json:"thread_id"`
}

// flexTime is a *time.Time that also accepts date-only strings ("2026-03-20")
// in addition to the standard RFC3339 format. A JSON null sets the pointer to nil
// while still marking the field as "present" via the wasSet flag.
type flexTime struct {
	Time   *time.Time
	wasSet bool // true when the JSON key was present (even if null)
}

func (f *flexTime) UnmarshalJSON(b []byte) error {
	f.wasSet = true
	s := strings.Trim(string(b), `"`)
	if s == "null" || s == "" {
		f.Time = nil
		return nil
	}
	// Try RFC3339 first.
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		f.Time = &t
		return nil
	}
	// Fall back to date-only (YYYY-MM-DD).
	if t, err := time.Parse("2006-01-02", s); err == nil {
		f.Time = &t
		return nil
	}
	return errors.New("due_date must be RFC3339 or YYYY-MM-DD")
}

// updateTaskRequest represents the JSON body for partially updating a task.
type updateTaskRequest struct {
	Title            *string                 `json:"title"`
	Description      *string                 `json:"description"`
	Priority         *domain.Priority        `json:"priority"`
	AssigneeID       *uuid.UUID              `json:"assignee_id"`
	AssigneeType     *domain.AssigneeType    `json:"assignee_type"`
	ReviewerID       *uuid.UUID              `json:"reviewer_id"`
	ReviewerType     *domain.AssigneeType    `json:"reviewer_type"`
	ClearReviewer    bool                    `json:"clear_reviewer"`
	DueDate          flexTime                `json:"due_date"`
	EstimatedHours   *float64                `json:"estimated_hours"`
	Labels           *[]string               `json:"labels"`
	CustomFields     json.RawMessage         `json:"custom_fields"`
	DelegationLevel  *domain.DelegationLevel `json:"delegation_level"`
	ThreadID         *string                 `json:"thread_id"`
	HumanGate        *bool                   `json:"human_gate"`
	CompletionSignal *bool                   `json:"completion_signal"`
}

// moveTaskRequest represents the JSON body for moving a task.
type moveTaskRequest struct {
	StatusID          *uuid.UUID          `json:"status_id"`
	Position          *float64            `json:"position"`
	AssigneeID        *uuid.UUID          `json:"assignee_id,omitempty"`
	AssigneeType      domain.AssigneeType `json:"assignee_type,omitempty"`
	ExpectedStatusID  *uuid.UUID          `json:"expected_status_id,omitempty"`
	ExpectedUpdatedAt *time.Time          `json:"expected_updated_at,omitempty"`
	Source            string              `json:"source,omitempty"` // "mcp" | "api" | "ui"
}

// Create handles POST /projects/:proj_id/tasks
func (h *TaskHandler) Create(c echo.Context) error {
	projectIDStr := c.Param("proj_id")
	projectID, err := uuid.Parse(projectIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	var req createTaskRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.Title == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"title": "title is required",
		}))
	}

	// Resolve status: use provided status_id or fall back to the project's default.
	var statusID uuid.UUID
	if req.StatusID != "" {
		statusID, err = uuid.Parse(req.StatusID)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid status_id"))
		}
	} else {
		defaultStatus, err := h.taskService.GetDefaultStatus(c.Request().Context(), projectID)
		if err != nil || defaultStatus == nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("project has no default status; provide status_id"))
		}
		statusID = defaultStatus.ID
	}

	// Guard: creating tasks directly in review status is a workflow error.
	// MESH_ALLOW_REVIEW_AT_CREATE=true bypasses this for import/migration use cases.
	if os.Getenv("MESH_ALLOW_REVIEW_AT_CREATE") != "true" {
		if st, lookupErr := h.taskService.GetStatusByID(c.Request().Context(), statusID); lookupErr == nil && st != nil && st.Category == domain.StatusCategoryReview {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest(
				"Cannot create task in review status. Use 'todo' or 'in_progress'. Review is for tasks with completed work awaiting check.",
			))
		}
	}

	// Resolve assignee type. When assignee_id is supplied without a type,
	// infer "agent" so the explicit assignee_id is not silently clobbered
	// by applyAutoAssign (which fires on "unassigned").
	assigneeType := req.AssigneeType
	if assigneeType == "" {
		if req.AssigneeID != nil {
			assigneeType = domain.AssigneeTypeAgent
		} else {
			assigneeType = domain.AssigneeTypeUnassigned
		}
	}

	// Resolve priority (default to "medium").
	priority := req.Priority
	if priority == "" {
		priority = domain.PriorityMedium
	}

	// Resolve creator from auth context.
	var createdBy uuid.UUID
	var createdByType domain.ActorType
	if mw.IsAgent(c) {
		createdBy, _ = mw.GetAgentID(c)
		createdByType = domain.ActorTypeAgent
	} else {
		createdBy, _ = mw.GetUserID(c)
		createdByType = domain.ActorTypeUser
	}

	delegationLevel := req.DelegationLevel
	if delegationLevel == "" {
		delegationLevel = domain.DelegationLevelAuto
	}
	switch delegationLevel {
	case domain.DelegationLevelAuto, domain.DelegationLevelReview, domain.DelegationLevelSupervised:
	default:
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("delegation_level must be auto, review, or supervised"))
	}

	task := &domain.Task{
		ID:              uuid.New(),
		ProjectID:       projectID,
		StatusID:        statusID,
		Title:           req.Title,
		Description:     req.Description,
		Priority:        priority,
		ParentTaskID:    req.ParentTaskID,
		AssigneeID:      req.AssigneeID,
		AssigneeType:    assigneeType,
		ReviewerID:      req.ReviewerID,
		ReviewerType:    req.ReviewerType,
		DueDate:         req.DueDate,
		EstimatedHours:  req.EstimatedHours,
		Labels:          pq.StringArray(req.Labels),
		CustomFields:    req.CustomFields,
		CreatedBy:       createdBy,
		CreatedByType:   createdByType,
		DelegationLevel: delegationLevel,
		ThreadID:        req.ThreadID,
	}

	if err := h.taskService.Create(c.Request().Context(), task); err != nil {
		return handleError(c, err)
	}

	// Re-fetch from DB to get enriched fields (assignee_name, subtask_count, etc.)
	// that are populated via SQL JOINs but not available on the in-memory object.
	if enriched, err := h.taskService.GetByID(c.Request().Context(), task.ID); err == nil && enriched != nil {
		enriched.URL = computeTaskURL(c.Request(), enriched.ID)
		return c.JSON(http.StatusCreated, enriched)
	}

	task.URL = computeTaskURL(c.Request(), task.ID)
	return c.JSON(http.StatusCreated, task)
}

// isHexPrefix returns true when s looks like a valid UUID short-ID (6–12 hex chars).
func isHexPrefix(s string) bool {
	if len(s) < 6 || len(s) > 12 {
		return false
	}
	for _, ch := range s {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') && (ch < 'A' || ch > 'F') {
			return false
		}
	}
	return true
}

// taskIDResolver is the minimal interface needed to resolve a short-ID prefix to a UUID.
// Implemented by service.TaskService.
type taskIDResolver interface {
	GetByShortID(ctx context.Context, prefix string) (*domain.Task, error)
}

// resolveTaskID parses a task ID string as a full UUID or a 6–12 hex short-ID prefix.
// If svc is non-nil and the string is a valid hex prefix, it looks up the full UUID via GetByShortID.
// Returns apierror.BadRequest("invalid task_id") when the string is neither.
func resolveTaskID(ctx context.Context, s string, svc taskIDResolver) (uuid.UUID, error) {
	if id, err := uuid.Parse(s); err == nil {
		return id, nil
	}
	if svc != nil && isHexPrefix(s) {
		task, err := svc.GetByShortID(ctx, strings.ToLower(s))
		if err != nil {
			return uuid.Nil, err
		}
		return task.ID, nil
	}
	return uuid.Nil, apierror.BadRequest("invalid task_id")
}

// attachHumanGateInfo populates task.HumanGateInfo (task #040cddcf) — a
// read-only exposure of the ownership commentService already computes
// internally to gate withdrawal, via GetHumanGateOwner. No-op when the task
// isn't gated or commentService isn't wired; failures are logged, never
// surfaced as an error, so a lookup hiccup can't turn a GET into a 500.
func (h *TaskHandler) attachHumanGateInfo(ctx context.Context, task *domain.Task) {
	if task == nil || !task.HumanGate || h.commentService == nil {
		return
	}
	info, err := h.commentService.GetHumanGateOwner(ctx, task.ID)
	if err != nil {
		log.Printf("[human-gate] WARNING: GetHumanGateOwner for task %s failed: %v", task.ID, err)
		return
	}
	task.HumanGateInfo = info
}

// GetByID handles GET /tasks/:task_id
// Falls back to short-ID lookup when task_id is a 6–12 char hex prefix rather than a full UUID.
func (h *TaskHandler) GetByID(c echo.Context) error {
	taskIDStr := c.Param("task_id")
	taskID, err := uuid.Parse(taskIDStr)
	if err != nil {
		if isHexPrefix(taskIDStr) {
			task, err2 := h.taskService.GetByShortID(c.Request().Context(), strings.ToLower(taskIDStr))
			if err2 != nil {
				return handleError(c, err2)
			}
			h.attachHumanGateInfo(c.Request().Context(), task)
			task.URL = computeTaskURL(c.Request(), task.ID)
			return c.JSON(http.StatusOK, task)
		}
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}

	task, err := h.taskService.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return handleError(c, err)
	}

	h.attachHumanGateInfo(c.Request().Context(), task)
	task.URL = computeTaskURL(c.Request(), task.ID)
	return c.JSON(http.StatusOK, task)
}

// GetByShortID handles GET /tasks/by-short-id/:short
func (h *TaskHandler) GetByShortID(c echo.Context) error {
	short := c.Param("short")
	if !isHexPrefix(short) {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("short must be 6–12 hex chars"))
	}

	task, err := h.taskService.GetByShortID(c.Request().Context(), strings.ToLower(short))
	if err != nil {
		return handleError(c, err)
	}

	h.attachHumanGateInfo(c.Request().Context(), task)
	task.URL = computeTaskURL(c.Request(), task.ID)
	return c.JSON(http.StatusOK, task)
}

// SearchGlobal handles GET /workspaces/:ws_id/tasks?search=...
func (h *TaskHandler) SearchGlobal(c echo.Context) error {
	wsIDStr := c.Param("ws_id")
	wsID, err := uuid.Parse(wsIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid ws_id"))
	}

	var q listTasksQuery
	if err = c.Bind(&q); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid query parameters"))
	}

	if q.Search == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("search parameter required"))
	}

	var pg pagination.Params
	if err = c.Bind(&pg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination parameters"))
	}
	pg.Normalize()

	filter := repository.TaskFilter{Search: q.Search}

	page, err := h.taskService.Search(c.Request().Context(), wsID, filter, pg)
	if err != nil {
		return handleError(c, err)
	}

	decorateTaskList(c, page.Items)
	enforceListSizeCeiling(page)

	return c.JSON(http.StatusOK, page)
}

// decorateTaskList fills in the per-response fields of a list of tasks: the
// computed URL, the has_description flag, and — if the caller asked for it — the
// removal of the description bodies themselves.
//
// Both list-shaped endpoints go through here on purpose. has_description must
// mean the same thing everywhere it appears, and the version of this change that
// touched only one of them left the other reporting has_description:false for
// every task that had one.
func decorateTaskList(c echo.Context, items []domain.Task) {
	includeDesc := includeDescriptionRequested(c)
	for i := range items {
		items[i].URL = computeTaskURL(c.Request(), items[i].ID)
		// Computed BEFORE any blanking below, so has_description describes the
		// task and not the projection the caller happened to ask for.
		items[i].HasDescription = strings.TrimSpace(items[i].Description) != ""
		if !includeDesc {
			items[i].Description = ""
		}
	}
}

// maxListDescriptionBytes bounds how many bytes of description content a
// single list-shaped response may carry before the handler starts dropping
// descriptions from the tail of the page. Measured live (Riker, 2026-08-20):
// a full 200-item project list with descriptions serialized to 494,772 bytes
// in one line and blew the caller's tool-output limit; the same shape hit a
// workspace-wide search response at 124,847 bytes the same day. Description
// bytes dominate a list response (~77% of payload — see
// includeDescriptionRequested) so bounding them is what actually caps size.
const maxListDescriptionBytes = 200_000

// enforceListSizeCeiling drops descriptions from the tail of a task list, in
// item order, once the running total of description bytes crosses
// maxListDescriptionBytes. It never removes an item, changes item order, or
// touches count/offset/total_count — pagination stays exactly as requested —
// it only blanks the Description field on the items past the ceiling and
// flags the page as Truncated. A caller that hits this can recover a dropped
// description with get_task(task_id), or avoid it up front with a smaller
// page_size / include_description=false. A page whose descriptions were
// already excluded (include_description=false) or that never approaches the
// ceiling is completely unaffected.
func enforceListSizeCeiling(page *pagination.Page[domain.Task]) {
	total := 0
	for i := range page.Items {
		if total > maxListDescriptionBytes {
			if page.Items[i].Description != "" {
				page.Items[i].Description = ""
				page.Truncated = true
			}
			continue
		}
		total += len(page.Items[i].Description)
	}
}

// includeDescriptionRequested reports whether this list response should carry the
// full task descriptions. Default is TRUE — the caller has to ask to lose them.
//
// The default is deliberate and the opposite of what the perf ticket proposed.
// Descriptions are 77% of the board's payload and the board renders none of it,
// so "just drop it from the list" looks free. It is not: the fleet dispatcher's
// dependency-park gate (_task_prose_gated in mesh-dispatcher.py) scans the
// description of every task in a todo feed for phrases like "после merge" /
// "blocked on #". Serve that list without descriptions and the gate does not
// error — it silently matches nothing, and tasks that should park in backlog get
// auto-triaged into Pavel's queue instead. Opt-in keeps the byte win where it is
// worth having (one caller, the board) and keeps every reader that has been
// relying on this field for months working unchanged.
func includeDescriptionRequested(c echo.Context) bool {
	switch strings.ToLower(strings.TrimSpace(c.QueryParam("include_description"))) {
	case "false", "0", "no":
		return false
	default:
		return true
	}
}

// Update handles PATCH /tasks/:task_id
func (h *TaskHandler) Update(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskService)
	if err != nil {
		return handleError(c, err)
	}

	var req updateTaskRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	// Fetch existing task first
	task, err := h.taskService.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return handleError(c, err)
	}

	// Apply partial updates
	if req.Title != nil {
		task.Title = *req.Title
	}
	if req.Description != nil {
		task.Description = *req.Description
	}
	if req.Priority != nil {
		task.Priority = *req.Priority
	}
	if req.AssigneeID != nil {
		task.AssigneeID = req.AssigneeID
	}
	if req.AssigneeType != nil {
		task.AssigneeType = *req.AssigneeType
	}
	if req.ClearReviewer {
		task.ReviewerID = nil
		task.ReviewerType = nil
	} else if req.ReviewerID != nil {
		task.ReviewerID = req.ReviewerID
		task.ReviewerType = req.ReviewerType
	}
	if req.DueDate.wasSet {
		task.DueDate = req.DueDate.Time // nil clears, non-nil sets
	}
	if req.EstimatedHours != nil {
		task.EstimatedHours = req.EstimatedHours
	}
	if req.Labels != nil {
		task.Labels = pq.StringArray(*req.Labels)
	}
	if req.CustomFields != nil {
		task.CustomFields = req.CustomFields
	}
	if req.DelegationLevel != nil {
		switch *req.DelegationLevel {
		case domain.DelegationLevelAuto, domain.DelegationLevelReview, domain.DelegationLevelSupervised:
		default:
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("delegation_level must be auto, review, or supervised"))
		}
		task.DelegationLevel = *req.DelegationLevel
	}
	if req.ThreadID != nil {
		task.ThreadID = req.ThreadID
	}
	prevHumanGate := task.HumanGate
	if req.HumanGate != nil {
		// Only a human user may clear the gate (true → false).
		// Agents clearing the gate bypass the sign-off mechanism.
		if task.HumanGate && !*req.HumanGate {
			_, actorType := actorctx.FromContext(c.Request().Context())
			if actorType != domain.ActorTypeUser {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("clearing human_gate requires user authentication"))
			}
		}
		task.HumanGate = *req.HumanGate
	}
	if req.CompletionSignal != nil {
		task.CompletionSignal = *req.CompletionSignal
	}

	if err := h.taskService.Update(c.Request().Context(), task); err != nil {
		return handleError(c, err)
	}

	if prevHumanGate && req.HumanGate != nil && !*req.HumanGate && h.commentService != nil {
		actorID, _ := actorctx.FromContext(c.Request().Context())
		_ = h.commentService.Create(c.Request().Context(), &domain.Comment{
			TaskID:     task.ID,
			AuthorID:   actorID,
			AuthorType: domain.ActorTypeUser,
			Body:       fmt.Sprintf("🔓 human_gate снят вручную (actor: %s)", actorID),
			IsInternal: true,
		})
	}

	// Task #a2e2ac72: a false→true PATCH (or UI, same endpoint) carries no
	// "❓ Blocking @user" comment at all — unlike enforceBlockingTriage's arm
	// path, nothing here records WHO raised this ask or WHY. Without this,
	// releaseHumanGateOnWithdrawal's soleMarkerAuthor check (task #9959f201)
	// cannot tell "this agent's marker is the one genuine ask on this thread"
	// apart from "this agent fabricated a marker onto a gate someone else (or
	// a human via UI) already armed" — the two are byte-identical in the
	// comment log otherwise. Post a system comment marking the raw arm so
	// that check has something to look for; see hasRawArmMarker in
	// comment_service.go for the matching read side.
	//
	// Fixed 2026-07-31 (task #15694816, found in cross-verification of #486):
	// this MUST be authored as ActorTypeSystem / systemActorID (uuid.Nil), not
	// the real actor — comment_handler.go's Create always derives AuthorType
	// from the caller's OWN authenticated identity (agent or user, NEVER
	// system) for any comment posted through the public API, so an
	// ActorTypeSystem comment is the one thing no external caller can forge.
	// Using the real actor here (as the symmetric release-comment below does,
	// safely, because ONLY users can reach that branch) would have let any
	// agent post an ordinary comment containing the same substring BEFORE a
	// real ask ever existed — pinning lastRawArmAt in the past and applying
	// the 30-minute friction to every future legitimate sole-owner withdrawal
	// on that task, forever. See hasRawArmMarker's matching AuthorType check.
	if !prevHumanGate && req.HumanGate != nil && *req.HumanGate && h.commentService != nil {
		actorID, actorType := actorctx.FromContext(c.Request().Context())
		_ = h.commentService.Create(c.Request().Context(), &domain.Comment{
			TaskID:     task.ID,
			AuthorID:   uuid.Nil,
			AuthorType: domain.ActorTypeSystem,
			Body:       fmt.Sprintf("🔒 Auto: human_gate взведён напрямую (PATCH/UI), без маркерного коммента — actor: %s (%s)", actorID, actorType),
			IsInternal: true,
		})
	}

	task.URL = computeTaskURL(c.Request(), task.ID)
	return c.JSON(http.StatusOK, task)
}

// Delete handles DELETE /tasks/:task_id
func (h *TaskHandler) Delete(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskService)
	if err != nil {
		return handleError(c, err)
	}

	if err := h.taskService.Delete(c.Request().Context(), taskID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// listTasksQuery represents query parameters for listing tasks.
type listTasksQuery struct {
	Status         string `query:"status"`          // comma-separated status UUIDs
	StatusID       string `query:"status_id"`       // single status UUID alias
	StatusCategory string `query:"status_category"` // backlog|todo|in_progress|review|done|cancelled
	AssigneeType   string `query:"assignee_type"`
	Priority       string `query:"priority"`
	Labels         string `query:"labels"`
	Search         string `query:"search"`
}

// List handles GET /projects/:proj_id/tasks
func (h *TaskHandler) List(c echo.Context) error {
	projectIDStr := c.Param("proj_id")
	projectID, err := uuid.Parse(projectIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	var q listTasksQuery
	if err = c.Bind(&q); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid query parameters"))
	}

	var pg pagination.Params
	if err = c.Bind(&pg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination parameters"))
	}
	// Support limit= as an alias for page_size= (page_size wins if both are set).
	if rawLimit := c.QueryParam("limit"); rawLimit != "" && pg.PageSize < 1 {
		if n, atoiErr := strconv.Atoi(rawLimit); atoiErr == nil && n > 0 {
			pg.PageSize = n
		}
	}
	// Support offset= as an alias for page-based pagination.
	if rawOffset := c.QueryParam("offset"); rawOffset != "" && pg.Page <= 1 {
		if n, atoiErr := strconv.Atoi(rawOffset); atoiErr == nil && n > 0 {
			ps := pg.PageSize
			if ps < 1 {
				ps = pagination.DefaultPageSize
			}
			if ps > pagination.MaxPageSize {
				ps = pagination.MaxPageSize
			}
			pg.Page = n/ps + 1
		}
	}
	pg.Normalize()

	filter := repository.TaskFilter{
		Search: q.Search,
	}

	if q.AssigneeType != "" {
		at := domain.AssigneeType(q.AssigneeType)
		filter.AssigneeType = &at
	}
	if q.Priority != "" {
		p := domain.Priority(q.Priority)
		filter.Priority = &p
	}
	if q.Labels != "" {
		filter.Labels = []string{q.Labels}
	}
	// status= and status_id= both accept comma-separated UUIDs.
	for _, raw := range strings.Split(q.Status+","+q.StatusID, ",") {
		raw = strings.TrimSpace(raw)
		if statusID, parseErr := uuid.Parse(raw); parseErr == nil {
			filter.StatusIDs = append(filter.StatusIDs, statusID)
		}
	}
	if q.StatusCategory != "" {
		cat := domain.StatusCategory(q.StatusCategory)
		filter.StatusCategory = &cat
	}

	// Parse custom field filters from query params with "custom." prefix.
	// Supported: custom.{slug}=value, custom.{slug}_gte=5, custom.{slug}_lte=10
	cfFilters := parseCustomFieldFilters(c)
	if len(cfFilters) > 0 {
		filter.CustomFields = cfFilters
	}

	page, err := h.taskService.List(c.Request().Context(), projectID, filter, pg)
	if err != nil {
		return handleError(c, err)
	}

	decorateTaskList(c, page.Items)
	enforceListSizeCeiling(page)

	return c.JSON(http.StatusOK, page)
}

// parseCustomFieldFilters extracts custom field filter parameters from query string.
// Supports: custom.{slug}=value, custom.{slug}_gte=N, custom.{slug}_lte=N
func parseCustomFieldFilters(c echo.Context) map[string]repository.CustomFieldFilter {
	result := make(map[string]repository.CustomFieldFilter)

	for key, values := range c.QueryParams() {
		if !strings.HasPrefix(key, "custom.") || len(values) == 0 {
			continue
		}
		val := values[0]
		fieldKey := strings.TrimPrefix(key, "custom.")

		switch {
		case strings.HasSuffix(fieldKey, "_gte"):
			slug := strings.TrimSuffix(fieldKey, "_gte")
			if !validSlugRe.MatchString(slug) {
				continue
			}
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				continue
			}
			cf := result[slug]
			cf.Gte = &f
			result[slug] = cf
		case strings.HasSuffix(fieldKey, "_lte"):
			slug := strings.TrimSuffix(fieldKey, "_lte")
			if !validSlugRe.MatchString(slug) {
				continue
			}
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				continue
			}
			cf := result[slug]
			cf.Lte = &f
			result[slug] = cf
		default:
			// Exact equality.
			if !validSlugRe.MatchString(fieldKey) {
				continue
			}
			cf := result[fieldKey]
			cf.Eq = val
			result[fieldKey] = cf
		}
	}

	return result
}

// MoveTask handles POST /tasks/:task_id/move
func (h *TaskHandler) MoveTask(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskService)
	if err != nil {
		return handleError(c, err)
	}

	var req moveTaskRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.StatusID == nil && req.Position == nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("status_id or position is required"))
	}

	input := service.MoveTaskInput{
		StatusID:          req.StatusID,
		Position:          req.Position,
		AssigneeID:        req.AssigneeID,
		AssigneeType:      req.AssigneeType,
		ExpectedStatusID:  req.ExpectedStatusID,
		ExpectedUpdatedAt: req.ExpectedUpdatedAt,
		Source:            req.Source,
	}
	if input.Source == "" {
		input.Source = "api"
	}

	if err := h.taskService.MoveTask(c.Request().Context(), taskID, input); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// ListSubtasks handles GET /tasks/:task_id/subtasks
func (h *TaskHandler) ListSubtasks(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskService)
	if err != nil {
		return handleError(c, err)
	}

	subtasks, err := h.taskService.ListSubtasks(c.Request().Context(), taskID)
	if err != nil {
		return handleError(c, err)
	}

	// Third list-shaped endpoint through the shared decorator (#32f4c087 follow-up).
	// ListSubtasks never truncates description text — no caller asked it to, and
	// this handler does not read include_description — but without this call
	// HasDescription is left at its Go zero value (false) for every item, because
	// it is computed here rather than carried by the repository row. A subtask
	// with a real description would report has_description:false, and the list
	// view's card (which trusts has_description over a live text check, see
	// EnhancedTitleCell) would hide the glyph despite the text being right there
	// in the same payload. decorateTaskList also fills in URL, which this
	// endpoint never set.
	decorateTaskList(c, subtasks)

	return c.JSON(http.StatusOK, map[string]any{"items": subtasks})
}

// assignTaskRequest represents the JSON body for assigning a task.
type assignTaskRequest struct {
	AssigneeID   *uuid.UUID          `json:"assignee_id"`
	AssigneeType domain.AssigneeType `json:"assignee_type"`
	// Source defaults to "system" when omitted. Pass "human" to set a human pin.
	// Rule/system sources are rejected if the task is already pinned by a human.
	Source domain.AssignmentSource `json:"source"`
}

// AssignTask handles POST /tasks/:task_id/assign
func (h *TaskHandler) AssignTask(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskService)
	if err != nil {
		return handleError(c, err)
	}

	var req assignTaskRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	assigneeType := req.AssigneeType
	if assigneeType == "" {
		if req.AssigneeID == nil {
			assigneeType = domain.AssigneeTypeUnassigned
		} else {
			assigneeType = domain.AssigneeTypeAgent
		}
	}

	input := service.AssignTaskInput{
		AssigneeID:   req.AssigneeID,
		AssigneeType: assigneeType,
		Source:       req.Source,
	}

	if err = h.taskService.AssignTask(c.Request().Context(), taskID, input); err != nil {
		var pinnedErr *service.AssignmentPinnedError
		if errors.As(err, &pinnedErr) {
			return c.JSON(http.StatusUnprocessableEntity, map[string]any{
				"code":    "assignment_pinned",
				"message": "Task assignee is pinned by a human and cannot be overridden by rule or system sources. Pass source=human to override.",
			})
		}
		return handleError(c, err)
	}

	// Return the updated task.
	task, err := h.taskService.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, task)
}

// createSubtaskRequest represents the JSON body for creating a subtask.
type createSubtaskRequest struct {
	Title          string              `json:"title"`
	Description    string              `json:"description"`
	Priority       domain.Priority     `json:"priority"`
	StatusID       string              `json:"status_id"`
	AssigneeID     *uuid.UUID          `json:"assignee_id"`
	AssigneeType   domain.AssigneeType `json:"assignee_type"`
	Labels         []string            `json:"labels"`
	CustomFields   json.RawMessage     `json:"custom_fields"`
	DueDate        *time.Time          `json:"due_date"`
	EstimatedHours *float64            `json:"estimated_hours"`
}

// CreateSubtask handles POST /tasks/:task_id/subtasks
func (h *TaskHandler) CreateSubtask(c echo.Context) error {
	parentTaskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskService)
	if err != nil {
		return handleError(c, err)
	}

	var req createSubtaskRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.Title == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"title": "title is required",
		}))
	}

	priority := req.Priority
	if priority == "" {
		priority = domain.PriorityMedium
	}

	// Resolve assignee type. When assignee_id is supplied without a type,
	// infer "agent" so the explicit assignee_id is not silently clobbered
	// by applyAutoAssign (which fires on "unassigned") — same rule as Create.
	assigneeType := req.AssigneeType
	if assigneeType == "" && req.AssigneeID != nil {
		assigneeType = domain.AssigneeTypeAgent
	}

	input := service.CreateSubtaskInput{
		Title:          req.Title,
		Description:    req.Description,
		Priority:       priority,
		AssigneeID:     req.AssigneeID,
		AssigneeType:   assigneeType,
		Labels:         req.Labels,
		CustomFields:   req.CustomFields,
		DueDate:        req.DueDate,
		EstimatedHours: req.EstimatedHours,
	}

	// Omitted status_id means "project default" — resolved by the service.
	if req.StatusID != "" {
		statusID, parseErr := uuid.Parse(req.StatusID)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid status_id"))
		}
		input.StatusID = &statusID
	}

	subtask, err := h.taskService.CreateSubtask(c.Request().Context(), parentTaskID, input)
	if err != nil {
		return handleError(c, err)
	}

	// Re-fetch from DB to get enriched fields (assignee_name, etc.)
	if enriched, err := h.taskService.GetByID(c.Request().Context(), subtask.ID); err == nil && enriched != nil {
		return c.JSON(http.StatusCreated, enriched)
	}

	return c.JSON(http.StatusCreated, subtask)
}

// bulkUpdateRequest represents the JSON body for bulk-updating multiple tasks.
type bulkUpdateRequest struct {
	TaskIDs []uuid.UUID      `json:"task_ids"`
	Updates bulkUpdateFields `json:"updates"`
}

// bulkUpdateFields holds the optional fields that can be changed in a bulk update.
type bulkUpdateFields struct {
	StatusID     *uuid.UUID           `json:"status_id,omitempty"`
	Priority     *domain.Priority     `json:"priority,omitempty"`
	AssigneeID   *uuid.UUID           `json:"assignee_id,omitempty"`
	AssigneeType *domain.AssigneeType `json:"assignee_type,omitempty"`
	Labels       *[]string            `json:"labels,omitempty"`
}

// bulkUpdateResponse is returned after a bulk update operation.
type bulkUpdateResponse struct {
	Updated int      `json:"updated"`
	Errors  []string `json:"errors,omitempty"`
}

// BulkUpdate handles POST /projects/:proj_id/tasks/bulk-update
func (h *TaskHandler) BulkUpdate(c echo.Context) error {
	projectIDStr := c.Param("proj_id")
	projectID, err := uuid.Parse(projectIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	var req bulkUpdateRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if len(req.TaskIDs) == 0 {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("task_ids must not be empty"))
	}
	if len(req.TaskIDs) > 100 {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("max 100 tasks per bulk update"))
	}

	// Require at least one field to update.
	u := req.Updates
	if u.StatusID == nil && u.Priority == nil && u.AssigneeID == nil && u.AssigneeType == nil && u.Labels == nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("at least one field in updates is required"))
	}

	input := service.BulkUpdateTasksInput{
		TaskIDs:      req.TaskIDs,
		StatusID:     u.StatusID,
		Priority:     u.Priority,
		AssigneeID:   u.AssigneeID,
		AssigneeType: u.AssigneeType,
		Labels:       u.Labels,
	}

	result := h.taskService.BulkUpdate(c.Request().Context(), projectID, input)

	return c.JSON(http.StatusOK, bulkUpdateResponse{
		Updated: result.Updated,
		Errors:  result.Errors,
	})
}

// checkoutRequest represents the JSON body for POST /tasks/:task_id/checkout.
// checkoutRequest represents the JSON body for POST /tasks/:task_id/checkout.
// SessionMetadata is optional forensic context (hostname, pid, branch, etc.)
// that is recorded into the activity log entry only — never persisted on
// the task row, so the schema is unchanged.
type checkoutRequest struct {
	TTLMinutes      int                    `json:"ttl_minutes"`
	SessionMetadata map[string]interface{} `json:"session_metadata,omitempty"`
}

// releaseCheckoutRequest represents the JSON body for DELETE /tasks/:task_id/checkout.
type releaseCheckoutRequest struct {
	CheckoutToken string `json:"checkout_token"`
}

// extendCheckoutRequest represents the JSON body for PATCH /tasks/:task_id/checkout.
type extendCheckoutRequest struct {
	CheckoutToken string `json:"checkout_token"`
	TTLMinutes    int    `json:"ttl_minutes"`
}

// Checkout handles POST /tasks/:task_id/checkout
func (h *TaskHandler) Checkout(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskService)
	if err != nil {
		return handleError(c, err)
	}

	var req checkoutRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	result, err := h.taskService.CheckoutTask(c.Request().Context(), taskID, req.TTLMinutes, req.SessionMetadata)
	if err != nil {
		var conflict *service.CheckoutConflictError
		if errors.As(err, &conflict) {
			holderName := conflict.CheckedOutByName
			if holderName == "" {
				holderName = conflict.CheckedOutBy.String()
			}
			remaining := time.Until(conflict.ExpiresAt)
			expiresInSeconds := int(remaining.Seconds())
			if expiresInSeconds < 0 {
				expiresInSeconds = 0
			}
			details := map[string]interface{}{
				"checked_out_by":      conflict.CheckedOutBy,
				"checked_out_by_name": holderName,
				"checked_out_by_kind": "agent",
				"expires_at":          conflict.ExpiresAt,
				"expires_in_seconds":  expiresInSeconds,
			}
			if conflict.AcquiredAt != nil {
				details["acquired_at"] = conflict.AcquiredAt
			}
			return c.JSON(http.StatusConflict, map[string]interface{}{
				"code":    409,
				"message": "Task is already checked out",
				"details": details,
			})
		}
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, result)
}

// ReleaseCheckout handles DELETE /tasks/:task_id/checkout.
//
// When the ?force=true query parameter is present, the request is treated as
// an admin force-unlock: no checkout_token is required, but the caller must
// be authenticated. Stricter RBAC (workspace.admin) is not enforced here yet
// — see admin-RBAC TODO in the design doc.
func (h *TaskHandler) ReleaseCheckout(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskService)
	if err != nil {
		return handleError(c, err)
	}

	if c.QueryParam("force") == "true" {
		if forceErr := h.taskService.ForceReleaseCheckout(c.Request().Context(), taskID); forceErr != nil {
			return handleError(c, forceErr)
		}
		return c.NoContent(http.StatusNoContent)
	}

	var req releaseCheckoutRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.CheckoutToken == "" {
		// No token provided — fall back to identity-based self-release.
		// The caller must be the lock holder; see SelfReleaseCheckout.
		if releaseErr := h.taskService.SelfReleaseCheckout(c.Request().Context(), taskID); releaseErr != nil {
			return handleError(c, releaseErr)
		}
		return c.NoContent(http.StatusNoContent)
	}

	token, err := uuid.Parse(req.CheckoutToken)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid checkout_token"))
	}

	if err := h.taskService.ReleaseCheckout(c.Request().Context(), taskID, token); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// ExtendCheckout handles PATCH /tasks/:task_id/checkout
func (h *TaskHandler) ExtendCheckout(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskService)
	if err != nil {
		return handleError(c, err)
	}

	var req extendCheckoutRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.CheckoutToken == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("checkout_token is required"))
	}

	token, err := uuid.Parse(req.CheckoutToken)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid checkout_token"))
	}

	result, err := h.taskService.ExtendCheckout(c.Request().Context(), taskID, token, req.TTLMinutes)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, result)
}

// moveToProjectRequest represents the JSON body for moving a task to another project.
type moveToProjectRequest struct {
	ProjectID string `json:"project_id"`
}

// MoveToProject handles POST /tasks/:task_id/move-to-project
func (h *TaskHandler) MoveToProject(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskService)
	if err != nil {
		return handleError(c, err)
	}

	var req moveToProjectRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.ProjectID == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"project_id": "project_id is required",
		}))
	}

	projectID, err := uuid.Parse(req.ProjectID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	task, err := h.taskService.MoveToProject(c.Request().Context(), taskID, projectID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, task)
}

// GetCurrentUserTasks handles GET /me/tasks
// Returns paginated active (non-done, non-cancelled) tasks assigned to the authenticated user.
// Query params: per_page (1-50, default 20), workspace_id (required UUID).
func (h *TaskHandler) GetCurrentUserTasks(c echo.Context) error {
	actorID, actorType := actorctx.FromContext(c.Request().Context())
	if actorID == uuid.Nil || actorType != "user" {
		return apierror.Unauthorized("authentication required")
	}

	wsIDStr := c.QueryParam("workspace_id")
	workspaceID, err := uuid.Parse(wsIDStr)
	if err != nil {
		return apierror.ValidationError(map[string]string{"workspace_id": "required UUID"})
	}

	perPage := 20
	if v := c.QueryParam("per_page"); v != "" {
		n, parseErr := strconv.Atoi(v)
		if parseErr != nil || n < 1 || n > 50 {
			return apierror.ValidationError(map[string]string{"per_page": "must be 1-50"})
		}
		perPage = n
	}
	pg := pagination.Params{Page: 1, PageSize: perPage}

	page, err := h.taskService.GetUserActiveTasks(c.Request().Context(), workspaceID, actorID, pg)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, page)
}

// GetCostSummary handles GET /tasks/:task_id/cost-summary
// Returns aggregated cost, token, session, and quality metrics for the given task.
// Requires workspace membership (same auth level as GET /tasks/:task_id).
func (h *TaskHandler) GetCostSummary(c echo.Context) error {
	if h.sessionRepo == nil {
		return c.JSON(http.StatusNotImplemented, apierror.InternalError("session tracking not configured"))
	}

	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskService)
	if err != nil {
		return handleError(c, err)
	}

	ctx := c.Request().Context()

	task, err := h.taskService.GetByID(ctx, taskID)
	if err != nil {
		return handleError(c, err)
	}
	if task == nil {
		return c.JSON(http.StatusNotFound, apierror.NotFound("Task"))
	}

	summary, err := h.sessionRepo.GetTaskCostSummary(ctx, taskID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, summary)
}

// ShipTask handles PATCH /tasks/:task_id/ship.
// Body: {"shipped": true|false}
// Sets or clears the is_shipped terminal flag. Once shipped, MoveTask to non-done returns 422.
func (h *TaskHandler) ShipTask(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskService)
	if err != nil {
		return handleError(c, err)
	}

	var req struct {
		Shipped bool `json:"shipped"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if err := h.taskService.ShipTask(c.Request().Context(), taskID, req.Shipped); err != nil {
		return handleError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// SetDodCheck handles PATCH /tasks/:task_id/dod-check.
// Accepts a JSON body {"gate": "name", "status": "pending|pass|fail", "reporter": "..."}
// and updates the named gate entry in the task's dod_checks map.
func (h *TaskHandler) SetDodCheck(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskService)
	if err != nil {
		return handleError(c, err)
	}

	var req struct {
		Gate     string `json:"gate"`
		Status   string `json:"status"`
		Reporter string `json:"reporter"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	if req.Gate == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("gate is required"))
	}
	switch req.Status {
	case "pending", "pass", "fail":
	default:
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("status must be pending, pass, or fail"))
	}

	if err := h.taskService.SetDodCheck(c.Request().Context(), taskID, req.Gate, req.Status, req.Reporter); err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

// ruleViolationAPIResponse is the JSON shape for 422 rule violation responses.
type ruleViolationAPIResponse struct {
	Error      string                 `json:"error"`
	Message    string                 `json:"message"`
	Violations []domain.RuleViolation `json:"violations"`
}

// handleError inspects the error type and returns appropriate JSON response.
func handleError(c echo.Context, err error) error {
	var evidenceErr *service.ReviewEvidenceError
	if errors.As(err, &evidenceErr) {
		return c.JSON(http.StatusUnprocessableEntity, map[string]any{
			"code":    "review_evidence_required",
			"message": "Task cannot be moved to review without evidence. Add at least one of: a PR/VCS link (use add_dependency or the vcs field), an artifact upload, or a comment with command output/proof (see §1c). To flag a human blocker, add a ❓ Blocking @pavel comment first, then retry move→review.",
		})
	}

	var foreignAssigneeErr *service.AssigneeNotInWorkspaceError
	if errors.As(err, &foreignAssigneeErr) {
		// 422, not 403: the caller is normally a legitimate member of this
		// workspace and it is the principal they named that is invalid here.
		// Answering "forbidden" would send them to audit their own permissions.
		// The message never reveals which workspace the principal does belong to.
		return c.JSON(http.StatusUnprocessableEntity, map[string]any{
			"code": "assignee_not_in_workspace",
			"message": "Assignee does not belong to this task's workspace. Tasks can only be assigned to " +
				"agents and users of the workspace that owns the project. Pick an assignee from " +
				"GET /workspaces/:ws_id/team.",
		})
	}

	var unresolvedAssigneeErr *service.AssigneeUnresolvedError
	if errors.As(err, &unresolvedAssigneeErr) {
		// 422 for the same reason as the sibling above, but a distinct code and a
		// message naming the mistake that actually produces this: an 8-hex short id
		// padded out into a full UUID. Only the first 8 hex of a task/agent id are
		// visible in short form; the other 24 are not guessable, so a reconstructed
		// UUID names nobody. Resolve short ids by lookup instead of arithmetic.
		return c.JSON(http.StatusUnprocessableEntity, map[string]any{
			"code": "assignee_unresolved",
			"message": "assignee_id does not match any agent or user. If you built this id from an " +
				"8-character short id, it is not recoverable that way — only the first 8 hex " +
				"digits are shared. Look the principal up in GET /workspaces/:ws_id/team and " +
				"use the full id it returns, or omit assignee_id to create the task unassigned.",
		})
	}

	var doneEvidenceErr *service.DoneEvidenceError
	if errors.As(err, &doneEvidenceErr) {
		return c.JSON(http.StatusUnprocessableEntity, map[string]any{
			"code":    "done_evidence_required",
			"message": doneEvidenceErr.Error(),
		})
	}

	var dodGateErr *service.DodGateBlockedError
	if errors.As(err, &dodGateErr) {
		return c.JSON(http.StatusUnprocessableEntity, map[string]any{
			"code":           "dod_gate_blocked",
			"message":        dodGateErr.Error(),
			"blocking_gates": dodGateErr.BlockingGates,
		})
	}

	var humanGateErr *service.HumanGateFrozenError
	if errors.As(err, &humanGateErr) {
		return c.JSON(http.StatusUnprocessableEntity, map[string]any{
			"code":    "human_gate_frozen",
			"message": "Task is awaiting human sign-off (human_gate=true). Only a user may move it to backlog/done/cancelled. To clear the gate, a human must either move the task manually or call PATCH /tasks/:id with human_gate=false.",
		})
	}

	var shippedErr *service.TaskShippedError
	if errors.As(err, &shippedErr) {
		return c.JSON(http.StatusUnprocessableEntity, map[string]any{
			"code":    "task_shipped",
			"message": "Task is marked as shipped and cannot be moved to a non-done status. To reopen it, call PATCH /tasks/:id/ship with {\"shipped\": false} first.",
		})
	}

	var ruleErr *service.RuleViolationError
	if errors.As(err, &ruleErr) {
		msg := "Action blocked by governance rules"
		if len(ruleErr.Violations) > 0 {
			v := ruleErr.Violations[0]
			msg = fmt.Sprintf("Blocked by rule «%s»: %s", v.RuleName, v.Message)
			if len(ruleErr.Violations) > 1 {
				msg += fmt.Sprintf(" (and %d more rule(s))", len(ruleErr.Violations)-1)
			}
		}
		return c.JSON(http.StatusUnprocessableEntity, ruleViolationAPIResponse{
			Error:      "rule_violation",
			Message:    msg,
			Violations: ruleErr.Violations,
		})
	}

	var ambiguousQuoteErr *mdoc.AmbiguousQuoteError
	if errors.As(err, &ambiguousQuoteErr) {
		// The match count is the whole content of this answer: it is what tells the
		// caller whether to add a few words of context or to quote something else
		// entirely. Flattening it into prose would make it unreadable by the agent
		// that has to act on it.
		return c.JSON(http.StatusBadRequest, map[string]any{
			"code":    "ambiguous_quote",
			"message": ambiguousQuoteErr.Error(),
			"matches": ambiguousQuoteErr.Matches,
		})
	}

	var docVersionErr *service.DocumentVersionConflictError
	if errors.As(err, &docVersionErr) {
		// 409 carrying the version the document is actually at, so the caller can
		// re-read that revision and retry instead of guessing at a number. Nothing
		// was written — not the row, not the markdown in object storage — which is
		// the part worth being able to rely on.
		return c.JSON(http.StatusConflict, map[string]any{
			"code": "document_version_conflict",
			"message": "Document was modified since you read it — re-read it, re-apply your change and " +
				"retry with the new base_version. To add to the end of a document without reading it " +
				"first, send append_body instead, which needs no base_version.",
			"current_version": docVersionErr.CurrentVersion,
		})
	}

	var casErr *service.CASConflictError
	if errors.As(err, &casErr) {
		return c.JSON(http.StatusConflict, map[string]interface{}{
			"code":               "cas_conflict",
			"message":            "Task was modified concurrently — re-read the task and retry",
			"current_status_id":  casErr.CurrentStatusID,
			"current_updated_at": casErr.CurrentUpdatedAt,
		})
	}

	// ADR-0004 Decision 4: 410, not a silent fallback to page 1 or to the
	// requested offset against the new state — a plausible-looking 200 here
	// is exactly the defect class ad22bfda exists to close.
	var listRevErr *service.ListRevisionStaleError
	if errors.As(err, &listRevErr) {
		return c.JSON(http.StatusGone, map[string]interface{}{
			"error": "list_revision_stale",
			"message": fmt.Sprintf(
				"task_list_revision changed since this cursor was issued (had %d, now %d); restart pagination from page 1",
				listRevErr.Requested, listRevErr.Current),
			"requested_revision": listRevErr.Requested,
			"current_revision":   listRevErr.Current,
		})
	}

	if apiErr, ok := err.(*apierror.Error); ok {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}

	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		switch pqErr.Code {
		case "23505": // unique_violation
			return c.JSON(http.StatusConflict, apierror.Conflict("already exists"))
		case "23503": // foreign_key_violation
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("referenced entity not found"))
		case "23514": // check_violation
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("value violates constraint"))
		case "22P02": // invalid_text_representation (bad enum)
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid value for field"))
		}
	}

	log.Printf("ERROR %s %s: %v", c.Request().Method, c.Request().URL.Path, err)
	return c.JSON(http.StatusInternalServerError, apierror.InternalError(""))
}
