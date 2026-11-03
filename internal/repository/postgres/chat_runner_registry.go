package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ChatRunnerTokenPrefix marks a runner bearer (crt_ = chat runner token).
const ChatRunnerTokenPrefix = "crt_"

// ChatRunner is a registered runner of an agent. The bearer is never stored.
type ChatRunner struct {
	ID         uuid.UUID  `db:"id" json:"runner_id"`
	AgentID    uuid.UUID  `db:"agent_id" json:"agent_id"`
	Name       string     `db:"name" json:"name"`
	CreatedAt  time.Time  `db:"created_at" json:"created_at"`
	LastSeenAt *time.Time `db:"last_seen_at" json:"last_seen_at"`
}

func hashRunnerToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// RegisterRunner adds a runner under the calling agent and returns its bearer,
// which exists only in this answer. Holding the agent key lets a caller add a
// runner of its own, but never read the bearer of an existing one, so orders
// addressed to another runner stay out of reach.
func (r *ChatRunnerRepo) RegisterRunner(ctx context.Context, caller uuid.UUID, name string) (*ChatRunner, string, error) {
	if caller == uuid.Nil {
		return nil, "", errChatRunnerForbidden()
	}
	name = truncateUTF8(strings.ToValidUTF8(strings.TrimSpace(name), ""), 100)
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, "", err
	}
	raw := ChatRunnerTokenPrefix + hex.EncodeToString(buf)
	var out ChatRunner
	err := r.db.GetContext(ctx, &out, `INSERT INTO ext_agent_chat_runners (workspace_id, agent_id, name, token_hash)
		SELECT workspace_id, id, $2, $3 FROM agents WHERE id = $1
		RETURNING id, agent_id, name, created_at, last_seen_at`, caller, name, hashRunnerToken(raw))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", errChatRunnerForbidden()
	}
	if err != nil {
		return nil, "", err
	}
	return &out, raw, nil
}

// AuthenticateRunner resolves a runner bearer presented by the calling agent to
// the runner's id. A bearer of another agent's runner, a revoked or an unknown
// one are all the same 403.
func (r *ChatRunnerRepo) AuthenticateRunner(ctx context.Context, caller uuid.UUID, token string) (uuid.UUID, error) {
	if caller == uuid.Nil || !strings.HasPrefix(token, ChatRunnerTokenPrefix) {
		return uuid.Nil, errChatRunnerForbidden()
	}
	var id uuid.UUID
	err := r.db.GetContext(ctx, &id, `UPDATE ext_agent_chat_runners SET last_seen_at = $3
		WHERE token_hash = $1 AND agent_id = $2 AND revoked_at IS NULL RETURNING id`,
		hashRunnerToken(token), caller, r.now().UTC())
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, errChatRunnerForbidden()
	}
	return id, err
}

// RevokeRunner kills a runner's credential. Its pending orders stay and expire;
// nobody can authenticate as that runner any more. Existing-session receivers
// registered by the runner end with it: queued deliveries fail as
// receiver_revoked, received ones as delivery_uncertain, and their text is dropped.
func (r *ChatRunnerRepo) RevokeRunner(ctx context.Context, caller, runner uuid.UUID) error {
	if caller == uuid.Nil {
		return errChatRunnerForbidden()
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := r.now().UTC()
	// Receivers first: this UPDATE waits for a concurrent Enqueue holding the
	// receiver FOR SHARE, so the delivery it inserts is committed (and settled
	// below) or, if revocation won, Enqueue no longer finds an active receiver.
	if _, err = tx.ExecContext(ctx, `UPDATE existing_session_receivers SET revoked_at = $3
		WHERE runner_id = $1 AND agent_id = $2 AND revoked_at IS NULL`, runner, caller, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE existing_session_deliveries d SET state = 'failed', body = NULL, updated_at = $2,
		error_code = CASE WHEN d.state = 'received' THEN 'delivery_uncertain' ELSE 'receiver_revoked' END
		FROM existing_session_receivers rc
		WHERE rc.id = d.receiver_id AND rc.runner_id = $1 AND d.state IN ('queued', 'received')`, runner, now); err != nil {
		return err
	}
	// The runner row last: an unknown or foreign runner is a 404 and the
	// transaction, receivers included, rolls back.
	res, err := tx.ExecContext(ctx, `UPDATE ext_agent_chat_runners SET revoked_at = $3
		WHERE id = $1 AND agent_id = $2 AND revoked_at IS NULL`, runner, caller, now)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apierror.NotFound("runner")
	}
	return tx.Commit()
}
