package handler

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// RecurringHandler handles HTTP requests for recurring task schedule management.
type RecurringHandler struct {
	recurringSvc service.RecurringService
	projectSvc   service.ProjectService
	workspaceSvc service.WorkspaceService
}

// NewRecurringHandler creates a new RecurringHandler.
func NewRecurringHandler(svc service.RecurringService, projectSvc service.ProjectService, workspaceSvc service.WorkspaceService) *RecurringHandler {
	return &RecurringHandler{recurringSvc: svc, projectSvc: projectSvc, workspaceSvc: workspaceSvc}
}

// decorateScheduleURLs sets the canonical deep-link on each schedule in place:
// the project settings Recurring tab anchored on the schedule, the exact
// target scheduleHref builds in web/src/lib/entity-deep-links.ts and the only
// address that opens the tab (#5f065856) — the /p/<id> resolver forwards the
// query but lands on the project board, not the settings tab, so both slugs
// are embedded here, resolved via the project and workspace services.
//
// Every schedule in one response shares a project (List is project-scoped by
// route; Get/Create/Update are single schedules), so the slugs are resolved
// once from the first schedule and reused. Lookup failure leaves URL empty
// (omitempty drops it) rather than failing a response whose data is otherwise
// valid — same never-stored trade-off Project.URL makes.
func (h *RecurringHandler) decorateScheduleURLs(ctx context.Context, r *http.Request, schedules ...*domain.RecurringSchedule) {
	if h.projectSvc == nil || h.workspaceSvc == nil {
		return
	}
	scheme, host := requestOrigin(r)
	var wsSlug, projSlug string
	for _, s := range schedules {
		if s == nil {
			continue
		}
		if wsSlug == "" || projSlug == "" {
			project, err := h.projectSvc.GetByID(ctx, s.ProjectID)
			if err != nil || project == nil {
				continue
			}
			workspace, err := h.workspaceSvc.GetByID(ctx, s.WorkspaceID)
			if err != nil || workspace == nil {
				continue
			}
			wsSlug, projSlug = workspace.Slug, project.Slug
		}
		s.URL = fmt.Sprintf("%s://%s/w/%s/p/%s/settings?tab=recurring&schedule=%s",
			scheme, host, wsSlug, projSlug, s.ID)
	}
}

// createRecurringRequest is the JSON body for creating a recurring schedule.
type createRecurringRequest struct {
	TitleTemplate       string                    `json:"title_template"`
	DescriptionTemplate string                    `json:"description_template"`
	Frequency           domain.RecurringFrequency `json:"frequency"`
	CronExpr            string                    `json:"cron_expr"`
	Timezone            string                    `json:"timezone"`
	AssigneeID          *uuid.UUID                `json:"assignee_id"`
	AssigneeType        domain.AssigneeType       `json:"assignee_type"`
	Priority            domain.Priority           `json:"priority"`
	Labels              []string                  `json:"labels"`
	StatusID            *uuid.UUID                `json:"status_id"`
	IsActive            *bool                     `json:"is_active"`
	StartsAt            *time.Time                `json:"starts_at"`
	EndsAt              *time.Time                `json:"ends_at"`
	MaxInstances        *int                      `json:"max_instances"`
}

// updateRecurringRequest is the JSON body for partially updating a recurring schedule.
type updateRecurringRequest struct {
	TitleTemplate       *string                    `json:"title_template"`
	DescriptionTemplate *string                    `json:"description_template"`
	Frequency           *domain.RecurringFrequency `json:"frequency"`
	CronExpr            *string                    `json:"cron_expr"`
	Timezone            *string                    `json:"timezone"`
	AssigneeID          *uuid.UUID                 `json:"assignee_id"`
	AssigneeType        *domain.AssigneeType       `json:"assignee_type"`
	Priority            *domain.Priority           `json:"priority"`
	Labels              *[]string                  `json:"labels"`
	StatusID            *uuid.UUID                 `json:"status_id"`
	IsActive            *bool                      `json:"is_active"`
	EndsAt              *time.Time                 `json:"ends_at"`
	MaxInstances        *int                       `json:"max_instances"`
}

// Create handles POST /projects/:proj_id/recurring
func (h *RecurringHandler) Create(c echo.Context) error {
	projectIDStr := c.Param("proj_id")
	projectID, err := uuid.Parse(projectIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	var req createRecurringRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.TitleTemplate == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"title_template": "title_template is required",
		}))
	}

	// Resolve frequency default.
	freq := req.Frequency
	if freq == "" {
		freq = domain.RecurringFrequencyWeekly
	}

	// Resolve assignee_type default.
	assigneeType := req.AssigneeType
	if assigneeType == "" {
		assigneeType = domain.AssigneeTypeUnassigned
	}

	// Resolve priority default.
	priority := req.Priority
	if priority == "" {
		priority = domain.PriorityNone
	}

	// Resolve is_active default (true).
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}

	// Resolve starts_at default.
	startsAt := time.Now()
	if req.StartsAt != nil {
		startsAt = *req.StartsAt
	}

	// Resolve workspace_id from project context (set by WorkspaceRLS middleware).
	workspaceID, err := mw.GetWorkspaceID(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("could not resolve workspace_id"))
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

	input := service.CreateRecurringInput{
		WorkspaceID:         workspaceID,
		ProjectID:           projectID,
		TitleTemplate:       req.TitleTemplate,
		DescriptionTemplate: req.DescriptionTemplate,
		Frequency:           freq,
		CronExpr:            req.CronExpr,
		Timezone:            req.Timezone,
		AssigneeID:          req.AssigneeID,
		AssigneeType:        assigneeType,
		Priority:            priority,
		Labels:              req.Labels,
		StatusID:            req.StatusID,
		IsActive:            isActive,
		StartsAt:            startsAt,
		EndsAt:              req.EndsAt,
		MaxInstances:        req.MaxInstances,
		CreatedBy:           createdBy,
		CreatedByType:       createdByType,
	}

	schedule, err := h.recurringSvc.Create(c.Request().Context(), input)
	if err != nil {
		return handleError(c, err)
	}

	h.decorateScheduleURLs(c.Request().Context(), c.Request(), schedule)

	return c.JSON(http.StatusCreated, schedule)
}

