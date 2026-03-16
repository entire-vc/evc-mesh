package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// MemoryHandler handles HTTP requests for agent persistent memory management.
type MemoryHandler struct {
	memoryService service.MemoryService
}

// NewMemoryHandler creates a new MemoryHandler with the given service.
func NewMemoryHandler(ms service.MemoryService) *MemoryHandler {
	return &MemoryHandler{memoryService: ms}
}

// rememberRequest is the JSON body for creating or updating a memory entry.
type rememberRequest struct {
	WorkspaceID uuid.UUID          `json:"workspace_id"`
	ProjectID   *uuid.UUID         `json:"project_id,omitempty"`
	Key         string             `json:"key"`
	Content     string             `json:"content"`
	Scope       domain.MemoryScope `json:"scope"`
	Tags        []string           `json:"tags,omitempty"`
	ExpiresAt   *string            `json:"expires_at,omitempty"` // RFC3339 string or Go duration
}

// rememberResponse wraps the upserted memory with the operation outcome.
type rememberResponse struct {
	Memory  *domain.Memory `json:"memory"`
	Outcome string         `json:"outcome"` // "created" or "updated"
}

// listMemoriesQuery represents query params for listing memories.
type listMemoriesQuery struct {
	WorkspaceID string `query:"workspace_id"`
	ProjectID   string `query:"project_id"`
	Scope       string `query:"scope"`
	Limit       string `query:"limit"`
}

// searchMemoriesQuery represents query params for searching memories.
type searchMemoriesQuery struct {
	Q           string `query:"q"`
	WorkspaceID string `query:"workspace_id"`
	ProjectID   string `query:"project_id"`
	Scope       string `query:"scope"`
	Tags        string `query:"tags"` // comma-separated
	Limit       string `query:"limit"`
}

// projectKnowledgeResponse is returned by GetProjectKnowledge.
type projectKnowledgeResponse struct {
	WorkspaceMemories []domain.Memory `json:"workspace_memories"`
	ProjectMemories   []domain.Memory `json:"project_memories"`
	TotalCount        int             `json:"total_count"`
}

// Remember handles POST /api/v1/memories
// Creates or updates a memory entry.
func (h *MemoryHandler) Remember(c echo.Context) error {
	var req rememberRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.WorkspaceID == uuid.Nil {
		// Fall back to workspace_id from auth context.
		if wsIDVal := c.Get("workspace_id"); wsIDVal != nil {
			if wsID, ok := wsIDVal.(uuid.UUID); ok {
				req.WorkspaceID = wsID
			}
		}
	}

	// Determine actor from context.
	var agentID *uuid.UUID
	if agentIDVal := c.Get("agent_id"); agentIDVal != nil {
		if aid, ok := agentIDVal.(uuid.UUID); ok {
			agentID = &aid
		}
	}

	// Default scope to workspace when not provided.
	if req.Scope == "" {
		req.Scope = domain.ScopeWorkspace
	}

	// Determine source type from actor.
	sourceType := domain.SourceHuman
	if agentID != nil {
		sourceType = domain.SourceAgent
	}

	mem := &domain.Memory{
		WorkspaceID: req.WorkspaceID,
		ProjectID:   req.ProjectID,
		AgentID:     agentID,
		Key:         req.Key,
		Content:     req.Content,
		Scope:       req.Scope,
		SourceType:  sourceType,
	}
	if len(req.Tags) > 0 {
		mem.Tags = req.Tags
	}

	outcome, err := h.memoryService.Remember(c.Request().Context(), mem)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, rememberResponse{
		Memory:  mem,
		Outcome: outcome,
	})
}

