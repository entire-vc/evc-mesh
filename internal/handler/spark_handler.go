package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/internal/spark"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// SparkHandler handles HTTP requests for the Spark agent catalog integration.
type SparkHandler struct {
	sparkClient  *spark.Client
	agentService service.AgentService
	memberRepo   repository.WorkspaceMemberRepository
}

// NewSparkHandler creates a new SparkHandler.
//
// memberRepo is what Install checks its caller against. The Spark routes are the
// only ones in the authenticated group exempt from RequireWorkspaceMemberScoped —
// their :agent_id is a Spark catalog id, not a local agents.id, so no workspace
// can be resolved from the path — which leaves Install to do the check itself.
func NewSparkHandler(client *spark.Client, agentService service.AgentService, memberRepo repository.WorkspaceMemberRepository) *SparkHandler {
	return &SparkHandler{
		sparkClient:  client,
		agentService: agentService,
		memberRepo:   memberRepo,
	}
}

// sparkSearchQuery holds query parameters for searching the Spark catalog.
type sparkSearchQuery struct {
	Q     string `query:"q"`
	Tags  string `query:"tags"`
	Limit string `query:"limit"`
}

// Search handles GET /api/v1/spark/agents?q=...&tags=...&limit=...
func (h *SparkHandler) Search(c echo.Context) error {
	var q sparkSearchQuery
	if err := c.Bind(&q); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid query parameters"))
	}

	limit := 20
	if q.Limit != "" {
		if n, err := strconv.Atoi(q.Limit); err == nil && n > 0 {
			limit = n
		}
	}

	var tags []string
	if q.Tags != "" {
		for _, t := range strings.Split(q.Tags, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				tags = append(tags, t)
			}
		}
	}

	agents, err := h.sparkClient.Search(c.Request().Context(), q.Q, tags, limit)
	if err != nil {
		// Client already degrades gracefully; this path is hit only for GetByID.
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"items": agents,
		"count": len(agents),
	})
}

// GetByID handles GET /api/v1/spark/agents/:agent_id
func (h *SparkHandler) GetByID(c echo.Context) error {
	agentID := c.Param("agent_id")
	if agentID == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("agent_id is required"))
	}

	manifest, err := h.sparkClient.GetByID(c.Request().Context(), agentID)
	if err != nil {
		return c.JSON(http.StatusServiceUnavailable, apierror.BadRequestWithDetails(
			"Spark catalog unavailable",
			err.Error(),
		))
	}

	if manifest == nil {
		return c.JSON(http.StatusNotFound, apierror.NotFound("SparkAgent"))
	}

	return c.JSON(http.StatusOK, manifest)
}

// Popular handles GET /api/v1/spark/agents/popular
func (h *SparkHandler) Popular(c echo.Context) error {
	limitStr := c.QueryParam("limit")
	limit := 20
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			limit = n
		}
	}

	agents, err := h.sparkClient.ListPopular(c.Request().Context(), limit)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"items": agents,
		"count": len(agents),
	})
}

// installRequest holds the body for installing an agent from Spark.
type installRequest struct {
	WorkspaceID string `json:"workspace_id"`
}

// Install handles POST /api/v1/spark/agents/:agent_id/install
// Fetches the manifest from Spark and registers the agent in the local workspace.
func (h *SparkHandler) Install(c echo.Context) error {
	sparkAgentID := c.Param("agent_id")
	if sparkAgentID == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("agent_id is required"))
	}

	var req installRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.WorkspaceID == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"workspace_id": "workspace_id is required",
		}))
	}

	wsID, err := uuid.Parse(req.WorkspaceID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	// The target workspace arrives in the body, so no middleware saw it: without
	// this check any authenticated stranger could register an agent into someone
	// else's workspace and be handed its API key in the response. Same bar as
	// POST /workspaces/:ws_id/agents, which this route otherwise duplicates.
	if denied := h.requireRegisterAgent(c, wsID); denied != nil {
		return denied
	}

	// Fetch agent manifest from Spark catalog.
	manifest, err := h.sparkClient.GetByID(c.Request().Context(), sparkAgentID)
	if err != nil {
		return c.JSON(http.StatusServiceUnavailable, apierror.BadRequestWithDetails(
			"Spark catalog unavailable",
			err.Error(),
		))
	}
	if manifest == nil {
		return c.JSON(http.StatusNotFound, apierror.NotFound("SparkAgent"))
	}

	// Resolve capabilities from manifest.
	capabilities := manifest.Capabilities
	if capabilities == nil {
		capabilities = map[string]any{}
	}
	// Merge Spark config into capabilities for reference.
	if len(manifest.Config) > 0 {
		capabilities["spark_config"] = manifest.Config
	}
	capabilities["spark_id"] = manifest.ID
	capabilities["spark_version"] = manifest.Version
	capabilities["spark_author"] = manifest.Author

	// Register agent locally using the manifest data.
	input := service.RegisterAgentInput{
		WorkspaceID:  wsID,
		Name:         manifest.Name,
		AgentType:    domain.AgentType(resolveAgentType(manifest.AgentType)),
		Capabilities: capabilities,
	}

	output, err := h.agentService.Register(c.Request().Context(), input)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, map[string]any{
		"agent":   output.Agent,
		"api_key": output.APIKey,
		"spark": map[string]any{
			"id":      manifest.ID,
			"version": manifest.Version,
			"author":  manifest.Author,
		},
	})
}

// requireRegisterAgent enforces, for a workspace named in a request body, the
// same rule mw.RequirePermission(PermRegisterAgent) enforces for one named in the
// path: agents may not do it at all, and a user must hold the permission in that
// specific workspace. Returns nil when the caller is allowed.
func (h *SparkHandler) requireRegisterAgent(c echo.Context, wsID uuid.UUID) error {
	if mw.IsAgent(c) {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("agents cannot perform this action"))
	}
	if h.memberRepo == nil {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
	}
	userID, err := mw.GetUserID(c)
	if err != nil {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("user context required"))
	}
	role, err := h.memberRepo.GetRole(c.Request().Context(), wsID, userID)
	if err != nil {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("not a workspace member"))
	}
	if !mw.RoleHasPermission(role, mw.PermRegisterAgent) {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("insufficient permissions"))
	}
	return nil
}

// resolveAgentType maps Spark agent_type string to local domain.AgentType.
// Falls back to "custom" for unknown types.
func resolveAgentType(sparkType string) string {
	known := map[string]string{
		"claude_code": "claude_code",
		"openclaw":    "openclaw",
		"cline":       "cline",
		"aider":       "aider",
		"custom":      "custom",
	}
	if t, ok := known[strings.ToLower(sparkType)]; ok {
		return t
	}
	return "custom"
}
