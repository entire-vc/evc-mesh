package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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

// ErrNoPreferenceSubject marks a preference that names neither a user nor an
// agent. The table's chk_single_actor constraint requires exactly one, and
// without knowing which, there is no key to match an existing row on.
var ErrNoPreferenceSubject = errors.New("notification preference names neither a user nor an agent")

// UpsertPreference inserts or updates a notification preference, keyed on
// (workspace_id, user_id, channel) or (workspace_id, agent_id, channel).
//
// It conflicts on that key, not on the primary key. Conflicting on the id was
// the same as not conflicting at all: the caller has no existing row's id to
// supply — it is looking the row up BY the key — so every call arrived with a
// fresh uuid, matched nothing, and inserted. The table grew by one row per PUT
// and the update the user asked for never happened.
//
// The two ON CONFLICT targets are the two partial unique indexes from migration
// 20260729084; which one applies is decided by which subject the row names.
func (r *NotificationRepo) UpsertPreference(ctx context.Context, pref *domain.NotificationPreference) error {
	cfg := pref.Config
	if cfg == nil {
		cfg = json.RawMessage(`{}`)
	}

	if pref.ID == uuid.Nil {
		pref.ID = uuid.New()
	}

	const base = `
		INSERT INTO notification_preferences
			(id, workspace_id, user_id, agent_id, channel, events, is_enabled, config, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now(), now())
		ON CONFLICT %s DO UPDATE SET
			events     = EXCLUDED.events,
			is_enabled = EXCLUDED.is_enabled,
			config     = EXCLUDED.config,
			updated_at = now()
		RETURNING id, created_at, updated_at
	`

	var conflict string
	switch {
	case pref.UserID != nil:
		conflict = `(workspace_id, user_id, channel) WHERE user_id IS NOT NULL`
	case pref.AgentID != nil:
		conflict = `(workspace_id, agent_id, channel) WHERE agent_id IS NOT NULL`
	default:
		return ErrNoPreferenceSubject
	}

	return r.db.QueryRowContext(ctx, fmt.Sprintf(base, conflict),
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

// DeletePreferenceBySubject removes the preference row for one subscriber in one
// workspace on one channel, and reports how many rows that was.
//
// It takes the workspace as a parameter rather than deleting by id alone so the
// caller's authorisation — which is over a workspace — and the statement that
// acts on it cannot disagree. Deleting by id is how the composite-route bugs
// worked: the id was somebody else's and nothing in the statement said so.
func (r *NotificationRepo) DeletePreferenceBySubject(
	ctx context.Context,
	workspaceID uuid.UUID,
	userID, agentID *uuid.UUID,
	channel string,
) (int64, error) {
	var (
		q   string
		arg uuid.UUID
	)
	switch {
	case userID != nil:
		q = `DELETE FROM notification_preferences
		     WHERE workspace_id = $1 AND channel = $2 AND user_id = $3`
		arg = *userID
	case agentID != nil:
		q = `DELETE FROM notification_preferences
		     WHERE workspace_id = $1 AND channel = $2 AND agent_id = $3`
		arg = *agentID
	default:
		return 0, ErrNoPreferenceSubject
	}

	res, err := r.db.ExecContext(ctx, q, workspaceID, channel, arg)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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

// GetDeliverablePreferences returns the enabled preferences for a workspace whose
// subscriber is entitled to be in it — the rows an event in this workspace may
// actually be delivered to.
//
// Membership is part of the query on purpose. Its predecessor selected every
// enabled row with a matching workspace_id and left the question of whose row it
// was to the caller, and the caller — dispatch() — never asked: it filtered on
// the channel and the event type and delivered. A single planted row was
// therefore a standing subscription to a stranger's workspace, and every comment
// posted in it went out to the outsider, body and all. There is no caller that
// wants the unfiltered set, so there is no longer a method that returns it.
//
// The membership rule is the one middleware.UserIsWorkspaceMember applies — a
// workspace_members row, or the owner fallback for the workspaces whose owner row
// was never written — and for agents, middleware.AgentIsInWorkspace. Duplicating
// it in SQL rather than calling those per row is what keeps this one query
// instead of one plus N, on a path that runs on every comment, assignment and
// status change.
func (r *NotificationRepo) GetDeliverablePreferences(ctx context.Context, workspaceID uuid.UUID) ([]domain.NotificationPreference, error) {
	const q = `
		SELECT p.id, p.workspace_id, p.user_id, p.agent_id, p.channel,
		       p.events, p.is_enabled, p.config, p.created_at, p.updated_at
		FROM notification_preferences p
		WHERE p.workspace_id = $1
		  AND p.is_enabled = true
		  AND (
		        (p.user_id IS NOT NULL AND (
		              EXISTS (
		                  SELECT 1 FROM workspace_members m
		                  WHERE m.workspace_id = p.workspace_id
		                    AND m.user_id = p.user_id
		              )
		           OR EXISTS (
		                  SELECT 1 FROM workspaces w
		                  WHERE w.id = p.workspace_id
		                    AND w.deleted_at IS NULL
		                    AND w.owner_id = p.user_id
		              )
		        ))
		     OR (p.agent_id IS NOT NULL AND EXISTS (
		              SELECT 1 FROM agents a
		              WHERE a.id = p.agent_id
		                AND a.deleted_at IS NULL
		                AND a.workspace_id = p.workspace_id
		        ))
		  )
		ORDER BY p.created_at ASC
	`
	var prefs []domain.NotificationPreference
	if err := r.db.SelectContext(ctx, &prefs, q, workspaceID); err != nil {
		return nil, err
	}
	return prefs, nil
}
