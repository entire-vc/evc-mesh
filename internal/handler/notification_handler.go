package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/mail"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// NotificationHandler handles HTTP requests for in-app notifications.
type NotificationHandler struct {
	svc     service.NotificationService
	members repository.WorkspaceMemberRepository
}

// NewNotificationHandler creates a new NotificationHandler. members is used
// only by GetTelegramBotInfo, to authorize the ?workspace_id= it reads — see
// requireWorkspaceMember.
func NewNotificationHandler(svc service.NotificationService, members repository.WorkspaceMemberRepository) *NotificationHandler {
	return &NotificationHandler{svc: svc, members: members}
}

// requireWorkspaceMember reports whether the authenticated caller is a member
// of wsID. Mirrors MemoryHandler.workspaceAllowed — same rule (workspace_members
// row via GetRole), same caveat (a founding owner with no explicit membership
// row is not covered; memories has carried that gap since this pattern was
// introduced and this reuses it rather than inventing a second one).
func (h *NotificationHandler) requireWorkspaceMember(c echo.Context, wsID uuid.UUID) bool {
	if wsID == uuid.Nil || h.members == nil {
		return false
	}
	userID, ok := currentUserID(c)
	if !ok {
		return false
	}
	_, err := h.members.GetRole(c.Request().Context(), wsID, userID)
	return err == nil
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

// GetEmailAvailability handles GET /notifications/email-availability
//
// Read by the settings page before it lets anyone subscribe to the email
// channel, the same role GET /me/push-subscriptions/vapid-key plays for
// browser push: an instance without SMTP configured can still store an email
// preference row (nothing about save requires SMTP), but dispatch will never
// deliver it, so the UI needs to say so up front rather than let the setting
// look like it works.
func (h *NotificationHandler) GetEmailAvailability(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{"available": h.svc.EmailAvailable()})
}

// telegramBotInfoQuery binds ?workspace_id= via echo's struct-tag path so the
// query-tenant AST scan in internal/middleware picks it up the same way it
// already does for memory_handler.go's structs — see declaredQueryTenantParams
// in internal/middleware/query_tenant_verdict_test.go.
type telegramBotInfoQuery struct {
	WorkspaceID string `query:"workspace_id"`
}

// GetTelegramBotInfo handles GET /notifications/telegram-bot-info?workspace_id=...
//
// Read by the settings page the same way GetEmailAvailability is, except the
// Telegram channel is configured per workspace rather than instance-wide, so
// availability needs a workspace to ask about — and unlike every other route
// in this handler, that workspace has to be named in the query string rather
// than being implied by the caller. requireWorkspaceMember is what makes that
// safe: this is the "checked:" verdict declaredQueryTenantParams records for it.
func (h *NotificationHandler) GetTelegramBotInfo(c echo.Context) error {
	var q telegramBotInfoQuery
	if err := c.Bind(&q); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request"))
	}
	wsID, err := uuid.Parse(q.WorkspaceID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}
	if !h.requireWorkspaceMember(c, wsID) {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
	}

	botUsername, available := h.svc.TelegramBotInfo(c.Request().Context(), wsID)
	return c.JSON(http.StatusOK, map[string]any{
		"available":    available,
		"bot_username": botUsername,
	})
}

// deliverableChannels is the set of channel names notificationService.dispatch
// actually has a delivery branch for. It is the whitelist PUT
// /notifications/preferences validates against.
//
// The route used to store whatever string arrived. A row saved with a channel
// nobody dispatches — a typo like "e-mail", or a channel someone assumed
// existed, like "slack" — was answered 200 with the stored row echoed back, and
// then skipped by every fan-out loop without a log line, because each loop
// selects the rows it recognises rather than rejecting the ones it does not. The
// subscription looked saved in the UI and delivered nothing, forever, with no
// evidence anywhere that it never could. Refusing the value at the edge is the
// only place the difference between "no notifications yet" and "this channel
// does not exist" is still visible.
//
// Keep in step with the branches in notificationService.dispatch.
var deliverableChannels = map[string]bool{
	"web_push":     true, // in-app bell: rows in the notifications table
	"browser_push": true, // W3C web push via PushService
	"email":        true, // SMTP via EmailService
	"telegram":     true, // per-workspace bot via TelegramClient
}

