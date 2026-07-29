package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// NotificationHandler handles HTTP requests for in-app notifications.
type NotificationHandler struct {
	svc        service.NotificationService
	members    repository.WorkspaceMemberRepository
	workspaces repository.WorkspaceRepository
}

// NewNotificationHandler creates a new NotificationHandler.
//
// members and workspaces are needed only by Delete, to tell a workspace's owner
// or admin — who may evict anybody's subscription — from an ordinary member, who
// may evict their own.
func NewNotificationHandler(
	svc service.NotificationService,
	members repository.WorkspaceMemberRepository,
	workspaces repository.WorkspaceRepository,
) *NotificationHandler {
	return &NotificationHandler{svc: svc, members: members, workspaces: workspaces}
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
	IDs     []string `json:"ids"`
	MarkAll bool     `json:"mark_all"`
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
//
// WorkspaceID names the tenant this row is written into, and no route parameter
// carries it — the path is a bare /notifications/preferences. That is why the
// route is registered with middleware.RequireBodyWorkspace: without it,
// RequireWorkspaceMemberScoped has nothing to resolve and never runs, and any
// authenticated caller can write a row into a workspace they have never been a
// member of. See the comment on Delete for what such a row was worth.
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
		events = []string{"task.assigned", "task.status_changed", "comment.created", "task.blocking_triage"}
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

// deletePreferencesRequest is the JSON body for removing a preference.
//
// UserID / AgentID are optional and name whose subscription to remove. Omitted,
// the caller's own is removed; naming somebody else is the workspace owner's and
// admins' privilege (see Delete). WorkspaceID is guarded by the same
// middleware.RequireBodyWorkspace as the PUT.
type deletePreferencesRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Channel     string `json:"channel"`
	UserID      string `json:"user_id,omitempty"`
	AgentID     string `json:"agent_id,omitempty"`
}

// Delete handles DELETE /notifications/preferences
//
// Until this existed, a subscription could be created through the API and removed
// through none of it — not by the subscriber, not by the workspace owner, not by
// anybody. That is a poor property for an ordinary preference and an unacceptable
// one for a row that decides who a workspace's comment bodies are delivered to:
// the only remedy for an unwanted subscription was a manual DELETE against the
// database.
//
// Who may remove what:
//
//   - Your own subscription, in any workspace you belong to — always. Naming
//     nobody means yourself, which is the ordinary case.
//   - Somebody else's, in a workspace you own or administer — allowed. This is
//     the incident-response lever, and it grants no authority that was not
//     already there: an owner or admin can already remove the member outright,
//     and the row goes with them via ON DELETE CASCADE. Naming the row is the
//     same power applied less destructively.
//   - Somebody else's, as an ordinary member — refused. Membership of a
//     workspace is not authority over what its other members hear about it.
func (h *NotificationHandler) Delete(c echo.Context) error {
	var req deletePreferencesRequest
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

	// Who the caller is, and therefore what "my own" means. RequireBodyWorkspace
	// has already established that they may act inside wsID at all — for an agent
	// key as well as a user JWT, which rbac() would not have.
	var (
		callerUser  *uuid.UUID
		callerAgent *uuid.UUID
	)
	if mw.IsAgent(c) {
		agentID, agentErr := mw.GetAgentID(c)
		if agentErr != nil {
			return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("authentication required"))
		}
		callerAgent = &agentID
	} else {
		userID, ok := currentUserID(c)
		if !ok {
			return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("authentication required"))
		}
		callerUser = &userID
	}

	// Whose subscription is being removed.
	targetUser, targetAgent := callerUser, callerAgent
	if req.UserID != "" {
		parsed, parseErr := uuid.Parse(req.UserID)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid user_id"))
		}
		targetUser, targetAgent = &parsed, nil
	}
	if req.AgentID != "" {
		if req.UserID != "" {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("name either user_id or agent_id, not both"))
		}
		parsed, parseErr := uuid.Parse(req.AgentID)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid agent_id"))
		}
		targetUser, targetAgent = nil, &parsed
	}

	if !sameSubject(callerUser, callerAgent, targetUser, targetAgent) && !h.mayManageWorkspace(c, wsID) {
		return c.JSON(http.StatusForbidden,
			apierror.Forbidden("only the subscriber, or a workspace owner or admin, may remove this preference"))
	}

	if _, err := h.svc.DeletePreferences(c.Request().Context(), wsID, targetUser, targetAgent, channel); err != nil {
		return handleError(c, err)
	}

	// 204 whether or not a row was there: the caller asked for the subscription to
	// be gone and it is. Reporting "no such row" would also report, to somebody
	// allowed to ask, whether a given person is subscribed.
	return c.NoContent(http.StatusNoContent)
}

// sameSubject reports whether the caller is the subject of the row.
func sameSubject(callerUser, callerAgent, targetUser, targetAgent *uuid.UUID) bool {
	if callerUser != nil && targetUser != nil {
		return *callerUser == *targetUser
	}
	if callerAgent != nil && targetAgent != nil {
		return *callerAgent == *targetAgent
	}
	return false
}

// mayManageWorkspace reports whether the caller administers wsID: an owner or
// admin membership row, or ownership of the workspace itself — the fallback that
// exists because the workspaces created before membership rows were written have
// an owner_id and nothing else.
//
// Agents are not workspace administrators. An agent key is scoped to a workspace,
// not granted a role in it, so there is no role to read; an agent may remove its
// own preference and no one else's.
func (h *NotificationHandler) mayManageWorkspace(c echo.Context, wsID uuid.UUID) bool {
	if mw.IsAgent(c) {
		return false
	}
	userID, ok := currentUserID(c)
	if !ok {
		return false
	}
	ctx := c.Request().Context()

	if h.members != nil {
		if role, err := h.members.GetRole(ctx, wsID, userID); err == nil {
			if role == domain.RoleOwner || role == domain.RoleAdmin {
				return true
			}
		}
	}
	if h.workspaces != nil {
		if ws, err := h.workspaces.GetByID(ctx, wsID); err == nil && ws != nil {
			return ws.OwnerID == userID
		}
	}
	return false
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