// List handles GET /api/v1/memories
// Returns memories filtered by workspace, project, and scope.
func (h *MemoryHandler) List(c echo.Context) error {
	var q listMemoriesQuery
	if err := c.Bind(&q); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid query parameters"))
	}

	wsID, err := requireWorkspaceID(c, q.WorkspaceID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, err)
	}

	var projID *uuid.UUID
	if q.ProjectID != "" {
		pid, parseErr := uuid.Parse(q.ProjectID)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
		}
		projID = &pid
	}

	limit := 50
	if q.Limit != "" {
		if l, parseErr := strconv.Atoi(q.Limit); parseErr == nil && l > 0 {
			limit = l
		}
	}

	opts := domain.RecallOpts{
		Query:       "", // empty query — use scope-based listing
		WorkspaceID: wsID,
		Scope:       domain.MemoryScope(q.Scope),
		Limit:       limit,
	}
	if projID != nil {
		opts.ProjectID = *projID
	}

	// Use FindByScope via service-level GetProjectKnowledge for plain listing.
	memories, err := h.memoryService.GetProjectKnowledge(c.Request().Context(), wsID, projID)
	if err != nil {
		return handleError(c, err)
	}

	// Apply scope filter client-side when specified (FindByScope is used server-side in search).
	filtered := memories
	if opts.Scope != "" {
		filtered = make([]domain.Memory, 0, len(memories))
		for _, m := range memories {
			if m.Scope == opts.Scope {
				filtered = append(filtered, m)
			}
		}
	}

	// Apply limit.
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"items": filtered,
		"total": len(filtered),
	})
}

// Search handles GET /api/v1/memories/search
// Full-text search across memories.
func (h *MemoryHandler) Search(c echo.Context) error {
	var q searchMemoriesQuery
	if err := c.Bind(&q); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid query parameters"))
	}

	if q.Q == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"q": "search query is required",
		}))
	}

	wsID, err := requireWorkspaceID(c, q.WorkspaceID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, err)
	}

	var projID uuid.UUID
	if q.ProjectID != "" {
		pid, parseErr := uuid.Parse(q.ProjectID)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
		}
		projID = pid
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

	limit := 20
	if q.Limit != "" {
		if l, parseErr := strconv.Atoi(q.Limit); parseErr == nil && l > 0 {
			limit = l
		}
	}

	opts := domain.RecallOpts{
		Query:       q.Q,
		WorkspaceID: wsID,
		ProjectID:   projID,
		Scope:       domain.MemoryScope(q.Scope),
		Tags:        tags,
		Limit:       limit,
	}

	results, err := h.memoryService.Recall(c.Request().Context(), opts)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"items": results,
	})
}

// GetByID handles GET /api/v1/memories/:id
func (h *MemoryHandler) GetByID(c echo.Context) error {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid memory id"))
	}

	mem, err := h.memoryService.GetByID(c.Request().Context(), id)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, mem)
}

// Delete handles DELETE /api/v1/memories/:id
func (h *MemoryHandler) Delete(c echo.Context) error {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid memory id"))
	}

	// Determine actor and admin status from context.
	var agentID *uuid.UUID
	isAdmin := false

	if agentIDVal := c.Get("agent_id"); agentIDVal != nil {
		if aid, ok := agentIDVal.(uuid.UUID); ok {
			agentID = &aid
		}
	}
	if roleVal := c.Get("role"); roleVal != nil {
		if role, ok := roleVal.(string); ok {
			isAdmin = role == "owner" || role == "admin"
		}
	}
	// User callers (non-agents) are treated as admins for memory deletion.
	if agentID == nil {
		if userIDVal := c.Get("user_id"); userIDVal != nil {
			isAdmin = true
		}
	}

	if err := h.memoryService.Forget(c.Request().Context(), id, agentID, isAdmin); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// GetProjectKnowledge handles GET /api/v1/projects/:proj_id/knowledge
