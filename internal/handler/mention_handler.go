package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
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
// Query params: seen (bool), since (RFC3339), project_id (UUID), limit (int, max 100).
func (h *MentionHandler) List(c echo.Context) error {
	actorID, actorType := actorctx.FromContext(c.Request().Context())
	if actorID == uuid.Nil {
		return apierror.Unauthorized("authentication required")
	}

	filter := repository.MentionFilter{Limit: 50}

	if v := c.QueryParam("seen"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return apierror.ValidationError(map[string]string{"seen": "must be true or false"})
		}
		filter.Seen = &b
	}
	if v := c.QueryParam("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return apierror.ValidationError(map[string]string{"since": "must be RFC3339 timestamp"})
		}
		filter.Since = &t
	}
	if v := c.QueryParam("project_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			return apierror.ValidationError(map[string]string{"project_id": "must be a UUID"})
		}
		filter.ProjectID = &id
	}
	if v := c.QueryParam("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return apierror.ValidationError(map[string]string{"limit": "must be 1-100"})
		}
		filter.Limit = n
	}

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

// UnseenCount returns the number of unseen mentions for the caller.
// Response: {"count": N}  Cache-Control: max-age=10
func (h *MentionHandler) UnseenCount(c echo.Context) error {
	actorID, actorType := actorctx.FromContext(c.Request().Context())
	if actorID == uuid.Nil {
		return apierror.Unauthorized("authentication required")
	}

	kind := mentionKind(actorType)
	count, err := h.mentionService.CountUnseen(c.Request().Context(), actorID, kind)
	if err != nil {
		return err
	}

	c.Response().Header().Set("Cache-Control", "max-age=10")
	return c.JSON(http.StatusOK, map[string]int64{"count": count})
}

func mentionKind(t domain.ActorType) string {
	if t == domain.ActorTypeAgent {
		return "agent"
	}
	return "user"
}
