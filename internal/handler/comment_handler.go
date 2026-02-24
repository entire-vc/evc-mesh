package handler

import (
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// CommentHandler handles HTTP requests for comment management.
type CommentHandler struct {
	commentService service.CommentService
}

// NewCommentHandler creates a new CommentHandler with the given service.
func NewCommentHandler(cs service.CommentService) *CommentHandler {
	return &CommentHandler{commentService: cs}
}

// createCommentRequest represents the JSON body for creating a comment.
type createCommentRequest struct {
	Body            string     `json:"body"`
	ParentCommentID *uuid.UUID `json:"parent_comment_id"`
	IsInternal      bool       `json:"is_internal"`
}

// updateCommentRequest represents the JSON body for updating a comment.
type updateCommentRequest struct {
	Body *string `json:"body"`
}

// List handles GET /tasks/:task_id/comments
func (h *CommentHandler) List(c echo.Context) error {
	taskIDStr := c.Param("task_id")
	taskID, err := uuid.Parse(taskIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}

	var pg pagination.Params
	if err := c.Bind(&pg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination parameters"))
	}
	pg.Normalize()

	filter := repository.CommentFilter{}
	if v := c.QueryParam("include_internal"); v != "" {
		b, err := strconv.ParseBool(v)
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
	taskIDStr := c.Param("task_id")
	taskID, err := uuid.Parse(taskIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
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
