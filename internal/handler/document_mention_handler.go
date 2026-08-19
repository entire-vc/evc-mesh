package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// DocumentMentionHandler serves /me/document-mentions — the caller's @-mentions
// inside document comments.
//
// A sibling of MentionHandler rather than more methods on it: the two read
// different tables and return different views, and one handler holding both
// services would make "which inbox is this route" a matter of reading the method
// body.
type DocumentMentionHandler struct {
	mentionService service.DocumentMentionService
}

// NewDocumentMentionHandler returns a new DocumentMentionHandler.
func NewDocumentMentionHandler(ms service.DocumentMentionService) *DocumentMentionHandler {
	return &DocumentMentionHandler{mentionService: ms}
}

// List returns the caller's document-comment mentions.
// Query params: seen (bool), since (RFC3339), project_id (UUID), limit (1-100).
func (h *DocumentMentionHandler) List(c echo.Context) error {
	actorID, actorType := actorctx.FromContext(c.Request().Context())
	if actorID == uuid.Nil {
		return apierror.Unauthorized("authentication required")
	}

	filter, err := parseMentionFilter(c)
	if err != nil {
		return err
	}

	views, err := h.mentionService.List(c.Request().Context(), actorID, mentionKind(actorType), filter)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, views)
}

// MarkSeen marks one document-comment mention as seen for the caller.
//
// The comment is named by :dcom_id, the same parameter the document-comment
// object routes use, so middleware.RequireWorkspaceMemberScoped resolves it to a
// workspace and refuses a stranger's id before the handler runs. The row that
// gets written is additionally keyed on the caller's own id, so even inside the
// right workspace a caller can only mark their own mention.
func (h *DocumentMentionHandler) MarkSeen(c echo.Context) error {
	actorID, _ := actorctx.FromContext(c.Request().Context())
	if actorID == uuid.Nil {
		return apierror.Unauthorized("authentication required")
	}

	commentID, err := uuid.Parse(c.Param("dcom_id"))
	if err != nil {
		return apierror.ValidationError(map[string]string{"dcom_id": "must be a UUID"})
	}

	if err := h.mentionService.MarkSeen(c.Request().Context(), commentID, actorID); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// UnseenCount returns the number of unseen document mentions for the caller.
func (h *DocumentMentionHandler) UnseenCount(c echo.Context) error {
	actorID, actorType := actorctx.FromContext(c.Request().Context())
	if actorID == uuid.Nil {
		return apierror.Unauthorized("authentication required")
	}

	count, err := h.mentionService.CountUnseen(c.Request().Context(), actorID, mentionKind(actorType))
	if err != nil {
		return err
	}

	c.Response().Header().Set("Cache-Control", "max-age=10")
	return c.JSON(http.StatusOK, map[string]int64{"count": count})
}

// parseMentionFilter reads the filter query parameters shared by both mention
// inboxes.
//
// Shared rather than copied because the two endpoints are the same query over
// two tables: a limit accepted by one and rejected by the other would be a
// difference nobody chose.
func parseMentionFilter(c echo.Context) (repository.MentionFilter, error) {
	filter := repository.MentionFilter{Limit: 50}

	if v := c.QueryParam("seen"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return filter, apierror.ValidationError(map[string]string{"seen": "must be true or false"})
		}
		filter.Seen = &b
	}
	if v := c.QueryParam("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return filter, apierror.ValidationError(map[string]string{"since": "must be RFC3339 timestamp"})
		}
		filter.Since = &t
	}
	if v := c.QueryParam("project_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			return filter, apierror.ValidationError(map[string]string{"project_id": "must be a UUID"})
		}
		filter.ProjectID = &id
	}
	if v := c.QueryParam("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return filter, apierror.ValidationError(map[string]string{"limit": "must be 1-100"})
		}
		filter.Limit = n
	}

	return filter, nil
}
