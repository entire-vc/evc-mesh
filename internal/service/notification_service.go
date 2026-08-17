package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// NotificationService dispatches in-app notifications to users based on their preferences.
// For agents, existing AgentNotifyService handles delivery.
type NotificationService interface {
	// Notify dispatches a notification event to all subscribed users in the workspace.
	// Fire-and-forget: spawns a goroutine and never blocks the caller.
	Notify(ctx context.Context, event domain.NotificationEvent)

	// GetPreferences returns notification preferences for the current user.
	GetPreferences(ctx context.Context, userID uuid.UUID) ([]domain.NotificationPreference, error)

	// UpsertPreferences creates or updates notification preferences for a user.
	UpsertPreferences(ctx context.Context, pref *domain.NotificationPreference) (*domain.NotificationPreference, error)

	// DeletePreference removes one of the caller's own preference rows, which is
	// how a subscription is cancelled. Returns apierror.NotFound if the row is
	// not the caller's or no longer exists.
	DeletePreference(ctx context.Context, userID, prefID uuid.UUID) error

	// EmailAvailable reports whether the email notification channel can
	// actually deliver anything on this instance — i.e. whether an EmailService
	// was wired in and its SMTP config is set. The Notification Settings page
	// uses this to tell the user email is unavailable instead of letting them
	// subscribe to a channel that will silently never send.
	EmailAvailable() bool

	// TelegramBotInfo reports whether workspaceID has an active Telegram bot
	// configured and, if so, its @username — so the settings page can build
	// the t.me/<bot>?start=<token> link without calling Telegram itself.
	TelegramBotInfo(ctx context.Context, workspaceID uuid.UUID) (botUsername string, available bool)

	// TelegramReachable reports whether this instance can actually talk to the
	// Telegram Bot API with workspaceID's configured bot, and why not when it
	// cannot. It is the Telegram counterpart of EmailAvailable, and it has to
	// make a real call because the failure it exists to catch — no outbound
	// route from this host to api.telegram.org — is invisible to every check
	// that only reads the database. A configured, decryptable, perfectly valid
	// bot token looks completely healthy right up until the first send times
	// out.
	//
	// Bounded by telegramProbeTimeout so a settings page asking the question
	// cannot hang on the same timeout the dispatcher hits.
	TelegramReachable(ctx context.Context, workspaceID uuid.UUID) (reachable bool, reason string)

	// ListUnread returns unread notifications for the given user (up to 50).
	ListUnread(ctx context.Context, userID uuid.UUID) ([]domain.Notification, error)

	// CountUnread returns the count of unread notifications for the given user.
	CountUnread(ctx context.Context, userID uuid.UUID) (int, error)

	// MarkRead marks the given notification IDs as read for the user.
	MarkRead(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) error

	// MarkAllRead marks all unread notifications as read for the user.
	MarkAllRead(ctx context.Context, userID uuid.UUID) error
}

// notificationRepository is the slice of *postgres.NotificationRepo this service
// uses. It is named as an interface so that dispatch — the code that decides who
// is told the contents of a comment — can be tested against a stranger without a
// database standing by to make the point.
type notificationRepository interface {
	GetPreferencesByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.NotificationPreference, error)
	FilterWorkspaceMembers(ctx context.Context, workspaceID uuid.UUID, userIDs []uuid.UUID) (map[uuid.UUID]bool, error)
	CreateNotification(ctx context.Context, n *domain.Notification) error
	GetPreferencesByUser(ctx context.Context, userID uuid.UUID) ([]domain.NotificationPreference, error)
	UpsertPreference(ctx context.Context, pref *domain.NotificationPreference) error
	DeletePreferenceForUser(ctx context.Context, id, userID uuid.UUID) (int64, error)
	ListUnread(ctx context.Context, userID uuid.UUID, limit int) ([]domain.Notification, error)
	CountUnread(ctx context.Context, userID uuid.UUID) (int, error)
	MarkRead(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) error
	MarkAllRead(ctx context.Context, userID uuid.UUID) error
}

// *postgres.NotificationRepo is the only production implementation.
var _ notificationRepository = (*postgres.NotificationRepo)(nil)

