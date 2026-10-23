package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/storage"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ChatPurgeAfter is how long a soft-deleted thread stays in the database before
// its rows and S3 objects are removed for good (agent-chat-openapi §2 rule 6).
const ChatPurgeAfter = 30 * 24 * time.Hour

// Stable reasons written to turns.stop_reason / commands.detail.
const (
	chatStopThreadDeleted = "thread_deleted"
	chatStopRetention     = "retention"
)

// ChatObjectStore is the slice of the S3 client the purge needs. Delete must
// treat a missing object as an error wrapping storage.ErrNotFound, which the
// purge counts as already gone.
type ChatObjectStore interface {
	Delete(ctx context.Context, key string) error
}

// ChatThreadDeleteRepo owns thread deletion and retention: soft delete now,
// physical purge later, and the retention-driven soft delete of old history.
type ChatThreadDeleteRepo struct {
	db    *sqlx.DB
	store ChatObjectStore
	now   func() time.Time
}

func NewChatThreadDeleteRepo(db *sqlx.DB, store ChatObjectStore) *ChatThreadDeleteRepo {
	return &ChatThreadDeleteRepo{db: db, store: store, now: time.Now}
}

type chatThreadRow struct {
	ID          uuid.UUID `db:"id"`
	WorkspaceID uuid.UUID `db:"workspace_id"`
	AgentID     uuid.UUID `db:"agent_id"`
	Runtime     []byte    `db:"runtime"`
}

func errThreadNotFound() error {
	return apierror.NotFoundWithDetails("thread", "thread_not_found")
}

// SoftDeleteThread deletes the caller's own thread of the project at once. A thread of another
// user, an already deleted one and a missing one are indistinguishable: all
// answer 404 thread_not_found (rule 1), so a repeated DELETE is a 404 too.
func (r *ChatThreadDeleteRepo) SoftDeleteThread(ctx context.Context, actor UserRuntimeActor, project, thread uuid.UUID) error {
	if actor.Connector || actor.AgentID != uuid.Nil || actor.UserID == uuid.Nil {
		return apierror.ForbiddenWithDetails("chat is for signed-in users", "user_only")
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var row chatThreadRow
	err = tx.GetContext(ctx, &row, `SELECT id, workspace_id, agent_id, runtime FROM ext_agent_chat_threads
		WHERE id = $1 AND project_id = $2 AND user_id = $3 AND deleted_at IS NULL FOR UPDATE`, thread, project, actor.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		return errThreadNotFound()
	}
	if err != nil {
		return err
	}
	if err := r.softDeleteLocked(ctx, tx, row, chatStopThreadDeleted); err != nil {
		return err
	}
	return tx.Commit()
}

// softDeleteLocked marks a locked thread deleted, ends its unfinished turn
// (T14), bumps the execution epoch so any session permission fails the epoch
// check, and queues the stop_session + delete_transcript orders for the runner.
func (r *ChatThreadDeleteRepo) softDeleteLocked(ctx context.Context, tx *sqlx.Tx, row chatThreadRow, reason string) error {
	now := r.now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE ext_agent_chat_threads
		SET deleted_at = $2, execution_epoch = execution_epoch + 1 WHERE id = $1`, row.ID, now); err != nil {
		return err
	}
	// Queued, claimed and streaming turns end cancelled; the agent message of
	// each follows (N32). A runner's next call then gets chat_turn_terminal.
	if _, err := tx.ExecContext(ctx, `UPDATE ext_agent_chat_messages m SET status = 'cancelled'
		FROM ext_agent_chat_turns t
		WHERE t.thread_id = $1 AND t.state IN ('queued', 'claimed', 'streaming')
		  AND m.id = t.agent_message_id AND m.workspace_id = t.workspace_id
		  AND m.status IN ('queued', 'streaming')`, row.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ext_agent_chat_turns
		SET state = 'cancelled', cancel_requested = true, cancel_requested_at = $2,
		    stop_reason = $3, finished_at = $2, lease_until = NULL
		WHERE thread_id = $1 AND state IN ('queued', 'claimed', 'streaming')`, row.ID, now, reason); err != nil {
		return err
	}
	// runtime: runner_id, harness, profile, session_id (all optional).
	_, err := tx.ExecContext(ctx, `INSERT INTO ext_agent_chat_commands
			(workspace_id, agent_id, runner_id, kind, thread_id, session_id, harness, detail, expires_at)
		SELECT $2, $3, NULLIF(rt->>'runner_id', '')::uuid, k.kind, $1,
		       NULLIF(rt->>'session_id', ''), NULLIF(rt->>'harness', ''), $4, $5
		FROM (SELECT $6::jsonb AS rt) r
		CROSS JOIN (VALUES ('stop_session'), ('delete_transcript')) AS k(kind)`,
		row.ID, row.WorkspaceID, row.AgentID, reason, now.Add(ChatPurgeAfter), string(row.Runtime))
	return err
}

