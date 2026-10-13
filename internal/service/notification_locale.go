package service

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/i18n"
)

// WithLocaleLookups injects what recipient-language resolution needs: the
// account (users.locale) and the workspace (workspaces.default_locale). Without
// it, or when a lookup fails, every recipient gets English — the same text the
// event carries — so a missing dependency degrades to the old behaviour rather
// than to silence.
func WithLocaleLookups(users userEmailLookup, workspaces workspaceNameLookup) NotificationServiceOption {
	return func(s *notificationService) {
		s.localeUsers = users
		s.localeWorkspaces = workspaces
	}
}

// recipientLocale is the language a notification to userID in workspaceID is
// written in: the person's own choice when they made one, otherwise the
// workspace default, otherwise English.
//
// users.locale alone is not enough: the column is NOT NULL DEFAULT 'en', so an
// account that never picked a language reads "en" and would override a Russian
// workspace. locale_set_explicitly is what tells a choice from a default.
func (s *notificationService) recipientLocale(ctx context.Context, workspaceID, userID uuid.UUID) string {
	users := s.localeUsers
	if users == nil {
		users = s.userRepo
	}
	if users != nil {
		if u, err := users.GetByID(ctx, userID); err == nil && u != nil && u.LocaleSetExplicitly {
			return i18n.Normalize(u.Locale)
		}
	}
	return s.workspaceLocale(ctx, workspaceID)
}

func (s *notificationService) workspaceLocale(ctx context.Context, workspaceID uuid.UUID) string {
	ws := s.localeWorkspaces
	if ws == nil {
		ws = s.telegramWorkspaces
	}
	if ws != nil {
		if w, err := ws.GetByID(ctx, workspaceID); err == nil && w != nil {
			return i18n.Normalize(w.DefaultLocale)
		}
	}
	return i18n.Default
}

// renderEventText returns the subject and body of event in locale. An event with
// no code, or a code the catalog does not know, keeps the text it carries; a
// code with no body entry keeps the event's body (user-authored text).
func renderEventText(locale string, e domain.NotificationEvent) (title, body string) {
	return renderCode(locale, e.MessageCode, e.MessageParams, e.Title, e.Body)
}

func renderCode(locale, code string, params map[string]any, title, body string) (outTitle, outBody string) {
	if code == "" {
		return title, body
	}
	if t := i18n.T(locale, code+".title", params); t != "" {
		title = t
	}
	if b := i18n.T(locale, code+".body", params); b != "" {
		body = b
	}
	return title, body
}

// renderStoredNotification re-renders a persisted notification in locale.
// Rows written before message codes existed are left exactly as stored.
func renderStoredNotification(locale string, n *domain.Notification) {
	if n.MessageCode == nil || *n.MessageCode == "" {
		return
	}
	var params map[string]any
	if len(n.MessageParams) > 0 {
		if err := json.Unmarshal(n.MessageParams, &params); err != nil {
			return
		}
	}
	n.Title, n.Body = renderCode(locale, *n.MessageCode, params, n.Title, n.Body)
}
