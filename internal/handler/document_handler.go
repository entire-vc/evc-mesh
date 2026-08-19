package handler

import (
	"net/http"

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

	doc, err := h.documentService.Update(c.Request().Context(), docID, wsID, service.UpdateDocumentInput{
		Title:       req.Title,
		ParentID:    req.ParentID,
		ClearParent: req.ClearParent,
		Position:    req.Position,
		Body:        req.Body,
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

	if err := h.documentService.Delete(c.Request().Context(), docID, wsID); err != nil {
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
