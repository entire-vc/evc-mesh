package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// PushSubscriptionHandler handles Web Push subscription management endpoints.
type PushSubscriptionHandler struct {
	svc service.PushService
}

// NewPushSubscriptionHandler creates a new PushSubscriptionHandler.
func NewPushSubscriptionHandler(svc service.PushService) *PushSubscriptionHandler {
	return &PushSubscriptionHandler{svc: svc}
}

type subscribePushRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// Subscribe handles POST /me/push-subscriptions
func (h *PushSubscriptionHandler) Subscribe(c echo.Context) error {
	userID, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("authentication required"))
	}

	var req subscribePushRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	if req.Endpoint == "" || req.Keys.P256dh == "" || req.Keys.Auth == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("endpoint, keys.p256dh and keys.auth are required"))
	}

	ua := c.Request().UserAgent()
	sub, err := h.svc.Subscribe(c.Request().Context(), userID, req.Endpoint, req.Keys.P256dh, req.Keys.Auth, ua)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusCreated, sub)
}

type unsubscribePushRequest struct {
	Endpoint string `json:"endpoint"`
}

// Unsubscribe handles DELETE /me/push-subscriptions
func (h *PushSubscriptionHandler) Unsubscribe(c echo.Context) error {
	userID, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("authentication required"))
	}

	var req unsubscribePushRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	if req.Endpoint == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("endpoint is required"))
	}

	if err := h.svc.Unsubscribe(c.Request().Context(), userID, req.Endpoint); err != nil {
		return handleError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// List handles GET /me/push-subscriptions
func (h *PushSubscriptionHandler) List(c echo.Context) error {
	userID, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("authentication required"))
	}

	subs, err := h.svc.ListByUser(c.Request().Context(), userID)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"subscriptions": subs})
}

// GetVAPIDKey handles GET /me/push-subscriptions/vapid-key
func (h *PushSubscriptionHandler) GetVAPIDKey(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{"publicKey": h.svc.GetVAPIDPublicKey()})
}
