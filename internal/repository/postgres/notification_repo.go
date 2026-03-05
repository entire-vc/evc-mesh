package postgres

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// NotificationRepo implements repository.NotificationRepository with PostgreSQL.
type NotificationRepo struct {
	db *sqlx.DB
}

// NewNotificationRepo creates a new NotificationRepo.
func NewNotificationRepo(db *sqlx.DB) *NotificationRepo {
	return &NotificationRepo{db: db}
}

// GetPreferencesByUser returns all notification preferences for a user.
func (r *NotificationRepo) GetPreferencesByUser(ctx context.Context, userID uuid.UUID) ([]domain.NotificationPreference, error) {
	const q = `
		SELECT id, workspace_id, user_id, agent_id, channel, events, is_enabled, config, created_at, updated_at
		FROM notification_preferences
		WHERE user_id = $1
		ORDER BY created_at ASC
	`
	var prefs []domain.NotificationPreference
	if err := r.db.SelectContext(ctx, &prefs, q, userID); err != nil {
		return nil, err
	}
	return prefs, nil
}

// GetPreferencesByAgent returns all notification preferences for an agent.
func (r *NotificationRepo) GetPreferencesByAgent(ctx context.Context, agentID uuid.UUID) ([]domain.NotificationPreference, error) {
	const q = `
		SELECT id, workspace_id, user_id, agent_id, channel, events, is_enabled, config, created_at, updated_at
		FROM notification_preferences
		WHERE agent_id = $1
		ORDER BY created_at ASC
	`
	var prefs []domain.NotificationPreference
	if err := r.db.SelectContext(ctx, &prefs, q, agentID); err != nil {
		return nil, err
	}
	return prefs, nil
}

// UpsertPreference inserts or updates a notification preference.
// Match key: (workspace_id, user_id, channel) or (workspace_id, agent_id, channel).
func (r *NotificationRepo) UpsertPreference(ctx context.Context, pref *domain.NotificationPreference) error {
	cfg := pref.Config
	if cfg == nil {
		cfg = json.RawMessage(`{}`)
	}

	if pref.ID == uuid.Nil {
		pref.ID = uuid.New()
	}

	const q = `
		INSERT INTO notification_preferences
			(id, workspace_id, user_id, agent_id, channel, events, is_enabled, config, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now(), now())
		ON CONFLICT (id) DO UPDATE SET
			events     = EXCLUDED.events,
			is_enabled = EXCLUDED.is_enabled,
			config     = EXCLUDED.config,
			updated_at = now()
		RETURNING id, created_at, updated_at
	`
	return r.db.QueryRowContext(ctx, q,
		pref.ID, pref.WorkspaceID, pref.UserID, pref.AgentID,
		pref.Channel, pref.Events, pref.IsEnabled, cfg,
	).Scan(&pref.ID, &pref.CreatedAt, &pref.UpdatedAt)
}

// DeletePreference removes a notification preference by ID.
func (r *NotificationRepo) DeletePreference(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM notification_preferences WHERE id = $1`
	_, err := r.db.ExecContext(ctx, q, id)
	return err
}

// CreateNotification persists a new notification.
func (r *NotificationRepo) CreateNotification(ctx context.Context, n *domain.Notification) error {
	if n.ID == uuid.Nil {
		n.ID = uuid.New()
	}
	meta := n.Metadata
	if meta == nil {
		meta = json.RawMessage(`{}`)
	}

	const q = `
		INSERT INTO notifications (id, workspace_id, user_id, event_type, title, body, metadata, is_read, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, false, now())
		RETURNING created_at
	`
	return r.db.QueryRowContext(ctx, q,
		n.ID, n.WorkspaceID, n.UserID, n.EventType, n.Title, n.Body, meta,
	).Scan(&n.CreatedAt)
}

// ListUnread returns up to limit unread notifications for the given user, newest first.
func (r *NotificationRepo) ListUnread(ctx context.Context, userID uuid.UUID, limit int) ([]domain.Notification, error) {
	const q = `
		SELECT id, workspace_id, user_id, event_type, title, body, metadata, is_read, created_at
		FROM notifications
		WHERE user_id = $1 AND is_read = false
		ORDER BY created_at DESC
		LIMIT $2
	`
	var items []domain.Notification
	if err := r.db.SelectContext(ctx, &items, q, userID, limit); err != nil {
		return nil, err
	}
	return items, nil
}

// CountUnread returns the number of unread notifications for the given user.
func (r *NotificationRepo) CountUnread(ctx context.Context, userID uuid.UUID) (int, error) {
	const q = `SELECT COUNT(*) FROM notifications WHERE user_id = $1 AND is_read = false`
	var count int
	if err := r.db.GetContext(ctx, &count, q, userID); err != nil {
		return 0, err
	}
	return count, nil
}

// MarkRead marks specific notifications as read.
func (r *NotificationRepo) MarkRead(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	query, args, err := sqlx.In(
		`UPDATE notifications SET is_read = true WHERE user_id = ? AND id IN (?)`,
		userID, ids,
	)
	if err != nil {
		return err
	}
	query = r.db.Rebind(query)
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

// MarkAllRead marks all unread notifications for a user as read.
func (r *NotificationRepo) MarkAllRead(ctx context.Context, userID uuid.UUID) error {
	const q = `UPDATE notifications SET is_read = true WHERE user_id = $1 AND is_read = false`
	_, err := r.db.ExecContext(ctx, q, userID)
	return err
}

// GetPreferencesByWorkspace returns all preferences for a workspace, used when
// dispatching an event to find which users should receive it.
func (r *NotificationRepo) GetPreferencesByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.NotificationPreference, error) {
	const q = `
		SELECT id, workspace_id, user_id, agent_id, channel, events, is_enabled, config, created_at, updated_at
		FROM notification_preferences
		WHERE workspace_id = $1 AND is_enabled = true
		ORDER BY created_at ASC
	`
	var prefs []domain.NotificationPreference
	if err := r.db.SelectContext(ctx, &prefs, q, workspaceID); err != nil {
		return nil, err
	}
	return prefs, nil
}
