package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// DocumentAttachmentHandler handles HTTP requests for the files uploaded into a
// document.
type DocumentAttachmentHandler struct {
	attachmentService service.DocumentAttachmentService
}

// NewDocumentAttachmentHandler creates a new DocumentAttachmentHandler.
func NewDocumentAttachmentHandler(as service.DocumentAttachmentService) *DocumentAttachmentHandler {
	return &DocumentAttachmentHandler{attachmentService: as}
}

// List handles GET /documents/:doc_id/attachments
func (h *DocumentAttachmentHandler) List(c echo.Context) error {
	docID, wsID, apiErr := documentScope(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}

	var pg pagination.Params
	if err := c.Bind(&pg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination parameters"))
	}
	pg.Normalize()

	page, err := h.attachmentService.ListByDocument(c.Request().Context(), docID, wsID, pg)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, page)
}

// Upload handles POST /documents/:doc_id/attachments (multipart form).
//
// `file` is required. `name` is optional and overrides the uploaded filename —
// the browser sends whatever the OS called it, and the editor knows the name the
// user actually meant.
func (h *DocumentAttachmentHandler) Upload(c echo.Context) error {
	docID, wsID, apiErr := documentScope(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("file is required"))
	}

	file, err := fileHeader.Open()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierror.InternalError("failed to open uploaded file"))
	}
	defer func() { _ = file.Close() }()

	name := c.FormValue("name")
	if name == "" {
		name = fileHeader.Filename
	}

	callerID, callerType := callerActor(c)

	att, err := h.attachmentService.Upload(c.Request().Context(), service.UploadDocumentAttachmentInput{
		DocumentID:  docID,
		WorkspaceID: wsID,
		Name:        name,
		// The browser's Content-Type is preferred and the extension is the
		// fallback: a generic application/octet-stream would make an image
		// download rather than render, however the disposition is set.
		MimeType:       inferMimeType(fileHeader.Header.Get("Content-Type"), fileHeader.Filename),
		Size:           fileHeader.Size,
		Reader:         file,
		UploadedBy:     callerID,
		UploadedByType: callerType,
	})
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, att)
}

// Download handles GET /document-attachments/:att_id/download.
//
// It answers with the presigned URL rather than the bytes, matching the artifact
// download endpoint exactly: the frontend resolver that turns a stored markdown
// path into a live <img src> is shared between the two, so a different response
// shape here would need a second resolver.
func (h *DocumentAttachmentHandler) Download(c echo.Context) error {
	attID, wsID, apiErr := attachmentScope(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}

	inline := c.QueryParam("disposition") == "inline"
	url, err := h.attachmentService.GetDownloadURL(c.Request().Context(), attID, wsID, inline)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]string{"url": url})
}

// Delete handles DELETE /document-attachments/:att_id — soft delete.
func (h *DocumentAttachmentHandler) Delete(c echo.Context) error {
	attID, wsID, apiErr := attachmentScope(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}

	if err := h.attachmentService.Delete(c.Request().Context(), attID, wsID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// attachmentScope parses :att_id and reads the caller's workspace, the same
// pairing documentScope does for :doc_id: the workspace narrows every lookup to
// the caller's own tenant, which is defense-in-depth behind wsAccess rather than a
// substitute for it.
//
// It returns an *apierror.Error rather than the echo response, because c.JSON
// returns nil on success — a refusal handed back as a plain `error` would read as
// "no error" at the call site and let the request through.
func attachmentScope(c echo.Context) (attID, wsID uuid.UUID, apiErr *apierror.Error) {
	attID, parseErr := uuid.Parse(c.Param("att_id"))
	if parseErr != nil {
		return uuid.Nil, uuid.Nil, apierror.BadRequest("invalid att_id")
	}

	wsID, wsErr := mw.GetWorkspaceID(c)
	if wsErr != nil {
		return uuid.Nil, uuid.Nil, apierror.Forbidden("workspace access denied")
	}

	return attID, wsID, nil
}
