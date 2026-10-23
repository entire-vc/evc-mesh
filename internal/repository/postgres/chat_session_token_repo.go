package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

var _ repository.ChatSessionTokenRepository = (*ChatSessionTokenRepo)(nil)

// ChatSessionTokenRepo implements repository.ChatSessionTokenRepository.
type ChatSessionTokenRepo struct {
	db *sqlx.DB
}

// NewChatSessionTokenRepo creates a new ChatSessionTokenRepo.
func NewChatSessionTokenRepo(db *sqlx.DB) *ChatSessionTokenRepo {
	return &ChatSessionTokenRepo{db: db}
}

const chatSessionTokenCols = `id, token_hash, user_id, agent_id, workspace_id, project_id, chat_thread_id, chat_session_id, runner_id, execution_epoch, issued_at, expires_at, revoked_at, revoked_reason`

func (r *ChatSessionTokenRepo) Issue(ctx context.Context, t *domain.ChatSessionToken, supersedeAt time.Time) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Serialise concurrent issue/renew of one session so the epoch check below
	// and the supersede cannot interleave with another runner's.
	if _, lerr := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1::text))`, "chat_session:"+t.ChatSessionID.String()); lerr != nil {
		return lerr
	}

	// A stop recorded before any token existed ends the session id just as a revoked
	// row does; without this a Stop that wins the lock first would be forgotten.
	var stopped bool
	if serr := tx.GetContext(ctx, &stopped, `SELECT EXISTS (SELECT 1 FROM chat_session_stops WHERE chat_session_id = $1)`, t.ChatSessionID); serr != nil {
		return serr
	}
	if stopped {
		return repository.ErrChatEpochStale
	}

	var maxEpoch sql.NullInt64
	if merr := tx.GetContext(ctx, &maxEpoch, `SELECT MAX(execution_epoch) FROM chat_session_tokens WHERE chat_session_id = $1`, t.ChatSessionID); merr != nil {
		return merr
	}
	if maxEpoch.Valid && t.ExecutionEpoch < maxEpoch.Int64 {
		return repository.ErrChatEpochStale
	}

	var latest domain.ChatSessionToken
	err = tx.GetContext(ctx, &latest, `SELECT `+chatSessionTokenCols+` FROM chat_session_tokens WHERE chat_session_id = $1 ORDER BY issued_at DESC, id LIMIT 1`, t.ChatSessionID)
	switch {
	case err == nil:
		// A stop is final for the session id, whatever the epoch: a restart is a
		// new chat_session_id, so a stopped session cannot be revived by a runner
		// that merely claims a higher epoch.
		if latest.SessionEnded() {
			return repository.ErrChatEpochStale
		}
		// Within one epoch a single runner holds the session under one binding:
		// another runner (or a stale one after a takeover), or the same runner with a
		// different user/agent/workspace/project/thread, needs a strictly newer epoch.
		if t.ExecutionEpoch == latest.ExecutionEpoch && !sameChatBinding(t, &latest) {
			return repository.ErrChatEpochStale
		}
	case errors.Is(err, sql.ErrNoRows):
	default:
		return err
	}

	// A takeover under a newer epoch cuts every older-epoch token off at once: the
	// overlap below exists for the holder's own renewal, never for a runner that lost the session.
	if _, err := tx.ExecContext(ctx, `
		UPDATE chat_session_tokens
		   SET revoked_at = $2, revoked_reason = $4
		 WHERE chat_session_id = $1
		   AND execution_epoch < $3
		   AND (revoked_at IS NULL OR revoked_at > $2)
		   AND expires_at > $2`,
		t.ChatSessionID, t.IssuedAt, t.ExecutionEpoch, domain.ChatSessionRevokedSuperseded); err != nil {
		return err
	}

	// Older generation(s) of the same epoch stop being usable at supersedeAt at the latest.
	if _, err := tx.ExecContext(ctx, `
		UPDATE chat_session_tokens
		   SET revoked_at = $2, revoked_reason = $3
		 WHERE chat_session_id = $1
		   AND (revoked_at IS NULL OR revoked_at > $2)
		   AND expires_at > $2`,
		t.ChatSessionID, supersedeAt, domain.ChatSessionRevokedSuperseded); err != nil {
		return err
	}

	if _, err := tx.NamedExecContext(ctx, `
		INSERT INTO chat_session_tokens (`+chatSessionTokenCols+`)
		VALUES (:id, :token_hash, :user_id, :agent_id, :workspace_id, :project_id, :chat_thread_id, :chat_session_id, :runner_id, :execution_epoch, :issued_at, :expires_at, :revoked_at, :revoked_reason)`, t); err != nil {
		return err
	}
	return tx.Commit()
}

// sameChatBinding reports whether two tokens carry the same runner and the same
// user, agent, workspace, project and thread.
func sameChatBinding(a, b *domain.ChatSessionToken) bool {
	return a.RunnerID == b.RunnerID && a.UserID == b.UserID && a.AgentID == b.AgentID &&
		a.WorkspaceID == b.WorkspaceID && a.ProjectID == b.ProjectID && a.ChatThreadID == b.ChatThreadID
}

func (r *ChatSessionTokenRepo) GetByHash(ctx context.Context, tokenHash string) (*domain.ChatSessionToken, error) {
	var t domain.ChatSessionToken
	if err := r.db.GetContext(ctx, &t, `SELECT `+chatSessionTokenCols+` FROM chat_session_tokens WHERE token_hash = $1`, tokenHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &t, nil
}

func (r *ChatSessionTokenRepo) LatestForSession(ctx context.Context, sessionID uuid.UUID) (*domain.ChatSessionToken, error) {
	var t domain.ChatSessionToken
	if err := r.db.GetContext(ctx, &t, `SELECT `+chatSessionTokenCols+` FROM chat_session_tokens WHERE chat_session_id = $1 ORDER BY issued_at DESC, id LIMIT 1`, sessionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &t, nil
}

func (r *ChatSessionTokenRepo) RevokeSession(ctx context.Context, sessionID uuid.UUID, reason string, now time.Time) (int64, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	// Same per-session lock as Issue: a stop cannot slip between an issue's checks
	// and its insert and leave a live token behind. After the stop, Issue sees the
	// ended session as the latest row and refuses.
	if _, lerr := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1::text))`, "chat_session:"+sessionID.String()); lerr != nil {
		return 0, lerr
	}
	// Also pulls forward a pending supersede deadline: Stop is immediate.
	res, err := tx.ExecContext(ctx, `
		UPDATE chat_session_tokens
		   SET revoked_at = $2, revoked_reason = $3
		 WHERE chat_session_id = $1
		   AND (revoked_at IS NULL OR revoked_at > $2)`,
		sessionID, now, reason)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	// Tombstone for the session id, so a Stop that finds no token yet still ends the
	// session: the first Issue after it is refused. A renewal reason never ends it.
	// The first stop's time and reason stand; a repeated stop is a no-op.
	if reason != domain.ChatSessionRevokedSuperseded {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO chat_session_stops (chat_session_id, stopped_at, reason)
			VALUES ($1, $2, $3)
			ON CONFLICT (chat_session_id) DO NOTHING`,
			sessionID, now, reason); err != nil {
			return 0, err
		}
	}
	return n, tx.Commit()
}

func (r *ChatSessionTokenRepo) PurgeExpired(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM chat_session_tokens WHERE expires_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
