package service

import (
	"context"
	"encoding/json"
	"log"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// PushService manages browser Web Push subscriptions and fan-out delivery.
// When VAPID keys are absent, all send operations are silently skipped.
type PushService interface {
	Subscribe(ctx context.Context, userID uuid.UUID, endpoint, p256dh, auth, ua string) (*domain.PushSubscription, error)
	Unsubscribe(ctx context.Context, userID uuid.UUID, endpoint string) error
	ListByUser(ctx context.Context, userID uuid.UUID) ([]domain.PushSubscription, error)
	// SendToUser fan-outs a push payload to all subscriptions for a user.
	// If the user has browser_push disabled for the event type in their prefs, it is a no-op.
	SendToUser(ctx context.Context, userID uuid.UUID, workspaceID uuid.UUID, payload domain.PushPayload) error
	GetVAPIDPublicKey() string
}

type pushService struct {
	subRepo      repository.PushSubscriptionRepository
	notifRepo    notifRecipientRepo
	vapidPublic  string
	vapidPrivate string
	vapidSubject string
}

// notifRecipientRepo is the slice of *postgres.NotificationRepo this service
// needs, and it is two questions rather than one: what the subscriber asked to
// be sent, and whether they are in the workspace whose contents that would be.
// Only the second is an entitlement.
type notifRecipientRepo interface {
	GetPreferencesByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.NotificationPreference, error)
	FilterWorkspaceMembers(ctx context.Context, workspaceID uuid.UUID, userIDs []uuid.UUID) (map[uuid.UUID]bool, error)
}

// NewPushService creates a PushService. Missing VAPID keys disable push silently.
func NewPushService(
	subRepo repository.PushSubscriptionRepository,
	notifRepo notifRecipientRepo,
	vapidPublic, vapidPrivate, vapidSubject string,
) PushService {
	if vapidPublic == "" || vapidPrivate == "" {
		log.Println("[push] VAPID keys not set — browser push disabled (safe for local dev)")
	}
	return &pushService{
		subRepo:      subRepo,
		notifRepo:    notifRepo,
		vapidPublic:  vapidPublic,
		vapidPrivate: vapidPrivate,
		vapidSubject: vapidSubject,
	}
}

func (s *pushService) GetVAPIDPublicKey() string { return s.vapidPublic }

func (s *pushService) Subscribe(ctx context.Context, userID uuid.UUID, endpoint, p256dh, auth, ua string) (*domain.PushSubscription, error) {
	sub := &domain.PushSubscription{
		UserID:    userID,
		Endpoint:  endpoint,
		P256DHKey: p256dh,
		AuthKey:   auth,
		UserAgent: ua,
	}
	if err := s.subRepo.Upsert(ctx, sub); err != nil {
		return nil, err
	}
	return sub, nil
}

func (s *pushService) Unsubscribe(ctx context.Context, userID uuid.UUID, endpoint string) error {
	return s.subRepo.DeleteByEndpoint(ctx, userID, endpoint)
}

func (s *pushService) ListByUser(ctx context.Context, userID uuid.UUID) ([]domain.PushSubscription, error) {
	return s.subRepo.ListByUser(ctx, userID)
}

func (s *pushService) SendToUser(ctx context.Context, userID, workspaceID uuid.UUID, payload domain.PushPayload) error {
	if s.vapidPublic == "" || s.vapidPrivate == "" {
		return nil
	}

	// A push subscription and a preference row are both things the recipient
	// wrote about themselves. Neither is evidence that they are in this
	// workspace, and neither stops being true when they are removed from it —
	// so membership is checked here, before a payload carrying a comment body
	// is addressed to one of their devices.
	//
	// notificationService.dispatch applies the same rule before calling this,
	// and this is deliberately not a reliance on that. SendToUser is exported
	// and takes the workspace as an argument; a second caller that forgot would
	// deliver the workspace's contents to a stranger's browser, and the in-app
	// path's filter would never see it. Both fan-outs answer to one rule.
	members, err := s.notifRepo.FilterWorkspaceMembers(ctx, workspaceID, []uuid.UUID{userID})
	if err != nil {
		// Fail closed, as dispatch does: a membership question that could not be
		// answered is not permission to send. A missed push is a nuisance; a
		// comment body on the wrong device is not.
		log.Printf("[push] failed to resolve membership for user %s in workspace %s, sending nothing: %v",
			userID, workspaceID, err)
		return nil
	}
	if !members[userID] {
		return nil
	}

	// Check browser_push prefs for this user + event type.
	prefs, err := s.notifRepo.GetPreferencesByWorkspace(ctx, workspaceID)
	if err != nil {
		log.Printf("[push] failed to load prefs for workspace %s: %v", workspaceID, err)
		return nil
	}
	if !s.pushEnabled(prefs, userID, payload.EventType) {
		return nil
	}

	subs, err := s.subRepo.ListByUser(ctx, userID)
	if err != nil {
		log.Printf("[push] failed to list subscriptions for user %s: %v", userID, err)
		return nil
	}
	if len(subs) == 0 {
		return nil
	}

	body, _ := json.Marshal(payload)

	for _, sub := range subs {
		wpSub := &webpush.Subscription{
			Endpoint: sub.Endpoint,
			Keys: webpush.Keys{
				P256dh: sub.P256DHKey,
				Auth:   sub.AuthKey,
			},
		}
		resp, sendErr := webpush.SendNotification(body, wpSub, &webpush.Options{
			Subscriber:      s.vapidSubject,
			VAPIDPublicKey:  s.vapidPublic,
			VAPIDPrivateKey: s.vapidPrivate,
			TTL:             30,
		})
		if resp != nil {
			resp.Body.Close()
			if resp.StatusCode == 410 || resp.StatusCode == 404 {
				// Subscription expired — clean up.
				if delErr := s.subRepo.Delete(ctx, sub.ID); delErr != nil {
					log.Printf("[push] failed to delete stale sub %s: %v", sub.ID, delErr)
				}
				continue
			}
		}
		if sendErr != nil {
			log.Printf("[push] send to sub %s failed: %v", sub.ID, sendErr)
		}
	}
	return nil
}

func (s *pushService) pushEnabled(prefs []domain.NotificationPreference, userID uuid.UUID, eventType string) bool {
	for i := range prefs {
		p := &prefs[i]
		if p.Channel != "browser_push" {
			continue
		}
		if p.UserID == nil || *p.UserID != userID {
			continue
		}
		if !p.IsEnabled {
			return false
		}
		return containsInStringArray(p.Events, eventType)
	}
	// No explicit pref → default ON (user subscribed = opt-in).
	return true
}
