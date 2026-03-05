package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// NotificationHandler handles HTTP requests for in-app notifications.
type NotificationHandler struct {
	svc service.NotificationService
}

// NewNotificationHandler creates a new NotificationHandler.
func NewNotificationHandler(svc service.NotificationService) *NotificationHandler {
	return &NotificationHandler{svc: svc}
}

// List handles GET /notifications
// Returns unread notifications for the authenticated user.
func (h *NotificationHandler) List(c echo.Context) error {
	userID, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("authentication required"))
	}

	items, err := h.svc.ListUnread(c.Request().Context(), userID)
	if err != nil {
		return handleError(c, err)
	}

	count, err := h.svc.CountUnread(c.Request().Context(), userID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"items":        items,
		"unread_count": count,
	})
}

// markReadRequest is the JSON body for marking notifications as read.
type markReadRequest struct {
	IDs    []string `json:"ids"`
	MarkAll bool    `json:"mark_all"`
}

// MarkRead handles POST /notifications/mark-read
func (h *NotificationHandler) MarkRead(c echo.Context) error {
	userID, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("authentication required"))
	}

	var req markReadRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	ctx := c.Request().Context()

	if req.MarkAll {
		if err := h.svc.MarkAllRead(ctx, userID); err != nil {
			return handleError(c, err)
		}
		return c.JSON(http.StatusOK, map[string]any{"marked": "all"})
	}

	ids := make([]uuid.UUID, 0, len(req.IDs))
	for _, raw := range req.IDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid notification id: "+raw))
		}
		ids = append(ids, id)
	}

	if err := h.svc.MarkRead(ctx, userID, ids); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]any{"marked": len(ids)})
}

// GetPreferences handles GET /notifications/preferences
func (h *NotificationHandler) GetPreferences(c echo.Context) error {
	userID, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("authentication required"))
	}

	prefs, err := h.svc.GetPreferences(c.Request().Context(), userID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]any{"preferences": prefs})
}

// updatePreferencesRequest is the JSON body for updating preferences.
type updatePreferencesRequest struct {
	WorkspaceID string   `json:"workspace_id"`
	Channel     string   `json:"channel"`
	Events      []string `json:"events"`
	IsEnabled   *bool    `json:"is_enabled"`
}

// UpdatePreferences handles PUT /notifications/preferences
func (h *NotificationHandler) UpdatePreferences(c echo.Context) error {
	userID, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("authentication required"))
	}

	var req updatePreferencesRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	wsID, err := uuid.Parse(req.WorkspaceID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	channel := req.Channel
	if channel == "" {
		channel = "web_push"
	}

	events := req.Events
	if len(events) == 0 {
		events = []string{"task.assigned", "task.status_changed", "comment.created"}
	}

	isEnabled := true
	if req.IsEnabled != nil {
		isEnabled = *req.IsEnabled
	}

	pref := &domain.NotificationPreference{
		WorkspaceID: wsID,
		UserID:      &userID,
		Channel:     channel,
		Events:      pq.StringArray(events),
		IsEnabled:   isEnabled,
	}

	result, err := h.svc.UpsertPreferences(c.Request().Context(), pref)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, result)
}

// currentUserID extracts the authenticated user's UUID from the Echo context.
// Returns the UUID and true on success, zero UUID and false if not found.
func currentUserID(c echo.Context) (uuid.UUID, bool) {
	val := c.Get("user_id")
	if val == nil {
		return uuid.Nil, false
	}
	id, ok := val.(uuid.UUID)
	if !ok || id == uuid.Nil {
		return uuid.Nil, false
	}
	return id, true
}
