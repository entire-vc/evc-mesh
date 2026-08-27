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
	resolver     *service.SparkIntegrationResolver
	agentService service.AgentService
	memberRepo   repository.WorkspaceMemberRepository
}

// NewSparkHandler creates a new SparkHandler.
//
// memberRepo is what Install and requireWorkspaceAccess check their caller
// against. The Spark routes are the only ones in the authenticated group
// exempt from RequireWorkspaceMemberScoped — their :agent_id is a Spark
// catalog id, not a local agents.id, so no workspace can be resolved from
// the path — which leaves every method that accepts a workspace_id (in the
// body for Install, in the query for the read endpoints) to check it itself.
func NewSparkHandler(resolver *service.SparkIntegrationResolver, agentService service.AgentService, memberRepo repository.WorkspaceMemberRepository) *SparkHandler {
	return &SparkHandler{
		resolver:     resolver,
		agentService: agentService,
		memberRepo:   memberRepo,
	}
}

// clientFor resolves the Spark base URL governing workspaceID right now
// (§C1/§C5 of specsintegration-provider-contract — fresh on every call, a
// workspace row wins wholly over env, disabled means disabled) and builds a
// fresh client against it. workspaceID may be uuid.Nil — that never matches
// a stored row, so resolution falls straight through to the env fallback,
// which is what every caller that does not yet send workspace_id gets
// (unchanged single-instance behavior).
//
// A non-nil workspaceID is checked against the caller FIRST (requireWorkspaceAccess):
// resolving is not a pure narrowing read the way e.g. a project_id filter is — a
// workspace's spark row can point at a DIFFERENT catalog host than this
// deployment's own env fallback, so an unauthorized caller supplying a foreign
// workspace_id would make Mesh's server proxy real results back from wherever
// that OTHER workspace pointed it, not just get an empty answer (see
// internal/middleware/query_tenant_verdict_test.go's narrows-vs-checked rule —
// this is exactly the "reaches a different answer, not an empty one" case that
// forces "checked:" over "narrows:").
//
// Reports whether the request was refused via a bool, NOT via the error
// return alone — c.JSON returns nil once it has written the response (see
// requireRegisterAgent's doc comment below for the same trap), so
// `if _, err := clientFor(...); err != nil` would silently continue with a
// nil client on refusal instead of stopping. The caller must check `handled`
// and return immediately when true.
func (h *SparkHandler) clientFor(c echo.Context, workspaceID uuid.UUID) (client *spark.Client, handled bool, resp error) {
	if workspaceID != uuid.Nil && !h.requireWorkspaceAccess(c, workspaceID) {
		return nil, true, c.JSON(http.StatusForbidden, apierror.Forbidden("not a member of this workspace"))
	}

	url, _, ok := h.resolver.ResolveSparkURL(c.Request().Context(), workspaceID)
	if !ok {
		return nil, true, c.JSON(http.StatusServiceUnavailable, apierror.ServiceUnavailable(
			"spark URL not configured: no active spark integration for this workspace and no MESH_SPARK_URL fallback"))
	}
	return spark.NewClient(url), false, nil
}

// requireWorkspaceAccess reports whether the authenticated caller may act as
// wsID for Spark purposes. Two actor shapes, because Search/Popular/GetByID
// (unlike Install) ARE reachable by agents:
//   - an agent may only ever pass its own workspace_id — the auth middleware
//     puts it straight into the context (mw.GetWorkspaceID) at request time,
//     since agents.workspace_id hard-binds one agent key to one workspace;
//     there is no "agent that is a member of several workspaces" the way a
//     user can be, so this needs no extra lookup;
//   - a user must hold a workspace_members row for wsID (mirrors
//     NotificationHandler.requireWorkspaceMember / MemoryHandler.workspaceAllowed
//     — same rule, same caveat: a founding owner with no explicit membership
//     row is not covered by this check, a pre-existing gap this reuses rather
//     than re-litigates).
func (h *SparkHandler) requireWorkspaceAccess(c echo.Context, wsID uuid.UUID) bool {
	if mw.IsAgent(c) {
		ownWS, err := mw.GetWorkspaceID(c)
		if err != nil {
			return false
		}
		return ownWS == wsID
	}
	if h.memberRepo == nil {
		return false
	}
	userID, err := mw.GetUserID(c)
	if err != nil {
		return false
	}
	_, err = h.memberRepo.GetRole(c.Request().Context(), wsID, userID)
	return err == nil
}

