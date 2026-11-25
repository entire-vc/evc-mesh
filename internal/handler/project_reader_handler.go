package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ProjectReaderHandler serves POST /workspaces/:ws_id/project-readers
// (docs/project-reader.md). Revocation is the ordinary DELETE /agents/:id.
type ProjectReaderHandler struct {
	svc *service.ProjectReaderService
}

// NewProjectReaderHandler builds the handler.
func NewProjectReaderHandler(svc *service.ProjectReaderService) *ProjectReaderHandler {
	return &ProjectReaderHandler{svc: svc}
}

type issueProjectReaderRequest struct {
	Name      string    `json:"name"`
	ProjectID uuid.UUID `json:"project_id"`
}

// Issue creates a project reader and answers its key once (201). The route
// carries rbac(PermManageMembers) and RequireUserAuth: only a human
// workspace admin issues one.
func (h *ProjectReaderHandler) Issue(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}
	var req issueProjectReaderRequest
	if bindErr := c.Bind(&req); bindErr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	in := service.IssueProjectReaderInput{WorkspaceID: wsID, ProjectID: req.ProjectID, Name: req.Name}
	if uid, uerr := mw.GetUserID(c); uerr == nil {
		in.IssuedBy = &uid
	}
	out, err := h.svc.Issue(c.Request().Context(), in)
	if err != nil {
		return handleError(c, err)
	}
	// The key exists only in this answer.
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusCreated, out)
}