// userEmailLookup is the slice of *postgres.UserRepo the email channel needs:
// the account address to fall back to when a subscriber has not set a custom
// delivery address on their preference row.
type userEmailLookup interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error)
}

// telegramTokenLookup is the slice of *postgres.IntegrationRepo the Telegram
// channel needs: the workspace's telegram integration row, from which the
// bot token is decrypted (see decodeTelegramIntegration).
type telegramTokenLookup interface {
	GetByProvider(ctx context.Context, workspaceID uuid.UUID, provider domain.IntegrationProvider) (*domain.IntegrationConfig, error)
}

// telegramProjectNameLookup is the slice of *postgres.ProjectRepo dispatch
// needs for the message header — narrower than telegram_bot_manager.go's
// projectLookup, which also needs ListForUserInWorkspace for the welcome
// message dispatch never sends.
type telegramProjectNameLookup interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Project, error)
}

type notificationService struct {
	repo         notificationRepository
	pushService  PushService
	emailSvc     EmailService
	userRepo     userEmailLookup
	emailBaseURL string

	telegramClient       TelegramClient
	telegramIntegrations telegramTokenLookup
	telegramWorkspaces   workspaceNameLookup
	telegramProjects     telegramProjectNameLookup
}

// NotificationServiceOption is a functional option for configuring notificationService.
type NotificationServiceOption func(*notificationService)

// WithPushService injects an optional PushService for browser push fan-out.
func WithPushService(ps PushService) NotificationServiceOption {
	return func(s *notificationService) { s.pushService = ps }
}

// WithEmailService injects the email channel's dependencies: the transport,
// account lookup for the default delivery address, and the base URL used to
// build the task link in the email body. Without this option the email
// channel is a no-op — preference rows can still be saved, but dispatch skips
// them the same way it would if SMTP were unconfigured, and EmailAvailable
// reports false.
func WithEmailService(svc EmailService, users userEmailLookup, baseURL string) NotificationServiceOption {
	return func(s *notificationService) {
		s.emailSvc = svc
		s.userRepo = users
		s.emailBaseURL = baseURL
	}
}

// WithTelegramService injects the Telegram channel's dependencies: the bot
// API client, per-workspace bot-token lookup, and the workspace/project name
// lookups the message header needs. Without this option the channel is a
// no-op — preference rows can still be saved and even bound via /start
// through the bot manager, but dispatch skips them the same way it would if
// no bot were configured.
func WithTelegramService(client TelegramClient, integrations telegramTokenLookup, workspaces workspaceNameLookup, projects telegramProjectNameLookup) NotificationServiceOption {
	return func(s *notificationService) {
		s.telegramClient = client
		s.telegramIntegrations = integrations
		s.telegramWorkspaces = workspaces
		s.telegramProjects = projects
	}
}

