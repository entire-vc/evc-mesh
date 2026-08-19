package handler

import (
	"net/http"
	"strconv"
	"strings"

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

// TOC handles GET /documents/:doc_id/toc — the heading outline of the document.
//
// Separate from GET /documents/:doc_id rather than a field on it: the outline is
// what a caller reads INSTEAD of the body, and returning both would defeat the
// point. It is recomputed from the body on every request and cached nowhere.
func (h *DocumentHandler) TOC(c echo.Context) error {
	docID, wsID, apiErr := documentScope(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}

	toc, err := h.documentService.TOC(c.Request().Context(), docID, wsID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, toc)
}

// Section handles GET /documents/:doc_id/section?ref=<anchor|heading/path>.
//
// ref is an anchor from the table of contents (`linux-1`) or a path of heading
// slugs (`установка/linux`). A ref matching several headings is a 400 listing
// the anchors that resolve it — never a first match dressed up as an answer.
func (h *DocumentHandler) Section(c echo.Context) error {
	docID, wsID, apiErr := documentScope(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}

	section, err := h.documentService.Section(c.Request().Context(), docID, wsID, c.QueryParam("ref"))
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, section)
}

// GetByPath handles GET /projects/:proj_id/documents/by-path?path=a/b/c
// with an optional &section=<ref>.
//
// The section parameter is what makes this worth one call instead of three: an
// agent that knows the path and the heading it wants gets that heading, rather
// than resolving a path, then an id, then a section, downloading the whole body
// on the way.
//
// The two shapes are deliberate and documented: without `section` this returns
// the document exactly as GET /documents/:doc_id does; with it, the section
// envelope. It is the same trade the route exists to make — say what you want and
// carry only that.
func (h *DocumentHandler) GetByPath(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	wsID, wsErr := mw.GetWorkspaceID(c)
	if wsErr != nil {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
	}

	doc, err := h.documentService.GetByPathInWorkspace(c.Request().Context(), projID, wsID, c.QueryParam("path"))
	if err != nil {
		return handleError(c, err)
	}

	ref := c.QueryParam("section")
	if strings.TrimSpace(ref) == "" {
		return c.JSON(http.StatusOK, doc)
	}

	section, err := h.documentService.Section(c.Request().Context(), doc.ID, wsID, ref)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, section)
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
		// The editor is read from the request, never bound from the body: an
		// updated_by a caller could choose is a byline anyone can forge.
		UpdatedBy:     callerID,
		UpdatedByType: callerType,
	})
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, doc)
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
