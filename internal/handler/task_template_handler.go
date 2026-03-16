package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// TaskTemplateHandler handles HTTP requests for task template management.
type TaskTemplateHandler struct {
	svc service.TaskTemplateService
}

// NewTaskTemplateHandler creates a new TaskTemplateHandler.
func NewTaskTemplateHandler(svc service.TaskTemplateService) *TaskTemplateHandler {
	return &TaskTemplateHandler{svc: svc}
}

// createTemplateRequest is the JSON body for creating a task template.
type createTemplateRequest struct {
	Name                string               `json:"name"`
	Description         string               `json:"description"`
	TitleTemplate       string               `json:"title_template"`
	DescriptionTemplate string               `json:"description_template"`
	Priority            domain.Priority      `json:"priority"`
	Labels              []string             `json:"labels"`
	EstimatedHours      *float64             `json:"estimated_hours"`
	CustomFields        json.RawMessage      `json:"custom_fields"`
	AssigneeID          *uuid.UUID           `json:"assignee_id"`
	AssigneeType        *domain.AssigneeType `json:"assignee_type"`
	StatusID            *uuid.UUID           `json:"status_id"`
}

// updateTemplateRequest is the JSON body for partially updating a task template.
type updateTemplateRequest struct {
	Name                *string              `json:"name"`
	Description         *string              `json:"description"`
	TitleTemplate       *string              `json:"title_template"`
	DescriptionTemplate *string              `json:"description_template"`
	Priority            *domain.Priority     `json:"priority"`
	Labels              *[]string            `json:"labels"`
	EstimatedHours      *float64             `json:"estimated_hours"`
	CustomFields        json.RawMessage      `json:"custom_fields"`
	AssigneeID          *uuid.UUID           `json:"assignee_id"`
	AssigneeType        *domain.AssigneeType `json:"assignee_type"`
	StatusID            *uuid.UUID           `json:"status_id"`
}

// createTaskFromTemplateRequest is the JSON body for instantiating a task from a template.
type createTaskFromTemplateRequest struct {
	Overrides map[string]any `json:"overrides"`
}

// Create handles POST /projects/:proj_id/templates
func (h *TaskTemplateHandler) Create(c echo.Context) error {
	projectIDStr := c.Param("proj_id")
	projectID, err := uuid.Parse(projectIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	var req createTemplateRequest
	err = c.Bind(&req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"name": "name is required",
		}))
	}

	var createdBy *uuid.UUID
	if mw.IsAgent(c) {
		id, _ := mw.GetAgentID(c)
		createdBy = &id
	} else {
		id, _ := mw.GetUserID(c)
		createdBy = &id
	}

	input := domain.CreateTemplateInput{
		ProjectID:           projectID,
		Name:                req.Name,
		Description:         req.Description,
		TitleTemplate:       req.TitleTemplate,
		DescriptionTemplate: req.DescriptionTemplate,
		Priority:            req.Priority,
		Labels:              req.Labels,
		EstimatedHours:      req.EstimatedHours,
		CustomFields:        req.CustomFields,
		AssigneeID:          req.AssigneeID,
		AssigneeType:        req.AssigneeType,
		StatusID:            req.StatusID,
		CreatedBy:           createdBy,
	}

	tmpl, err := h.svc.Create(c.Request().Context(), input)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, tmpl)
}

// List handles GET /projects/:proj_id/templates
func (h *TaskTemplateHandler) List(c echo.Context) error {
	projectIDStr := c.Param("proj_id")
	projectID, err := uuid.Parse(projectIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	tmpls, err := h.svc.List(c.Request().Context(), projectID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]any{"items": tmpls, "total": len(tmpls)})
}

// GetByID handles GET /templates/:tmpl_id
func (h *TaskTemplateHandler) GetByID(c echo.Context) error {
	tmplIDStr := c.Param("tmpl_id")
	tmplID, err := uuid.Parse(tmplIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid tmpl_id"))
	}

	tmpl, err := h.svc.GetByID(c.Request().Context(), tmplID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, tmpl)
}

// Update handles PATCH /templates/:tmpl_id
func (h *TaskTemplateHandler) Update(c echo.Context) error {
	tmplIDStr := c.Param("tmpl_id")
	tmplID, err := uuid.Parse(tmplIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid tmpl_id"))
	}

	var req updateTemplateRequest
	err = c.Bind(&req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	input := domain.UpdateTemplateInput{
		Name:                req.Name,
		Description:         req.Description,
		TitleTemplate:       req.TitleTemplate,
		DescriptionTemplate: req.DescriptionTemplate,
		Priority:            req.Priority,
		Labels:              req.Labels,
		EstimatedHours:      req.EstimatedHours,
		CustomFields:        req.CustomFields,
		AssigneeID:          req.AssigneeID,
		AssigneeType:        req.AssigneeType,
		StatusID:            req.StatusID,
	}

	tmpl, err := h.svc.Update(c.Request().Context(), tmplID, input)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, tmpl)
}

// Delete handles DELETE /templates/:tmpl_id
func (h *TaskTemplateHandler) Delete(c echo.Context) error {
	tmplIDStr := c.Param("tmpl_id")
	tmplID, err := uuid.Parse(tmplIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid tmpl_id"))
	}

	if err := h.svc.Delete(c.Request().Context(), tmplID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// CreateTask handles POST /templates/:tmpl_id/create-task
func (h *TaskTemplateHandler) CreateTask(c echo.Context) error {
	tmplIDStr := c.Param("tmpl_id")
	tmplID, err := uuid.Parse(tmplIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid tmpl_id"))
	}

	var req createTaskFromTemplateRequest
	err = c.Bind(&req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	var createdBy uuid.UUID
	var createdByType domain.ActorType
	if mw.IsAgent(c) {
		createdBy, _ = mw.GetAgentID(c)
		createdByType = domain.ActorTypeAgent
	} else {
		createdBy, _ = mw.GetUserID(c)
		createdByType = domain.ActorTypeUser
	}

	overrides := req.Overrides
	if overrides == nil {
		overrides = map[string]any{}
	}

	task, err := h.svc.CreateTaskFromTemplate(c.Request().Context(), tmplID, createdBy, createdByType, overrides)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, task)
}
