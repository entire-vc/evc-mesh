package handler

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// InitiativeHandler handles HTTP requests for initiative management.
type InitiativeHandler struct {
	initiativeService service.InitiativeService
}

// NewInitiativeHandler creates a new InitiativeHandler.
func NewInitiativeHandler(svc service.InitiativeService) *InitiativeHandler {
	return &InitiativeHandler{initiativeService: svc}
}

// createInitiativeRequest is the JSON body for creating an initiative.
type createInitiativeRequest struct {
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Status      domain.InitiativeStatus `json:"status"`
	TargetDate  *string                 `json:"target_date"` // ISO date string YYYY-MM-DD
}

// updateInitiativeRequest is the JSON body for partially updating an initiative.
type updateInitiativeRequest struct {
	Name        *string                  `json:"name"`
	Description *string                  `json:"description"`
	Status      *domain.InitiativeStatus `json:"status"`
	TargetDate  *string                  `json:"target_date"` // ISO date or null to clear
}

// linkProjectRequest holds the project ID to link.
type linkProjectRequest struct {
	ProjectID string `json:"project_id"`
}

// Create handles POST /workspaces/:ws_id/initiatives
func (h *InitiativeHandler) Create(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	var req createInitiativeRequest
	err = c.Bind(&req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	callerID := callerUUID(c)

	targetDate, err := parseOptionalDate(req.TargetDate)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"target_date": "must be a valid date in YYYY-MM-DD format",
		}))
	}

	initiative, err := h.initiativeService.Create(c.Request().Context(), service.CreateInitiativeInput{
		WorkspaceID: wsID,
		Name:        req.Name,
		Description: req.Description,
		Status:      req.Status,
		TargetDate:  targetDate,
		CreatedBy:   callerID,
	})
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, initiative)
}

// List handles GET /workspaces/:ws_id/initiatives
func (h *InitiativeHandler) List(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	initiatives, err := h.initiativeService.List(c.Request().Context(), wsID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, initiatives)
}

// GetByID handles GET /initiatives/:init_id
func (h *InitiativeHandler) GetByID(c echo.Context) error {
	initID, err := uuid.Parse(c.Param("init_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid initiative_id"))
	}

	initiative, err := h.initiativeService.GetByID(c.Request().Context(), initID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, initiative)
}

// Update handles PATCH /initiatives/:init_id
func (h *InitiativeHandler) Update(c echo.Context) error {
	initID, err := uuid.Parse(c.Param("init_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid initiative_id"))
	}

	var req updateInitiativeRequest
	err = c.Bind(&req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	targetDate, err := parseOptionalDate(req.TargetDate)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"target_date": "must be a valid date in YYYY-MM-DD format",
		}))
	}

	input := service.UpdateInitiativeInput{
		Name:        req.Name,
		Description: req.Description,
		Status:      req.Status,
		TargetDate:  targetDate,
	}

	initiative, err := h.initiativeService.Update(c.Request().Context(), initID, input)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, initiative)
}

// Delete handles DELETE /initiatives/:init_id
func (h *InitiativeHandler) Delete(c echo.Context) error {
	initID, err := uuid.Parse(c.Param("init_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid initiative_id"))
	}

	if err := h.initiativeService.Delete(c.Request().Context(), initID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// LinkProject handles POST /initiatives/:init_id/projects
func (h *InitiativeHandler) LinkProject(c echo.Context) error {
	initID, err := uuid.Parse(c.Param("init_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid initiative_id"))
	}

	var req linkProjectRequest
	err = c.Bind(&req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	projID, err := uuid.Parse(req.ProjectID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"project_id": "must be a valid UUID",
		}))
	}

	if err := h.initiativeService.LinkProject(c.Request().Context(), initID, projID); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "linked"})
}

// UnlinkProject handles DELETE /initiatives/:init_id/projects/:proj_id
func (h *InitiativeHandler) UnlinkProject(c echo.Context) error {
	initID, err := uuid.Parse(c.Param("init_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid initiative_id"))
	}

	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	if err := h.initiativeService.UnlinkProject(c.Request().Context(), initID, projID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// parseOptionalDate parses an optional ISO date string (YYYY-MM-DD) into *time.Time.
// Returns nil if s is nil or empty string pointer, error if invalid format.
func parseOptionalDate(s *string) (*time.Time, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", *s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
