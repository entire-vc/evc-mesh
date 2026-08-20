package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// DocumentCommentHandler handles HTTP requests for comments anchored to a
// document's text.
type DocumentCommentHandler struct {
	commentService service.DocumentCommentService
}

// NewDocumentCommentHandler creates a new DocumentCommentHandler.
func NewDocumentCommentHandler(cs service.DocumentCommentService) *DocumentCommentHandler {
	return &DocumentCommentHandler{commentService: cs}
}

// documentCommentAnchorBody is the JSON shape of an anchor on the wire.
//
// It mirrors domain.DocumentCommentAnchor's stored fields and deliberately does
// not accept `orphaned`: that is computed from whether the offsets are present,
// and letting a client assert it separately is how the flag and the offsets end
// up disagreeing.
type documentCommentAnchorBody struct {
	Exact  string `json:"exact"`
	Prefix string `json:"prefix"`
	Suffix string `json:"suffix"`
	Start  *int   `json:"start"`
	End    *int   `json:"end"`
}

func (a *documentCommentAnchorBody) toDomain() *domain.DocumentCommentAnchor {
	if a == nil {
		return nil
	}
	// Built directly rather than through domain.NewDocumentCommentAnchor: the
	// constructor drops a quoteless anchor to nil, and the service has to be able
	// to tell "no anchor was sent" from "an anchor was sent with fields missing"
	// in order to answer 400 on the second.
	return &domain.DocumentCommentAnchor{
		Exact:  a.Exact,
		Prefix: a.Prefix,
		Suffix: a.Suffix,
		Start:  a.Start,
		End:    a.End,
	}
}

// createDocumentCommentRequest is the JSON body for creating a comment. The
// document is named by the route, so it is deliberately absent here: an id in the
// body is one the workspace guard cannot see (see declaredBodyTenantFields).
//
// There are two ways to say what the comment is about, and they are for two
// different callers. `anchor` carries offsets and is for a client that measured a
// selection — the editor. `quote` carries the text and asks the server to find
// it, and is for a client with no selection to measure — an agent. Sending both
// is refused: offsets from a caller that also asks where the text is are two
// answers to one question, and the server would have to pick one silently on
// every write.
//
// The extension is additive on purpose. The editor goes on sending `anchor` and
// is unaffected; nothing here changes what an existing client has to send.
type createDocumentCommentRequest struct {
	Body            string                     `json:"body"`
	ParentCommentID *uuid.UUID                 `json:"parent_comment_id"`
	Anchor          *documentCommentAnchorBody `json:"anchor"`

	// Quote is the text this comment is about, verbatim as it reads in the
	// document. The server locates it and builds the anchor.
	Quote string `json:"quote"`

	// QuotePrefix and QuoteSuffix narrow a quote that occurs more than once: the
	// text immediately before and after the one meant. Without them a repeated
	// quote is refused rather than guessed at.
	QuotePrefix string `json:"quote_prefix"`
	QuoteSuffix string `json:"quote_suffix"`
}

// updateDocumentCommentRequest is the JSON body for editing a comment. Only the
// body: see UpdateDocumentCommentInput for why the anchor is not editable.
type updateDocumentCommentRequest struct {
	Body string `json:"body"`
}

// List handles GET /documents/:doc_id/comments
//
// Resolved threads are excluded unless ?include_resolved=true — a resolved thread
// is one somebody deliberately put away, and the reading view is the default.
func (h *DocumentCommentHandler) List(c echo.Context) error {
	docID, wsID, apiErr := documentScope(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}

	var pg pagination.Params
	if err := c.Bind(&pg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination parameters"))
	}
	pg.Normalize()

	filter := repository.DocumentCommentFilter{
		IncludeResolved: c.QueryParam("include_resolved") == "true",
	}

	page, err := h.commentService.ListByDocument(c.Request().Context(), docID, wsID, filter, pg)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, page)
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
		DocumentID:      docID,
		WorkspaceID:     wsID,
		ParentCommentID: req.ParentCommentID,
		Body:            req.Body,
		Anchor:          req.Anchor.toDomain(),
		Quote:           req.Quote,
		QuotePrefix:     req.QuotePrefix,
		QuoteSuffix:     req.QuoteSuffix,
		AuthorID:        callerID,
		AuthorType:      callerType,
	})
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, comment)
}

// Update handles PATCH /document-comments/:dcom_id — edit your own comment.
func (h *DocumentCommentHandler) Update(c echo.Context) error {
	commentID, wsID, apiErr := documentCommentScope(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}

	var req updateDocumentCommentRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	callerID, callerType := callerActor(c)

	comment, err := h.commentService.Update(c.Request().Context(), commentID, wsID, service.UpdateDocumentCommentInput{
		Body:       req.Body,
		EditorID:   callerID,
		EditorType: callerType,
	})
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, comment)
}

// Resolve handles POST /document-comments/:dcom_id/resolve
func (h *DocumentCommentHandler) Resolve(c echo.Context) error {
	return h.setResolved(c, true)
}

// Unresolve handles POST /document-comments/:dcom_id/unresolve
func (h *DocumentCommentHandler) Unresolve(c echo.Context) error {
	return h.setResolved(c, false)
}

// setResolved is the shared body of Resolve and Unresolve.
//
// Two routes rather than one taking a `resolved` boolean: the desired state is
// then in the URL, so a retried or replayed request lands in the same place, and
// neither endpoint can be invoked by accident with a field the caller forgot to
// set.
func (h *DocumentCommentHandler) setResolved(c echo.Context, resolved bool) error {
	commentID, wsID, apiErr := documentCommentScope(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}

	callerID, callerType := callerActor(c)

	comment, err := h.commentService.SetResolved(c.Request().Context(), commentID, wsID, service.ResolveDocumentCommentInput{
		Resolved:  resolved,
		ActorID:   callerID,
		ActorType: callerType,
	})
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, comment)
}

// Delete handles DELETE /document-comments/:dcom_id — soft delete.
func (h *DocumentCommentHandler) Delete(c echo.Context) error {
	commentID, wsID, apiErr := documentCommentScope(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}

	callerID, callerType := callerActor(c)

	if err := h.commentService.Delete(c.Request().Context(), commentID, wsID, callerID, callerType); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// documentCommentScope parses :dcom_id and reads the caller's workspace — the
// same pairing documentScope does for :doc_id and attachmentScope for :att_id.
//
// The parameter is :dcom_id rather than :comment_id because resolvers in
// workspaceParamResolvers are keyed on the parameter name alone: :comment_id
// already reads the `comments` table, so reusing it here would send document
// comment ids to the task comment lookup and refuse every legitimate request.
// TestScopedParamNamesAreUnambiguous holds that apart.
//
// It returns an *apierror.Error rather than the echo response, because c.JSON
// returns nil on success — a refusal handed back as a plain `error` would read as
// "no error" at the call site and let the request through.
func documentCommentScope(c echo.Context) (commentID, wsID uuid.UUID, apiErr *apierror.Error) {
	commentID, parseErr := uuid.Parse(c.Param("dcom_id"))
	if parseErr != nil {
		return uuid.Nil, uuid.Nil, apierror.BadRequest("invalid dcom_id")
	}

	wsID, wsErr := mw.GetWorkspaceID(c)
	if wsErr != nil {
		return uuid.Nil, uuid.Nil, apierror.Forbidden("workspace access denied")
	}

	return commentID, wsID, nil
}