// List handles GET /projects/:proj_id/recurring
func (h *RecurringHandler) List(c echo.Context) error {
	projectIDStr := c.Param("proj_id")
	projectID, err := uuid.Parse(projectIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	var pg pagination.Params
	if err = c.Bind(&pg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination params"))
	}

	page, err := h.recurringSvc.ListByProject(c.Request().Context(), projectID, pg)
	if err != nil {
		return handleError(c, err)
	}

	// Every item of a list response carries its own deep-link — a field the
	// handler computes on some paths only reads as absent on the others (the
	// #fc032545 lesson on tasks). One call for the whole page: the helper
	// resolves the shared project's slugs once instead of per item.
	items := make([]*domain.RecurringSchedule, len(page.Items))
	for i := range page.Items {
		items[i] = &page.Items[i]
	}
	h.decorateScheduleURLs(c.Request().Context(), c.Request(), items...)

	return c.JSON(http.StatusOK, page)
}

// GetByID handles GET /recurring/:recurring_id
func (h *RecurringHandler) GetByID(c echo.Context) error {
	idStr := c.Param("recurring_id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid recurring schedule id"))
	}

	schedule, err := h.recurringSvc.GetByID(c.Request().Context(), id)
	if err != nil {
		return handleError(c, err)
	}

	h.decorateScheduleURLs(c.Request().Context(), c.Request(), schedule)

	return c.JSON(http.StatusOK, schedule)
}

// Update handles PATCH /recurring/:recurring_id
func (h *RecurringHandler) Update(c echo.Context) error {
	idStr := c.Param("recurring_id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid recurring schedule id"))
	}

	var req updateRecurringRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	input := service.UpdateRecurringInput{
		TitleTemplate:       req.TitleTemplate,
		DescriptionTemplate: req.DescriptionTemplate,
		Frequency:           req.Frequency,
		CronExpr:            req.CronExpr,
		Timezone:            req.Timezone,
		AssigneeID:          req.AssigneeID,
		AssigneeType:        req.AssigneeType,
		Priority:            req.Priority,
		Labels:              convertLabels(req.Labels),
		StatusID:            req.StatusID,
		IsActive:            req.IsActive,
		EndsAt:              req.EndsAt,
		MaxInstances:        req.MaxInstances,
	}

	schedule, err := h.recurringSvc.Update(c.Request().Context(), id, input)
	if err != nil {
		return handleError(c, err)
	}

	h.decorateScheduleURLs(c.Request().Context(), c.Request(), schedule)

	return c.JSON(http.StatusOK, schedule)
}

// Delete handles DELETE /recurring/:recurring_id
func (h *RecurringHandler) Delete(c echo.Context) error {
	idStr := c.Param("recurring_id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid recurring schedule id"))
	}

	if err := h.recurringSvc.Delete(c.Request().Context(), id); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// triggerNowResponse is the JSON response for POST /recurring/:recurring_id/trigger
type triggerNowResponse struct {
	Task           *domain.Task `json:"task"`
	InstanceNumber int          `json:"instance_number"`
}

// Trigger handles POST /recurring/:recurring_id/trigger
func (h *RecurringHandler) Trigger(c echo.Context) error {
	idStr := c.Param("recurring_id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid recurring schedule id"))
	}

	task, err := h.recurringSvc.TriggerNow(c.Request().Context(), id)
	if err != nil {
		return handleError(c, err)
	}

	instanceNumber := 0
	if task.RecurringInstanceNumber != nil {
		instanceNumber = *task.RecurringInstanceNumber
	}

	return c.JSON(http.StatusCreated, triggerNowResponse{
		Task:           task,
		InstanceNumber: instanceNumber,
	})
}

// History handles GET /recurring/:recurring_id/history
func (h *RecurringHandler) History(c echo.Context) error {
	idStr := c.Param("recurring_id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid recurring schedule id"))
	}

	var pg pagination.Params
	if err = c.Bind(&pg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination params"))
	}
	// Default: newest first.
	if pg.SortDir == "" {
		pg.SortDir = "desc"
	}

	page, err := h.recurringSvc.GetHistory(c.Request().Context(), id, pg)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, page)
}

// convertLabels safely converts *[]string to *[]string (pointer passthrough for UpdateRecurringInput).
func convertLabels(labels *[]string) *[]string {
	return labels
}