// dispatchableEvents is the set of event types some producer actually emits
// through NotificationService.Notify. Subscribing to anything else is the same
// dead-end as an unknown channel: stored, echoed back, never delivered.
//
// Keep in step with the Notify call sites (task_service.go, comment_service.go).
var dispatchableEvents = map[string]bool{
	"task.assigned":          true,
	"task.status_changed":    true,
	"task.mentioned":         true,
	"task.blocking_triage":   true,
	"task.reviewer_assigned": true,
	"task.ready_for_review":  true,
	"comment.created":        true,
}

// defaultSubscribedEvents is what a preference row gets when the caller names no
// events. Every dispatchable event is included: the caller asked to be notified
// and said nothing about exclusions, so the default is the full set rather than
// a subset that quietly drops mentions.
func defaultSubscribedEvents() []string { return sortedEvents() }

func sortedChannels() []string { return sortedKeys(deliverableChannels) }

func sortedEvents() []string { return sortedKeys(dispatchableEvents) }

// sortedKeys returns the set's members in a stable order, so the error messages
// built from them do not reshuffle between identical requests.
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// updatePreferencesRequest is the JSON body for updating preferences.
//
// WorkspaceID names a tenant the route does not, which is the shape that has to
// be guarded outside the handler: middleware.RequireBodyWorkspace is registered on
// PUT /notifications/preferences and refuses a caller who is not in the workspace
// they are subscribing to. Before it was, any authenticated user could subscribe
// to any workspace by pasting its id here, and the dispatcher then delivered that
// workspace's comment bodies to them. It is recorded as guarded in
// declaredBodyTenantFields, and TestBodyWorkspaceIDGuardsAreWiredToTheirRoutes
// declines to take that record on trust — it reads the router back.
type updatePreferencesRequest struct {
	WorkspaceID string   `json:"workspace_id"`
	Channel     string   `json:"channel"`
	Events      []string `json:"events"`
	IsEnabled   *bool    `json:"is_enabled"`
	// Config carries channel-specific settings — currently just the email
	// channel's optional custom delivery address ({"email": "..."}). Passed
	// through to the stored preference row as-is except for the validation
	// below; empty/absent means "use the account default", decided at
	// dispatch time, not here.
	Config map[string]string `json:"config,omitempty"`
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
	if !deliverableChannels[channel] {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest(
			"unknown notification channel: "+channel+" (supported: "+strings.Join(sortedChannels(), ", ")+")"))
	}

	events := req.Events
	if len(events) == 0 {
		events = defaultSubscribedEvents()
	}
	for _, ev := range events {
		if !dispatchableEvents[ev] {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest(
				"unknown notification event: "+ev+" (supported: "+strings.Join(sortedEvents(), ", ")+")"))
		}
	}

	isEnabled := true
	if req.IsEnabled != nil {
		isEnabled = *req.IsEnabled
	}

	// A custom delivery address is only meaningful on the email channel, but
	// checked whenever present rather than gated on channel=="email" — a typo'd
	// address saved under the wrong channel should still fail loudly instead of
	// waiting to surface as a silently undelivered email later.
	if addr, ok := req.Config["email"]; ok && addr != "" {
		if _, addrErr := mail.ParseAddress(addr); addrErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid email address: "+addr))
		}
	}

	var cfgJSON json.RawMessage
	if channel == "telegram" {
		encoded, tgErr := h.buildTelegramPreferenceConfig(c.Request().Context(), userID, wsID, isEnabled, strings.TrimSpace(req.Config["telegram_username"]))
		if tgErr != nil {
			return c.JSON(tgErr.StatusCode(), tgErr)
		}
		cfgJSON = encoded
	} else if len(req.Config) > 0 {
		encoded, marshalErr := json.Marshal(req.Config)
		if marshalErr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid config"))
		}
		cfgJSON = encoded
	}

	pref := &domain.NotificationPreference{
		WorkspaceID: wsID,
		UserID:      &userID,
		Channel:     channel,
		Events:      pq.StringArray(events),
		IsEnabled:   isEnabled,
		Config:      cfgJSON,
	}

	result, err := h.svc.UpsertPreferences(c.Request().Context(), pref)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, result)
}

