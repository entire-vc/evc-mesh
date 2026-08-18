package handler

import (
	"encoding/json"
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
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// CommentHandler handles HTTP requests for comment management.
type CommentHandler struct {
	commentService service.CommentService
	taskSvc        taskIDResolver
}

// NewCommentHandler creates a new CommentHandler with the given service.
func NewCommentHandler(cs service.CommentService, ts taskIDResolver) *CommentHandler {
	return &CommentHandler{commentService: cs, taskSvc: ts}
}

// createCommentRequest represents the JSON body for creating a comment.
//
// Metadata is a caller-supplied JSON object stored verbatim on the comment. It exists so
// automation can label the comments it posts (`{"source":"pr-task-driver","auto":true}`)
// and so staleness/stall detectors can tell an auto-nudge from genuine activity. Until
// task #13e391d2 this field was absent from the struct, so `c.Bind` discarded it and the
// API answered 201 while losing it — every consumer filtering on `metadata.source` was
// filtering on a value that could never be set. The shape is validated (see
// validateCommentMetadata); it is never silently dropped.
type createCommentRequest struct {
	Body            string          `json:"body"`
	ParentCommentID *uuid.UUID      `json:"parent_comment_id"`
	IsInternal      bool            `json:"is_internal"`
	Metadata        json.RawMessage `json:"metadata"`
}

// updateCommentRequest represents the JSON body for updating a comment.
type updateCommentRequest struct {
	Body *string `json:"body"`
}

// List handles GET /tasks/:task_id/comments
func (h *CommentHandler) List(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskSvc)
	if err != nil {
		return handleError(c, err)
	}

	var pg pagination.Params
	if err = c.Bind(&pg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination parameters"))
	}
	pg.Normalize()

	filter := repository.CommentFilter{}
	if v := c.QueryParam("include_internal"); v != "" {
		var b bool
		b, err = strconv.ParseBool(v)
		if err == nil {
			filter.IncludeInternal = b
		}
	}

	page, err := h.commentService.ListByTask(c.Request().Context(), taskID, filter, pg)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, page)
}

// Create handles POST /tasks/:task_id/comments
func (h *CommentHandler) Create(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskSvc)
	if err != nil {
		return handleError(c, err)
	}

	var req createCommentRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.Body == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"body": "body is required",
		}))
	}

	// Determine author from context.
	var authorID uuid.UUID
	var authorType domain.ActorType

	if agentIDVal := c.Get("agent_id"); agentIDVal != nil {
		if aid, ok := agentIDVal.(uuid.UUID); ok {
			authorID = aid
			authorType = domain.ActorTypeAgent
		}
	} else if userIDVal := c.Get("user_id"); userIDVal != nil {
		if uid, ok := userIDVal.(uuid.UUID); ok {
			authorID = uid
			authorType = domain.ActorTypeUser
		}
	}

	comment := &domain.Comment{
		ID:              uuid.New(),
		TaskID:          taskID,
		ParentCommentID: req.ParentCommentID,
		AuthorID:        authorID,
		AuthorType:      authorType,
		Body:            req.Body,
		IsInternal:      req.IsInternal,
		Metadata:        req.Metadata,
	}

	if err := h.commentService.Create(c.Request().Context(), comment); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, comment)
}

// Update handles PATCH /comments/:comment_id
func (h *CommentHandler) Update(c echo.Context) error {
	commentIDStr := c.Param("comment_id")
	commentID, err := uuid.Parse(commentIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid comment_id"))
	}

	var req updateCommentRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	// Build a comment with the ID and updated body.
	comment := &domain.Comment{
		ID: commentID,
	}

	if req.Body != nil {
		comment.Body = *req.Body
	}

	if err := h.commentService.Update(c.Request().Context(), comment); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, comment)
}

// Delete handles DELETE /comments/:comment_id
func (h *CommentHandler) Delete(c echo.Context) error {
	commentIDStr := c.Param("comment_id")
	commentID, err := uuid.Parse(commentIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid comment_id"))
	}

	if err := h.commentService.Delete(c.Request().Context(), commentID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// GetMyComments handles GET /me/comments — returns the caller's own comments, newest first.
func (h *CommentHandler) GetMyComments(c echo.Context) error {
	actorID, _ := actorctx.FromContext(c.Request().Context())
	if actorID == uuid.Nil {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("authentication required"))
	}

	filter := repository.CommentViewFilter{Limit: 50}

	if v := c.QueryParam("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{"limit": "must be 1-200"}))
		}
		filter.Limit = n
	}
	if v := c.QueryParam("before"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{"before": "must be RFC3339 timestamp"}))
		}
		filter.Before = &t
	}
	if v := c.QueryParam("before_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{"before_id": "must be a UUID"}))
		}
		filter.BeforeID = &id
	}
	if v := c.QueryParam("workspace_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{"workspace_id": "must be a UUID"}))
		}
		filter.WorkspaceID = &id
	}
	if v := c.QueryParam("project_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{"project_id": "must be a UUID"}))
		}
		filter.ProjectID = &id
	}

	page, err := h.commentService.ListByAuthor(c.Request().Context(), actorID, filter)
	if err != nil {
		return handleError(c, err)
	}

	c.Response().Header().Set("Cache-Control", "private, max-age=30")
	return c.JSON(http.StatusOK, page)
}

// GetRecentByWorkspace handles GET /workspaces/:ws_id/comments/recent — workspace-wide recent comments.
func (h *CommentHandler) GetRecentByWorkspace(c echo.Context) error {
	wsIDStr := c.Param("ws_id")
	wsID, err := uuid.Parse(wsIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	filter := repository.CommentViewFilter{Limit: 50}

	if v := c.QueryParam("limit"); v != "" {
		n, parseErr := strconv.Atoi(v)
		if parseErr != nil || n < 1 || n > 200 {
			return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{"limit": "must be 1-200"}))
		}
		filter.Limit = n
	}
	if v := c.QueryParam("before"); v != "" {
		t, parseErr := time.Parse(time.RFC3339, v)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{"before": "must be RFC3339 timestamp"}))
		}
		filter.Before = &t
	}
	if v := c.QueryParam("before_id"); v != "" {
		id, parseErr := uuid.Parse(v)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{"before_id": "must be a UUID"}))
		}
		filter.BeforeID = &id
	}
	if v := c.QueryParam("include_internal"); v != "" {
		if b, parseErr := strconv.ParseBool(v); parseErr == nil {
			filter.IncludeInternal = b
		}
	}

	page, err := h.commentService.ListRecentByWorkspace(c.Request().Context(), wsID, filter)
	if err != nil {
		return handleError(c, err)
	}

	c.Response().Header().Set("Cache-Control", "private, max-age=30")
	return c.JSON(http.StatusOK, page)
}
