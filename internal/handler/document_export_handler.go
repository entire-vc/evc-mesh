package handler

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// DocumentExportHandler handles HTTP requests for exporting a document,
// optionally with its live subtree, into a downloadable file.
type DocumentExportHandler struct {
	exportService service.DocumentExportService
}

// NewDocumentExportHandler creates a new DocumentExportHandler.
func NewDocumentExportHandler(es service.DocumentExportService) *DocumentExportHandler {
	return &DocumentExportHandler{exportService: es}
}

// Export handles GET /documents/:doc_id/export?format=md|pdf|docx&scope=self|tree.
//
// docID and the workspace both come from documentScope, the same helper every
// other /documents/:doc_id route uses — this endpoint is not a special path
// that reads the tenant differently from GetByID or Outline. The actual
// tenancy decision happens underneath, in DocumentService.WalkExportTree
// (scope=tree) or GetByIDInWorkspace (scope=self); this handler's own job
// ends at parsing the two query params and turning the result into an HTTP
// response.
func (h *DocumentExportHandler) Export(c echo.Context) error {
	docID, wsID, apiErr := documentScope(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}

	format := c.QueryParam("format")
	if format != "md" && format != "pdf" && format != "docx" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"format": `must be "md", "pdf", or "docx"`,
		}))
	}

	scope := service.ExportScope(c.QueryParam("scope"))
	if scope != service.ExportScopeSelf && scope != service.ExportScopeTree {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"scope": `must be "self" or "tree"`,
		}))
	}

	ctx := c.Request().Context()
	var data []byte
	var filename, contentType string
	var err error
	switch format {
	case "md":
		data, filename, contentType, err = h.exportService.ExportMarkdown(ctx, docID, wsID, scope)
	case "pdf":
		data, filename, contentType, err = h.exportService.ExportPDF(ctx, docID, wsID, scope)
	case "docx":
		data, filename, contentType, err = h.exportService.ExportDOCX(ctx, docID, wsID, scope)
	}
	if err != nil {
		return handleError(c, err)
	}

	c.Response().Header().Set(echo.HeaderContentDisposition, fmt.Sprintf("attachment; filename=%q", filename))
	return c.Blob(http.StatusOK, contentType, data)
}
