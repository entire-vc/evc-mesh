package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// RulesHandler handles HTTP requests for the workspace/project rules system.
type RulesHandler struct {
	rulesSvc service.RulesService
}

// NewRulesHandler creates a new RulesHandler.
func NewRulesHandler(rs service.RulesService) *RulesHandler {
	return &RulesHandler{rulesSvc: rs}
}

// --------------------------------------------------------------------------
// Team Directory
// --------------------------------------------------------------------------

// GetTeamDirectory handles GET /workspaces/:ws_id/team
// Supports ?format=tree for hierarchical org chart output with project affiliations.
func (h *RulesHandler) GetTeamDirectory(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	format := c.QueryParam("format")
	if format == "tree" {
		tree, err := h.rulesSvc.GetTeamDirectoryTree(c.Request().Context(), wsID)
		if err != nil {
			return handleError(c, err)
		}
		return c.JSON(http.StatusOK, tree)
	}

	dir, err := h.rulesSvc.GetTeamDirectory(c.Request().Context(), wsID)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, dir)
}

// UpdateAgentProfile handles PUT /agents/:agent_id/profile
func (h *RulesHandler) UpdateAgentProfile(c echo.Context) error {
	agentID, err := uuid.Parse(c.Param("agent_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid agent_id"))
	}

	var profile domain.AgentProfileUpdate
	if err := c.Bind(&profile); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if err := h.rulesSvc.UpdateAgentProfile(c.Request().Context(), agentID, profile); err != nil {
		return handleError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// --------------------------------------------------------------------------
// Assignment Rules
// --------------------------------------------------------------------------

// GetWorkspaceAssignmentRules handles GET /workspaces/:ws_id/rules/assignment
func (h *RulesHandler) GetWorkspaceAssignmentRules(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	cfg, err := h.rulesSvc.GetWorkspaceAssignmentRules(c.Request().Context(), wsID)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, cfg)
}

// SetWorkspaceAssignmentRules handles PUT /workspaces/:ws_id/rules/assignment
func (h *RulesHandler) SetWorkspaceAssignmentRules(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	var cfg domain.AssignmentRulesConfig
	if err := c.Bind(&cfg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if err := h.rulesSvc.SetWorkspaceAssignmentRules(c.Request().Context(), wsID, cfg); err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// GetEffectiveAssignmentRules handles GET /projects/:proj_id/rules/assignment
func (h *RulesHandler) GetEffectiveAssignmentRules(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	effective, err := h.rulesSvc.GetEffectiveAssignmentRules(c.Request().Context(), projID)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, effective)
}

// SetProjectAssignmentRules handles PUT /projects/:proj_id/rules/assignment
func (h *RulesHandler) SetProjectAssignmentRules(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	var cfg domain.AssignmentRulesConfig
	if err := c.Bind(&cfg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if err := h.rulesSvc.SetProjectAssignmentRules(c.Request().Context(), projID, cfg); err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// --------------------------------------------------------------------------
// Workflow Rules
// --------------------------------------------------------------------------

// GetProjectWorkflowRules handles GET /projects/:proj_id/rules/workflow
func (h *RulesHandler) GetProjectWorkflowRules(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	// Extract caller agent ID if the request is from an agent.
	var callerAgentID *uuid.UUID
	actorID, actorType := actorctx.FromContext(c.Request().Context())
	if actorType == domain.ActorTypeAgent && actorID != uuid.Nil {
		callerAgentID = &actorID
	}

	resp, err := h.rulesSvc.GetProjectWorkflowRules(c.Request().Context(), projID, callerAgentID)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}

// SetProjectWorkflowRules handles PUT /projects/:proj_id/rules/workflow
func (h *RulesHandler) SetProjectWorkflowRules(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	// Accept raw JSON to preserve the full config structure.
	var raw json.RawMessage
	if err := c.Bind(&raw); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	var cfg domain.WorkflowRulesConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workflow rules config"))
	}

	if err := h.rulesSvc.SetProjectWorkflowRules(c.Request().Context(), projID, cfg); err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// --------------------------------------------------------------------------
// Violations
// --------------------------------------------------------------------------

// ListViolations handles GET /workspaces/:ws_id/violations
func (h *RulesHandler) ListViolations(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	limit := 100
	if l := c.QueryParam("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	violations, err := h.rulesSvc.ListViolations(c.Request().Context(), wsID, limit)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"items":       violations,
		"total_count": len(violations),
	})
}

// --------------------------------------------------------------------------
// Sprint 21 — Config Import/Export
// --------------------------------------------------------------------------

// ImportConfig handles POST /workspaces/:ws_id/config/import
// Accepts a YAML body and applies the full MeshConfig to the workspace.
func (h *RulesHandler) ImportConfig(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("failed to read request body"))
	}
	if len(body) == 0 {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("request body is empty"))
	}

	result, err := h.rulesSvc.ImportConfig(c.Request().Context(), wsID, body)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, result)
}

// ExportConfig handles GET /workspaces/:ws_id/config/export
// Returns the current workspace config as YAML.
func (h *RulesHandler) ExportConfig(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	yamlData, err := h.rulesSvc.ExportConfig(c.Request().Context(), wsID)
	if err != nil {
		return handleError(c, err)
	}
	return c.Blob(http.StatusOK, "application/yaml", yamlData)
}

// ImportTeam handles POST /workspaces/:ws_id/team/import
// Accepts a YAML body with TeamConfig and bulk-imports agent/human profiles.
func (h *RulesHandler) ImportTeam(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("failed to read request body"))
	}
	if len(body) == 0 {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("request body is empty"))
	}

	result, err := h.rulesSvc.ImportTeam(c.Request().Context(), wsID, body)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, result)
}

// --------------------------------------------------------------------------
// Sprint 21 — Workflow Templates
// --------------------------------------------------------------------------

// GetWorkflowTemplates handles GET /workspaces/:ws_id/rules/workflow-templates
func (h *RulesHandler) GetWorkflowTemplates(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	templates, err := h.rulesSvc.GetWorkflowTemplates(c.Request().Context(), wsID)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, templates)
}

// SetWorkflowTemplates handles PUT /workspaces/:ws_id/rules/workflow-templates
func (h *RulesHandler) SetWorkflowTemplates(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	var raw json.RawMessage
	if err := c.Bind(&raw); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	var templates map[string]domain.WorkflowRulesConfig
	if err := json.Unmarshal(raw, &templates); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workflow templates config"))
	}

	if err := h.rulesSvc.SetWorkflowTemplates(c.Request().Context(), wsID, templates); err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

