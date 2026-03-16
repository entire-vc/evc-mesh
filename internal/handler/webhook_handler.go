package handler

import (
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// WebhookHandler handles HTTP requests for outbound webhook management.
type WebhookHandler struct {
	webhookService service.WebhookService
}

// NewWebhookHandler creates a new WebhookHandler.
func NewWebhookHandler(ws service.WebhookService) *WebhookHandler {
	return &WebhookHandler{webhookService: ws}
}

// createWebhookRequest is the JSON body for creating a webhook.
type createWebhookRequest struct {
	Name   string   `json:"name"`
	URL    string   `json:"url"`
	Events []string `json:"events"`
}

// updateWebhookRequest is the JSON body for partially updating a webhook.
type updateWebhookRequest struct {
	Name     *string  `json:"name"`
	URL      *string  `json:"url"`
	Events   []string `json:"events"`
	IsActive *bool    `json:"is_active"`
}

// Create handles POST /workspaces/:ws_id/webhooks
func (h *WebhookHandler) Create(c echo.Context) error {
	wsIDStr := c.Param("ws_id")
	wsID, err := uuid.Parse(wsIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	var req createWebhookRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	// Resolve the creator identity (user or nil UUID for agent).
	var createdBy uuid.UUID
	if userIDVal := c.Get("user_id"); userIDVal != nil {
		if uid, ok := userIDVal.(uuid.UUID); ok {
			createdBy = uid
		}
	}

	input := domain.CreateWebhookInput{
		WorkspaceID: wsID,
		Name:        req.Name,
		URL:         req.URL,
		Events:      req.Events,
		CreatedBy:   createdBy,
	}

	wh, err := h.webhookService.Create(c.Request().Context(), input)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, wh)
}

// List handles GET /workspaces/:ws_id/webhooks
func (h *WebhookHandler) List(c echo.Context) error {
	wsIDStr := c.Param("ws_id")
	wsID, err := uuid.Parse(wsIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	webhooks, err := h.webhookService.ListByWorkspace(c.Request().Context(), wsID)
	if err != nil {
		return handleError(c, err)
	}

	if webhooks == nil {
		webhooks = []domain.WebhookConfig{}
	}

	return c.JSON(http.StatusOK, map[string]any{
		"webhooks": webhooks,
		"count":    len(webhooks),
	})
}

// GetByID handles GET /webhooks/:webhook_id
func (h *WebhookHandler) GetByID(c echo.Context) error {
	webhookID, err := parseWebhookID(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid webhook_id"))
	}

	wh, err := h.webhookService.GetByID(c.Request().Context(), webhookID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, wh)
}

// Update handles PATCH /webhooks/:webhook_id
func (h *WebhookHandler) Update(c echo.Context) error {
	webhookID, err := parseWebhookID(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid webhook_id"))
	}

	var req updateWebhookRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	input := domain.UpdateWebhookInput{
		Name:     req.Name,
		URL:      req.URL,
		Events:   req.Events,
		IsActive: req.IsActive,
	}

	wh, err := h.webhookService.Update(c.Request().Context(), webhookID, input)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, wh)
}

// Delete handles DELETE /webhooks/:webhook_id
func (h *WebhookHandler) Delete(c echo.Context) error {
	webhookID, err := parseWebhookID(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid webhook_id"))
	}

	if err := h.webhookService.Delete(c.Request().Context(), webhookID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// ListDeliveries handles GET /webhooks/:webhook_id/deliveries
func (h *WebhookHandler) ListDeliveries(c echo.Context) error {
	webhookID, err := parseWebhookID(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid webhook_id"))
	}

	limit := 50
	if limitStr := c.QueryParam("limit"); limitStr != "" {
		var l int
		var parseErr error
		if l, parseErr = strconv.Atoi(limitStr); parseErr == nil && l > 0 && l <= 200 {
			limit = l
		}
	}

	deliveries, err := h.webhookService.ListDeliveries(c.Request().Context(), webhookID, limit)
	if err != nil {
		return handleError(c, err)
	}

	if deliveries == nil {
		deliveries = []domain.WebhookDelivery{}
	}

	return c.JSON(http.StatusOK, map[string]any{
		"deliveries": deliveries,
		"count":      len(deliveries),
	})
}

// Test handles POST /webhooks/:webhook_id/test — sends a test delivery directly to the webhook URL.
func (h *WebhookHandler) Test(c echo.Context) error {
	webhookID, err := parseWebhookID(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid webhook_id"))
	}

	// Verify the webhook exists before accepting the request.
	if _, err := h.webhookService.GetByID(c.Request().Context(), webhookID); err != nil {
		return handleError(c, err)
	}

	// Fire a test delivery directly to the webhook URL, bypassing event subscription filtering.
	// TestDelivery is fire-and-forget and records the delivery in the log.
	h.webhookService.TestDelivery(c.Request().Context(), webhookID)

	return c.JSON(http.StatusAccepted, map[string]string{
		"status":  "dispatched",
		"message": "Test delivery queued",
	})
}

// parseWebhookID extracts and parses the webhook_id URL parameter.
func parseWebhookID(c echo.Context) (uuid.UUID, error) {
	return uuid.Parse(c.Param("webhook_id"))
}
