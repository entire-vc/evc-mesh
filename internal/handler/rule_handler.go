package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// RuleHandler handles HTTP requests for rule management.
type RuleHandler struct {
	ruleSvc service.RuleService
}

// NewRuleHandler creates a new RuleHandler.
func NewRuleHandler(svc service.RuleService) *RuleHandler {
	return &RuleHandler{ruleSvc: svc}
}

// createRuleRequest is the request body for creating a rule.
type createRuleRequest struct {
	Scope               domain.RuleScope       `json:"scope"`
	RuleType            string                 `json:"rule_type"`
	Name                string                 `json:"name"`
	Description         string                 `json:"description"`
	Config              json.RawMessage        `json:"config"`
	AppliesToActorTypes []string               `json:"applies_to_actor_types"`
	AppliesToRoles      []string               `json:"applies_to_roles"`
	Enforcement         domain.RuleEnforcement `json:"enforcement"`
	Priority            int                    `json:"priority"`
	AgentID             *uuid.UUID             `json:"agent_id,omitempty"`
}

// updateRuleRequest is the request body for partially updating a rule.
type updateRuleRequest struct {
	Name                *string                 `json:"name"`
	Description         *string                 `json:"description"`
	Config              json.RawMessage         `json:"config"`
	AppliesToActorTypes []string                `json:"applies_to_actor_types"`
	AppliesToRoles      []string                `json:"applies_to_roles"`
	Enforcement         *domain.RuleEnforcement `json:"enforcement"`
	Priority            *int                    `json:"priority"`
	IsEnabled           *bool                   `json:"is_enabled"`
}

// evaluateRuleRequest is the request body for dry-run rule evaluation.
type evaluateRuleRequest struct {
	Action         string     `json:"action"`
	TaskID         *uuid.UUID `json:"task_id"`
	TargetStatusID *uuid.UUID `json:"target_status_id"`
	WorkspaceID    uuid.UUID  `json:"workspace_id"`
	ProjectID      *uuid.UUID `json:"project_id"`
}

// CreateWorkspaceRule handles POST /workspaces/:ws_id/rules
func (h *RuleHandler) CreateWorkspaceRule(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	var req createRuleRequest
	err = c.Bind(&req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	input := service.CreateRuleInput{
		WorkspaceID:         wsID,
		Scope:               domain.RuleScopeWorkspace,
		RuleType:            req.RuleType,
		Name:                req.Name,
		Description:         req.Description,
		Config:              req.Config,
		AppliesToActorTypes: req.AppliesToActorTypes,
		AppliesToRoles:      req.AppliesToRoles,
		Enforcement:         req.Enforcement,
		Priority:            req.Priority,
	}

	rule, err := h.ruleSvc.Create(c.Request().Context(), input)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusCreated, rule)
}

// ListWorkspaceRules handles GET /workspaces/:ws_id/rules
func (h *RuleHandler) ListWorkspaceRules(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	includeDisabled := c.QueryParam("include_disabled") == "true"
	rules, err := h.ruleSvc.ListByWorkspace(c.Request().Context(), wsID, includeDisabled)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"items": rules, "total_count": len(rules)})
}

// GetWorkspaceEffectiveRules handles GET /workspaces/:ws_id/rules/effective
func (h *RuleHandler) GetWorkspaceEffectiveRules(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	ruleCtx := h.buildRuleContext(c, wsID, nil)
	rules, err := h.ruleSvc.GetEffective(c.Request().Context(), ruleCtx)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"items": rules, "total_count": len(rules)})
}

// CreateProjectRule handles POST /projects/:proj_id/rules
func (h *RuleHandler) CreateProjectRule(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	wsID, err := mw.GetWorkspaceID(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("workspace context required"))
	}

	var req createRuleRequest
	err = c.Bind(&req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	input := service.CreateRuleInput{
		WorkspaceID:         wsID,
		ProjectID:           &projID,
		Scope:               domain.RuleScopeProject,
		RuleType:            req.RuleType,
		Name:                req.Name,
		Description:         req.Description,
		Config:              req.Config,
		AppliesToActorTypes: req.AppliesToActorTypes,
		AppliesToRoles:      req.AppliesToRoles,
		Enforcement:         req.Enforcement,
		Priority:            req.Priority,
	}

	rule, err := h.ruleSvc.Create(c.Request().Context(), input)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusCreated, rule)
}