// parseOptionalWorkspaceID reads an optional workspace_id query parameter.
// Absent → uuid.Nil (falls through to env in clientFor, matching pre-C1
// behavior for callers that don't send it yet). Present but malformed → an
// explicit 400, same bar as every other place this codebase parses a
// workspace_id — a caller that tried to scope the request and got the id
// wrong should not be silently treated as "didn't scope it".
//
// Same bool-not-error-alone contract as clientFor above, for the same reason.
func parseOptionalWorkspaceID(c echo.Context) (id uuid.UUID, handled bool, resp error) {
	raw := c.QueryParam("workspace_id")
	if raw == "" {
		return uuid.Nil, false, nil
	}
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, true, c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}
	return parsed, false, nil
}

// sparkSearchQuery holds query parameters for searching the Spark catalog.
type sparkSearchQuery struct {
	Q           string `query:"q"`
	Tags        string `query:"tags"`
	Limit       string `query:"limit"`
	WorkspaceID string `query:"workspace_id"`
}

// Search handles GET /api/v1/spark/agents?q=...&tags=...&limit=...&workspace_id=...
func (h *SparkHandler) Search(c echo.Context) error {
	var q sparkSearchQuery
	if err := c.Bind(&q); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid query parameters"))
	}

	wsID, handled, resp := parseOptionalWorkspaceID(c)
	if handled {
		return resp
	}
	client, handled, resp := h.clientFor(c, wsID)
	if handled {
		return resp
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

	agents, err := client.Search(c.Request().Context(), q.Q, tags, limit)
	if err != nil {
		// Client already degrades gracefully; this path is hit only for GetByID.
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"items": agents,
		"count": len(agents),
	})
}

// GetByID handles GET /api/v1/spark/agents/:agent_id?workspace_id=...
func (h *SparkHandler) GetByID(c echo.Context) error {
	agentID := c.Param("agent_id")
	if agentID == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("agent_id is required"))
	}

	wsID, handled, resp := parseOptionalWorkspaceID(c)
	if handled {
		return resp
	}
	client, handled, resp := h.clientFor(c, wsID)
	if handled {
		return resp
	}

	manifest, err := client.GetByID(c.Request().Context(), agentID)
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

// Popular handles GET /api/v1/spark/agents/popular?workspace_id=...
func (h *SparkHandler) Popular(c echo.Context) error {
	wsID, handled, resp := parseOptionalWorkspaceID(c)
	if handled {
		return resp
	}
	client, handled, resp := h.clientFor(c, wsID)
	if handled {
		return resp
	}

	limitStr := c.QueryParam("limit")
	limit := 20
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			limit = n
		}
	}

	agents, err := client.ListPopular(c.Request().Context(), limit)
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
	if denied, refusal := h.requireRegisterAgent(c, wsID); denied {
		return refusal
	}

	client, handled, resp := h.clientFor(c, wsID)
	if handled {
		return resp
	}

	// Fetch agent manifest from Spark catalog.
	manifest, err := client.GetByID(c.Request().Context(), sparkAgentID)
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
// specific workspace.
//
// It reports whether the request was refused, and the caller must return
// immediately when it was. The refusal cannot be signalled through the error
// return alone: c.JSON returns nil once it has written the response, so an
// `if err := check(); err != nil` guard would write the 403 and then carry on and
// install the agent anyway.
func (h *SparkHandler) requireRegisterAgent(c echo.Context, wsID uuid.UUID) (bool, error) {
	if mw.IsAgent(c) {
		return true, c.JSON(http.StatusForbidden, apierror.Forbidden("agents cannot perform this action"))
	}
	if h.memberRepo == nil {
		return true, c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
	}
	userID, err := mw.GetUserID(c)
	if err != nil {
		return true, c.JSON(http.StatusForbidden, apierror.Forbidden("user context required"))
	}
	role, err := h.memberRepo.GetRole(c.Request().Context(), wsID, userID)
	if err != nil {
		return true, c.JSON(http.StatusForbidden, apierror.Forbidden("not a workspace member"))
	}
	if !mw.RoleHasPermission(role, mw.PermRegisterAgent) {
		return true, c.JSON(http.StatusForbidden, apierror.Forbidden("insufficient permissions"))
	}
	return false, nil
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
