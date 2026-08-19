package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// DocumentHandler handles HTTP requests for project documents.
type DocumentHandler struct {
	documentService service.DocumentService
}

// NewDocumentHandler creates a new DocumentHandler with the given service.
func NewDocumentHandler(ds service.DocumentService) *DocumentHandler {
	return &DocumentHandler{documentService: ds}
}

// createDocumentRequest is the JSON body for creating a document. The project is
// named by the route, so it is deliberately absent here: an id in the body is one
// the workspace guard cannot see (see declaredBodyTenantFields).
type createDocumentRequest struct {
	Title    string     `json:"title"`
	Slug     string     `json:"slug"`
	ParentID *uuid.UUID `json:"parent_id"`
	Position int        `json:"position"`
	Body     string     `json:"body"`
}

// updateDocumentRequest is the JSON body for partially updating a document.
type updateDocumentRequest struct {
	Title       *string    `json:"title"`
	ParentID    *uuid.UUID `json:"parent_id"`
	ClearParent bool       `json:"clear_parent"`
	Position    *int       `json:"position"`
	Body        *string    `json:"body"`

	// BaseVersion is the version the caller read before editing. Required — the
	// service refuses an update without one, rather than writing it
	// unconditionally. A pointer so an omitted field is distinguishable from a
	// sent zero; binding it into an int64 would turn "I forgot" into "version 0"
	// and the refusal into a confusing conflict.
	BaseVersion *int64 `json:"base_version"`
}

// appendDocumentRequest is the JSON body for adding text to the end of a
// document. No base_version: an append is unconditional by design — see
// service.DocumentService.AppendBody.
type appendDocumentRequest struct {
	Text string `json:"text"`
}

// documentVersionConflictResponse is the 409 body for a write built on a stale
// version.
//
// It carries the version the document is actually at, which is the thing the
// caller needs and cannot get from the status code. Without it the only way
// forward from a 409 is another GET, and a client that has to re-read anyway
// tends to re-read and then write unconditionally, which is the behaviour this
// whole change exists to remove.
type documentVersionConflictResponse struct {
	Code string `json:"code"`
	// Message is what a user-facing client is expected to show verbatim; the
	// numbers below are for the client's own logic.
	Message        string `json:"message"`
	BaseVersion    int64  `json:"base_version"`
	CurrentVersion int64  `json:"current_version"`
}

// List handles GET /projects/:proj_id/documents
func (h *DocumentHandler) List(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	var pg pagination.Params
	if err = c.Bind(&pg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination parameters"))
	}
	pg.Normalize()

	page, err := h.documentService.ListByProject(c.Request().Context(), projID, pg)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, page)
}

// Search handles GET /projects/:proj_id/documents/search?q=&limit=
//
// The project is named by the PATH, not by a query parameter, and that is not a
// style choice: a tenant-shaped value in the query string pulls this route into
// the declared-query-tenant grammar, where every verdict has to be spelled a
// particular way. In the path it costs one resolver and the guard already
// understands it.
func (h *DocumentHandler) Search(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	wsID, wsErr := mw.GetWorkspaceID(c)
	if wsErr != nil {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
	}

	limit := 0
	if raw := c.QueryParam("limit"); raw != "" {
		parsed, convErr := strconv.Atoi(raw)
		if convErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid limit"))
		}
		limit = parsed
	}

	hits, err := h.documentService.Search(c.Request().Context(), projID, wsID, c.QueryParam("q"), limit)
	if err != nil {
		return handleError(c, err)
	}

	// `items`, matching every other list this API returns, so a caller does not
	// need a second shape for this one.
	return c.JSON(http.StatusOK, map[string]any{"items": hits})
}

// Create handles POST /projects/:proj_id/documents
func (h *DocumentHandler) Create(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	var req createDocumentRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	callerID, callerType := callerActor(c)

	doc, err := h.documentService.Create(c.Request().Context(), service.CreateDocumentInput{
		ProjectID:     projID,
		ParentID:      req.ParentID,
		Slug:          req.Slug,
		Title:         req.Title,
		Body:          req.Body,
		Position:      req.Position,
		CreatedBy:     callerID,
		CreatedByType: callerType,
	})
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, doc)
}

// GetByID handles GET /documents/:doc_id — metadata plus the markdown body.
func (h *DocumentHandler) GetByID(c echo.Context) error {
	docID, wsID, apiErr := documentScope(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}

	doc, err := h.documentService.GetByIDInWorkspace(c.Request().Context(), docID, wsID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, doc)
}