// NewNotificationService creates a new NotificationService.
func NewNotificationService(repo notificationRepository, opts ...NotificationServiceOption) NotificationService {
	svc := &notificationService{repo: repo}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

// Notify dispatches a notification event to all subscribed users in the workspace.
// Runs fire-and-forget in a goroutine.
func (s *notificationService) Notify(ctx context.Context, event domain.NotificationEvent) {
	go s.dispatch(event)
}

func (s *notificationService) dispatch(event domain.NotificationEvent) {
	bgCtx := context.Background()

	// Load all enabled preferences for the workspace.
	prefs, err := s.repo.GetPreferencesByWorkspace(bgCtx, event.WorkspaceID)
	if err != nil {
		log.Printf("[notification] failed to load preferences for workspace %s: %v", event.WorkspaceID, err)
		return
	}

	// A preference row is a claim, not an entitlement. Every recipient is checked
	// against the workspace's membership before being told anything, so that a
	// subscription created by somebody who was never in this workspace — or who
	// has since been removed from it — delivers nothing. Without this the
	// notification body, which carries the comment text, is the payload of any
	// stray row in notification_preferences.
	members, err := s.repo.FilterWorkspaceMembers(bgCtx, event.WorkspaceID, subscribedUserIDs(prefs))
	if err != nil {
		// Fail closed: an unanswerable membership question is not permission to
		// send. A dropped notification is a nuisance; a leaked comment is not.
		log.Printf("[notification] failed to resolve membership for workspace %s, delivering nothing: %v",
			event.WorkspaceID, err)
		return
	}

	meta, _ := json.Marshal(event.Metadata)
	if meta == nil {
		meta = json.RawMessage(`{}`)
	}

	for i := range prefs {
		p := &prefs[i]

		// Only dispatch to web_push channel (in-app). Agents use AgentNotifyService.
		if p.Channel != "web_push" {
			continue
		}

		if p.UserID != nil && !members[*p.UserID] {
			continue
		}

		// Check if this event type is subscribed.
		if !containsInStringArray(p.Events, event.EventType) {
			continue
		}

		// Only user preferences are stored in notifications table.
		if p.UserID == nil {
			continue
		}

		// A targeted event (e.g. "you were made reviewer") is about one specific
		// person, not the workspace at large — everyone else's matching preference
		// row is not a delivery instruction for this event.
		if event.TargetUserID != nil && *p.UserID != *event.TargetUserID {
			continue
		}

		// And the same for an event that is about several specific people
		// rather than one — a comment concerns the task's own people, not
		// every colleague subscribed to comment.created.
		if !relevantToUser(event, *p.UserID) {
			continue
		}

		n := &domain.Notification{
			WorkspaceID: event.WorkspaceID,
			UserID:      p.UserID,
			EventType:   event.EventType,
			Title:       event.Title,
			Body:        event.Body,
			Metadata:    meta,
		}

		if err := s.repo.CreateNotification(bgCtx, n); err != nil {
			log.Printf("[notification] failed to create notification for user %s: %v", p.UserID, err)
		}
	}

	// Fan-out to browser push (per-user, respects browser_push prefs).
	if s.pushService != nil {
		sentTo := make(map[uuid.UUID]bool)
		for i := range prefs {
			p := &prefs[i]
			if p.Channel != "browser_push" {
				continue
			}
			if p.UserID == nil || sentTo[*p.UserID] {
				continue
			}
			if !members[*p.UserID] {
				continue
			}
			if !containsInStringArray(p.Events, event.EventType) {
				continue
			}
			if event.TargetUserID != nil && *p.UserID != *event.TargetUserID {
				continue
			}
			if !relevantToUser(event, *p.UserID) {
				continue
			}
			taskURL := ""
			if event.TaskID != nil {
				taskURL = "/t/" + event.TaskID.String()
			}
			pushPayload := domain.PushPayload{
				Title: event.Title,
				Body:  event.Body,
				URL:   taskURL,
				Tag: func() string {
					if event.TaskID != nil {
						return event.TaskID.String()
					}
					return event.EventType
				}(),
				Icon:      "/icons/icon-192.png",
				EventType: event.EventType,
			}
			sentTo[*p.UserID] = true
			userID := *p.UserID
			go func() {
				if err := s.pushService.SendToUser(context.Background(), userID, event.WorkspaceID, pushPayload); err != nil {
					log.Printf("[push] SendToUser error for %s: %v", userID, err)
				}
			}()
		}
	}

	// Fan-out to email (per-user, respects the email channel's own prefs).
	//
	// Silently skipped when the channel isn't wired up or SMTP isn't
	// configured, same as browser push above when pushService is nil. This is
	// not the "tell the user" half of the SMTP-unconfigured story — that is
	// EmailAvailable(), read by the settings page before anyone subscribes.
	// Here, after a subscription already exists, the alternative to skipping
	// is failing every dispatch loop's other channels over a channel nobody
	// can currently receive.
	if s.emailSvc == nil || !s.emailSvc.Enabled() {
		// Say so, once, and only when somebody was actually waiting on it.
		// The skip itself is deliberate (see the comment above), but skipping
		// silently is what let "email notifications don't arrive" go unnoticed:
		// the rows were in the table, the events were firing, and the one place
		// that knew the two would never meet said nothing. Counting the
		// would-be recipients keeps this to one line per dispatch instead of
		// one per subscriber.
		if n := subscriberCountForChannel(prefs, "email", event, members); n > 0 {
			log.Printf("[notification][email] channel unavailable on this instance (no SMTP configured) — %d subscriber(s) to %s in workspace %s were not emailed",
				n, event.EventType, event.WorkspaceID)
		}
	} else {
		sentTo := make(map[uuid.UUID]bool)
		for i := range prefs {
			p := &prefs[i]
			if p.Channel != "email" || p.UserID == nil || sentTo[*p.UserID] {
				continue
			}
			if !members[*p.UserID] {
				continue
			}
			if !containsInStringArray(p.Events, event.EventType) {
				continue
			}
			// Same personal-event restriction as web_push/browser_push above:
			// task.assigned / task.reviewer_assigned are about one specific
			// person, and everyone else's matching row is not a delivery
			// instruction for them.
			if event.TargetUserID != nil && *p.UserID != *event.TargetUserID {
				continue
			}
			if !relevantToUser(event, *p.UserID) {
				continue
			}
			sentTo[*p.UserID] = true
			userID := *p.UserID
			taskURL := ""
			if event.TaskID != nil && s.emailBaseURL != "" {
				taskURL = strings.TrimRight(s.emailBaseURL, "/") + "/t/" + event.TaskID.String()
			}
			go func(prefConfig json.RawMessage) {
				addr, err := s.resolveEmailAddress(context.Background(), userID, prefConfig)
				if err != nil {
					log.Printf("[notification][email] NOT DELIVERED: no usable address for user %s (event %s, workspace %s): %v",
						userID, event.EventType, event.WorkspaceID, err)
					return
				}
				body := buildNotificationEmail(event.Title, event.Body, taskURL)
				if err := s.emailSvc.SendNotification(context.Background(), addr, event.Title, body); err != nil {
					// The address is logged deliberately: the failures this line
					// exists to diagnose (a rejected sender, a typo'd custom
					// address, a bounce) are unreadable without knowing which
					// recipient the server refused.
					log.Printf("[notification][email] NOT DELIVERED: send to %s failed for user %s (event %s, workspace %s): %v",
						addr, userID, event.EventType, event.WorkspaceID, err)
				}
			}(p.Config)
		}
	}

	// Fan-out to Telegram (per-user, respects the telegram channel's own
	// prefs).
	//
	// Every way this channel can fail to deliver now says so. It used to have
	// three silent exits — deps not wired, no active bot for the workspace, and
	// a subscriber who never finished /start — and all three present to the
	// person waiting as "Telegram notifications don't work", with nothing in the
	// log to tell them apart. telegramUndeliverable renders whichever applies,
	// once per dispatch, and only when somebody was actually waiting on it.
	switch {
	case s.telegramClient == nil || s.telegramIntegrations == nil:
		s.logTelegramUndeliverable(prefs, event, members, "the Telegram channel is not wired up on this instance")
	default:
		integration, err := s.telegramIntegrations.GetByProvider(bgCtx, event.WorkspaceID, domain.IntegrationProviderTelegram)
		if err != nil {
			log.Printf("[telegram] failed to load bot integration for workspace %s: %v", event.WorkspaceID, err)
			s.logTelegramUndeliverable(prefs, event, members, "the workspace's bot integration could not be read")
		} else if token, _, ok := decodeTelegramIntegration(integration); !ok {
			s.logTelegramUndeliverable(prefs, event, members, "this workspace has no active Telegram bot configured")
		} else {
			wsName := ""
			if s.telegramWorkspaces != nil {
				if ws, wsErr := s.telegramWorkspaces.GetByID(bgCtx, event.WorkspaceID); wsErr == nil && ws != nil {
					wsName = ws.Name
				}
			}
			projName := ""
			if s.telegramProjects != nil && event.ProjectID != nil {
				if proj, projErr := s.telegramProjects.GetByID(bgCtx, *event.ProjectID); projErr == nil && proj != nil {
					projName = proj.Name
				}
			}
			text := buildTelegramNotificationText(wsName, projName, event.Labels, event.Title, event.Body)

			unbound := 0
			sentTo := make(map[uuid.UUID]bool)
			for i := range prefs {
				p := &prefs[i]
				if p.Channel != "telegram" || p.UserID == nil || sentTo[*p.UserID] {
					continue
				}
				if !members[*p.UserID] {
					continue
				}
				if !containsInStringArray(p.Events, event.EventType) {
					continue
				}
				if event.TargetUserID != nil && *p.UserID != *event.TargetUserID {
					continue
				}
				if !relevantToUser(event, *p.UserID) {
					continue
				}
				var cfg TelegramPreferenceConfig
				if len(p.Config) > 0 {
					_ = json.Unmarshal(p.Config, &cfg)
				}
				if cfg.ChatID == 0 {
					// Enabled and a username is on file, but /start was never
					// completed (or the bot was later blocked and this row
					// hasn't been re-bound) — nothing to deliver to yet.
					// Counted rather than logged per row: a workspace where
					// nobody finished /start would otherwise produce a line per
					// subscriber per event.
					unbound++
					continue
				}
				sentTo[*p.UserID] = true
				userID := *p.UserID
				chatID := cfg.ChatID
				go func() {
					sendCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					if sendErr := s.telegramClient.SendMessage(sendCtx, token, chatID, text); sendErr != nil {
						var apiErr *TelegramAPIError
						if errors.As(sendErr, &apiErr) && apiErr.Forbidden() {
							// The bot was blocked or the chat deleted. Distinct
							// from a transient failure and worth saying so: the
							// subscription is being switched off, and the user
							// will not find out from Telegram.
							log.Printf("[notification][telegram] NOT DELIVERED: user %s has blocked the bot (event %s, workspace %s) — disabling their Telegram channel: %v",
								userID, event.EventType, event.WorkspaceID, sendErr)
							s.disableTelegramForUser(context.Background(), event.WorkspaceID, userID)
							return
						}
						log.Printf("[notification][telegram] NOT DELIVERED: send to chat %d failed for user %s (event %s, workspace %s): %v",
							chatID, userID, event.EventType, event.WorkspaceID, sendErr)
					}
				}()
			}

			if unbound > 0 {
				log.Printf("[notification][telegram] NOT DELIVERED: %d subscriber(s) to %s in workspace %s have not completed /start — no chat is bound to their account yet",
					unbound, event.EventType, event.WorkspaceID)
			}
		}
	}
}

// relevantToUser reports whether userID is one of the people this event is
// about, according to its producer (RelevantUserIDs). An event that carries no
// relevance information is relevant to everyone who subscribed to it, which is
// the behaviour every channel had before this existed.
//
// Every fan-out loop calls this, in the same position as the TargetUserID check
// it generalises. Which events narrow is decided once, by the producer that
// knows who the event is about, rather than four times over by each channel
// guessing — so a comment reaches the same people whether they read it in the
// bell, their inbox or Telegram, and adding a fifth channel cannot accidentally
// ship with a wider audience than the four before it.
func relevantToUser(event domain.NotificationEvent, userID uuid.UUID) bool {
	if len(event.RelevantUserIDs) == 0 {
		return true
	}
	for _, id := range event.RelevantUserIDs {
		if id == userID {
			return true
		}
	}
	return false
}

// logTelegramUndeliverable reports, once per dispatch, that the Telegram
// channel could not deliver anything at all — and to how many people. Mirrors
// the email branch's unavailable-channel line, and for the same reason: the
// rows are in the table and the events are firing, so the only place the
// difference between "nothing happened yet" and "this can never arrive" is
// still visible is the moment dispatch declines to try.
func (s *notificationService) logTelegramUndeliverable(
	prefs []domain.NotificationPreference,
	event domain.NotificationEvent,
	members map[uuid.UUID]bool,
	reason string,
) {
	n := subscriberCountForChannel(prefs, "telegram", event, members)
	if n == 0 {
		return
	}
	log.Printf("[notification][telegram] NOT DELIVERED: %s — %d subscriber(s) to %s in workspace %s got nothing",
		reason, n, event.EventType, event.WorkspaceID)
}

// disableTelegramForUser turns off the telegram preference row for one user
// in one workspace after Telegram reports the bot is blocked (403) — the
// chat_id will never accept another message until they unblock it, so
// leaving is_enabled=true would just mean every future dispatch calls
// SendMessage and gets 403 again, forever. Mirrors the existing
// auto-deactivation of a webhook that starts failing the same way.
func (s *notificationService) disableTelegramForUser(ctx context.Context, workspaceID, userID uuid.UUID) {
	prefs, err := s.repo.GetPreferencesByWorkspace(ctx, workspaceID)
	if err != nil {
		log.Printf("[telegram] failed to load preferences while disabling blocked channel for user %s: %v", userID, err)
		return
	}
	for i := range prefs {
		p := &prefs[i]
		if p.Channel == "telegram" && p.UserID != nil && *p.UserID == userID {
			p.IsEnabled = false
			if upsertErr := s.repo.UpsertPreference(ctx, p); upsertErr != nil {
				log.Printf("[telegram] failed to disable channel for user %s after blocked bot: %v", userID, upsertErr)
			}
			return
		}
	}
}

// notificationEmailConfig is the shape stored in NotificationPreference.Config
// for the email channel — currently just the optional custom delivery address.
type notificationEmailConfig struct {
	Email string `json:"email"`
}

// resolveEmailAddress returns the address a notification email should go to:
// the custom address saved on the preference row's Config, or — when that is
// empty, which is the default state for a row nobody has edited yet — the
// user's account email.
func (s *notificationService) resolveEmailAddress(ctx context.Context, userID uuid.UUID, cfg json.RawMessage) (string, error) {
	if len(cfg) > 0 {
		var parsed notificationEmailConfig
		if err := json.Unmarshal(cfg, &parsed); err == nil && parsed.Email != "" {
			return parsed.Email, nil
		}
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return "", err
	}
	if user == nil || user.Email == "" {
		return "", fmt.Errorf("user %s has no account email on file", userID)
	}
	return user.Email, nil
}

// subscriberCountForChannel counts the distinct users who would have received
// this event on the named channel had it been able to deliver — i.e. the rows
// that pass exactly the same membership, event-type and targeting filters the
// channel's own fan-out loop applies, so the number in the log is the number of
// people actually affected rather than the size of the table.
//
// Used only to decide whether an unavailable channel is worth a log line, and
// what to say in it.
func subscriberCountForChannel(
	prefs []domain.NotificationPreference,
	channel string,
	event domain.NotificationEvent,
	members map[uuid.UUID]bool,
) int {
	counted := make(map[uuid.UUID]bool)
	for i := range prefs {
		p := &prefs[i]
		if p.Channel != channel || p.UserID == nil || counted[*p.UserID] {
			continue
		}
		if !members[*p.UserID] {
			continue
		}
		if !containsInStringArray(p.Events, event.EventType) {
			continue
		}
		if event.TargetUserID != nil && *p.UserID != *event.TargetUserID {
			continue
		}
		if !relevantToUser(event, *p.UserID) {
			continue
		}
		counted[*p.UserID] = true
	}
	return len(counted)
}

// subscribedUserIDs is the deduplicated set of users named by these preference
// rows — the candidate recipients, before anyone has checked whether they are
// entitled to be one.
func subscribedUserIDs(prefs []domain.NotificationPreference) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(prefs))
	ids := make([]uuid.UUID, 0, len(prefs))
	for i := range prefs {
		id := prefs[i].UserID
		if id == nil || seen[*id] {
			continue
		}
		seen[*id] = true
		ids = append(ids, *id)
	}
	return ids
}

