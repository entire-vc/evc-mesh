package handler

import (
	"net/http"
	"strconv"
	"strings"
	"time"

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
	WorkspaceID  uuid.UUID          `json:"workspace_id"`
	ProjectID    *uuid.UUID         `json:"project_id,omitempty"`
	Key          string             `json:"key"`
	Content      string             `json:"content"`
	Scope        domain.MemoryScope `json:"scope"`
	Tags         []string           `json:"tags,omitempty"`
	Relevance    *float32           `json:"relevance,omitempty"`
	ExpiresAt    *string            `json:"expires_at,omitempty"` // RFC3339 string or Go duration
	SourceURL    *string            `json:"source_url,omitempty"`
	SourceTaskID *uuid.UUID         `json:"source_task_id,omitempty"` // Mesh task that produced this memory (Amendment 2/3)
	ThreadID     *string            `json:"thread_id,omitempty"`      // explicit thread override; auto-derived from source task when omitted
}

// rememberResponse wraps the upserted memory with the operation outcome.
type rememberResponse struct {
	Memory     *domain.Memory `json:"memory"`
	Outcome    string         `json:"outcome"`               // "created" or "updated"
	NearDupKey string         `json:"near_dup_key,omitempty"` // non-empty when a near-duplicate was detected
}

// listMemoriesQuery represents query params for listing memories.
type listMemoriesQuery struct {
	WorkspaceID       string `query:"workspace_id"`
	ProjectID         string `query:"project_id"`
	Scope             string `query:"scope"`
	Tags              string `query:"tags"`     // comma-separated, AND filter
	TagsAny           string `query:"tags_any"` // comma-separated, OR filter
	CreatedBy         string `query:"created_by"`
	SourceType        string `query:"source_type"`         // agent|human|system
	Since             string `query:"since"`               // RFC3339
	Until             string `query:"until"`               // RFC3339
	RelevanceMin      string `query:"relevance_min"`       // float
	MinImportance     string `query:"min_importance"`      // float; default 0.4 applied at service layer
	ApplyRecencyDecay string `query:"apply_recency_decay"` // bool
	OrderBy           string `query:"order_by"`
	IncludeExpired    string `query:"include_expired"`  // bool
	IncludeArchived   string `query:"include_archived"` // bool; default false — archived entries hidden
	Limit             string `query:"limit"`
	Offset            string `query:"offset"`
}

// searchMemoriesQuery represents query params for searching memories.
type searchMemoriesQuery struct {
	Q                 string `query:"q"`
	WorkspaceID       string `query:"workspace_id"`
	ProjectID         string `query:"project_id"`
	Scope             string `query:"scope"`
	Tags              string `query:"tags"`     // comma-separated, AND filter
	TagsAny           string `query:"tags_any"` // comma-separated, OR filter
	CreatedBy         string `query:"created_by"`
	SourceType        string `query:"source_type"` // agent|human|system
	Since             string `query:"since"`
	Until             string `query:"until"`
	RelevanceMin      string `query:"relevance_min"`
	MinImportance     string `query:"min_importance"` // float; default 0.4 applied at service layer
	ApplyRecencyDecay string `query:"apply_recency_decay"`
	OrderBy           string `query:"order_by"`
	IncludeExpired    string `query:"include_expired"`
	Limit             string `query:"limit"`
	Offset            string `query:"offset"`
}

