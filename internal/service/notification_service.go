package service

import (
	"context"
	"encoding/json"
	"log"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
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

	// ListUnread returns unread notifications for the given user (up to 50).
	ListUnread(ctx context.Context, userID uuid.UUID) ([]domain.Notification, error)

	// CountUnread returns the count of unread notifications for the given user.
	CountUnread(ctx context.Context, userID uuid.UUID) (int, error)

	// MarkRead marks the given notification IDs as read for the user.
	MarkRead(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) error

	// MarkAllRead marks all unread notifications as read for the user.
	MarkAllRead(ctx context.Context, userID uuid.UUID) error
}

type notificationService struct {
	repo *postgres.NotificationRepo
}

// NewNotificationService creates a new NotificationService.
func NewNotificationService(repo *postgres.NotificationRepo) NotificationService {
	return &notificationService{repo: repo}
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