// containsInStringArray checks if a pq.StringArray contains the given value.
func containsInStringArray(arr pq.StringArray, val string) bool {
	for _, s := range arr {
		if s == val {
			return true
		}
	}
	return false
}

// GetPreferences returns notification preferences for the given user.
func (s *notificationService) GetPreferences(ctx context.Context, userID uuid.UUID) ([]domain.NotificationPreference, error) {
	return s.repo.GetPreferencesByUser(ctx, userID)
}

// UpsertPreferences creates or updates notification preferences.
func (s *notificationService) UpsertPreferences(ctx context.Context, pref *domain.NotificationPreference) (*domain.NotificationPreference, error) {
	if err := s.repo.UpsertPreference(ctx, pref); err != nil {
		return nil, err
	}
	return pref, nil
}

// DeletePreference removes one of the caller's own preference rows.
//
// Until this existed there was no way to cancel a subscription at all: the API
// could only create and update them, so a row written by someone who had no
// business in the workspace stayed there for good.
func (s *notificationService) DeletePreference(ctx context.Context, userID, prefID uuid.UUID) error {
	removed, err := s.repo.DeletePreferenceForUser(ctx, prefID, userID)
	if err != nil {
		return err
	}
	if removed == 0 {
		return apierror.NotFound("notification preference")
	}
	return nil
}

