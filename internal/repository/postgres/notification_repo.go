package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

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
//
// The match key is applied by an UPDATE ... RETURNING, falling back to an INSERT
// when it matches nothing. The previous version wrote ON CONFLICT (id) against an
// id it had just generated, so the conflict never fired and every call appended a
// row: toggling one setting five times left five rows, dispatch iterated all of
// them, and there was no single row for an unsubscribe to remove. There is no
// unique index on the natural key to conflict on, and adding one would first have
// to dedupe whatever is already stored, so the upsert is done in two statements
// rather than in the constraint.
func (r *NotificationRepo) UpsertPreference(ctx context.Context, pref *domain.NotificationPreference) error {
	cfg := pref.Config
	if cfg == nil {
		cfg = json.RawMessage(`{}`)
	}

	// chk_single_actor guarantees exactly one of the two is set, so the actor
	// predicate is decided by which one it is.
	actorPredicate := "user_id = $2 AND agent_id IS NULL"
	actorID := pref.UserID
	if pref.UserID == nil {
		actorPredicate = "agent_id = $2 AND user_id IS NULL"
		actorID = pref.AgentID
	}

	updateQ := `
		UPDATE notification_preferences
		SET events = $4, is_enabled = $5, config = $6, updated_at = now()
		WHERE workspace_id = $1 AND channel = $3 AND ` + actorPredicate + `
		RETURNING id, created_at, updated_at
	`
	err := r.db.QueryRowContext(ctx, updateQ,
		pref.WorkspaceID, actorID, pref.Channel, pref.Events, pref.IsEnabled, cfg,
	).Scan(&pref.ID, &pref.CreatedAt, &pref.UpdatedAt)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	if pref.ID == uuid.Nil {
		pref.ID = uuid.New()
	}
	const insertQ = `
		INSERT INTO notification_preferences
			(id, workspace_id, user_id, agent_id, channel, events, is_enabled, config, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now(), now())
		RETURNING id, created_at, updated_at
	`
	return r.db.QueryRowContext(ctx, insertQ,
		pref.ID, pref.WorkspaceID, pref.UserID, pref.AgentID,
		pref.Channel, pref.Events, pref.IsEnabled, cfg,
	).Scan(&pref.ID, &pref.CreatedAt, &pref.UpdatedAt)
}

// DeletePreferenceForUser removes one preference row, and only if it is the given
// user's own.
//
// The user_id in the WHERE clause is the authorization, not a filter: it is what
// keeps DELETE /notifications/preferences/:pref_id from being a way to cancel
// somebody else's subscriptions by id. It returns the number of rows removed so
// the caller can tell "not yours" from "already gone" — both answer 404, which is
// deliberate: a 403 on someone else's id would confirm the id exists.
func (r *NotificationRepo) DeletePreferenceForUser(ctx context.Context, id, userID uuid.UUID) (int64, error) {
	const q = `DELETE FROM notification_preferences WHERE id = $1 AND user_id = $2`
	res, err := r.db.ExecContext(ctx, q, id, userID)
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

// GetPreferencesByWorkspace returns every stored preference row for a workspace.
//
// It answers "what is in the table", which is not the same question as "who may
// be told". A row here is a claim the subscriber made about themselves; it is not
// evidence that they are in the workspace, and it survives them being removed
// from it. Callers that deliver workspace content — comment bodies, task titles —
// MUST reduce the recipients with FilterWorkspaceMembers first. See
// notificationService.dispatch.
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

// FilterWorkspaceMembers returns the subset of userIDs that are entitled to see
// the workspace's content, in one query rather than one per candidate.
//
// It is the membership rule of middleware.UserIsWorkspaceMember — a non-empty
// role row, or ownership of the workspace, which is how the founding owner is a
// member without a workspace_members row — asked about many users at once,
// because the caller asking is a fan-out loop and not a request handler.
func (r *NotificationRepo) FilterWorkspaceMembers(ctx context.Context, workspaceID uuid.UUID, userIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	members := make(map[uuid.UUID]bool, len(userIDs))
	if len(userIDs) == 0 {
		return members, nil
	}

	candidates := make(pq.StringArray, 0, len(userIDs))
	for _, id := range userIDs {
		candidates = append(candidates, id.String())
	}

	const q = `
		SELECT wm.user_id
		FROM workspace_members wm
		WHERE wm.workspace_id = $1 AND wm.user_id = ANY($2::uuid[]) AND wm.role <> ''
		UNION
		SELECT w.owner_id
		FROM workspaces w
		WHERE w.id = $1 AND w.owner_id = ANY($2::uuid[])
	`
	rows, err := r.db.QueryContext(ctx, q, workspaceID, candidates)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id uuid.UUID
		if scanErr := rows.Scan(&id); scanErr != nil {
			return nil, scanErr
		}
		members[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return members, nil
}
