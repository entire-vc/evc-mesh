package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// SavedViewHandler handles HTTP requests for saved view management.
type SavedViewHandler struct {
	savedViewService service.SavedViewService
}

// NewSavedViewHandler creates a new SavedViewHandler.
func NewSavedViewHandler(svc service.SavedViewService) *SavedViewHandler {
	return &SavedViewHandler{savedViewService: svc}
}

// createSavedViewRequest is the JSON body for creating a saved view.
type createSavedViewRequest struct {
	Name      string                 `json:"name"`
	ViewType  string                 `json:"view_type"`
	Filters   map[string]interface{} `json:"filters"`
	SortBy    *string                `json:"sort_by"`
	SortOrder *string                `json:"sort_order"`
	Columns   []string               `json:"columns"`
	IsShared  bool                   `json:"is_shared"`
}

// updateSavedViewRequest is the JSON body for partially updating a saved view.
type updateSavedViewRequest struct {
	Name      *string                `json:"name"`
	ViewType  *string                `json:"view_type"`
	Filters   map[string]interface{} `json:"filters"`
	SortBy    *string                `json:"sort_by"`
	SortOrder *string                `json:"sort_order"`
	Columns   []string               `json:"columns"`
	IsShared  *bool                  `json:"is_shared"`
}

// callerUserID extracts the authenticated user's UUID from the echo context.
// Returns uuid.Nil if not available (agent auth).
func callerUserID(c echo.Context) uuid.UUID {
	if userIDVal := c.Get("user_id"); userIDVal != nil {
		if uid, ok := userIDVal.(uuid.UUID); ok {
			return uid
		}
	}
	return uuid.Nil
}

// Create handles POST /projects/:proj_id/views
func (h *SavedViewHandler) Create(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	var req createSavedViewRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	createdBy := callerUserID(c)

	input := domain.CreateSavedViewInput{
		ProjectID: projID,
		Name:      req.Name,
		ViewType:  req.ViewType,
		Filters:   req.Filters,
		SortBy:    req.SortBy,
		SortOrder: req.SortOrder,
		Columns:   req.Columns,
		IsShared:  req.IsShared,
		CreatedBy: createdBy,
	}

	view, err := h.savedViewService.Create(c.Request().Context(), input)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, view)
}

// List handles GET /projects/:proj_id/views
func (h *SavedViewHandler) List(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	userID := callerUserID(c)

	views, err := h.savedViewService.ListByProject(c.Request().Context(), projID, userID)
	if err != nil {
		return handleError(c, err)
	}

	if views == nil {
		views = []domain.SavedView{}
	}

	return c.JSON(http.StatusOK, map[string]any{
		"views": views,
		"count": len(views),
	})
}

// GetByID handles GET /views/:view_id
func (h *SavedViewHandler) GetByID(c echo.Context) error {
	viewID, err := parseSavedViewID(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid view_id"))
	}

	view, err := h.savedViewService.GetByID(c.Request().Context(), viewID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, view)
}

// Update handles PATCH /views/:view_id
func (h *SavedViewHandler) Update(c echo.Context) error {
	viewID, err := parseSavedViewID(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid view_id"))
	}

	var req updateSavedViewRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	callerID := callerUserID(c)

	input := domain.UpdateSavedViewInput{
		Name:      req.Name,
		ViewType:  req.ViewType,
		Filters:   req.Filters,
		SortBy:    req.SortBy,
		SortOrder: req.SortOrder,
		Columns:   req.Columns,
		IsShared:  req.IsShared,
	}

	view, err := h.savedViewService.Update(c.Request().Context(), viewID, input, callerID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, view)
}

// Delete handles DELETE /views/:view_id
func (h *SavedViewHandler) Delete(c echo.Context) error {
	viewID, err := parseSavedViewID(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid view_id"))
	}

	callerID := callerUserID(c)

	if err := h.savedViewService.Delete(c.Request().Context(), viewID, callerID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// parseSavedViewID extracts and parses the view_id URL parameter.
func parseSavedViewID(c echo.Context) (uuid.UUID, error) {
	return uuid.Parse(c.Param("view_id"))
}
