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

// ProjectUpdateHandler handles HTTP requests for project updates.
type ProjectUpdateHandler struct {
	updateService service.ProjectUpdateService
}

// NewProjectUpdateHandler creates a new ProjectUpdateHandler.
func NewProjectUpdateHandler(svc service.ProjectUpdateService) *ProjectUpdateHandler {
	return &ProjectUpdateHandler{updateService: svc}
}

// createProjectUpdateRequest is the JSON body for creating a project update.
type createProjectUpdateRequest struct {
	Title      string             `json:"title"`
	Status     domain.UpdateStatus `json:"status"`
	Summary    string             `json:"summary"`
	Highlights []domain.TextItem  `json:"highlights"`
	Blockers   []domain.TextItem  `json:"blockers"`
	NextSteps  []domain.TextItem  `json:"next_steps"`
}

// Create handles POST /projects/:proj_id/updates
func (h *ProjectUpdateHandler) Create(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	var req createProjectUpdateRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	callerID := callerUUID(c)

	update, err := h.updateService.Create(c.Request().Context(), service.CreateProjectUpdateInput{
		ProjectID:  projID,
		Title:      req.Title,
		Status:     req.Status,
		Summary:    req.Summary,
		Highlights: req.Highlights,
		Blockers:   req.Blockers,
		NextSteps:  req.NextSteps,
		CreatedBy:  callerID,
	})
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, update)
}

// List handles GET /projects/:proj_id/updates
func (h *ProjectUpdateHandler) List(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	var pg pagination.Params
	if err := c.Bind(&pg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination parameters"))
	}
	pg.Normalize()

	page, err := h.updateService.List(c.Request().Context(), projID, pg)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, page)
}

// GetLatest handles GET /projects/:proj_id/updates/latest
func (h *ProjectUpdateHandler) GetLatest(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	update, err := h.updateService.GetLatest(c.Request().Context(), projID)
	if err != nil {
		return handleError(c, err)
	}
	if update == nil {
		return c.JSON(http.StatusNotFound, apierror.NotFound("ProjectUpdate"))
	}

	return c.JSON(http.StatusOK, update)
}

// callerUUID extracts the authenticated caller's UUID from the Echo context.
// Returns uuid.Nil if not available.
func callerUUID(c echo.Context) uuid.UUID {
	// Try user_id first, then agent_id.
	if rawID := c.Get(mw.ContextKeyUserID); rawID != nil {
		if id, ok := rawID.(uuid.UUID); ok {
			return id
		}
	}
	if rawID := c.Get(mw.ContextKeyAgentID); rawID != nil {
		if id, ok := rawID.(uuid.UUID); ok {
			return id
		}
	}
	// Fall back to actor context.
	if id, _ := actorctx.FromContext(c.Request().Context()); id != uuid.Nil {
		return id
	}
	return uuid.Nil
}
