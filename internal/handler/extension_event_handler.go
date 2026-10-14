package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ExtensionEventHandler serves the public extension event list and the
// subscriptions of an extension connection to those events.
type ExtensionEventHandler struct {
	subs service.ExtensionEventSubscriptionService
}

// NewExtensionEventHandler creates a new ExtensionEventHandler.
func NewExtensionEventHandler(subs service.ExtensionEventSubscriptionService) *ExtensionEventHandler {
	return &ExtensionEventHandler{subs: subs}
}

type subscribeExtensionEventRequest struct {
	Event     string `json:"event"`
	Delivery  string `json:"delivery"`
	Target    string `json:"target"`
	SecretRef string `json:"secret_ref"`
}

// ListEvents handles GET /extensions/events.
func (h *ExtensionEventHandler) ListEvents(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{
		"version": extensions.EventsVersion,
		"events":  extensions.PublicEvents(),
	})
}

// Subscribe handles POST /integrations/:int_id/event-subscriptions.
func (h *ExtensionEventHandler) Subscribe(c echo.Context) error {
	connID, err := uuid.Parse(c.Param("int_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid integration_id"))
	}
	var req subscribeExtensionEventRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	sub, err := h.subs.Subscribe(c.Request().Context(), service.CreateExtensionEventSubscriptionInput{
		ConnectionID: connID,
		Event:        req.Event,
		Delivery:     domain.ExtensionEventDelivery(req.Delivery),
		Target:       req.Target,
		SecretRef:    req.SecretRef,
	})
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusCreated, sub)
}

// List handles GET /integrations/:int_id/event-subscriptions.
func (h *ExtensionEventHandler) List(c echo.Context) error {
	connID, err := uuid.Parse(c.Param("int_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid integration_id"))
	}
	subs, err := h.subs.ListByConnection(c.Request().Context(), connID)
	if err != nil {
		return handleError(c, err)
	}
	if subs == nil {
		subs = []domain.ExtensionEventSubscription{}
	}
	return c.JSON(http.StatusOK, map[string]any{"items": subs})
}

// Unsubscribe handles DELETE /event-subscriptions/:sub_id.
func (h *ExtensionEventHandler) Unsubscribe(c echo.Context) error {
	id, err := uuid.Parse(c.Param("sub_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid subscription_id"))
	}
	if err = h.subs.Unsubscribe(c.Request().Context(), id); err != nil {
		return handleError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}
