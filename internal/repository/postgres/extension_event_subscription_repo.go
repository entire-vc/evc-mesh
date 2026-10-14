package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ExtensionEventSubscriptionRepo implements repository.ExtensionEventSubscriptionRepository.
type ExtensionEventSubscriptionRepo struct {
	db *sqlx.DB
}

// NewExtensionEventSubscriptionRepo creates a new ExtensionEventSubscriptionRepo.
func NewExtensionEventSubscriptionRepo(db *sqlx.DB) *ExtensionEventSubscriptionRepo {
	return &ExtensionEventSubscriptionRepo{db: db}
}

const extensionEventSubscriptionCols = `id, connection_id, event, delivery, target, secret_ref, created_at`

func (r *ExtensionEventSubscriptionRepo) Create(ctx context.Context, sub *domain.ExtensionEventSubscription) (*domain.ExtensionEventSubscription, error) {
	// DO UPDATE SET event = event is a no-op that makes RETURNING yield the
	// existing row, so a repeated subscribe is idempotent.
	const q = `
		INSERT INTO extension_event_subscriptions (id, connection_id, event, delivery, target, secret_ref)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (connection_id, event, delivery, target) DO UPDATE SET event = EXCLUDED.event
		RETURNING ` + extensionEventSubscriptionCols
	var out domain.ExtensionEventSubscription
	err := r.db.GetContext(ctx, &out, q, sub.ID, sub.ConnectionID, sub.Event, string(sub.Delivery), sub.Target, sub.SecretRef)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *ExtensionEventSubscriptionRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.ExtensionEventSubscription, error) {
	var out domain.ExtensionEventSubscription
	err := r.db.GetContext(ctx, &out, `SELECT `+extensionEventSubscriptionCols+` FROM extension_event_subscriptions WHERE id = $1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *ExtensionEventSubscriptionRepo) ListByConnection(ctx context.Context, connectionID uuid.UUID) ([]domain.ExtensionEventSubscription, error) {
	var out []domain.ExtensionEventSubscription
	err := r.db.SelectContext(ctx, &out, `SELECT `+extensionEventSubscriptionCols+
		` FROM extension_event_subscriptions WHERE connection_id = $1 ORDER BY created_at, id`, connectionID)
	return out, err
}

func (r *ExtensionEventSubscriptionRepo) ListByEvent(ctx context.Context, event string) ([]domain.ExtensionEventSubscription, error) {
	var out []domain.ExtensionEventSubscription
	err := r.db.SelectContext(ctx, &out, `SELECT `+extensionEventSubscriptionCols+
		` FROM extension_event_subscriptions WHERE event = $1 ORDER BY created_at, id`, event)
	return out, err
}

func (r *ExtensionEventSubscriptionRepo) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM extension_event_subscriptions WHERE id = $1`, id)
	return err
}
