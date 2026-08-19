package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// DocumentCommentHandler handles HTTP requests for comments anchored inside a
// document.
type DocumentCommentHandler struct {
	commentService service.DocumentCommentService
}

// NewDocumentCommentHandler creates a new DocumentCommentHandler.
func NewDocumentCommentHandler(cs service.DocumentCommentService) *DocumentCommentHandler {
	return &DocumentCommentHandler{commentService: cs}
}

// createDocumentCommentRequest is the JSON body for adding a comment. The
// document is named by the route, so it is deliberately absent here: an id in the
// body is one the workspace guard cannot see (see declaredBodyTenantFields).
type createDocumentCommentRequest struct {
	Body     string                 `json:"body"`
	ParentID *uuid.UUID             `json:"parent_comment_id"`
	Anchor   *domain.DocumentAnchor `json:"anchor"`
}

// updateDocumentCommentRequest carries the two things about a comment that can
// change, and they are separate verbs on purpose.
//
// Body is an edit by the author; Resolved is a statement about the conversation
// that anyone may make. Merging them into one nullable-field PATCH would make
// "resolve someone else's thread" and "rewrite someone else's words" the same
// request, and the second must be refused while the first is allowed.
type updateDocumentCommentRequest struct {
	Body     *string `json:"body"`
	Resolved *bool   `json:"resolved"`
}

// List handles GET /documents/:doc_id/comments
//
// Unpaginated, unlike every other list endpoint: the client needs the whole tree
// to render any of it, and a page boundary through a comment thread would hand
// back replies whose roots are on the next page.
func (h *DocumentCommentHandler) List(c echo.Context) error {
	docID, wsID, apiErr := documentScope(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}

	comments, err := h.commentService.ListByDocument(c.Request().Context(), docID, wsID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]any{"items": comments})
}

// Create handles POST /documents/:doc_id/comments
func (h *DocumentCommentHandler) Create(c echo.Context) error {
	docID, wsID, apiErr := documentScope(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}

	var req createDocumentCommentRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	callerID, callerType := callerActor(c)

	comment, err := h.commentService.Create(c.Request().Context(), service.CreateDocumentCommentInput{
		DocumentID:  docID,
		WorkspaceID: wsID,
		Body:        req.Body,
		ParentID:    req.ParentID,
		Anchor:      req.Anchor,
		AuthorID:    callerID,
		AuthorType:  callerType,
	})
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, comment)
}

// Update handles PATCH /document-comments/:dc_id — edit the body, resolve or
// unresolve the thread.
func (h *DocumentCommentHandler) Update(c echo.Context) error {
	commentID, wsID, apiErr := documentCommentScope(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}

	var req updateDocumentCommentRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	if req.Body == nil && req.Resolved == nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("nothing to update: send body, resolved, or both"))
	}

	callerID, callerType := callerActor(c)
	ctx := c.Request().Context()

	var (
		comment *domain.DocumentComment
		err     error
	)
	// Body first: an edit is refused for a non-author, and doing it first means a
	// request carrying both fields is refused whole rather than half-applied.
	if req.Body != nil {
		comment, err = h.commentService.UpdateBody(ctx, commentID, wsID, *req.Body, callerID)
		if err != nil {
			return handleError(c, err)
		}
	}
	if req.Resolved != nil {
		comment, err = h.commentService.SetResolved(ctx, commentID, wsID, *req.Resolved, callerID, callerType)
		if err != nil {
			return handleError(c, err)
		}
	}

	return c.JSON(http.StatusOK, comment)
}

// Delete handles DELETE /document-comments/:dc_id — soft delete of the comment
// and every reply beneath it.
func (h *DocumentCommentHandler) Delete(c echo.Context) error {
	commentID, wsID, apiErr := documentCommentScope(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}

	callerID, _ := callerActor(c)
	if err := h.commentService.Delete(c.Request().Context(), commentID, wsID, callerID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// documentCommentScope parses :dc_id and reads the caller's workspace — the same
// pairing documentScope and attachmentScope do, and for the same reason: the
// workspace narrows every lookup to the caller's own tenant, as defense in depth
// behind wsAccess rather than a substitute for it.
//
// It returns an *apierror.Error rather than the echo response, because c.JSON
// returns nil on success — a refusal handed back as a plain `error` would read as
// "no error" at the call site and let the request through.
func documentCommentScope(c echo.Context) (commentID, wsID uuid.UUID, apiErr *apierror.Error) {
	commentID, parseErr := uuid.Parse(c.Param("dc_id"))
	if parseErr != nil {
		return uuid.Nil, uuid.Nil, apierror.BadRequest("invalid dc_id")
	}

	wsID, wsErr := mw.GetWorkspaceID(c)
	if wsErr != nil {
		return uuid.Nil, uuid.Nil, apierror.Forbidden("workspace access denied")
	}

	return commentID, wsID, nil
}