// ListProjectRules handles GET /projects/:proj_id/rules
func (h *RuleHandler) ListProjectRules(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	includeDisabled := c.QueryParam("include_disabled") == "true"
	rules, err := h.ruleSvc.ListByProject(c.Request().Context(), projID, includeDisabled)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"items": rules, "total_count": len(rules)})
}

// GetProjectEffectiveRules handles GET /projects/:proj_id/rules/effective
func (h *RuleHandler) GetProjectEffectiveRules(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	wsID, err := mw.GetWorkspaceID(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("workspace context required"))
	}

	ruleCtx := h.buildRuleContext(c, wsID, &projID)
	rules, err := h.ruleSvc.GetEffective(c.Request().Context(), ruleCtx)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"items": rules, "total_count": len(rules)})
}

// GetRule handles GET /rules/:rule_id
func (h *RuleHandler) GetRule(c echo.Context) error {
	ruleID, err := uuid.Parse(c.Param("rule_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid rule_id"))
	}

	rule, err := h.ruleSvc.GetByID(c.Request().Context(), ruleID)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, rule)
}

// UpdateRule handles PATCH /rules/:rule_id
func (h *RuleHandler) UpdateRule(c echo.Context) error {
	ruleID, err := uuid.Parse(c.Param("rule_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid rule_id"))
	}

	var req updateRuleRequest
	err = c.Bind(&req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	input := service.UpdateRuleInput{
		Name:                req.Name,
		Description:         req.Description,
		Config:              req.Config,
		AppliesToActorTypes: req.AppliesToActorTypes,
		AppliesToRoles:      req.AppliesToRoles,
		Enforcement:         req.Enforcement,
		Priority:            req.Priority,
		IsEnabled:           req.IsEnabled,
	}

	rule, err := h.ruleSvc.Update(c.Request().Context(), ruleID, input)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, rule)
}

// DeleteRule handles DELETE /rules/:rule_id
func (h *RuleHandler) DeleteRule(c echo.Context) error {
	ruleID, err := uuid.Parse(c.Param("rule_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid rule_id"))
	}

	if err := h.ruleSvc.Delete(c.Request().Context(), ruleID); err != nil {
		return handleError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// EvaluateRules handles POST /rules/evaluate (dry-run)
func (h *RuleHandler) EvaluateRules(c echo.Context) error {
	var req evaluateRuleRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	actorID, actorType := actorctx.FromContext(c.Request().Context())

	input := service.EvaluateInput{
		Action:         req.Action,
		TaskID:         req.TaskID,
		TargetStatusID: req.TargetStatusID,
		ActorID:        actorID,
		ActorType:      actorType,
		WorkspaceID:    req.WorkspaceID,
		ProjectID:      req.ProjectID,
	}

	violations, err := h.ruleSvc.Evaluate(c.Request().Context(), input)
	if err != nil {
		return handleError(c, err)
	}

	if violations == nil {
		violations = []domain.RuleViolation{}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"violations": violations,
		"blocked":    hasBlockingViolation(violations),
	})
}

// buildRuleContext extracts actor identity from the Echo context.
func (h *RuleHandler) buildRuleContext(c echo.Context, wsID uuid.UUID, projID *uuid.UUID) service.RuleContext {
	actorID, actorType := actorctx.FromContext(c.Request().Context())

	ruleCtx := service.RuleContext{
		WorkspaceID: wsID,
		ProjectID:   projID,
		ActorID:     actorID,
		ActorType:   actorType,
	}

	if actorType == domain.ActorTypeAgent {
		ruleCtx.AgentID = &actorID
	}

	return ruleCtx
}

// hasBlockingViolation returns true if any violation has enforcement=block.
func hasBlockingViolation(violations []domain.RuleViolation) bool {
	for _, v := range violations {
		if v.Enforcement == domain.RuleEnforcementBlock {
			return true
		}
	}
	return false
}
