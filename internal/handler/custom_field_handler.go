package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// CustomFieldHandler handles HTTP requests for custom field definitions.
type CustomFieldHandler struct {
	fieldService service.CustomFieldService
}

// NewCustomFieldHandler creates a new CustomFieldHandler with the given service.
func NewCustomFieldHandler(fs service.CustomFieldService) *CustomFieldHandler {
	return &CustomFieldHandler{fieldService: fs}
}

// createCustomFieldRequest represents the JSON body for creating a custom field definition.
type createCustomFieldRequest struct {
	Name              string           `json:"name"`
	FieldType         domain.FieldType `json:"field_type"`
	Description       string           `json:"description"`
	Options           json.RawMessage  `json:"options"`
	DefaultValue      json.RawMessage  `json:"default_value"`
	IsRequired        bool             `json:"is_required"`
	IsVisibleToAgents bool             `json:"is_visible_to_agents"`
}

// updateCustomFieldRequest represents the JSON body for updating a custom field definition.
type updateCustomFieldRequest struct {
	Name              *string           `json:"name"`
	FieldType         *domain.FieldType `json:"field_type"`
	Description       *string           `json:"description"`
	Options           json.RawMessage   `json:"options"`
	DefaultValue      json.RawMessage   `json:"default_value"`
	IsRequired        *bool             `json:"is_required"`
	IsVisibleToAgents *bool             `json:"is_visible_to_agents"`
}

// reorderCustomFieldsRequest represents the JSON body for reordering custom fields.
type reorderCustomFieldsRequest struct {
	FieldIDs []uuid.UUID `json:"field_ids"`
}

// List handles GET /projects/:proj_id/custom-fields
func (h *CustomFieldHandler) List(c echo.Context) error {
	projIDStr := c.Param("proj_id")
	projID, err := uuid.Parse(projIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	fields, err := h.fieldService.ListByProject(c.Request().Context(), projID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, fields)
}

// Create handles POST /projects/:proj_id/custom-fields
func (h *CustomFieldHandler) Create(c echo.Context) error {
	projIDStr := c.Param("proj_id")
	projID, err := uuid.Parse(projIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	var req createCustomFieldRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"name": "name is required",
		}))
	}

	if req.FieldType == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"field_type": "field_type is required",
		}))
	}

	field := &domain.CustomFieldDefinition{
		ID:                uuid.New(),
		ProjectID:         projID,
		Name:              req.Name,
		FieldType:         req.FieldType,
		Description:       req.Description,
		Options:           req.Options,
		DefaultValue:      req.DefaultValue,
		IsRequired:        req.IsRequired,
		IsVisibleToAgents: req.IsVisibleToAgents,
	}

	if err := h.fieldService.Create(c.Request().Context(), field); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, field)
}

// GetByID handles GET /custom-fields/:field_id
func (h *CustomFieldHandler) GetByID(c echo.Context) error {
	fieldIDStr := c.Param("field_id")
	fieldID, err := uuid.Parse(fieldIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid field_id"))
	}

	field, err := h.fieldService.GetByID(c.Request().Context(), fieldID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, field)
}

// Update handles PATCH /custom-fields/:field_id
func (h *CustomFieldHandler) Update(c echo.Context) error {
	fieldIDStr := c.Param("field_id")
	fieldID, err := uuid.Parse(fieldIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid field_id"))
	}

	var req updateCustomFieldRequest
	err = c.Bind(&req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	// Fetch existing field first for partial update.
	field, err := h.fieldService.GetByID(c.Request().Context(), fieldID)
	if err != nil {
		return handleError(c, err)
	}

	// Apply partial updates.
	if req.Name != nil {
		field.Name = *req.Name
	}
	if req.FieldType != nil {
		field.FieldType = *req.FieldType
	}
	if req.Description != nil {
		field.Description = *req.Description
	}
	if req.Options != nil {
		field.Options = req.Options
	}
	if req.DefaultValue != nil {
		field.DefaultValue = req.DefaultValue
	}
	if req.IsRequired != nil {
		field.IsRequired = *req.IsRequired
	}
	if req.IsVisibleToAgents != nil {
		field.IsVisibleToAgents = *req.IsVisibleToAgents
	}

	if err := h.fieldService.Update(c.Request().Context(), field); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, field)
}

// Delete handles DELETE /custom-fields/:field_id
func (h *CustomFieldHandler) Delete(c echo.Context) error {
	fieldIDStr := c.Param("field_id")
	fieldID, err := uuid.Parse(fieldIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid field_id"))
	}

	if err := h.fieldService.Delete(c.Request().Context(), fieldID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// Reorder handles PUT /projects/:proj_id/custom-fields/reorder
func (h *CustomFieldHandler) Reorder(c echo.Context) error {
	projIDStr := c.Param("proj_id")
	projID, err := uuid.Parse(projIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
	}

	var req reorderCustomFieldsRequest
	err = c.Bind(&req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if len(req.FieldIDs) == 0 {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"field_ids": "field_ids is required",
		}))
	}

	err = h.fieldService.Reorder(c.Request().Context(), projID, req.FieldIDs)
	if err != nil {
		return handleError(c, err)
	}

	// Return the updated list so the client can refresh its state.
	fields, err := h.fieldService.ListByProject(c.Request().Context(), projID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, fields)
}
