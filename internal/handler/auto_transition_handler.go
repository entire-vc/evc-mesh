package handler

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// AutoTransitionHandler handles CRUD for project-level auto-transition rules.
type AutoTransitionHandler struct {
	svc service.AutoTransitionService
}

// NewAutoTransitionHandler creates a new AutoTransitionHandler.
func NewAutoTransitionHandler(svc service.AutoTransitionService) *AutoTransitionHandler {
	return &AutoTransitionHandler{svc: svc}
}

type createAutoTransitionRuleRequest struct {
	Trigger        string `json:"trigger"`
	TargetStatusID string `json:"target_status_id"`
	IsEnabled      *bool  `json:"is_enabled"`
}

type updateAutoTransitionRuleRequest struct {
	TargetStatusID *string `json:"target_status_id"`
	IsEnabled      *bool   `json:"is_enabled"`
}

// List returns all auto-transition rules for a project.
func (h *AutoTransitionHandler) List(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}
	rules, err := h.svc.ListRules(c.Request().Context(), projID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierror.InternalError(err.Error()))
	}
	return c.JSON(http.StatusOK, rules)
}

// Create adds a new auto-transition rule to a project.
func (h *AutoTransitionHandler) Create(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}
	var req createAutoTransitionRuleRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	trigger := domain.AutoTransitionTrigger(req.Trigger)
	if trigger != domain.TriggerAllSubtasksDone && trigger != domain.TriggerBlockingDepResolved {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("trigger must be 'all_subtasks_done' or 'blocking_dep_resolved'"))
	}
	if req.TargetStatusID == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("target_status_id is required"))
	}
	targetID, err := uuid.Parse(req.TargetStatusID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid target_status_id"))
	}
	enabled := true
	if req.IsEnabled != nil {
		enabled = *req.IsEnabled
	}
	now := time.Now()
	rule := &domain.AutoTransitionRule{
		ID:             uuid.New(),
		ProjectID:      projID,
		Trigger:        trigger,
		TargetStatusID: targetID,
		IsEnabled:      enabled,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := h.svc.CreateRule(c.Request().Context(), rule); err != nil {
		return c.JSON(http.StatusInternalServerError, apierror.InternalError(err.Error()))
	}
	return c.JSON(http.StatusCreated, rule)
}

// Update modifies an existing auto-transition rule.
func (h *AutoTransitionHandler) Update(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}
	ruleID, err := uuid.Parse(c.Param("rule_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid rule_id"))
	}
	var req updateAutoTransitionRuleRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	ctx := c.Request().Context()
	existingRules, err := h.svc.ListRules(ctx, projID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierror.InternalError(err.Error()))
	}
	var existing *domain.AutoTransitionRule
	for i := range existingRules {
		if existingRules[i].ID == ruleID {
			existing = &existingRules[i]
			break
		}
	}
	if existing == nil {
		return c.JSON(http.StatusNotFound, apierror.NotFound("AutoTransitionRule"))
	}
	if req.TargetStatusID != nil {
		tid, err := uuid.Parse(*req.TargetStatusID)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid target_status_id"))
		}
		existing.TargetStatusID = tid
	}
	if req.IsEnabled != nil {
		existing.IsEnabled = *req.IsEnabled
	}
	existing.UpdatedAt = time.Now()
	if err := h.svc.UpdateRule(ctx, existing); err != nil {
		return c.JSON(http.StatusInternalServerError, apierror.InternalError(err.Error()))
	}
	return c.JSON(http.StatusOK, existing)
}

// Delete removes an auto-transition rule.
func (h *AutoTransitionHandler) Delete(c echo.Context) error {
	_, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}
	ruleID, err := uuid.Parse(c.Param("rule_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid rule_id"))
	}
	if err := h.svc.DeleteRule(c.Request().Context(), ruleID); err != nil {
		return c.JSON(http.StatusInternalServerError, apierror.InternalError(err.Error()))
	}
	return c.NoContent(http.StatusNoContent)
}
