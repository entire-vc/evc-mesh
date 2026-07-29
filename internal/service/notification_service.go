package service

import (
	"context"
	"encoding/json"
	"log"

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

type notificationService struct {
	repo        notificationRepository
	pushService PushService
}

// NotificationServiceOption is a functional option for configuring notificationService.
type NotificationServiceOption func(*notificationService)

// WithPushService injects an optional PushService for browser push fan-out.
func WithPushService(ps PushService) NotificationServiceOption {
	return func(s *notificationService) { s.pushService = ps }
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
