package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// MentionHandler handles GET/POST requests for the /me/mentions resource.
type MentionHandler struct {
	mentionService service.MentionService
}

// NewMentionHandler returns a new MentionHandler.
func NewMentionHandler(ms service.MentionService) *MentionHandler {
	return &MentionHandler{mentionService: ms}
}

// List returns paginated @-mention records for the caller.
// Query params: workspace_id (required UUID), seen (bool), since (RFC3339),
// project_id (UUID), limit (int, max 100).
func (h *MentionHandler) List(c echo.Context) error {
	actorID, actorType := actorctx.FromContext(c.Request().Context())
	if actorID == uuid.Nil {
		return apierror.Unauthorized("authentication required")
	}

	// Shared with the document-mention inbox — see parseMentionFilter.
	filter, err := parseMentionFilter(c)
	if err != nil {
		return err
	}

	filter.Viewer = authorFeedViewer(c, actorID, actorType)
	kind := mentionKind(actorType)
	views, err := h.mentionService.List(c.Request().Context(), actorID, kind, filter)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, views)
}

// MarkSeen marks a single mention as seen for the caller (204 No Content).
func (h *MentionHandler) MarkSeen(c echo.Context) error {
	actorID, _ := actorctx.FromContext(c.Request().Context())
	if actorID == uuid.Nil {
		return apierror.Unauthorized("authentication required")
	}

	commentID, err := uuid.Parse(c.Param("comment_id"))
	if err != nil {
		return apierror.ValidationError(map[string]string{"comment_id": "must be a UUID"})
	}

	if err := h.mentionService.MarkSeen(c.Request().Context(), commentID, actorID); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// UnseenCount returns the number of unseen mentions for the caller, scoped to
// one workspace (required, same as List — see parseMentionFilter).
// Response: {"count": N}  Cache-Control: private, no-cache
func (h *MentionHandler) UnseenCount(c echo.Context) error {
	actorID, actorType := actorctx.FromContext(c.Request().Context())
	if actorID == uuid.Nil {
		return apierror.Unauthorized("authentication required")
	}

	workspaceID, err := parseRequiredWorkspaceID(c)
	if err != nil {
		return err
	}

	kind := mentionKind(actorType)
	count, err := h.mentionService.CountUnseen(c.Request().Context(), actorID, kind, workspaceID, authorFeedViewer(c, actorID, actorType))
	if err != nil {
		return err
	}

	// no-cache, not max-age: the badge and the mentions_unseen tile re-read
	// this count on each live mention frame and after POST .../seen; a
	// max-age answer let the browser serve the old count for its whole TTL.
	c.Response().Header().Set("Cache-Control", unseenCountCacheControl)
	return c.JSON(http.StatusOK, map[string]int64{"count": count})
}

// unseenCountCacheControl is shared by both unseen_count handlers (task and
// document mentions): the client sums them, so one stale side is enough to
// show a wrong total.
const unseenCountCacheControl = "private, no-cache"

func mentionKind(t domain.ActorType) string {
	if t == domain.ActorTypeAgent {
		return "agent"
	}
	return "user"
}
