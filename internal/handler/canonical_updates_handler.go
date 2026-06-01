package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// CanonicalUpdatesHandler serves GET /api/v1/canonical_updates.
// It returns canonical-decision memory records that are tagged privacy:public and
// targeted at the calling agent (via propagate_to:all or propagate_to:<slug>).
type CanonicalUpdatesHandler struct {
	memoryService service.MemoryService
	sessionRepo   repository.AgentSessionRepository
	agentService  service.AgentService
}

// NewCanonicalUpdatesHandler creates a CanonicalUpdatesHandler.
func NewCanonicalUpdatesHandler(
	ms service.MemoryService,
	sr repository.AgentSessionRepository,
	as service.AgentService,
) *CanonicalUpdatesHandler {
	return &CanonicalUpdatesHandler{
		memoryService: ms,
		sessionRepo:   sr,
		agentService:  as,
	}
}

// canonicalUpdateItem is a single item in the response updates array.
type canonicalUpdateItem struct {
	ID      uuid.UUID  `json:"id"`
	Kind    string     `json:"kind"`
	Source  string     `json:"source"`
	Scope   *uuid.UUID `json:"scope,omitempty"`
	Ts      time.Time  `json:"ts"`
	Summary string     `json:"summary"`
	Text    string     `json:"text"`
	Affects []string   `json:"affects"`
	Ref     string     `json:"ref"`
}

// canonicalUpdatesResponse is the top-level JSON response.
type canonicalUpdatesResponse struct {
	Since   string                `json:"since"`
	Now     string                `json:"now"`
	Count   int                   `json:"count"`
	Updates []canonicalUpdateItem `json:"updates"`
}

// GetCanonicalUpdates handles GET /api/v1/canonical_updates
//
// Query params:
//
//	since  — optional RFC3339. Defaults to started_at of the most recent ended session,
//	          or 24 h ago when no prior session exists.
//	agent  — optional agent slug. Used to filter propagate_to:<slug> tags.
//	          When omitted only the propagate_to:all tag is checked (no slug filter added).
//	scope  — optional UUID (project_id). Non-UUID non-empty values return 400.
//
// Privacy is enforced at query level: only records tagged privacy:public are returned.
// Propagation: records must have propagate_to:all OR propagate_to:<agent_slug>.
// Kind filter: records must have kind:canonical-decision.
func (h *CanonicalUpdatesHandler) GetCanonicalUpdates(c echo.Context) error {
	ctx := c.Request().Context()
	now := time.Now().UTC()

	// --- Auth context ---
	agentID, err := middleware.GetAgentID(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("agent authentication required"))
	}
	workspaceID, err := middleware.GetWorkspaceID(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("workspace_id not found in context"))
	}

	// --- Parse ?since ---
	var since time.Time
	sinceStr := c.QueryParam("since")
	if sinceStr != "" {
		t, parseErr := time.Parse(time.RFC3339, sinceStr)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid since: must be RFC3339"))
		}
		since = t
	} else {
		// Default: started_at of the most recent non-active session; fallback 24h ago.
		prevStarted, lookupErr := h.sessionRepo.GetPreviousStartedAt(ctx, agentID)
		if lookupErr != nil {
			return c.JSON(http.StatusInternalServerError, apierror.InternalError("failed to look up previous session"))
		}
		if prevStarted != nil {
			since = *prevStarted
		} else {
			since = now.Add(-24 * time.Hour)
		}
	}

	// --- Parse ?scope (project_id UUID or empty) ---
	var projectID *uuid.UUID
	scopeStr := c.QueryParam("scope")
	if scopeStr != "" {
		pid, parseErr := uuid.Parse(scopeStr)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid scope: must be a UUID"))
		}
		projectID = &pid
	}

	// --- Parse ?agent slug (optional) ---
	//
	// If agent param is given we resolve it to get the slug for propagate_to filter.
	// If agent param is empty we skip adding propagate_to:<slug> — only propagate_to:all
	// is used. This is intentional: we don't have the calling agent's slug in context,
	// only its UUID. Callers that want targeted filtering should pass ?agent=<their-slug>.
	var agentSlug string
	agentParam := c.QueryParam("agent")
	if agentParam != "" {
		ag, lookupErr := h.agentService.GetBySlug(ctx, workspaceID, agentParam)
		if lookupErr != nil {
			return c.JSON(http.StatusInternalServerError, apierror.InternalError("failed to look up agent"))
		}
		if ag == nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("agent not found"))
		}
		agentSlug = ag.Slug
	}

	// --- Build filter ---
	//
	// Privacy MUST be enforced at query level (Tags AND filter):
	//   privacy:public    — hard requirement
	//   kind:canonical-decision — hard requirement
	//
	// Propagation filter uses TagsAny (OR):
	//   propagate_to:all
	//   propagate_to:<agent_slug>   (only when slug is known)
	//
	// Note: Tags (AND) and TagsAny (OR) are combined by the repository as:
	//   all Tags conditions must be met AND at least one TagsAny condition must be met.
	filter := domain.MemoryListFilter{
		WorkspaceID: workspaceID,
		ProjectID:   projectID,
		// AND-required tags: privacy gate + kind gate (applied at SQL level)
		Tags:    []string{"privacy:public", "kind:canonical-decision"},
		Since:   &since,
		OrderBy: "created_at:desc",
		Limit:   500,
	}

	// Propagation OR filter.
	tagsAny := []string{"propagate_to:all"}
	if agentSlug != "" {
		tagsAny = append(tagsAny, "propagate_to:"+agentSlug)
	}
	filter.TagsAny = tagsAny

	result, err := h.memoryService.ListMemories(ctx, filter)
	if err != nil {
		return handleError(c, err)
	}

	// --- Build response items ---
	updates := make([]canonicalUpdateItem, 0, len(result.Items))
	for _, sm := range result.Items {
		m := sm.Memory
		item := canonicalUpdateItem{
			ID:      m.ID,
			Kind:    "canonical-decision",
			Ts:      m.CreatedAt,
			Summary: m.Key,
			Text:    m.Content,
			Scope:   m.ProjectID,
			Affects: []string{},
		}

		// Derive source, affects, ref from tags.
		for _, tag := range m.Tags {
			switch {
			case strings.HasPrefix(tag, "source:"):
				item.Source = strings.TrimPrefix(tag, "source:")
			case strings.HasPrefix(tag, "ref:"):
				item.Ref = strings.TrimPrefix(tag, "ref:")
			case strings.HasPrefix(tag, "affects:"):
				v := strings.TrimPrefix(tag, "affects:")
				if v != "" {
					item.Affects = append(item.Affects, v)
				}
			case strings.HasPrefix(tag, "propagate_to:"):
				v := strings.TrimPrefix(tag, "propagate_to:")
				if v != "" && v != "all" {
					item.Affects = append(item.Affects, v)
				}
			}
		}

		updates = append(updates, item)
	}

	resp := canonicalUpdatesResponse{
		Since:   since.Format(time.RFC3339),
		Now:     now.Format(time.RFC3339),
		Count:   len(updates),
		Updates: updates,
	}
	return c.JSON(http.StatusOK, resp)
}