// ExpireHistory soft-deletes threads whose last activity is older than the
// workspace's agent_chat.history_retention_days (unset or 0 = keep forever),
// through the same path as a user delete. It returns the number of threads.
func (r *ChatThreadDeleteRepo) ExpireHistory(ctx context.Context, limit int) (int, error) {
	var ids []uuid.UUID
	if err := r.db.SelectContext(ctx, &ids, `SELECT t.id FROM ext_agent_chat_threads t
		JOIN workspaces w ON w.id = t.workspace_id
		WHERE t.deleted_at IS NULL
		  AND COALESCE(NULLIF(w.settings->'agent_chat'->>'history_retention_days', '')::int, 0) > 0
		  AND COALESCE(t.last_message_at, t.created_at) <
		      $1::timestamptz - make_interval(days => (w.settings->'agent_chat'->>'history_retention_days')::int)
		  AND NOT EXISTS (SELECT 1 FROM ext_agent_chat_turns u
		      WHERE u.thread_id = t.id AND u.state IN ('queued', 'claimed', 'streaming'))
		ORDER BY t.id LIMIT $2`, r.now().UTC(), limit); err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		ok, err := r.expireOne(ctx, id)
		if err != nil {
			return n, err
		}
		if ok {
			n++
		}
	}
	return n, nil
}

func (r *ChatThreadDeleteRepo) expireOne(ctx context.Context, id uuid.UUID) (bool, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var row chatThreadRow
	// Re-check under the lock: a message may have arrived since the scan.
	err = tx.GetContext(ctx, &row, `SELECT t.id, t.workspace_id, t.agent_id, t.runtime
		FROM ext_agent_chat_threads t JOIN workspaces w ON w.id = t.workspace_id
		WHERE t.id = $1 AND t.deleted_at IS NULL
		  AND COALESCE(NULLIF(w.settings->'agent_chat'->>'history_retention_days', '')::int, 0) > 0
		  AND COALESCE(t.last_message_at, t.created_at) <
		      $2::timestamptz - make_interval(days => (w.settings->'agent_chat'->>'history_retention_days')::int)
		  AND NOT EXISTS (SELECT 1 FROM ext_agent_chat_turns u
		      WHERE u.thread_id = t.id AND u.state IN ('queued', 'claimed', 'streaming'))
		FOR UPDATE OF t SKIP LOCKED`, id, r.now().UTC())
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := r.softDeleteLocked(ctx, tx, row, chatStopRetention); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// PurgeResult counts what one purge pass removed.
type PurgeResult struct {
	Threads int
	Objects int
}

// PurgeDeleted physically removes threads soft-deleted more than ChatPurgeAfter
// ago: first their S3 objects, then the rows (messages, turns, attachments and
// tool events go by ON DELETE CASCADE). A thread whose objects could not all be
// deleted keeps its rows and is retried on the next pass, so no object is
// orphaned. Irreversible: callers on production take a backup first.
func (r *ChatThreadDeleteRepo) PurgeDeleted(ctx context.Context, limit int) (PurgeResult, error) {
	var res PurgeResult
	if r.store == nil {
		// Without object storage a purge would drop attachment rows and orphan their objects.
		return res, nil
	}
	var ids []uuid.UUID
	if err := r.db.SelectContext(ctx, &ids, `SELECT id FROM ext_agent_chat_threads
		WHERE deleted_at IS NOT NULL AND deleted_at < $1 ORDER BY deleted_at LIMIT $2`,
		r.now().UTC().Add(-ChatPurgeAfter), limit); err != nil {
		return res, err
	}
	for _, id := range ids {
		objects, err := r.purgeOne(ctx, id)
		res.Objects += objects
		if err != nil {
			return res, fmt.Errorf("purge thread %s: %w", id, err)
		}
		res.Threads++
	}
	return res, nil
}

func (r *ChatThreadDeleteRepo) purgeOne(ctx context.Context, id uuid.UUID) (int, error) {
	var keys []string
	if err := r.db.SelectContext(ctx, &keys, `SELECT s3_key FROM ext_agent_chat_attachments WHERE thread_id = $1`, id); err != nil {
		return 0, err
	}
	deleted := 0
	for _, key := range keys {
		if err := r.store.Delete(ctx, key); err != nil && !isChatObjectGone(err) {
			return deleted, err
		}
		deleted++
	}
	// The guard keeps a thread that was somehow restored out of the purge.
	_, err := r.db.ExecContext(ctx, `DELETE FROM ext_agent_chat_threads WHERE id = $1 AND deleted_at IS NOT NULL`, id)
	return deleted, err
}

func isChatObjectGone(err error) bool { return errors.Is(err, storage.ErrNotFound) }