// EmailAvailable reports whether the email channel can deliver anything on
// this instance.
func (s *notificationService) EmailAvailable() bool {
	return s.emailSvc != nil && s.emailSvc.Enabled()
}

// TelegramBotInfo reports whether workspaceID has an active Telegram bot
// configured and, if so, its @username.
func (s *notificationService) TelegramBotInfo(ctx context.Context, workspaceID uuid.UUID) (string, bool) {
	if s.telegramIntegrations == nil {
		return "", false
	}
	cfg, err := s.telegramIntegrations.GetByProvider(ctx, workspaceID, domain.IntegrationProviderTelegram)
	if err != nil {
		return "", false
	}
	_, botUsername, ok := decodeTelegramIntegration(cfg)
	return botUsername, ok
}

// telegramProbeTimeout bounds the reachability probe. It is deliberately much
// shorter than the dispatcher's 10s send timeout: this one runs while somebody
// is looking at a settings page, and an unreachable host answers by not
// answering, so the timeout *is* the response time in exactly the case the
// probe exists for. Long enough for a healthy round trip to Telegram, short
// enough that a blocked egress reports itself rather than looking like a hung
// page.
const telegramProbeTimeout = 3 * time.Second

// TelegramReachable reports whether this instance can reach the Telegram Bot
// API with workspaceID's bot.
func (s *notificationService) TelegramReachable(ctx context.Context, workspaceID uuid.UUID) (reachable bool, reason string) {
	if s.telegramClient == nil || s.telegramIntegrations == nil {
		return false, "The Telegram channel is not enabled on this instance."
	}
	integration, err := s.telegramIntegrations.GetByProvider(ctx, workspaceID, domain.IntegrationProviderTelegram)
	if err != nil {
		return false, "This workspace's Telegram bot configuration could not be read."
	}
	token, _, ok := decodeTelegramIntegration(integration)
	if !ok {
		return false, "No active Telegram bot is configured for this workspace."
	}

	probeCtx, cancel := context.WithTimeout(ctx, telegramProbeTimeout)
	defer cancel()

	if _, err := s.telegramClient.GetMe(probeCtx, token); err != nil {
		var unreachable *TelegramUnreachableError
		if errors.As(err, &unreachable) {
			// The case this whole probe exists for. Said in full, because the
			// person reading it is in a browser and will not be reading the
			// server log where the same explanation is waiting.
			log.Printf("[notification][telegram] unreachable from this host (workspace %s): %v", workspaceID, err)
			return false, "This server cannot reach api.telegram.org. Telegram notifications will not be delivered until the api container is allowed outbound HTTPS (port 443) to api.telegram.org, or an HTTPS_PROXY is configured for it."
		}
		var apiErr *TelegramAPIError
		if errors.As(err, &apiErr) {
			log.Printf("[notification][telegram] bot token rejected for workspace %s: %v", workspaceID, err)
			return false, "Telegram rejected this workspace's bot token. Reconnect the bot from the Integrations page."
		}
		log.Printf("[notification][telegram] reachability probe failed for workspace %s: %v", workspaceID, err)
		return false, "The Telegram bot could not be reached. Check the server logs for details."
	}
	return true, ""
}

// ListUnread returns unread notifications for the given user (up to 50).
func (s *notificationService) ListUnread(ctx context.Context, userID uuid.UUID) ([]domain.Notification, error) {
	return s.repo.ListUnread(ctx, userID, 50)
}

// CountUnread returns the count of unread notifications for the given user.
func (s *notificationService) CountUnread(ctx context.Context, userID uuid.UUID) (int, error) {
	return s.repo.CountUnread(ctx, userID)
}

// MarkRead marks the given notification IDs as read for the user.
func (s *notificationService) MarkRead(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) error {
	return s.repo.MarkRead(ctx, userID, ids)
}

// MarkAllRead marks all unread notifications as read for the user.
func (s *notificationService) MarkAllRead(ctx context.Context, userID uuid.UUID) error {
	return s.repo.MarkAllRead(ctx, userID)
}

// Ensure notificationService satisfies NotificationService.
var _ NotificationService = (*notificationService)(nil)
