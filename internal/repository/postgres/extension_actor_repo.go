package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ExtensionActorRepo implements repository.ExtensionActorRepository.
type ExtensionActorRepo struct {
	db *sqlx.DB
}

// NewExtensionActorRepo creates a new ExtensionActorRepo.
func NewExtensionActorRepo(db *sqlx.DB) *ExtensionActorRepo {
	return &ExtensionActorRepo{db: db}
}

const extensionActorCols = `connection_id, workspace_id, actor_id, extension_id, display_name, token_hash,
	prev_token_hash, prev_token_expires_at, capabilities, violation_count, last_violation_at, last_violation,
	issued_at, rotated_at, revoked_at, token_delivered_at`

func (r *ExtensionActorRepo) Issue(ctx context.Context, a *domain.ExtensionActor) (*domain.ExtensionActor, error) {
	// actor_id is kept on re-issue: history signed by the extension must keep
	// pointing at one principal across disable/enable cycles.
	const q = `
		INSERT INTO extension_actors (connection_id, workspace_id, actor_id, extension_id, display_name, token_hash, capabilities)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (connection_id) DO UPDATE SET
			extension_id = EXCLUDED.extension_id,
			display_name = EXCLUDED.display_name,
			token_hash = EXCLUDED.token_hash,
			capabilities = EXCLUDED.capabilities,
			prev_token_hash = NULL,
			prev_token_expires_at = NULL,
			violation_count = 0,
			last_violation_at = NULL,
			last_violation = '',
			issued_at = now(),
			rotated_at = NULL,
			revoked_at = NULL,
			token_delivered_at = NULL
		RETURNING ` + extensionActorCols
	var out domain.ExtensionActor
	if err := r.db.GetContext(ctx, &out, q, a.ConnectionID, a.WorkspaceID, a.ActorID, a.ExtensionID,
		a.DisplayName, a.TokenHash, a.Capabilities); err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *ExtensionActorRepo) GetByConnection(ctx context.Context, connectionID uuid.UUID) (*domain.ExtensionActor, error) {
	var out domain.ExtensionActor
	err := r.db.GetContext(ctx, &out, `SELECT `+extensionActorCols+` FROM extension_actors WHERE connection_id = $1`, connectionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *ExtensionActorRepo) GetByTokenHash(ctx context.Context, hash string, now time.Time) (*domain.ExtensionActor, error) {
	var out domain.ExtensionActor
	err := r.db.GetContext(ctx, &out, `SELECT `+extensionActorCols+`
		FROM extension_actors
		WHERE token_hash = $1
		   OR (prev_token_hash = $1 AND prev_token_expires_at > $2)`, hash, now)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *ExtensionActorRepo) Rotate(ctx context.Context, connectionID uuid.UUID, newHash string, prevExpiresAt time.Time) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE extension_actors SET
			prev_token_hash = token_hash,
			prev_token_expires_at = $3,
			token_hash = $2,
			rotated_at = now()
		WHERE connection_id = $1 AND revoked_at IS NULL`, connectionID, newHash, prevExpiresAt)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (r *ExtensionActorRepo) RotateKeepingState(ctx context.Context, connectionID uuid.UUID, newHash string, prevExpiresAt time.Time) (*domain.ExtensionActor, error) {
	// The row is locked and read in the same statement that rotates it, so the
	// returned state is exactly what this rotation replaced.
	var out domain.ExtensionActor
	err := r.db.GetContext(ctx, &out, `
		WITH old AS (
			SELECT connection_id, token_hash, prev_token_hash, prev_token_expires_at, rotated_at, token_delivered_at
			FROM extension_actors
			WHERE connection_id = $1 AND revoked_at IS NULL
			FOR UPDATE
		)
		UPDATE extension_actors e SET
			prev_token_hash = e.token_hash,
			prev_token_expires_at = $3,
			token_hash = $2,
			rotated_at = now()
		FROM old
		WHERE e.connection_id = old.connection_id
		RETURNING old.connection_id, old.token_hash, old.prev_token_hash, old.prev_token_expires_at,
			old.rotated_at, old.token_delivered_at`, connectionID, newHash, prevExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *ExtensionActorRepo) RestoreToken(ctx context.Context, connectionID uuid.UUID, newHash string, before *domain.ExtensionActor) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE extension_actors SET
			token_hash = $3,
			prev_token_hash = $4,
			prev_token_expires_at = $5,
			rotated_at = $6
		WHERE connection_id = $1 AND token_hash = $2`,
		connectionID, newHash, before.TokenHash, before.PrevTokenHash, before.PrevTokenExpiresAt, before.RotatedAt)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (r *ExtensionActorRepo) MarkTokenDelivered(ctx context.Context, connectionID uuid.UUID, tokenHash string, at time.Time) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE extension_actors SET token_delivered_at = $3
		WHERE connection_id = $1 AND token_hash = $2 AND revoked_at IS NULL`, connectionID, tokenHash, at)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (r *ExtensionActorRepo) RotateSelf(ctx context.Context, connectionID uuid.UUID, presentedHash, newHash string, prevExpiresAt, now time.Time) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE extension_actors SET
			prev_token_hash = $2,
			prev_token_expires_at = $4,
			token_hash = $3,
			rotated_at = now()
		WHERE connection_id = $1 AND revoked_at IS NULL
		  AND (token_hash = $2 OR (prev_token_hash = $2 AND prev_token_expires_at > $5))`,
		connectionID, presentedHash, newHash, prevExpiresAt, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (r *ExtensionActorRepo) DropPrevious(ctx context.Context, connectionID uuid.UUID, presentedHash string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE extension_actors SET prev_token_hash = NULL, prev_token_expires_at = NULL
		WHERE connection_id = $1 AND revoked_at IS NULL AND token_hash = $2`, connectionID, presentedHash)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (r *ExtensionActorRepo) Revoke(ctx context.Context, connectionID uuid.UUID) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE extension_actors SET revoked_at = now(), prev_token_hash = NULL, prev_token_expires_at = NULL
		WHERE connection_id = $1 AND revoked_at IS NULL`, connectionID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (r *ExtensionActorRepo) RecordViolation(ctx context.Context, connectionID uuid.UUID, what string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE extension_actors SET violation_count = violation_count + 1, last_violation_at = now(), last_violation = $2
		WHERE connection_id = $1`, connectionID, what)
	return err
}