// setProjectKnowledgeRequest is the JSON body for writing a project knowledge entry.
type setProjectKnowledgeRequest struct {
	Key        string   `json:"key"`
	Value      string   `json:"value"`
	Category   string   `json:"category,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	SourceType string   `json:"source_type,omitempty"`
	SourceURL  *string  `json:"source_url,omitempty"`
}

// projectKnowledgeResponse is returned by GetProjectKnowledge.
type projectKnowledgeResponse struct {
	WorkspaceMemories []domain.Memory `json:"workspace_memories"`
	ProjectMemories   []domain.Memory `json:"project_memories"`
	TotalCount        int             `json:"total_count"`
	WorkspaceTotal    int64           `json:"workspace_total"`
	HasMore           bool            `json:"has_more"`
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
		WorkspaceID:  req.WorkspaceID,
		ProjectID:    req.ProjectID,
		AgentID:      agentID,
		Key:          req.Key,
		Content:      req.Content,
		Scope:        req.Scope,
		SourceType:   sourceType,
		SourceURL:    req.SourceURL,
		SourceTaskID: req.SourceTaskID,
		ThreadID:     req.ThreadID,
	}
	if len(req.Tags) > 0 {
		mem.Tags = req.Tags
	}
	if req.Relevance != nil {
		mem.Relevance = *req.Relevance
	}

	// Parse optional expires_at: accept RFC3339 timestamp or Go duration string.
	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		if t, parseErr := time.Parse(time.RFC3339, *req.ExpiresAt); parseErr == nil {
			mem.ExpiresAt = &t
		} else {
			// Try as a Go duration (e.g. "72h").
			if d, durErr := time.ParseDuration(*req.ExpiresAt); durErr == nil {
				t2 := time.Now().Add(d)
				mem.ExpiresAt = &t2
			} else {
				return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid expires_at: must be RFC3339 timestamp or Go duration"))
			}
		}
	}

	result, err := h.memoryService.Remember(c.Request().Context(), mem)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, rememberResponse{
		Memory:     mem,
		Outcome:    result.Outcome,
		NearDupKey: result.NearDupKey,
	})
}

// List handles GET /api/v1/memories
// Returns memories filtered by workspace, project, scope, tags, dates, and relevance.
// Supports pagination via limit/offset.
func (h *MemoryHandler) List(c echo.Context) error {
	var q listMemoriesQuery
	if err := c.Bind(&q); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid query parameters"))
	}

	wsID, err := requireWorkspaceID(c, q.WorkspaceID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, err)
	}

	filter := domain.MemoryListFilter{
		WorkspaceID: wsID,
		Scope:       q.Scope,
		OrderBy:     q.OrderBy,
	}

	if q.ProjectID != "" {
		pid, parseErr := uuid.Parse(q.ProjectID)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
		}
		filter.ProjectID = &pid
	}
	if q.CreatedBy != "" {
		cid, parseErr := uuid.Parse(q.CreatedBy)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid created_by"))
		}
		filter.CreatedBy = &cid
	}
	if q.SourceType != "" {
		st := domain.MemorySourceType(q.SourceType)
		if !st.IsValid() {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid source_type: must be agent, human, or system"))
		}
		filter.SourceType = st
	}
	if q.Since != "" {
		t, parseErr := time.Parse(time.RFC3339, q.Since)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid since: must be RFC3339"))
		}
		filter.Since = &t
	}
	if q.Until != "" {
		t, parseErr := time.Parse(time.RFC3339, q.Until)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid until: must be RFC3339"))
		}
		filter.Until = &t
	}
	if q.RelevanceMin != "" {
		f, parseErr := strconv.ParseFloat(q.RelevanceMin, 32)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid relevance_min"))
		}
		rf := float32(f)
		filter.RelevanceMin = &rf
	}
	if q.MinImportance != "" {
		f, parseErr := strconv.ParseFloat(q.MinImportance, 32)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid min_importance"))
		}
		mf := float32(f)
		filter.MinImportance = &mf
	}
	if q.Tags != "" {
		for _, t := range strings.Split(q.Tags, ",") {
			if t = strings.TrimSpace(t); t != "" {
				filter.Tags = append(filter.Tags, t)
			}
		}
	}
	if q.TagsAny != "" {
		for _, t := range strings.Split(q.TagsAny, ",") {
			if t = strings.TrimSpace(t); t != "" {
				filter.TagsAny = append(filter.TagsAny, t)
			}
		}
	}
	filter.IncludeExpired = q.IncludeExpired == "true" || q.IncludeExpired == "1"
	filter.IncludeArchived = q.IncludeArchived == "true" || q.IncludeArchived == "1"
	filter.ApplyDecay = q.ApplyRecencyDecay == "true" || q.ApplyRecencyDecay == "1"
	if filter.ApplyDecay && filter.OrderBy == "" {
		filter.OrderBy = "decayed_relevance:desc"
	}

	filter.Limit = 20
	if q.Limit != "" {
		if l, parseErr := strconv.Atoi(q.Limit); parseErr == nil && l > 0 {
			filter.Limit = l
		}
	}
	if q.Offset != "" {
		if o, parseErr := strconv.Atoi(q.Offset); parseErr == nil && o >= 0 {
			filter.Offset = o
		}
	}

	result, err := h.memoryService.ListMemories(c.Request().Context(), filter)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"items":         result.Items,
		"total":         result.Total,
		"limit":         result.Limit,
		"offset":        result.Offset,
		"decay_applied": result.DecayApplied,
	})
}

// Search handles GET /api/v1/memories/search
// Full-text search across memories with optional extended filters.
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
			if t = strings.TrimSpace(t); t != "" {
				tags = append(tags, t)
			}
		}
	}
	var tagsAny []string
	if q.TagsAny != "" {
		for _, t := range strings.Split(q.TagsAny, ",") {
			if t = strings.TrimSpace(t); t != "" {
				tagsAny = append(tagsAny, t)
			}
		}
	}

	limit := 20
	if q.Limit != "" {
		if l, parseErr := strconv.Atoi(q.Limit); parseErr == nil && l > 0 {
			limit = l
		}
	}
	offset := 0
	if q.Offset != "" {
		if o, parseErr := strconv.Atoi(q.Offset); parseErr == nil && o >= 0 {
			offset = o
		}
	}

	opts := domain.RecallOpts{
		Query:          q.Q,
		WorkspaceID:    wsID,
		ProjectID:      projID,
		Scope:          domain.MemoryScope(q.Scope),
		Tags:           tags,
		TagsAny:        tagsAny,
		IncludeExpired: q.IncludeExpired == "true" || q.IncludeExpired == "1",
		OrderBy:        q.OrderBy,
		ApplyDecay:     q.ApplyRecencyDecay == "true" || q.ApplyRecencyDecay == "1",
		Limit:          limit,
		Offset:         offset,
	}

	if q.CreatedBy != "" {
		cid, parseErr := uuid.Parse(q.CreatedBy)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid created_by"))
		}
		opts.CreatedBy = &cid
	}
	if q.SourceType != "" {
		st := domain.MemorySourceType(q.SourceType)
		if !st.IsValid() {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid source_type: must be agent, human, or system"))
		}
		opts.SourceType = st
	}
	if q.Since != "" {
		t, parseErr := time.Parse(time.RFC3339, q.Since)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid since: must be RFC3339"))
		}
		opts.Since = &t
	}
	if q.Until != "" {
		t, parseErr := time.Parse(time.RFC3339, q.Until)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid until: must be RFC3339"))
		}
		opts.Until = &t
	}
	if q.RelevanceMin != "" {
		f, parseErr := strconv.ParseFloat(q.RelevanceMin, 32)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid relevance_min"))
		}
		rf := float32(f)
		opts.RelevanceMin = &rf
	}
	if q.MinImportance != "" {
		f, parseErr := strconv.ParseFloat(q.MinImportance, 32)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid min_importance"))
		}
		mf := float32(f)
		opts.MinImportance = &mf
	}

	results, err := h.memoryService.Recall(c.Request().Context(), opts)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"items":  results,
		"total":  len(results),
		"limit":  limit,
		"offset": offset,
	})
}

// SetProjectKnowledge handles POST /api/v1/projects/:proj_id/knowledge
// Upserts a project-scoped knowledge entry by key.
func (h *MemoryHandler) SetProjectKnowledge(c echo.Context) error {
	projIDStr := c.Param("proj_id")
	projID, err := uuid.Parse(projIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	wsID, wsErr := requireWorkspaceID(c, "")
	if wsErr != nil {
		return c.JSON(http.StatusBadRequest, wsErr)
	}

	var req setProjectKnowledgeRequest
	if bindErr := c.Bind(&req); bindErr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	// Determine actor from context.
	var agentID *uuid.UUID
	if agentIDVal := c.Get("agent_id"); agentIDVal != nil {
		if aid, ok := agentIDVal.(uuid.UUID); ok {
			agentID = &aid
		}
	}

	input := service.SetProjectKnowledgeInput{
		WorkspaceID: wsID,
		ProjectID:   projID,
		AgentID:     agentID,
		Key:         req.Key,
		Value:       req.Value,
		Category:    req.Category,
		Tags:        req.Tags,
		SourceType:  req.SourceType,
		SourceURL:   req.SourceURL,
	}

	mem, outcome, err := h.memoryService.SetProjectKnowledge(c.Request().Context(), input)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, rememberResponse{
		Memory:  mem,
		Outcome: outcome,
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
// Returns workspace-level (paginated) and project-level memories for a project.
//
// Query params:
//   - limit int (default 100, max 500) — applied to both tiers
//   - offset int (default 0)
//   - min_importance float64 (default 0) — workspace-tier filter only
//   - tags_any string comma-separated — workspace-tier OR tag filter only
func (h *MemoryHandler) GetProjectKnowledge(c echo.Context) error {
	projIDStr := c.Param("proj_id")
	projID, err := uuid.Parse(projIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	wsID, wsErr := requireWorkspaceID(c, "")
	if wsErr != nil {
		return c.JSON(http.StatusBadRequest, wsErr)
	}

	// Parse pagination query params for the workspace tier.
	limit := 100
	if raw := c.QueryParam("limit"); raw != "" {
		if v, parseErr := strconv.Atoi(raw); parseErr == nil && v > 0 && v <= 500 {
			limit = v
		}
	}
	offset := 0
	if raw := c.QueryParam("offset"); raw != "" {
		if v, parseErr := strconv.Atoi(raw); parseErr == nil && v >= 0 {
			offset = v
		}
	}
	var minImportance *float32
	if raw := c.QueryParam("min_importance"); raw != "" {
		if v, parseErr := strconv.ParseFloat(raw, 32); parseErr == nil {
			f := float32(v)
			minImportance = &f
		}
	}
	var tagsAny []string
	if raw := c.QueryParam("tags_any"); raw != "" {
		for _, t := range strings.Split(raw, ",") {
			if t = strings.TrimSpace(t); t != "" {
				tagsAny = append(tagsAny, t)
			}
		}
	}

	wsFilter := domain.MemoryListFilter{
		Limit:         limit,
		Offset:        offset,
		MinImportance: minImportance,
		TagsAny:       tagsAny,
	}

	// Project-scoped memories — same limit as workspace tier, no importance filter (they are curated).
	projFilter := domain.MemoryListFilter{Limit: limit, Offset: offset}
	projectMemories, projTotal, err := h.memoryService.GetProjectKnowledge(c.Request().Context(), wsID, &projID, projFilter)
	if err != nil {
		return handleError(c, err)
	}

	// Workspace-tier memories — paginated with full filter.
	allWS, wsTotal, err := h.memoryService.GetProjectKnowledge(c.Request().Context(), wsID, nil, wsFilter)
	if err != nil {
		return handleError(c, err)
	}

	// Retain only workspace-scoped entries to avoid duplicating project memories.
	var wsOnly []domain.Memory
	for _, m := range allWS {
		if m.Scope == domain.ScopeWorkspace {
			wsOnly = append(wsOnly, m)
		}
	}

	return c.JSON(http.StatusOK, projectKnowledgeResponse{
		WorkspaceMemories: wsOnly,
		ProjectMemories:   projectMemories,
		TotalCount:        len(wsOnly) + len(projectMemories),
		WorkspaceTotal:    wsTotal,
		HasMore:           wsTotal > int64(offset+limit) || projTotal > int64(offset+limit),
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

// recallGraphQuery represents query params for GET /memories/recall_graph.
type recallGraphQuery struct {
	Q               string `query:"query"`
	WorkspaceID     string `query:"workspace_id"`
	ProjectID       string `query:"project_id"`
	Hops            string `query:"hops"`             // default 2, max 5
	WeightThreshold string `query:"weight_threshold"` // default 0.3
	TaskID          string `query:"task_id"`          // optional; used as cache key discriminator
}

// RecallGraph handles GET /api/v1/memories/recall_graph
// Multi-hop knowledge-graph traversal: seeds from hybrid recall, then BFS-expands
// along memory_edges. Returns ranked results with composite scores and provenance.
func (h *MemoryHandler) RecallGraph(c echo.Context) error {
	var q recallGraphQuery
	if err := c.Bind(&q); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid query parameters"))
	}

	if q.Q == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"query": "query is required",
		}))
	}

	wsID, err := requireWorkspaceID(c, q.WorkspaceID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, err)
	}

	opts := domain.RecallGraphOpts{
		Query:           q.Q,
		WorkspaceID:     wsID,
		Hops:            2,
		WeightThreshold: 0.3,
	}

	if q.ProjectID != "" {
		pid, parseErr := uuid.Parse(q.ProjectID)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
		}
		opts.ProjectID = &pid
	}
	if q.Hops != "" {
		if h2, parseErr := strconv.Atoi(q.Hops); parseErr == nil && h2 > 0 {
			opts.Hops = h2
		}
	}
	if q.WeightThreshold != "" {
		if wt, parseErr := strconv.ParseFloat(q.WeightThreshold, 64); parseErr == nil && wt > 0 {
			opts.WeightThreshold = wt
		}
	}
	if q.TaskID != "" {
		tid, parseErr := uuid.Parse(q.TaskID)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
		}
		opts.TaskID = &tid
	}

	results, err := h.memoryService.RecallGraph(c.Request().Context(), opts)
	if err != nil {
		return handleError(c, err)
	}

	if results == nil {
		results = []domain.RecallGraphResult{}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"items": results,
		"total": len(results),
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