// telegramBindTokenTTL is how long a t.me/<bot>?start=<token> link stays
// valid before the /start handler treats it the same as an unknown token.
const telegramBindTokenTTL = 15 * time.Minute

// buildTelegramPreferenceConfig computes the Config JSON to store for a
// channel="telegram" preference row.
//
// chat_id is never set here — it is written only by a successful /start,
// which is the whole point of the token/TTL dance below: this endpoint can
// only ask to be linked, never claim a binding itself. A bind token is
// (re)issued only when enabling with no existing chat_id, so a user who is
// already bound and just adjusts their per-event toggles does not get a
// fresh, unnecessary link on every save.
//
// Disabling the channel (isEnabled=false) is how a user unbinds: the prior
// chat_id is deliberately NOT carried forward in that case, so it comes back
// empty. Without this, chat_id survived every save regardless of the toggle
// — there was no way to stop delivery to a bound account or switch which
// Telegram account is bound, since a fresh bind token is only ever issued
// when chat_id is already empty (see below).
func (h *NotificationHandler) buildTelegramPreferenceConfig(ctx context.Context, userID, wsID uuid.UUID, isEnabled bool, username string) (json.RawMessage, *apierror.Error) {
	username = strings.TrimPrefix(username, "@")
	if isEnabled && username == "" {
		return nil, apierror.BadRequest("telegram_username is required to enable Telegram notifications")
	}

	cfg := service.TelegramPreferenceConfig{Username: username}

	if isEnabled {
		if existing, err := h.svc.GetPreferences(ctx, userID); err == nil {
			for i := range existing {
				if existing[i].WorkspaceID == wsID && existing[i].Channel == "telegram" && len(existing[i].Config) > 0 {
					var prev service.TelegramPreferenceConfig
					if json.Unmarshal(existing[i].Config, &prev) == nil {
						cfg.ChatID = prev.ChatID
					}
					break
				}
			}
		}
	}

	if isEnabled && cfg.ChatID == 0 {
		token, tokenErr := service.GenerateTelegramBindToken()
		if tokenErr != nil {
			return nil, apierror.BadRequestWithDetails("failed to prepare Telegram binding", tokenErr.Error())
		}
		expires := time.Now().Add(telegramBindTokenTTL)
		cfg.BindToken = token
		cfg.BindExpiresAt = &expires
	}

	encoded, err := json.Marshal(cfg)
	if err != nil {
		return nil, apierror.BadRequestWithDetails("invalid config", err.Error())
	}
	return encoded, nil
}

// DeletePreference handles DELETE /notifications/preferences/:pref_id
//
// This is how a subscription is cancelled, and until it existed there was no way
// to cancel one: the API could create and update preference rows and nothing
// else, so a subscription to a workspace the subscriber had no business in
// outlived any attempt to undo it.
//
// The route names a row by id and no workspace can be resolved from it, so it is
// recorded in workspaceScopeHandlerCheckedRoutes: the check is the caller's own
// user_id in the DELETE's WHERE clause, which is stricter than workspace
// membership, not weaker. Somebody else's preference id answers 404.
func (h *NotificationHandler) DeletePreference(c echo.Context) error {
	userID, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("authentication required"))
	}

	prefID, err := uuid.Parse(c.Param("pref_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid preference id"))
	}

	if err := h.svc.DeletePreference(c.Request().Context(), userID, prefID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
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