// Update handles PATCH /documents/:doc_id
func (h *DocumentHandler) Update(c echo.Context) error {
	docID, wsID, apiErr := documentScope(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}

	var req updateDocumentRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	callerID, callerType := callerActor(c)

	doc, err := h.documentService.Update(c.Request().Context(), docID, wsID, service.UpdateDocumentInput{
		Title:       req.Title,
		ParentID:    req.ParentID,
		ClearParent: req.ClearParent,
		Position:    req.Position,
		Body:        req.Body,
		BaseVersion: req.BaseVersion,
		// The editor is read from the request, never bound from the body: an
		// updated_by a caller could choose is a byline anyone can forge.
		UpdatedBy:     callerID,
		UpdatedByType: callerType,
	})
	if err != nil {
		return documentError(c, err)
	}

	return c.JSON(http.StatusOK, doc)
}

// Append handles POST /documents/:doc_id/append — add text to the end of the
// body.
//
// Its own route rather than a field on PATCH, because it is its own contract:
// PATCH requires a base_version and this must not, and folding them together
// would mean one endpoint whose validation rules depend on which fields are
// present. Separate routes say which one a caller asked for.
func (h *DocumentHandler) Append(c echo.Context) error {
	docID, wsID, apiErr := documentScope(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}

	var req appendDocumentRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	callerID, callerType := callerActor(c)

	doc, err := h.documentService.AppendBody(c.Request().Context(), docID, wsID, service.AppendDocumentInput{
		Text:          req.Text,
		UpdatedBy:     callerID,
		UpdatedByType: callerType,
	})
	if err != nil {
		return documentError(c, err)
	}

	return c.JSON(http.StatusOK, doc)
}

// documentError is handleError plus the one refusal that needs a body of its
// own: a stale-version write, answered 409 with the version the document is
// actually at.
//
// Kept here rather than added as another branch inside handleError because it is
// document-specific, and handleError is the shared funnel every handler in the
// package routes through.
func documentError(c echo.Context, err error) error {
	var conflict *service.DocumentVersionConflictError
	if errors.As(err, &conflict) {
		return c.JSON(http.StatusConflict, documentVersionConflictResponse{
			Code:           "document_version_conflict",
			Message:        "This page changed since you opened it. Reload it to see the current version before saving again.",
			BaseVersion:    conflict.BaseVersion,
			CurrentVersion: conflict.CurrentVersion,
		})
	}
	return handleError(c, err)
}

// Delete handles DELETE /documents/:doc_id — soft delete.
func (h *DocumentHandler) Delete(c echo.Context) error {
	docID, wsID, apiErr := documentScope(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}

	callerID, callerType := callerActor(c)

	if err := h.documentService.Delete(c.Request().Context(), docID, wsID, callerID, callerType); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// documentScope parses :doc_id and reads the caller's workspace.
//
// Every /documents/:doc_id route pairs the two: the workspace narrows the lookup
// to the caller's own tenant, which is defense-in-depth behind wsAccess rather
// than a substitute for it.
//
// It returns an *apierror.Error rather than the echo response, because c.JSON
// returns nil on success — a refusal handed back as a plain `error` would read as
// "no error" at the call site and let the request through.
func documentScope(c echo.Context) (docID, wsID uuid.UUID, apiErr *apierror.Error) {
	docID, parseErr := uuid.Parse(c.Param("doc_id"))
	if parseErr != nil {
		return uuid.Nil, uuid.Nil, apierror.BadRequest("invalid doc_id")
	}

	wsID, wsErr := mw.GetWorkspaceID(c)
	if wsErr != nil {
		return uuid.Nil, uuid.Nil, apierror.Forbidden("workspace access denied")
	}

	return docID, wsID, nil
}

// callerActor returns who is making the request and which kind of actor they are.
// It is callerUUID plus the type, which the tables that record an author
// (documents, comments) need alongside the id.
func callerActor(c echo.Context) (uuid.UUID, domain.ActorType) {
	if rawID := c.Get(mw.ContextKeyAgentID); rawID != nil {
		if id, ok := rawID.(uuid.UUID); ok {
			return id, domain.ActorTypeAgent
		}
	}
	if rawID := c.Get(mw.ContextKeyUserID); rawID != nil {
		if id, ok := rawID.(uuid.UUID); ok {
			return id, domain.ActorTypeUser
		}
	}
	if id, at := actorctx.FromContext(c.Request().Context()); id != uuid.Nil && at != "" {
		return id, at
	}
	return uuid.Nil, domain.ActorTypeSystem
}
