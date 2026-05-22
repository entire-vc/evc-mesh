package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// PushSubscriptionRepo implements repository.PushSubscriptionRepository.
type PushSubscriptionRepo struct {
	db *sqlx.DB
}

// NewPushSubscriptionRepo creates a new PushSubscriptionRepo.
func NewPushSubscriptionRepo(db *sqlx.DB) *PushSubscriptionRepo {
	return &PushSubscriptionRepo{db: db}
}

// Upsert inserts a new subscription or updates p256dh_key, auth_key, user_agent, last_seen_at on conflict.
func (r *PushSubscriptionRepo) Upsert(ctx context.Context, sub *domain.PushSubscription) error {
	const q = `
		INSERT INTO push_subscriptions (id, user_id, endpoint, p256dh_key, auth_key, user_agent, created_at, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW())
		ON CONFLICT (endpoint) DO UPDATE
		  SET p256dh_key   = EXCLUDED.p256dh_key,
		      auth_key     = EXCLUDED.auth_key,
		      user_agent   = EXCLUDED.user_agent,
		      last_seen_at = NOW()
		RETURNING id, user_id, endpoint, p256dh_key, auth_key, user_agent, created_at, last_seen_at
	`
	if sub.ID == uuid.Nil {
		sub.ID = uuid.New()
	}
	return r.db.QueryRowxContext(ctx, q,
		sub.ID, sub.UserID, sub.Endpoint, sub.P256DHKey, sub.AuthKey, sub.UserAgent,
	).StructScan(sub)
}

// Delete removes a subscription by ID.
func (r *PushSubscriptionRepo) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE id = $1`, id)
	return err
}

// DeleteByEndpoint removes a subscription for a specific user+endpoint pair.
func (r *PushSubscriptionRepo) DeleteByEndpoint(ctx context.Context, userID uuid.UUID, endpoint string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM push_subscriptions WHERE user_id = $1 AND endpoint = $2`,
		userID, endpoint,
	)
	return err
}

// ListByUser returns all subscriptions for a user.
func (r *PushSubscriptionRepo) ListByUser(ctx context.Context, userID uuid.UUID) ([]domain.PushSubscription, error) {
	const q = `
		SELECT id, user_id, endpoint, p256dh_key, auth_key, user_agent, created_at, last_seen_at
		FROM push_subscriptions
		WHERE user_id = $1
		ORDER BY created_at ASC
	`
	var subs []domain.PushSubscription
	if err := r.db.SelectContext(ctx, &subs, q, userID); err != nil {
		return nil, err
	}
	return subs, nil
}

// GetByEndpoint returns the subscription with the given endpoint, or nil if not found.
func (r *PushSubscriptionRepo) GetByEndpoint(ctx context.Context, endpoint string) (*domain.PushSubscription, error) {
	const q = `
		SELECT id, user_id, endpoint, p256dh_key, auth_key, user_agent, created_at, last_seen_at
		FROM push_subscriptions
		WHERE endpoint = $1
	`
	var sub domain.PushSubscription
	if err := r.db.GetContext(ctx, &sub, q, endpoint); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &sub, nil
}