// Returns all workspace-level and project-level memories for a project.
func (h *MemoryHandler) GetProjectKnowledge(c echo.Context) error {
	projIDStr := c.Param("proj_id")
	projID, err := uuid.Parse(projIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	// Resolve workspace_id from context.
	wsID, wsErr := requireWorkspaceID(c, "")
	if wsErr != nil {
		return c.JSON(http.StatusBadRequest, wsErr)
	}

	// Fetch project-scoped memories.
	projectMemories, err := h.memoryService.GetProjectKnowledge(c.Request().Context(), wsID, &projID)
	if err != nil {
		return handleError(c, err)
	}

	// Fetch workspace-scoped memories (no project filter).
	workspaceMemories, err := h.memoryService.GetProjectKnowledge(c.Request().Context(), wsID, nil)
	if err != nil {
		return handleError(c, err)
	}

	// Filter workspace-only memories (scope=workspace) to avoid duplication.
	var wsOnly []domain.Memory
	for _, m := range workspaceMemories {
		if m.Scope == domain.ScopeWorkspace {
			wsOnly = append(wsOnly, m)
		}
	}

	return c.JSON(http.StatusOK, projectKnowledgeResponse{
		WorkspaceMemories: wsOnly,
		ProjectMemories:   projectMemories,
		TotalCount:        len(wsOnly) + len(projectMemories),
	})
}

// ExportMemories handles GET /api/v1/memories/export
// Returns a YAML file containing all memories for the given workspace (optionally filtered by project).
func (h *MemoryHandler) ExportMemories(c echo.Context) error {
	wsIDStr := c.QueryParam("workspace_id")
	wsID, err := requireWorkspaceID(c, wsIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, err)
	}

	var projID *uuid.UUID
	if pidStr := c.QueryParam("project_id"); pidStr != "" {
		pid, parseErr := uuid.Parse(pidStr)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
		}
		projID = &pid
	}

	data, err := h.memoryService.ExportMemories(c.Request().Context(), wsID, projID)
	if err != nil {
		return handleError(c, err)
	}

	c.Response().Header().Set("Content-Disposition", "attachment; filename=memories.yaml")
	return c.Blob(http.StatusOK, "application/x-yaml", data)
}

// ImportMemories handles POST /api/v1/memories/import
// Accepts a YAML body matching the export format and upserts each memory.
func (h *MemoryHandler) ImportMemories(c echo.Context) error {
	wsIDStr := c.QueryParam("workspace_id")
	wsID, err := requireWorkspaceID(c, wsIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, err)
	}

	body := c.Request().Body
	if body == nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("request body is required"))
	}
	defer func() { _ = body.Close() }()

	// Read the raw YAML body (limit to 10 MB to prevent abuse).
	const maxSize = 10 * 1024 * 1024
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, readErr := body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if readErr != nil {
			break
		}
		if len(buf) > maxSize {
			return c.JSON(http.StatusRequestEntityTooLarge, apierror.BadRequest("request body too large"))
		}
	}

	count, err := h.memoryService.ImportMemories(c.Request().Context(), wsID, buf)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]int{"imported": count})
}

// Reindex handles POST /api/v1/memories/reindex
// Triggers batch embedding for all memories without an embedding vector.
func (h *MemoryHandler) Reindex(c echo.Context) error {
	wsIDStr := c.QueryParam("workspace_id")
	wsID, err := requireWorkspaceID(c, wsIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, err)
	}

	count, err := h.memoryService.BatchEmbed(c.Request().Context(), wsID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]int{"reindexed": count})
}

// FindRelated handles GET /api/v1/memories/:id/related
// Returns memories related to the given memory ID via full-text search.
func (h *MemoryHandler) FindRelated(c echo.Context) error {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid memory id"))
	}

	limit := 5
	if limitStr := c.QueryParam("limit"); limitStr != "" {
		if l, parseErr := strconv.Atoi(limitStr); parseErr == nil && l > 0 {
			limit = l
		}
	}

	results, err := h.memoryService.FindRelated(c.Request().Context(), id, limit)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"items": results,
	})
}

// requireWorkspaceID extracts workspace_id either from the provided raw string
// or from the Echo context (set by DualAuth middleware).
// Returns a non-nil error when neither source yields a valid UUID.
func requireWorkspaceID(c echo.Context, raw string) (uuid.UUID, error) {
	if raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return uuid.Nil, apierror.BadRequest("invalid workspace_id")
		}
		return id, nil
	}
	if wsIDVal := c.Get("workspace_id"); wsIDVal != nil {
		if wsID, ok := wsIDVal.(uuid.UUID); ok {
			return wsID, nil
		}
	}
	return uuid.Nil, apierror.BadRequest("workspace_id is required")
}
